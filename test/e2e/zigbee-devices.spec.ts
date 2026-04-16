import { test, expect } from '@playwright/test';

// E2E for the ZigBee device manager [MESHSAT-509]. Verifies:
//   - /zigbee list page renders without API errors
//   - Coordinator status badge appears
//   - The legacy + enriched device list endpoints both respond
//   - Per-device detail page renders all panels (header, history, routing, command, danger)
//   - Alias rename round-trips through the PATCH endpoint
//   - Routing toggle round-trips through the PUT endpoint

test.describe('ZigBee device manager', () => {
  test('list page loads + shows coordinator status', async ({ page, request, baseURL }) => {
    // Sanity-check the API responds before rendering — surfaces backend
    // issues with a clearer error than a Playwright assertion failure.
    const status = await request.get('/api/zigbee/status');
    expect(status.ok()).toBeTruthy();
    const body = await status.json();
    expect(body).toHaveProperty('connected');

    await page.goto('/#/zigbee');
    await expect(page).toHaveTitle(/MeshSat/);
    await expect(page.getByRole('heading', { name: 'ZigBee Devices' })).toBeVisible({ timeout: 10000 });

    // Either we have devices, or we show the empty state. Both are valid.
    const hasEmpty = await page.getByText(/No paired ZigBee devices/i).isVisible().catch(() => false);
    const hasDevices = await page.locator('a[href^="#/zigbee/"]').count() > 0;
    expect(hasEmpty || hasDevices).toBeTruthy();
  });

  test('enriched + legacy device endpoints both respond', async ({ request }) => {
    const enriched = await request.get('/api/zigbee/devices2');
    expect(enriched.ok()).toBeTruthy();
    const ej = await enriched.json();
    expect(ej).toHaveProperty('devices');
    expect(Array.isArray(ej.devices)).toBeTruthy();

    const legacy = await request.get('/api/zigbee/devices');
    expect(legacy.ok()).toBeTruthy();
    const lj = await legacy.json();
    expect(lj).toHaveProperty('devices');
  });

  test('detail page renders + alias round-trips', async ({ page, request }) => {
    // Find a device to drive the test against. Skip cleanly on kits where
    // no device has been paired yet.
    const r = await request.get('/api/zigbee/devices2');
    const list = (await r.json()).devices || [];
    if (list.length === 0) {
      test.skip(true, 'no paired devices on this kit');
      return;
    }
    const dev = list[0];
    const addr = dev.ieee_addr || String(dev.short_addr);

    await page.goto(`/#/zigbee/${addr}`);
    await expect(page.getByText(/IEEE:/)).toBeVisible({ timeout: 10000 });

    // All four "live readings" tiles should render labels (values may be —).
    await expect(page.getByText('Temperature', { exact: true })).toBeVisible();
    await expect(page.getByText('Humidity', { exact: true })).toBeVisible();
    await expect(page.getByText('Battery', { exact: true })).toBeVisible();
    await expect(page.getByText('Signal LQI', { exact: true })).toBeVisible();

    // Sensor history section should render (with chart or empty state).
    await expect(page.getByText('Sensor history')).toBeVisible();

    // Routing section.
    await expect(page.getByText(/Forward to TAK/)).toBeVisible();

    // Command panel.
    await expect(page.getByRole('button', { name: 'Turn ON' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Turn OFF' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Toggle' })).toBeVisible();

    // Danger zone.
    await expect(page.getByRole('button', { name: 'Forget device' })).toBeVisible();

    // Alias rename — set, then revert. Use the API directly so we don't
    // race the in-memory cache update vs. polling refresh.
    const newAlias = `e2e-test-${Date.now() % 10000}`;
    const patch = await request.patch(`/api/zigbee/devices/${addr}`, {
      data: { alias: newAlias },
    });
    expect(patch.ok()).toBeTruthy();
    const patched = await patch.json();
    expect(patched.alias).toBe(newAlias);
    expect(patched.display_name).toBe(newAlias);

    // Revert to whatever the original alias was (may be empty).
    const revert = await request.patch(`/api/zigbee/devices/${addr}`, {
      data: { alias: dev.alias || '' },
    });
    expect(revert.ok()).toBeTruthy();
  });

  test('routing config round-trips', async ({ request }) => {
    const r = await request.get('/api/zigbee/devices2');
    const list = (await r.json()).devices || [];
    if (list.length === 0) {
      test.skip(true, 'no paired devices on this kit');
      return;
    }
    const addr = list[0].ieee_addr || String(list[0].short_addr);

    const initial = await request.get(`/api/zigbee/devices/${addr}/routing`);
    expect(initial.ok()).toBeTruthy();
    const orig = await initial.json();

    // Flip mesh on, then revert.
    const flipped = await request.put(`/api/zigbee/devices/${addr}/routing`, {
      data: { ...orig, to_mesh: true },
    });
    expect(flipped.ok()).toBeTruthy();
    const f = await flipped.json();
    expect(f.to_mesh).toBe(true);

    const restored = await request.put(`/api/zigbee/devices/${addr}/routing`, {
      data: orig,
    });
    expect(restored.ok()).toBeTruthy();
    const rr = await restored.json();
    expect(rr.to_mesh).toBe(orig.to_mesh);
  });

  test('history endpoint responds with shaped payload', async ({ request }) => {
    const r = await request.get('/api/zigbee/devices2');
    const list = (await r.json()).devices || [];
    if (list.length === 0) {
      test.skip(true, 'no paired devices on this kit');
      return;
    }
    const addr = list[0].ieee_addr || String(list[0].short_addr);

    const h = await request.get(`/api/zigbee/devices/${addr}/history?hours=24`);
    expect(h.ok()).toBeTruthy();
    const body = await h.json();
    expect(body).toHaveProperty('readings');
    expect(Array.isArray(body.readings)).toBeTruthy();
    expect(body.hours).toBe(24);
  });
});
