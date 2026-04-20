import { test, expect } from '@playwright/test';

// MESHSAT-623 (BLE Peers in Routing) + MESHSAT-624 (WiFi Network tab)
// Engineer shell is required — both are tier 'engineer'.

test.describe('Settings: Bluetooth Peers (MESHSAT-623) + Network/WiFi (MESHSAT-624)', () => {

  test.beforeEach(async ({ page }) => {
    await page.goto('/settings?shell=engineer', { waitUntil: 'domcontentloaded' });
    // SSE streams keep the page "busy" forever; just give the SPA a
    // beat to wire up the tab strip.
    await page.waitForTimeout(1200);
  });

  test('Network tab appears and renders WiFi status', async ({ page }) => {
    const networkTab = page.getByRole('button', { name: /^Network$/ });
    await expect(networkTab).toBeVisible();
    await networkTab.click();
    await page.waitForTimeout(300);

    await expect(page.getByRole('heading', { name: /WiFi Status/ })).toBeVisible();
    // Interface input prefilled
    const ifaceInput = page.locator('input[placeholder="wlan0"]').first();
    await expect(ifaceInput).toBeVisible();
    // Scan + Saved Networks sections present
    await expect(page.getByRole('heading', { name: /Available Networks/ })).toBeVisible();
    await expect(page.getByRole('heading', { name: /Saved Networks/ })).toBeVisible();
    // The Scan button
    const scanBtn = page.getByRole('button', { name: /^Scan$/ }).first();
    await expect(scanBtn).toBeVisible();
  });

  test('Routing tab has Bluetooth Peers section with scan controls', async ({ page }) => {
    await page.getByRole('button', { name: /^Routing$/ }).click();
    await page.waitForTimeout(300);

    const btHeading = page.getByRole('heading', { name: /Bluetooth Peers/ });
    await expect(btHeading).toBeVisible();

    // Scan + Refresh + power toggle present
    // BT Scan button lives inside that section — scope by the card
    const btCard = page.locator('div', { has: btHeading }).first();
    await expect(btCard.getByRole('button', { name: /^Scan$/ })).toBeVisible();
    await expect(btCard.getByRole('button', { name: /Refresh/ })).toBeVisible();
    // Paired + Discovered sublists
    await expect(btCard.getByText(/Paired \(/)).toBeVisible();
    await expect(btCard.getByText(/Discovered \(/)).toBeVisible();
  });

  test('BT adapter status surfaces ON indicator from /api/system/bluetooth/status', async ({ page }) => {
    await page.getByRole('button', { name: /^Routing$/ }).click();
    await page.waitForTimeout(500);

    const btHeading = page.getByRole('heading', { name: /Bluetooth Peers/ });
    await expect(btHeading).toBeVisible();

    // Powered-on adapter should show "ON" chip. Backend returned
    // powered=true in the pre-test curl.
    await expect(page.locator('text=/^ON$/').first()).toBeVisible();
  });

  test('WiFi status card shows the current SSID from wpa_cli', async ({ page }) => {
    await page.getByRole('button', { name: /^Network$/ }).click();
    await page.waitForTimeout(800);

    // Grab the SSID row's combined text (label + value) and assert
    // the value side is a non-empty SSID-shaped string.
    const card = page.locator('div', {
      has: page.getByRole('heading', { name: /WiFi Status/ }),
    }).first();
    const ssidRow = card.locator('div', { hasText: /^SSID/ }).filter({ hasNotText: 'BSSID' }).first();
    await expect(ssidRow).toBeVisible();
    const rowText = ((await ssidRow.textContent()) || '').trim();
    expect(rowText.startsWith('SSID')).toBe(true);
    // Strip the label and whatever whitespace remains; the tail is the SSID.
    const value = rowText.replace(/^SSID\s*/, '').trim();
    expect(value.length).toBeGreaterThan(0);
  });
});
