import { test, expect } from '@playwright/test';

test.describe('TAK Settings Tab [MESHSAT-449]', () => {

  test('TAK tab exists in Settings navigation', async ({ page }) => {
    await page.goto('/settings');
    await page.waitForTimeout(2000);
    const takTab = page.locator('button:has-text("TAK"), [role="tab"]:has-text("TAK"), a:has-text("TAK"), span:has-text("TAK")').first();
    await expect(takTab).toBeVisible();
  });

  test('TAK tab renders form fields', async ({ page }) => {
    await page.goto('/settings');
    await page.waitForTimeout(2000);
    // Click TAK tab
    await page.getByText('TAK', { exact: true }).click();
    await page.waitForTimeout(500);

    // Verify header
    await expect(page.getByText('TAK Gateway (CoT over TCP)')).toBeVisible();

    // Verify form fields exist
    await expect(page.locator('input[placeholder="tak-server.local"]')).toBeVisible();
    await expect(page.locator('input[placeholder="MESHSAT"]')).toBeVisible();

    // Verify enable checkbox
    await expect(page.getByText('Enable TAK gateway')).toBeVisible();

    // Verify SSL checkbox
    await expect(page.getByText('Use TLS/SSL')).toBeVisible();

    // Verify save button
    await expect(page.getByText('Save TAK Config')).toBeVisible();

    // Verify help text
    await expect(page.getByText('Connects to an OpenTAK Server')).toBeVisible();
  });

  test('SSL toggle shows/hides cert fields', async ({ page }) => {
    await page.goto('/settings');
    await page.waitForTimeout(2000);
    await page.getByText('TAK', { exact: true }).click();
    await page.waitForTimeout(500);

    // Cert fields should NOT be visible initially (SSL off by default)
    await expect(page.locator('input[placeholder="/path/to/cert.pem"]')).not.toBeVisible();

    // Toggle SSL on
    await page.locator('#tak_ssl').check();
    await page.waitForTimeout(300);

    // Cert fields should now be visible
    await expect(page.locator('input[placeholder="/path/to/cert.pem"]')).toBeVisible();
    await expect(page.locator('input[placeholder="/path/to/key.pem"]')).toBeVisible();
    await expect(page.locator('input[placeholder="/path/to/ca.pem"]')).toBeVisible();

    // Toggle SSL off
    await page.locator('#tak_ssl').uncheck();
    await page.waitForTimeout(300);

    // Cert fields should hide again
    await expect(page.locator('input[placeholder="/path/to/cert.pem"]')).not.toBeVisible();
  });

  test('can save TAK config via API', async ({ page }) => {
    await page.goto('/settings');
    await page.waitForTimeout(2000);
    await page.getByText('TAK', { exact: true }).click();
    await page.waitForTimeout(500);

    // Fill in a test host
    const hostInput = page.locator('input[placeholder="tak-server.local"]');
    await hostInput.fill('test-tak.example.com');

    // Click save
    await page.getByText('Save TAK Config').click();
    await page.waitForTimeout(1000);

    // Verify config was saved by checking API
    const response = await page.request.get('/api/gateways');
    const data = await response.json();
    const gateways = data.gateways || data;
    const takGw = gateways.find((g: any) => g.type === 'tak');
    expect(takGw).toBeTruthy();
    expect(takGw.config.tak_host).toBe('test-tak.example.com');
    expect(takGw.config.tak_port).toBe(8087);
    expect(takGw.config.callsign_prefix).toBe('MESHSAT');

    // Clean up: delete the test config
    await page.request.delete('/api/gateways/tak');
  });
});
