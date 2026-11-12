# Accessibility run (WCAG 2.1 AA)

SIDN deliverable D5 (MESHSAT-1843): contrast and programmatic labels at WCAG 2.1 AA on Dashboard, Messages (with Inbox and Compose), Map and Settings, shown by a published axe run, with a CI ratchet that fails on any increase against the baseline.

## What it measures

- **Engine:** axe-core 4.13.0 (through @axe-core/playwright 4.13.0) in Chromium from Playwright 1.59.1. Tags `wcag2a`, `wcag2aa`, `wcag21a`, `wcag21aa`. The executed rule ids are listed in every report.
- **Three counters of its own**, ratcheted like axe's rules:
  - `meshsat-unlabelled-control`: visible form controls with no programmatic label (no `<label>`, `aria-label`, `aria-labelledby` or `title`). axe accepts placeholder text as a name; WCAG 2.1 AA wants a label.
  - `meshsat-duplicate-id`: ids used twice where a `<label for>` or an ARIA reference points at them, so only the first element gets the name. axe 4.x no longer reports duplicate ids.
  - `meshsat-contrast-fallback`: contrast that axe leaves for review (text beside an icon, partly under the sticky header, a single character). Each case is measured directly: the text colour with its own and its ancestors' opacity against the background composited from its ancestors; below 4.5:1 (3:1 for large text) counts. Text over map tiles is skipped (its background is an image). The report gives the tally.
- **Three shells** (`states.mjs`):
  - `kiosk-operator`: the field kits' 853x480 panel;
  - `kiosk-engineer`: the same panel after someone taps ENG;
  - `desktop-engineer`: 1440x900.
- **States:** every state a person reaches on the six screens:
  - the header with its Power dialog and More menu;
  - the dashboard with its Customize tray, widget diagnostics and node detail;
  - the five Messages tabs, the compose panel, presets and the preset editor, an SMS conversation and the delivery detail;
  - Inbox, and Compose with its contact picker;
  - the map with a marker popup, the geofence list and the new-zone form;
  - every Settings tab of the shell, plus the SIM, SMS contact and OOB peer forms.
- **Scope of each state:**
  - The header and the bottom navigation are measured once per shell (`shell:*`).
  - Every other state is measured without them.
- **Two data profiles:**
  - `rich`: synthetic API answers;
  - `empty`: the same answers with every list empty, as on a new Bridge.
- **Out of scope:** axe best-practice rules, WCAG 2.2 and AAA, the night (NVIS) theme, hover and focus states, non-text contrast (1.4.11), text drawn in a canvas or over map tiles, and the jamming alert and token login overlays. Automated checks cover part of WCAG 2.1 AA, not all of it.

## Fixtures

`fixtures/rich/` holds one JSON answer per API path, made up and safe to publish:
- positions in open water off the coast;
- phone numbers in the +44 7700 900xxx drama range;
- zeroed IMEIs;
- node ids `!a11ceNNN`.

`fixtures.test.mjs` fails on anything that looks like real data. A path without a fixture answers 404, as an absent subsystem does on a Bridge; the report lists them.

## The ratchet

`baseline.json` holds, per state, the number of failing elements per rule. A run fails when:
- any count rises;
- a rule appears in a state that didn't have it;
- a new state has findings;
- a baseline state is no longer measured;
- a state errors;
- the axe version or tags change.

When counts fall, the run passes and writes `baseline.next.json`; commit that to lower the baseline. `--update-baseline` refuses to raise a count unless given `--allow-increase "<reason>"`, and keeps the reason in the file.

The canary proves the run can fail. Known defects are injected into the real app: low-contrast text, an input with only a placeholder, an `aria-label` on a plain `div`, a label whose id is used twice, and a one-character low-contrast count that axe cannot judge. They must be found, and a zero baseline must then fail. `ratchet.test.mjs` covers each rule above.

## Running it

Fixture mode needs only a built app. No server runs: the browser is answered from disk.

```sh
(cd web && npm ci && npm run build)
npm ci --prefix test/a11y
node --test test/a11y/ratchet.test.mjs test/a11y/fixtures.test.mjs
node test/a11y/run.mjs --dist web/dist --out a11y-results
node test/a11y/run.mjs --dist web/dist --update-baseline   # after a fix lowered the counts
```

Live mode browses a real kit read-only and records counts only. Every request that is not a GET is refused in the browser before it leaves. With `--dist`, it first checks the kit serves the same bundle.

```sh
node test/a11y/run.mjs --live parallax --dist web/dist --out a11y-kit
```

Reports in the output folder:
- `summary.md`;
- `results.json` (every finding with its element);
- `junit.xml`;
- `screens/` (fixture mode only);
- `baseline.next.json`.
