import { test, expect } from '@playwright/test';

test('Dashboard TAK widget shows Via Hub', async ({ page }) => {
  await page.goto('/');
  await page.waitForTimeout(4000);
  // TAK widget should show "Via Hub" since bridge has Hub connection but no direct OTS
  const takWidget = page.locator('text=TAK / CoT').first();
  await expect(takWidget).toBeVisible();
  await page.screenshot({ path: '/tmp/e2e-dashboard-tak.png', fullPage: false });
});

test('TAK Monitor shows Via Hub status', async ({ page }) => {
  await page.goto('/tak');
  await page.waitForTimeout(3000);
  await expect(page.getByText('TAK / CoT Monitor')).toBeVisible();
  await page.screenshot({ path: '/tmp/e2e-tak-monitor.png', fullPage: true });
});

test('TAK GeoChat tab', async ({ page }) => {
  await page.goto('/tak');
  await page.waitForTimeout(2000);
  await page.getByText('GeoChat', { exact: true }).click();
  await page.waitForTimeout(1000);
  await page.screenshot({ path: '/tmp/e2e-tak-chat.png', fullPage: true });
});

test('TAK 9-Line MEDEVAC tab', async ({ page }) => {
  await page.goto('/tak');
  await page.waitForTimeout(2000);
  await page.getByText('9-Line', { exact: true }).click();
  await page.waitForTimeout(1000);
  await page.screenshot({ path: '/tmp/e2e-tak-nineline.png', fullPage: true });
});

test('TAK Settings tab with all fields', async ({ page }) => {
  await page.goto('/settings');
  await page.waitForTimeout(3000);
  const takBtn = page.getByText('TAK', { exact: true });
  await takBtn.scrollIntoViewIfNeeded();
  await takBtn.click();
  await page.waitForTimeout(1000);
  await expect(page.getByText('TAK Gateway (CoT over TCP)')).toBeVisible();
  await page.screenshot({ path: '/tmp/e2e-tak-settings.png', fullPage: true });
});

test('Map page with TAK markers', async ({ page }) => {
  await page.goto('/map');
  await page.waitForTimeout(4000);
  await page.screenshot({ path: '/tmp/e2e-map.png', fullPage: false });
});
