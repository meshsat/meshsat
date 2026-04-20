import { test } from '@playwright/test';

test('Bridge map shows TAK positions', async ({ page }) => {
  await page.goto('/map');
  await page.waitForTimeout(5000);
  await page.screenshot({ path: '/tmp/e2e-bridge-map.png', fullPage: false });
});

test('Bridge TAK Monitor events', async ({ page }) => {
  await page.goto('/tak');
  await page.waitForTimeout(3000);
  await page.screenshot({ path: '/tmp/e2e-bridge-tak-events.png', fullPage: true });
});
