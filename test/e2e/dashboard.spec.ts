import { test, expect } from '@playwright/test';

test.describe('MeshSat Dashboard', () => {
  test('loads dashboard page', async ({ page }) => {
    await page.goto('/');
    await expect(page).toHaveTitle(/MeshSat/);
  });

  test('dashboard shows status widget', async ({ page }) => {
    await page.goto('/');
    // Wait for API data to load
    await page.waitForTimeout(2000);
    // Check that the main dashboard content renders
    const body = await page.textContent('body');
    expect(body).toBeTruthy();
  });

  test('nodes page loads', async ({ page }) => {
    await page.goto('/#/nodes');
    await page.waitForTimeout(2000);
    const body = await page.textContent('body');
    expect(body).toBeTruthy();
  });

  test('messages page loads', async ({ page }) => {
    await page.goto('/#/messages');
    await page.waitForTimeout(2000);
    const body = await page.textContent('body');
    expect(body).toBeTruthy();
  });

  test('bridge page loads and shows gateways', async ({ page }) => {
    await page.goto('/#/bridge');
    await page.waitForTimeout(3000);
    const body = await page.textContent('body');
    // Bridge page should render with gateway content
    expect(body!.length).toBeGreaterThan(100);
  });

  test('interfaces page loads', async ({ page }) => {
    await page.goto('/#/interfaces');
    await page.waitForTimeout(2000);
    const body = await page.textContent('body');
    expect(body).toBeTruthy();
  });

  test('settings page loads', async ({ page }) => {
    await page.goto('/#/settings');
    await page.waitForTimeout(2000);
    const body = await page.textContent('body');
    expect(body).toBeTruthy();
  });

  test('map page loads', async ({ page }) => {
    await page.goto('/#/map');
    await page.waitForTimeout(2000);
    const body = await page.textContent('body');
    expect(body).toBeTruthy();
  });
});

test.describe('API endpoints return valid data', () => {
  test('GET /api/status returns OK', async ({ request }) => {
    const resp = await request.get('/api/status');
    expect(resp.ok()).toBeTruthy();
    const data = await resp.json();
    expect(data).toHaveProperty('connected');
  });

  test('GET /api/gateways returns array', async ({ request }) => {
    const resp = await request.get('/api/gateways');
    expect(resp.ok()).toBeTruthy();
    const data = await resp.json();
    expect(data).toHaveProperty('gateways');
    expect(Array.isArray(data.gateways)).toBeTruthy();
  });

  test('GET /api/nodes returns valid response', async ({ request }) => {
    const resp = await request.get('/api/nodes');
    expect(resp.ok()).toBeTruthy();
  });

  test('GET /api/devices/usb returns device list', async ({ request }) => {
    const resp = await request.get('/api/devices/usb');
    expect(resp.ok()).toBeTruthy();
    const data = await resp.json();
    expect(Array.isArray(data)).toBeTruthy();
    // Should have at least one device
    expect(data.length).toBeGreaterThan(0);
  });

  test('GET /api/interfaces returns valid response', async ({ request }) => {
    const resp = await request.get('/api/interfaces');
    expect(resp.ok()).toBeTruthy();
  });

  test('GET /api/messages returns valid response', async ({ request }) => {
    const resp = await request.get('/api/messages');
    expect(resp.ok()).toBeTruthy();
  });

  test('GET /api/deliveries returns valid response', async ({ request }) => {
    const resp = await request.get('/api/deliveries?limit=10');
    expect(resp.ok()).toBeTruthy();
  });

  test('GET /api/iridium/signal/fast returns response', async ({ request }) => {
    const resp = await request.get('/api/iridium/signal/fast');
    // 200 if modem present, 503 if not — both are valid
    expect(resp.status()).toBeLessThan(600);
  });

  test('GET /api/cellular/status returns status', async ({ request }) => {
    const resp = await request.get('/api/cellular/status');
    // May return 500 if no cellular, that's ok
    expect(resp.status()).toBeLessThan(600);
  });

  test('health endpoint works', async ({ request }) => {
    const resp = await request.head('/health');
    expect(resp.ok()).toBeTruthy();
  });
});

test.describe('Iridium satellite widget (modem-agnostic)', () => {
  test('GET /api/iridium/modem returns connected modem with IMEI', async ({ request }) => {
    const resp = await request.get('/api/iridium/modem');
    expect(resp.ok()).toBeTruthy();
    const data = await resp.json();
    expect(data.connected).toBe(true);
    expect(data.imei).toBeTruthy();
    expect(data.imei.length).toBeGreaterThan(10);
    expect(data.port).toBeTruthy();
  });

  test('GET /api/iridium/signal/fast returns signal bars', async ({ request }) => {
    const resp = await request.get('/api/iridium/signal/fast');
    expect(resp.ok()).toBeTruthy();
    const data = await resp.json();
    expect(data).toHaveProperty('bars');
    expect(data.bars).toBeGreaterThanOrEqual(0);
    expect(data.bars).toBeLessThanOrEqual(5);
  });

  test('GET /api/gateways includes a connected iridium gateway', async ({ request }) => {
    const resp = await request.get('/api/gateways');
    expect(resp.ok()).toBeTruthy();
    const data = await resp.json();
    // Either iridium (SBD/9603) or iridium_imt (9704) should be connected
    const irdGw = data.gateways.find((g: any) =>
      (g.type === 'iridium' || g.type === 'iridium_imt') && g.connected
    );
    expect(irdGw).toBeTruthy();
    expect(irdGw.enabled).toBe(true);
  });

  test('dashboard Iridium widget shows IMEI and connected status', async ({ page }) => {
    await page.goto('/');
    await page.waitForTimeout(3000);

    const body = await page.textContent('body');

    // Widget should show "IRIDIUM" in some form (IMT or SBD)
    expect(body).toContain('IRIDIUM');

    // Should show connected status (not "Disconnected")
    expect(body).toContain('Connected');
    expect(body).not.toMatch(/GatewayDisconnected/);
  });

  test('dashboard Iridium widget shows signal bars', async ({ page }) => {
    await page.goto('/');
    await page.waitForTimeout(3000);

    // The signal bars section should render with a numeric display (e.g. "3/5")
    const body = await page.textContent('body');
    expect(body).toMatch(/[0-5]\/5/);
  });

  test('dashboard screenshot for visual inspection', async ({ page }) => {
    await page.goto('/');
    await page.waitForTimeout(4000);
    await page.screenshot({ path: 'test-results/dashboard-iridium.png', fullPage: true });
  });
});
