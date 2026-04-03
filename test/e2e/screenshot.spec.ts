import { test, expect } from '@playwright/test';

test('TAK Events tab renders', async ({ page }) => {
  await page.goto('/tak');
  await page.waitForTimeout(3000);
  await expect(page.getByText('TAK / CoT')).toBeVisible();
  await expect(page.getByText('Events')).toBeVisible();
  await page.screenshot({ path: '/tmp/tak-final-events.png', fullPage: true });
});

test('TAK GeoChat tab renders', async ({ page }) => {
  await page.goto('/tak');
  await page.waitForTimeout(2000);
  await page.getByText('GeoChat', { exact: true }).click();
  await page.waitForTimeout(1000);
  await page.screenshot({ path: '/tmp/tak-final-chat.png', fullPage: true });
});

test('TAK 9-Line tab renders', async ({ page }) => {
  await page.goto('/tak');
  await page.waitForTimeout(2000);
  await page.getByText('9-Line', { exact: true }).click();
  await page.waitForTimeout(1000);
  await page.screenshot({ path: '/tmp/tak-final-nineline.png', fullPage: true });
});

test('TAK Settings tab works', async ({ page }) => {
  await page.goto('/settings');
  await page.waitForTimeout(2000);
  await page.getByText('TAK', { exact: true }).click();
  await page.waitForTimeout(500);
  await expect(page.getByText('TAK Gateway')).toBeVisible();
});

test('Dashboard + Map load', async ({ page }) => {
  await page.goto('/');
  await page.waitForTimeout(3000);
  await page.screenshot({ path: '/tmp/tak-final-dashboard.png', fullPage: false });
  await page.goto('/map');
  await page.waitForTimeout(3000);
  await page.screenshot({ path: '/tmp/tak-final-map.png', fullPage: false });
});
