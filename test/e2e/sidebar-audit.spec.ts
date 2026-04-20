import { test } from '@playwright/test';

const pages = [
  { name: 'dashboard-op',  url: '/?shell=operator' },
  { name: 'dashboard-eng', url: '/?shell=engineer' },
  { name: 'interfaces-eng', url: '/interfaces?shell=engineer' },
  { name: 'settings-eng-network', url: '/settings?shell=engineer&tab=network' },
  { name: 'kiosk-op', url: '/?kiosk=1&shell=operator' },
];
const viewports = [
  { name: 'kiosk', width: 853, height: 480 },
  { name: 'desktop', width: 1280, height: 800 },
];

for (const p of pages) {
  for (const v of viewports) {
    test(`${p.name} @ ${v.name}`, async ({ page }) => {
      await page.setViewportSize({ width: v.width, height: v.height });
      await page.goto(p.url, { waitUntil: 'domcontentloaded' });
      await page.waitForTimeout(1200);
      await page.screenshot({
        path: `test-results/sidebar-${p.name}-${v.name}.png`,
        fullPage: false,
      });

      // Heuristic: look for elements whose layout matches "sidebar"
      // (fixed/sticky left or right, tall, narrow).
      const suspects = await page.evaluate((vw: number) => {
        const out: any[] = [];
        document.querySelectorAll('aside, nav, div').forEach(el => {
          const he = el as HTMLElement;
          const r = he.getBoundingClientRect();
          const style = window.getComputedStyle(he);
          const isFixedOrSticky = style.position === 'fixed' || style.position === 'sticky';
          const isTallAndNarrow = r.height > 300 && r.width > 0 && r.width < 280;
          const isHorizontallyEdge = r.left < 50 || r.right > vw - 50;
          if (isFixedOrSticky && isTallAndNarrow && isHorizontallyEdge && he.offsetParent !== null) {
            out.push({
              tag: he.tagName,
              id: he.id,
              cls: (he.className || '').toString().slice(0, 120),
              pos: { x: Math.round(r.left), y: Math.round(r.top), w: Math.round(r.width), h: Math.round(r.height) },
            });
          }
        });
        return out;
      }, v.width);
      console.log(`\n=== ${p.name} @ ${v.name} suspects ===`);
      suspects.forEach(s => console.log(' ', JSON.stringify(s)));
    });
  }
}
