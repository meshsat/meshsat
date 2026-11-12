# Accessibility run, 12 November 2026: WCAG 2.1 AA

SIDN fonds project 261042, deliverable D5: contrast and programmatic labels at WCAG 2.1 AA, the international accessibility standard, across Dashboard, Messages, Map and Settings.

## Result

No axe-core violation, no form control without a programmatic label, no label pointing at a duplicated id and no undecided contrast below the threshold, on all 249 measured states of Dashboard, Messages (with Inbox and Compose), Map and Settings, in three shells. The same check on both field kits, against the build they run, also finds nothing.

| Run | Build | States | Findings |
|---|---|---|---|
| First measured run | `b17698aa` | 197 | 3,153 |
| After the label fixes | `007f46ae` | 197 | 2,406 (all contrast) |
| After the contrast fixes | `db28bfa9` | 197 | 0 |
| Dialogs and forms added, four checks of our own | `9805ee51` | 249 | 0 |
| Field kit tesseract, live | `9805ee51` | 127 | 0 |
| Field kit parallax, live | `9805ee51` | 127 | 0 |

The project application estimated about a thousand contrast failures by counting low-contrast colour classes in the source code. axe counts each failing element on each measured screen and state, so the two numbers measure different things.

## What was run

- **Build:** commit `9805ee511db4509e3da4aab81b9bc3d75eee7935`, pipeline 64361, job 627105, bundle `index-CALbt59D.js`. This is the build the field kits run; the live runs check the bundle name first.
- **Engine:** axe-core 4.13.0 through @axe-core/playwright 4.13.0, Playwright 1.59.1, Chromium 147.0.7727.15, in the image `mcr.microsoft.com/playwright:v1.59.1-noble`.
- **Rules:** the axe tags `wcag2a`, `wcag2aa`, `wcag21a` and `wcag21aa` (69 rules, listed in `results.json`).
- **Checks of our own**, because axe does not make them:
  - form controls with no programmatic label (axe accepts placeholder text as a name);
  - ids used twice where a label points at them;
  - contrast that axe leaves for review, measured directly. This run measured 876 such cases and found none below the threshold; 132 more are text over map tiles, which is out of scope;
  - JavaScript errors on the page while it is measured.
- **Proof that each state is real:** every state must show the element its name promises (an open dialog, the active tab, a form field) and keep at least 80 % of the visible text it had when the baseline was written. Otherwise the run fails, so a screen that broke or lost its data cannot pass by showing nothing.
- **Shells:**
  - `kiosk-operator`: 853x480, the panel of the field kits, `?kiosk=1&shell=operator`;
  - `kiosk-engineer`: the same panel in engineer mode, `?kiosk=1&shell=engineer`;
  - `desktop-engineer`: 1440x900, `?shell=engineer`.
- **Data:** synthetic API answers, and the same answers with every list empty, as on a new Bridge. No real data.
- **States:**
  - the header with its power menu and More menu;
  - the dashboard with its Customize tray, widget diagnostics and node detail;
  - the five Messages tabs, the compose panel, presets and the preset editor, an SMS conversation and the delivery detail;
  - Inbox, and Compose with its contact picker;
  - the map with a marker popup, the geofence list and the new-zone form;
  - every Settings tab of each shell, plus the SIM, SMS contact and OOB peer forms.

## The ratchet

The pipeline job `test:a11y` runs on every push to the Bridge and blocks the release when any count rises above `test/a11y/baseline.json`, which is zero for every state. It also fails when:
- a state that was measured is no longer measured;
- a state errors or shows less than its content floor;
- the axe version or rules change.

Two things prove the gate can fail:
- 22 unit tests of the comparison;
- a canary in every run, which injects known defects into the real app and must find all of them.

## On the field kits

Both kits were checked from outside on 12 November 2026, read-only (every request except a GET is refused in the browser), with the same states and the build they run. Only counts are kept (`kits.json`); the kits hold real data, so no screenshots or page content are published. The delivery detail opens from a text message, and neither kit had one in its recent traffic, so it was measured in the pipeline run only.

## Limits

Automated checks cover part of WCAG 2.1 AA; this is not a claim of full conformance. Not measured:
- hover and focus states (buttons with white text were given darker hover colours by hand);
- non-text contrast (1.4.11);
- the night (NVIS) theme;
- text drawn in a canvas or over map tiles;
- keyboard operation of clickable rows (open as a separate issue);
- other screens of the app.

## Reproduce

```sh
git checkout 9805ee511db4509e3da4aab81b9bc3d75eee7935
(cd web && npm ci && npm run build)
npm ci --prefix test/a11y
node test/a11y/run.mjs --dist web/dist --out a11y-results
```

## Files

- `results.json`: the pipeline's full results, every state and rule (SHA-256 `8e6224f3d56705d6a44f627d386cae02a50e2e0e1c553a6715c8dc899741dbf3`).
- `kits.json`: counts from the two field kits.
- `screens/`: the measured screens with synthetic data, one per screen on the kit panel.
