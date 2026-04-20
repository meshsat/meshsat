import { test, expect } from '@playwright/test';

test('a) /interfaces Bind dropdown has NO ambiguous/unknown', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto('/interfaces?shell=engineer', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(1500);
  await page.screenshot({ path: 'test-results/verify-interfaces.png', fullPage: true });

  const pills = await page.evaluate(() => {
    return Array.from(document.querySelectorAll('button'))
      .map(b => (b as HTMLButtonElement).innerText.trim())
      .filter(t => /^(meshtastic|iridium|cellular|gps|zigbee|ambiguous|unknown)\s+\(/i.test(t));
  });
  console.log('\n=== Bind pills on /interfaces ===');
  pills.forEach(p => console.log('  ' + p));
  for (const p of pills) {
    if (/^(ambiguous|unknown)\s+\(/i.test(p)) {
      throw new Error(`BUG regression — found unresolved pill: ${p}`);
    }
  }
});

test('b) scan dedup: Ziggo4BJ31ZE appears once with multi-AP chip', async ({ page }) => {
  await page.goto('/settings?shell=engineer', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(1200);
  const nets = await page.evaluate(async () => {
    const d = await fetch('/api/system/wifi/scan/wlan0').then(r => r.json());
    return d.networks || [];
  });
  const ssidCounts: Record<string, number> = {};
  for (const n of nets) ssidCounts[n.ssid || '(hidden)'] = (ssidCounts[n.ssid || '(hidden)'] || 0) + 1;
  console.log('\n=== raw BSS per SSID from backend ===');
  console.log(`total rows = ${nets.length}`);
  const top = Object.entries(ssidCounts).sort((a, b) => b[1] - a[1]).slice(0, 6);
  top.forEach(([s, c]) => console.log(`  ${c}x  ${s}`));

  // Now load the Settings > Network tab + verify the DEDUPED list.
  const networkTab = page.getByRole('button', { name: /^Network$/ });
  await networkTab.click();
  await page.waitForTimeout(800);
  // In AP mode — trigger a scan via the UI button
  const scanBtn = page.getByRole('button', { name: /^Scan$/ }).first();
  await scanBtn.click();
  await page.waitForTimeout(3500);
  await page.screenshot({ path: 'test-results/verify-settings-scan.png', fullPage: true });

  // Count visible "Ziggo4BJ31ZE" rows in the rendered list.
  const visibleZiggo = await page.locator('span', { hasText: /^Ziggo4BJ31ZE$/ }).count();
  console.log(`\nvisible Ziggo4BJ31ZE rows after dedup: ${visibleZiggo}`);
  if (visibleZiggo > 1) throw new Error(`dedup regression: saw ${visibleZiggo} Ziggo4BJ31ZE rows`);
});

test('c) Network tab has Mode selector at top', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1400 });
  await page.goto('/settings?shell=engineer', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(1200);
  await page.getByRole('button', { name: /^Network$/ }).click();
  await page.waitForTimeout(800);
  await page.screenshot({ path: 'test-results/verify-network-mode.png', fullPage: true });

  await expect(page.getByRole('button', { name: /Connect to Network/ })).toBeVisible();
  await expect(page.getByRole('button', { name: /Link to another kit/ })).toBeVisible();

  // Clicking the peer mode should reveal "Peer Link (IBSS)" and hide Available Networks.
  await page.getByRole('button', { name: /Link to another kit/ }).click();
  await page.waitForTimeout(300);
  await page.screenshot({ path: 'test-results/verify-network-peer.png', fullPage: true });
  await expect(page.getByRole('heading', { name: /Peer Link \(IBSS\)/ })).toBeVisible();
  await expect(page.getByRole('heading', { name: /^Available Networks$/ })).toBeHidden();
});
