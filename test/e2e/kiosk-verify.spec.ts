import { test, expect } from '@playwright/test';
import * as fs from 'node:fs';

// Verify the current acceptance criteria for the engineer-in-kiosk
// fix against the live bridge (tesseract by default).  Produces a
// JSON report + 3 screenshots per URL tested.
//
// Acceptance:
//   A. engineer nav visible (≥15 tabs) when kiosk=1&shell=engineer
//   B. no mouse cursor (CSS + computed bodyCursor=none)
//   C. no visible scrollbar (scrollbar-width:none + webkit rules)
//   D. OP/ENG toggle is at least a 40×80 px tap target (≥44pt at 1.5×)

// Tesseract runs at 1280×720 native → with --force-device-scale-factor=1.5
// + --enable-use-zoom-for-dsf, the CSS viewport is 853×480.
const VP = { width: 853, height: 480 };

const URLS = [
  { name: 'kiosk-engineer',   path: '/?kiosk=1&shell=engineer', role: 'engineer' },
  { name: 'kiosk-operator',   path: '/?kiosk=1&shell=operator', role: 'operator' },
];

const OUT_DIR = 'test-results/kiosk-verify';
fs.mkdirSync(OUT_DIR, { recursive: true });

for (const url of URLS) {
  test(url.name, async ({ page }) => {
    await page.setViewportSize(VP);
    await page.goto(url.path, { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => { try { localStorage.clear(); } catch {} });
    await page.goto(url.path, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#app header', { timeout: 10000 });
    await page.waitForTimeout(800);

    // --- A. nav ---
    const navItems: string[] = await page.evaluate(() => {
      const links = Array.from(document.querySelectorAll('header nav a'));
      return links.map(a => a.textContent?.trim() || '').filter(Boolean);
    });

    // --- B. cursor ---
    const bodyCursor: string = await page.evaluate(() =>
      getComputedStyle(document.body).cursor);

    // --- C. scrollbar ---
    const scrollbarWidth: string = await page.evaluate(() => {
      return getComputedStyle(document.documentElement).scrollbarWidth || 'auto';
    });

    // --- D. OP/ENG toggle geometry ---
    const toggle = await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button'));
      const b = btns.find(x => x.textContent?.includes('OP') && x.textContent?.includes('ENG'));
      if (!b) return null;
      const r = b.getBoundingClientRect();
      return { x: r.x, y: r.y, w: r.width, h: r.height };
    });

    // --- Shell-kiosk class sanity ---
    const hasKioskClass: boolean = await page.evaluate(
      () => document.documentElement.classList.contains('shell-kiosk'));

    // Screenshot 1: full viewport.
    await page.screenshot({ path: `${OUT_DIR}/${url.name}-full.png`, fullPage: false });

    // Screenshot 2: header-only crop (~top 60 px).
    await page.screenshot({
      path: `${OUT_DIR}/${url.name}-header.png`,
      clip: { x: 0, y: 0, width: VP.width, height: 60 },
    });

    // Screenshot 3: OP/ENG toggle close-up (if present).
    if (toggle) {
      const pad = 12;
      await page.screenshot({
        path: `${OUT_DIR}/${url.name}-toggle.png`,
        clip: {
          x: Math.max(0, toggle.x - pad),
          y: Math.max(0, toggle.y - pad),
          width: Math.min(VP.width, toggle.w + 2 * pad),
          height: Math.min(VP.height, toggle.h + 2 * pad),
        },
      });
    }

    // Pass/fail evaluation.
    const expectedMinNav = url.role === 'engineer' ? 15 : 5;
    const pass = {
      A_nav: navItems.length >= expectedMinNav,
      B_cursor_hidden: bodyCursor === 'none',
      C_scrollbar_hidden: scrollbarWidth === 'none',
      D_toggle_tap_target: toggle != null && toggle.h >= 40 && toggle.w >= 80,
      E_kiosk_class: hasKioskClass === true,
    };

    const report = {
      url: url.path,
      viewport: VP,
      hasKioskClass,
      bodyCursor,
      scrollbarWidth,
      toggle,
      navCount: navItems.length,
      navFirst: navItems.slice(0, 10),
      pass,
      passAll: Object.values(pass).every(v => v === true),
    };

    fs.writeFileSync(
      `${OUT_DIR}/${url.name}-report.json`,
      JSON.stringify(report, null, 2),
    );
    console.log(`\n=== ${url.name} ===`);
    console.log(JSON.stringify(report, null, 2));

    // Spec passes/fails based on acceptance criteria.
    expect(pass.A_nav, 'A: nav has expected item count').toBe(true);
    expect(pass.B_cursor_hidden, 'B: cursor hidden').toBe(true);
    expect(pass.C_scrollbar_hidden, 'C: scrollbar hidden').toBe(true);
    expect(pass.D_toggle_tap_target, 'D: toggle ≥ 40×80').toBe(true);
    expect(pass.E_kiosk_class, 'E: html.shell-kiosk set').toBe(true);
  });
}
