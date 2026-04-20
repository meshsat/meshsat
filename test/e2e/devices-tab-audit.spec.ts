import { test } from '@playwright/test';

test('capture Devices tab + raw API sources', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1400 });
  await page.goto('/interfaces?shell=engineer', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(1200);

  // Click the "Devices" tab. Multiple buttons exist ("Devices" is a
  // tab in both Interfaces and Settings) — first Devices after "2 interfaces"
  // heading is the one we want. Use exact match with JS.
  await page.evaluate(() => {
    const btns = Array.from(document.querySelectorAll('button'));
    const target = btns.find(b => b.innerText.trim() === 'Devices');
    if (target) (target as HTMLButtonElement).click();
  });
  await page.waitForTimeout(1500);
  await page.screenshot({ path: 'test-results/devices-tab-full.png', fullPage: true });

  // Snap the two API sources the page uses.
  const sources = await page.evaluate(async () => {
    const [dev, usb] = await Promise.all([
      fetch('/api/devices').then(r => r.json()).catch(() => null),
      fetch('/api/devices/usb').then(r => r.json()).catch(() => null),
    ]);
    return { dev, usb };
  });
  console.log('\n=== /api/devices ===');
  for (const d of (sources.dev || [])) {
    console.log(`  ${d.port}  ${d.vid_pid}  ${d.device_type}  bound=${d.bound_to}`);
  }
  console.log('\n=== /api/devices/usb ===');
  for (const d of (sources.usb || [])) {
    console.log(`  ${d.dev_path}  ${d.vid_pid}  role=${d.role}  state=${d.state}  serial=${d.usb_serial}`);
  }
});
