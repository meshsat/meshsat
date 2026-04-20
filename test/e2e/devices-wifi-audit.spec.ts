import { test } from '@playwright/test';

test.describe('Devices + WiFi diagnostics', () => {

  test('interfaces -> devices — screenshot + enumerate rows', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto('/interfaces?shell=engineer', { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(1500);
    await page.screenshot({ path: 'test-results/interfaces-page.png', fullPage: true });

    const devicesTab = page.locator('button, a').filter({ hasText: /^Devices$/ }).first();
    if (await devicesTab.count() > 0) {
      await devicesTab.click();
      await page.waitForTimeout(800);
    }
    await page.screenshot({ path: 'test-results/interfaces-devices.png', fullPage: true });

    // Scrape whatever device-like rows we can find.
    const rows = await page.evaluate(() => {
      const out: any[] = [];
      document.querySelectorAll('tr, li, div').forEach(el => {
        const txt = (el as HTMLElement).innerText || '';
        if (/ttyUSB|ttyACM|ttyAMA|bcdDevice|VID|PID|\b0x[0-9a-f]{4}\b|Unknown|Untagged|wlan|wlx/i.test(txt) && txt.length < 400) {
          out.push(txt.slice(0, 300).replace(/\s+/g, ' '));
        }
      });
      return out.slice(0, 40);
    });
    console.log('\n=== /interfaces text captures ===');
    rows.forEach((r, i) => console.log(`${i}: ${r}`));
  });

  test('wifi scan on USB dongle — capture exit error', async ({ page }) => {
    await page.goto('/settings?shell=engineer', { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(1500);

    const networkTab = page.getByRole('button', { name: /^Network$/ });
    if (await networkTab.count() > 0) {
      await networkTab.click();
      await page.waitForTimeout(800);
    }
    await page.screenshot({ path: 'test-results/network-tab.png', fullPage: true });

    // Directly call the API from the page context.
    const status = await page.evaluate(async () => {
      const out: any = {};
      try { out.interfaces = await fetch('/api/system/wifi/interfaces').then(r => r.json()); } catch (e: any) { out.interfacesErr = String(e); }
      // Scan each iface.
      if (Array.isArray(out.interfaces)) {
        out.scans = [];
        for (const i of out.interfaces) {
          const r = await fetch('/api/system/wifi/scan/' + encodeURIComponent(i.name)).then(x => ({ status: x.status, body: x.text() })).catch(e => ({ err: String(e) }));
          out.scans.push({ iface: i.name, role: i.role, state: i.state, resp: r });
        }
      }
      return out;
    });
    console.log('\n=== WiFi status + per-iface scan ===');
    console.log(JSON.stringify(status, null, 2).slice(0, 3000));
  });
});
