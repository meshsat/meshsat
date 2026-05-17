# Design — UI Reshape (Phase 3)

## Goal

The current 13-route nav buries Compose 4 clicks deep + assumes operators know Engineer-mode vocabulary. Phase 3 collapses to 5-primary + Engineer-drawer, plus introduces Operator/Engineer modes so kiosk + Android default to large-target Operator UI while desktop browser defaults to full-power Engineer.

Reference: `UX-AUDIT-AND-REDESIGN.md §9` for full design rationale; this spec captures the implementation contract.

## Component layout

```
web/src/
├── stores/meshsat.js            ← gains operatorMode + setOperatorMode
├── i18n/operator.en.json        ← IQ-70 simplified labels
├── i18n/engineer.en.json        ← technical labels (current text)
├── views/
│   ├── ComposeView.vue          ← NEW — money screen
│   ├── InboxView.vue            ← NEW — replaces MessagesView
│   ├── PeopleView.vue           ← NEW — replaces NodesView contact section
│   ├── RadiosView.vue           ← NEW — consolidates Interfaces
│   ├── DashboardView.vue        ← existing — visible only in Engineer
│   ├── SettingsView.vue         ← existing — recategorized into Operator/Engineer tabs
│   └── ...                      ← all other existing views Engineer-only via v-show
├── components/
│   ├── PrimaryNav.vue           ← NEW — 5-item nav + ⋮ More
│   ├── ModeToggle.vue           ← NEW — Operator/Engineer switch
│   ├── StatusStrip.vue          ← NEW — persistent header
│   ├── NvisThemeToggle.vue      ← NEW — dim + backlight integration
│   └── ContactPicker.vue        ← NEW — typeahead used by ComposeView
└── composables/
    └── useShortcuts.ts          ← NEW — Engineer-mode keybinds
```

## Mode-toggle UX (REQ-300..302)

- Default: viewport-width-based + URL/UA hints (REQ-301).
- Toggle: header button shows current mode + icon; click flips.
- Persistence: `localStorage` so the choice survives reloads.
- Engineer reveal: Settings → Show Engineer Mode? PIN-protected in v2 for shared-device deployments (out of scope for v1 of this phase).

## NVIS theme (REQ-319..321)

The NVIS palette is for night operations — operators with NV goggles can read the SPA without bloom. Companion behavior: the SPA POSTs to `/api/system/backlight` (existing endpoint from Phase 7 kiosk plumbing) to dim the panel simultaneously. Motion disabled to prevent NV-distracting screen updates.

## Keyboard shortcuts (REQ-324 + REQ-325)

Engineer-mode only. The `g <letter>` two-key sequence is gmail-style — discoverable + low collision risk. Touch-typists in Operator mode (kiosk OSK) would accidentally trigger these otherwise; REQ-325 gates on mode.

## Compose flow (REQ-308 + REQ-309)

Operator:
1. ContactPicker → search "alice" → pick.
2. Precedence chip → "Routine" / "Priority" / "Flash" / etc.
3. Bearer-availability preview shows live: `Mesh ●  SMS ●  Iridium ⚠ (4 €)`.
4. Send → `POST /api/messages/send-to-contact` (from spec/003).

Engineer (after toggle):
- Same flow + a per-bearer override row letting operator force `strategy_override=ORDERED_FALLBACK` with custom bearer list.

## What this phase does NOT do

- Trust dots leading indicator → defined here (REQ-313) but the trust logic lives in `spec/005-trust-sidc-sos/`.
- USMTF templates → `spec/005-trust-sidc-sos/`.
- Kiosk OS setup → `spec/008-kiosk-pwa-spa-hardening/`.
- SOS button → status strip placement here (REQ-316), behavior in `spec/005-trust-sidc-sos/`.
