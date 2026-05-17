# Requirements — Kiosk + PWA + SPA Hardening (meshsat Phase 7, EXECUTION-PLAN §6.7)

Source: `EXECUTION-PLAN.md` §6.7 (16 stories S7-01..S7-16), `UX-MULTI-ACCESS-KIOSK-PAIRING.md` §2-3 (kiosk + SPA fitness).

Constitution invariants in scope: Article X (Pi 5 field-kit hardware: PSU_MAX_CURRENT=5000, UART boot fix), Article XI (single trusted container), `meshsat/adr/0012-pi5-field-kit-hardware-contract.md`.

Brings the Pi Touch Display 2 kiosk online + installs the SPA as a PWA + closes 16 concrete SPA-fitness gaps so 720×1280 kiosk + 360-wide Android + ≥1200 desktop all work without per-shell branching.

## Functional requirements

REQ-700: The Ansible playbook `field-kit.yml` shall append `dtoverlay=vc4-kms-v3d` and `dtoverlay=vc4-kms-dsi-ili9881-7inch,rotation=90` to `/boot/firmware/config.txt` of every field-kit Pi.

REQ-701: When the Pi reboots with the dtoverlay lines in place, the Pi 5 + Touch Display 2 combination shall present a labwc Wayland session at 1280×720 landscape.

REQ-702: The field-kit Pi shall have a dedicated `kiosk` user with `/usr/sbin/nologin` shell and `loginctl enable-linger kiosk` enabled.

REQ-703: The system shall configure `greetd` with `initial_session = { command = "labwc", user = "kiosk" }`.

REQ-704: The system shall install a systemd user unit `meshsat-kiosk.service` under `/home/kiosk/.config/systemd/user/` that launches Chromium in kiosk mode against `http://localhost:6050/`.

REQ-705: The systemd unit shall include `Restart=always` + `RestartSec=5` so the kiosk recovers within 5 seconds of a crash.

REQ-706: The systemd unit shall include an `ExecStartPre` that polls `http://localhost:6050/healthz` and waits until the bridge backend responds before launching Chromium.

REQ-707: The system shall install a Chromium policy file at `/etc/chromium/policies/managed/meshsat-lockdown.json` (and the `/etc/chromium-browser/...` mirror) restricting URLAllowlist to `localhost:6050` only, disabling devtools, passwords, autofill, translate, printing.

REQ-708: The system shall configure `swayidle` to dim the backlight to 32/255 after 10 minutes idle and restore to 200/255 on touch.

REQ-709: The system shall grant the kiosk user passwordless sudo scoped to ONLY the backlight sysfs path via `/etc/sudoers.d/kiosk-brightness`.

REQ-710: The system shall publish a PWA manifest at `web/public/manifest.json` declaring name, icons, display=fullscreen, orientation=landscape, theme + background colors.

REQ-711: The SPA shall register a service worker at `/sw.js` providing offline-first shell caching + network-first API passthrough.

REQ-712: The service worker shall expose a `/sw/reset` self-unregister endpoint so the operator can recover from a bad SW release.

REQ-713: The system shall include `web/public/icon-192.png`, `icon-512.png`, and `icon-maskable.png` referenced by `manifest.json`.

REQ-714: The Hub backend shall serve `/manifest.json`, `/sw.js`, and the icon files with correct MIME types and `Cache-Control: no-cache` on the SW.

REQ-715: The system shall include an in-SPA JS on-screen keyboard via `simple-keyboard` npm package that auto-shows on `pointer:coarse` AND no hardware keyboard detected.

REQ-716: The on-screen keyboard shall respect `inputmode` attributes (numeric pad for `inputmode=numeric`, etc.).

REQ-717: The `DashboardView.vue` widget reorder shall add `@touchstart/@touchmove/@touchend` handlers mirroring the HTML5 drag logic with `touch-action: none` on the drag handle.

REQ-718: The `SettingsView.vue` and `RadioConfigView.vue` 17-tab strip shall wrap in horizontally scrollable container with `snap-x snap-mandatory` and a fade-on-right indicator when scrollable.

REQ-719: The `DashboardView.vue` modals shall change `max-w-2xl` to `max-w-full sm:max-w-2xl` so modals fit at 720 wide.

REQ-720: The SPA shall add `env(keyboard-inset-height)` padding to the sticky header when `VirtualKeyboard.isSupported`.

REQ-721: The `JammingAlertModal.vue` shall add `bottom: env(keyboard-inset-height, 0)` so the OSK does not occlude the modal.

REQ-722: The SPA shall enforce a 48px (`h-12`) minimum tap-target floor on every primary action button and a 40px (`min-h-10`) floor on every secondary button.

REQ-723: The `InterfacesView.vue` rule-form checkboxes shall wrap each checkbox in a 40px-tall label so the full row is a tap target.

REQ-724: The `DashboardView.vue` grid shall change `md:grid-cols-2 lg:grid-cols-3` to `grid-cols-1 sm:grid-cols-2 lg:grid-cols-3` so 400px viewports render single-column.

REQ-725: The SPA shall declare semantic input attributes on every input element: `inputmode="tel"` for phone, `inputmode="numeric"` for PIN, `inputmode="decimal"` for lat/lon, `inputmode="text" enterkeyhint="send"` for Compose.

REQ-726: The SPA shall default every textarea to `rows="2"` and `resize-y sm:resize-none` so portrait phones do not see 8-row textareas eating the screen.

REQ-727: The SPA shall add `:focus-visible` styles for keyboard-only focus outlines on `web/src/style.css`.

REQ-728: The `InboxView.vue` and `DashboardView.vue` shall support pull-to-refresh via `@touchmove` overscroll detection → fetch.

REQ-729: The field-kit Pi onboarding script shall verify Pi 5 EEPROM contains `PSU_MAX_CURRENT=5000` and `cmdline.txt` lacks `console=serial0,115200` per Constitution Article X.

REQ-730: The kiosk shall complete a `systemctl --user restart meshsat-kiosk.service` nightly at 03:00 local time to prevent Chromium memory creep over 72+ hour uptimes.
