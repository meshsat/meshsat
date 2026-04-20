import { test } from '@playwright/test';

// Diagnostic: snap the main nav + settings tabs at default, operator,
// engineer, kiosk, narrow-mobile, kiosk-viewport — so we can see
// whether "Network" is missing because it's off-screen (clipping) or
// because it's not being emitted at all.
test.describe('UI menu audit', () => {

  const scenarios = [
    { name: 'default',  url: '/settings',                 viewport: { width: 1280, height: 720 } },
    { name: 'operator', url: '/settings?shell=operator',  viewport: { width: 1280, height: 720 } },
    { name: 'engineer', url: '/settings?shell=engineer',  viewport: { width: 1280, height: 720 } },
    { name: 'kiosk-op',  url: '/settings?kiosk=1&shell=operator',  viewport: { width: 853,  height: 480 } },
    { name: 'kiosk-eng', url: '/settings?kiosk=1&shell=engineer',  viewport: { width: 853,  height: 480 } },
    { name: 'narrow-360', url: '/settings?shell=engineer', viewport: { width: 360, height: 800 } },
  ];

  for (const s of scenarios) {
    test(s.name, async ({ page }) => {
      await page.setViewportSize(s.viewport);
      await page.goto(s.url, { waitUntil: 'domcontentloaded' });
      await page.waitForTimeout(1200);

      // Top-of-page screenshot (main nav + settings tab strip).
      await page.screenshot({
        path: `test-results/menu-audit-${s.name}.png`,
        fullPage: false,
        clip: { x: 0, y: 0, width: s.viewport.width, height: Math.min(320, s.viewport.height) },
      });

      // Enumerate visible tab buttons so we have DOM truth alongside pixels.
      const tabTexts = await page.evaluate(() => {
        // Tab strip is a row of buttons under activeTab control.
        const buttons = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return buttons
          .filter(b => b.offsetParent !== null && b.innerText.length > 0 && b.innerText.length < 30)
          .map(b => ({
            text: b.innerText.trim(),
            x: Math.round(b.getBoundingClientRect().x),
            y: Math.round(b.getBoundingClientRect().y),
            w: Math.round(b.getBoundingClientRect().width),
            inView: b.getBoundingClientRect().right <= window.innerWidth,
          }));
      });
      // Print — Playwright shows stdout/stderr in the test report.
      console.log(`\n=== ${s.name} (${s.viewport.width}x${s.viewport.height}) ===`);
      for (const b of tabTexts) {
        const flag = b.inView ? ' ' : '✗OFF';
        console.log(`  ${flag} [${b.x.toString().padStart(4)},${b.y.toString().padStart(3)}] ${b.w.toString().padStart(3)}w  "${b.text}"`);
      }
    });
  }
});
