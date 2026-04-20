import { test } from '@playwright/test';

test.describe('Re-audit devices + scan + peer UX', () => {

  test('interfaces-devices-tab', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 1200 });
    await page.goto('/interfaces?shell=engineer', { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(1500);
    const devicesTab = page.locator('button').filter({ hasText: /^Devices$/ }).first();
    if (await devicesTab.count() > 0) await devicesTab.click();
    await page.waitForTimeout(1500);
    await page.screenshot({ path: 'test-results/audit2-devices-tab.png', fullPage: true });

    // Enumerate everything that looks like a device row.
    const rows = await page.evaluate(() => {
      return Array.from(document.querySelectorAll('div, li, tr'))
        .map(el => (el as HTMLElement).innerText || '')
        .filter(t => /tty|wlan|wlx|unknown|ambiguous/i.test(t) && t.length < 400)
        .slice(0, 40);
    });
    console.log('=== device rows on Devices tab ===');
    rows.forEach((r, i) => console.log(`${i}: ${r.replace(/\s+/g,' ')}`));
  });

  test('wifi-scan-dedup', async ({ page }) => {
    await page.goto('/settings?shell=engineer', { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(1200);
    // Call scan directly + count SSID duplicates
    const nets = await page.evaluate(async () => {
      const d = await fetch('/api/system/wifi/scan/wlan0').then(r => r.json());
      return d.networks || [];
    });
    const ssidCounts: Record<string, number> = {};
    for (const n of nets) ssidCounts[n.ssid || '(hidden)'] = (ssidCounts[n.ssid || '(hidden)'] || 0) + 1;
    console.log(`=== scan returned ${nets.length} BSS entries ===`);
    const dups = Object.entries(ssidCounts).filter(([_, c]) => c > 1).sort((a, b) => b[1] - a[1]);
    console.log(`SSIDs with more than 1 BSS: ${dups.length}`);
    for (const [s, c] of dups.slice(0, 10)) console.log(`  ${c}x  "${s}"`);
  });

  test('settings-network-screenshot', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 1400 });
    await page.goto('/settings?shell=engineer', { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(1000);
    const tab = page.locator('button').filter({ hasText: /^Network$/ }).first();
    await tab.click();
    await page.waitForTimeout(1000);
    await page.screenshot({ path: 'test-results/audit2-network-tab.png', fullPage: true });
  });
});
