# Design — Kiosk + PWA + SPA Hardening (Phase 7)

## Goal

Operators want to bolt a 7-inch touchscreen onto the field kit lid and use the bridge from there with no laptop. Phase 7 delivers that across three layers:

1. **OS-level kiosk** — Pi Touch Display 2 + labwc Wayland + Chromium kiosk + locked-down policy.
2. **PWA** — same SPA installable on any browser as a fullscreen offline-capable app.
3. **SPA fitness** — 16 concrete touch-friendliness fixes so 720×1280 kiosk + 360-wide Android + ≥1200 desktop all work without per-shell branching.

Reference: `UX-MULTI-ACCESS-KIOSK-PAIRING.md` §2-3 for full rationale.

## Kiosk stack

```
Pi 5 + Pi Touch Display 2 (DSI1, rotation=90 → 1280×720 landscape)
  └─ greetd autologin → labwc Wayland session as user `kiosk`
       └─ systemctl --user meshsat-kiosk.service
            └─ chromium-browser --kiosk --app=http://localhost:6050/
            └─ Chromium policy /etc/chromium/policies/managed/meshsat-lockdown.json
                 (URLAllowlist=localhost only, no devtools, no autofill, no print)
            └─ swayidle backlight dim (32/255 after 10 min)
            └─ in-SPA simple-keyboard OSK (NOT compositor OSK — labwc bug)
```

## Why labwc + Chromium (not Xorg, not Cog/WPE)

Per `UX-MULTI-ACCESS-KIOSK-PAIRING.md §2.4`:
- labwc is Pi OS Bookworm default + actively maintained; X11 is legacy
- Chromium has enterprise-policy framework + hardware acceleration + PWA-install support
- Cog/WPE is rejected for now (8 GB Pi has memory headroom for Chromium's ~800 MB baseline)

## Why in-SPA JS OSK (not squeekboard)

Per `UX-MULTI-ACCESS-KIOSK-PAIRING.md §2.8`: squeekboard under Chromium-kiosk on labwc has a known layering bug. simple-keyboard (100 kB npm) embedded inside the SPA always works + matches the theme + auto-detects hardware keyboard.

## SPA fitness — 16 concrete fixes

| # | File | Fix |
|---|---|---|
| S7-07 | DashboardView.vue:57-91 | touch-drag widget reorder |
| S7-08 | SettingsView.vue, RadioConfigView.vue | 17-tab horizontal scroll with fade indicator |
| S7-09 | DashboardView.vue modals | max-w-full sm:max-w-2xl |
| S7-10 | App.vue + JammingAlertModal.vue | env(keyboard-inset-height) handling |
| S7-11 | all primary controls | h-12 (48dp) tap-target floor |
| S7-12 | InterfacesView.vue | checkbox row-as-target |
| S7-13 | DashboardView.vue:1217 | 400px = 1 col responsive grid |
| S7-14 | all forms | inputmode + enterkeyhint semantic attrs |
| S7-15 | all textareas | rows="2" + resize-y sm:resize-none |
| S7-16 | style.css + InboxView.vue + DashboardView.vue | :focus-visible + pull-to-refresh |

## Nightly restart (REQ-730)

Chromium's kiosk-mode RAM creep is real over 72+ hours. Nightly `systemctl --user restart` is the cheap hedge — operator sees a 5-second SPA reload at 03:00 local time. Acceptable for a field-kit deployment where nobody is touching the panel at 03:00 anyway.

## Out of scope

- Pair-protocol UI (covered by `spec/001-pair-protocol/`).
- Multi-bridge UI on Android (covered by `spec/009-multi-bridge-nat/`).
- Hub-relay browser-as-remote-control page (covered by `spec/009-multi-bridge-nat/`).
- Cross-vendor display drivers (only Pi Touch Display 2 in scope).
