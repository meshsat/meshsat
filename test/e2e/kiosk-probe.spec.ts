import { test } from '@playwright/test';
test('kiosk url state', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.goto('/?kiosk=1&shell=operator', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(1200);
  const info = await page.evaluate(() => ({
    htmlClass: document.documentElement.className,
    hasShellKiosk: document.documentElement.classList.contains('shell-kiosk'),
    docScrollHeight: document.documentElement.scrollHeight,
    docClientHeight: document.documentElement.clientHeight,
    bodyScrollbarWidth: getComputedStyle(document.body).scrollbarWidth,
    htmlScrollbarWidth: getComputedStyle(document.documentElement).scrollbarWidth,
  }));
  console.log(JSON.stringify(info, null, 2));
});
