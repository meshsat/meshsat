import { test } from '@playwright/test';
import * as fs from 'node:fs';

// Tour every primary route at kiosk viewport + collect small-text /
// small-button offenders so we know where to focus the polish pass.
// Writes screenshots + a JSON report to test-results/kiosk-tour-after.

const VP = { width: 853, height: 480 };
const OUT = 'test-results/kiosk-tour-after';
fs.mkdirSync(OUT, { recursive: true });

const ROUTES = [
  { name: 'dashboard', path: '/?kiosk=1&shell=engineer' },
  { name: 'compose',   path: '/compose?kiosk=1&shell=engineer' },
  { name: 'inbox',     path: '/inbox?kiosk=1&shell=engineer' },
  { name: 'map',       path: '/map?kiosk=1&shell=engineer' },
  { name: 'people',    path: '/people?kiosk=1&shell=engineer' },
  { name: 'radios',    path: '/radios?kiosk=1&shell=engineer' },
  { name: 'settings',  path: '/settings?kiosk=1&shell=engineer' },
  { name: 'bridge',    path: '/bridge?kiosk=1&shell=engineer' },
  { name: 'interfaces',path: '/interfaces?kiosk=1&shell=engineer' },
];

// Also tour once in operator mode on dashboard to verify bottom bar.
ROUTES.push({ name: 'op-dashboard', path: '/?kiosk=1&shell=operator' });

for (const r of ROUTES) {
  test(`tour ${r.name}`, async ({ page }) => {
    await page.setViewportSize(VP);
    await page.goto(r.path, { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => { try { localStorage.clear(); } catch {} });
    await page.goto(r.path, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#app header', { timeout: 10000 });
    await page.waitForTimeout(1200);

    // Small-text scan: font-size < 12 px on elements that render text.
    const smallText = await page.evaluate(() => {
      const out: any[] = [];
      document.querySelectorAll('body *').forEach(el => {
        const cs = getComputedStyle(el);
        const fs = parseFloat(cs.fontSize || '16');
        const r = el.getBoundingClientRect();
        const text = (el.textContent || '').trim().slice(0, 40);
        if (fs > 0 && fs < 12 && text.length > 0 && r.width > 0 && r.height > 0) {
          out.push({
            tag: el.tagName,
            cls: (el as HTMLElement).className?.toString?.().slice(0, 50) || '',
            fs: Number(fs.toFixed(1)),
            text,
          });
        }
      });
      // Dedup on (tag, cls, fs, text) to collapse repeated items.
      const seen = new Set<string>();
      return out.filter(o => {
        const k = `${o.tag}|${o.cls}|${o.fs}|${o.text}`;
        if (seen.has(k)) return false;
        seen.add(k);
        return true;
      });
    });

    // Small-button scan: clickable elements under 40×80 px.
    const smallButtons = await page.evaluate(() => {
      const out: any[] = [];
      document.querySelectorAll('button, a[href]').forEach(el => {
        const r = el.getBoundingClientRect();
        const text = (el.textContent || '').trim().slice(0, 30);
        if (r.width > 0 && r.height > 0 && (r.height < 40 || r.width < 44)) {
          out.push({
            tag: el.tagName,
            cls: (el as HTMLElement).className?.toString?.().slice(0, 50) || '',
            w: Number(r.width.toFixed(0)),
            h: Number(r.height.toFixed(0)),
            text,
          });
        }
      });
      const seen = new Set<string>();
      return out.filter(o => {
        const k = `${o.tag}|${o.cls}|${o.w}|${o.h}|${o.text}`;
        if (seen.has(k)) return false;
        seen.add(k);
        return true;
      });
    });

    fs.writeFileSync(`${OUT}/${r.name}-report.json`, JSON.stringify({
      route: r.path,
      smallText: smallText.slice(0, 30),
      smallTextCount: smallText.length,
      smallButtons: smallButtons.slice(0, 30),
      smallButtonCount: smallButtons.length,
    }, null, 2));

    await page.screenshot({ path: `${OUT}/${r.name}.png`, fullPage: false });
  });
}
