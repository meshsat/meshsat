import { test, expect } from '@playwright/test';

// Audit: for each URL variant we care about, capture:
//   1. whether html.shell-kiosk class is applied
//   2. whether a scrollbar is visible in the document
//   3. whether the compositor cursor would be visible (CSS says cursor:none?)
//   4. top-header visibility + OP/ENG toggle bounding box
//   5. which nav is rendered (operator 5-item vs engineer full)
//   6. a screenshot at the tesseract kiosk logical viewport (853×480,
//      which is 1280×720 / 1.5 DSF after --force-device-scale-factor).
//
// No mutations — read-only audit. Results go to stdout + screenshots.

const VIEWPORTS = [
  { name: 'kiosk-1.5x', width: 853, height: 480, dsf: 1.5 },
  { name: 'desktop',    width: 1400, height: 900, dsf: 1.0 },
];

const URLS = [
  { name: 'shell-kiosk',          path: '/?shell=kiosk' },
  { name: 'shell-engineer',       path: '/?shell=engineer' },
  { name: 'kiosk1-shell-operator',path: '/?kiosk=1&shell=operator' },
  { name: 'kiosk1-shell-engineer',path: '/?kiosk=1&shell=engineer' },
];

for (const vp of VIEWPORTS) {
  for (const url of URLS) {
    test(`${vp.name} ${url.name}`, async ({ page }) => {
      await page.setViewportSize({ width: vp.width, height: vp.height });

      // Bridge runs an SSE stream → networkidle never fires.
      // Use domcontentloaded + wait for Vue app mount (#app > *).
      await page.goto(url.path, { waitUntil: 'domcontentloaded' });
      await page.evaluate(() => { try { localStorage.clear(); } catch {} });
      await page.goto(url.path, { waitUntil: 'domcontentloaded' });
      await page.waitForSelector('#app header', { timeout: 10000 });
      await page.waitForTimeout(500);

      const report: Record<string, unknown> = {};

      report.hasKioskClass = await page.evaluate(
        () => document.documentElement.classList.contains('shell-kiosk'));

      report.htmlScrollbarWidth = await page.evaluate(() => {
        const html = document.documentElement;
        return getComputedStyle(html).getPropertyValue('scrollbar-width') || 'auto';
      });

      report.hasVerticalScrollbar = await page.evaluate(() => {
        return document.documentElement.scrollHeight > document.documentElement.clientHeight;
      });

      report.bodyCursor = await page.evaluate(
        () => getComputedStyle(document.body).cursor);

      report.htmlCursor = await page.evaluate(
        () => getComputedStyle(document.documentElement).cursor);

      report.headerBox = await page.evaluate(() => {
        const h = document.querySelector('header');
        if (!h) return null;
        const r = h.getBoundingClientRect();
        return { x: r.x, y: r.y, w: r.width, h: r.height, visible: r.height > 0 };
      });

      report.opEngToggleBox = await page.evaluate(() => {
        const btns = Array.from(document.querySelectorAll('button'));
        const btn = btns.find(b => b.textContent?.includes('OP') && b.textContent?.includes('ENG'));
        if (!btn) return null;
        const r = btn.getBoundingClientRect();
        return { x: r.x, y: r.y, w: r.width, h: r.height };
      });

      report.navItems = await page.evaluate(() => {
        const links = Array.from(document.querySelectorAll('nav a'));
        return links.slice(0, 30).map(a => a.textContent?.trim().slice(0, 40));
      });

      report.bottomTabBar = await page.evaluate(() => {
        const bars = Array.from(document.querySelectorAll('nav.fixed.bottom-0'));
        if (!bars.length) return null;
        const visible = bars.find(b => (b as HTMLElement).offsetParent !== null);
        return visible ? 'visible' : 'hidden';
      });

      console.log(`\n=== ${vp.name} ${url.name} ===`);
      console.log(JSON.stringify(report, null, 2));

      await page.screenshot({
        path: `test-results/kiosk-audit-${vp.name}-${url.name}.png`,
        fullPage: false,
      });
    });
  }
}
