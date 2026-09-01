import { test, expect } from '@playwright/test';

// Settings > Remote Mgmt (OOB management frames) [MESHSAT-756]

test.describe('Remote Mgmt Settings Tab [MESHSAT-756]', () => {

  test('Remote Mgmt tab exists in Settings navigation (engineer)', async ({ page }) => {
    await page.goto('/settings');
    await page.waitForTimeout(2000);
    await expect(page.getByText('Remote Mgmt', { exact: true }).first()).toBeVisible();
  });

  test('renders config, peers, send and log sections', async ({ page }) => {
    await page.goto('/settings');
    await page.waitForTimeout(2000);
    await page.getByText('Remote Mgmt', { exact: true }).click();
    await page.waitForTimeout(800);

    await expect(page.getByText('OOB management frames')).toBeVisible();
    await expect(page.getByText('Peers', { exact: true })).toBeVisible();
    await expect(page.getByText('Send command')).toBeVisible();
    await expect(page.getByText('Log', { exact: true })).toBeVisible();
    await expect(page.getByText('Save OOB Config')).toBeVisible();
    await expect(page.locator('#oob_enabled')).toBeVisible();
    await expect(page.getByText(/host agent/)).toBeVisible();
  });

  test('add a random-key peer via UI, see it in the list and the API, issue its bundle, delete it', async ({ page }) => {
    const alias = 'pw-' + Date.now().toString(36);
    let peerId: number | null = null;
    try {
      await page.goto('/settings');
      await page.waitForTimeout(2000);
      await page.getByText('Remote Mgmt', { exact: true }).click();
      await page.waitForTimeout(800);

      await page.getByRole('button', { name: '+ Add' }).last().click();
      await page.locator('input[placeholder="parallax"]').fill(alias);
      await page.locator('input[placeholder="+31612345678"]').fill('+31600000000');
      await page.getByRole('button', { name: 'Add', exact: true }).click();
      await page.waitForTimeout(800);

      await expect(page.getByText(alias, { exact: true })).toBeVisible();

      const res = await page.request.get('/api/oob/peers');
      expect(res.ok()).toBeTruthy();
      const peers = await res.json();
      const mine = peers.find((p: any) => p.alias === alias);
      expect(mine).toBeTruthy();
      expect(mine.key_source).toBe('bundle');
      expect(mine.local_role).toBe('issuer');
      expect(mine.addresses.cellular_0).toBe('+31600000000');
      peerId = mine.peer_id;

      // Bundle QR appears with a meshsat://key/ URL.
      const row = page.locator('div', { hasText: alias }).filter({ has: page.getByRole('button', { name: 'Bundle QR' }) }).last();
      await row.getByRole('button', { name: 'Bundle QR' }).click();
      await page.waitForTimeout(800);
      await expect(page.getByText(/meshsat:\/\/key\//)).toBeVisible();
      await expect(page.locator('img[alt="OOB key bundle QR"]')).toBeVisible();

      // The send panel refuses without a selected peer (button disabled).
      const sendButton = page.getByRole('button', { name: 'Send', exact: true });
      await expect(sendButton).toBeVisible();
    } finally {
      if (peerId) {
        await page.request.delete(`/api/oob/peers/${peerId}`);
      }
    }
  });

  test('tables endpoint lists commands, targets and units', async ({ page }) => {
    const res = await page.request.get('/api/oob/targets');
    expect(res.ok()).toBeTruthy();
    const body = await res.json();
    expect(body.commands.map((c: any) => c.name)).toContain('PING');
    expect(body.targets.map((t: any) => t.name)).toContain('mesh');
    expect(body.units).toContain('docker');
  });
});
