# Requirements — UI Reshape (meshsat Phase 3, EXECUTION-PLAN §6.3)

Source: `EXECUTION-PLAN.md` §6.3 (10 stories S3-01..S3-10), `UX-AUDIT-AND-REDESIGN.md` §9. Depends on `spec/002-unified-directory/` (contacts) AND `spec/003-contact-aware-dispatcher/` (SendToRecipient).

Constitution invariants in scope: Vue 3 Composition API + Tailwind per repo conventions; existing component patterns under `web/src/`.

Reshape the 13-route SPA into a 5-item primary navigation (Compose / Inbox / Map / People / Radios) with everything else behind `⋮ More`. Operator vs Engineer modes — Operator default on kiosk + Android (≤1200px), Engineer default on desktop.

## Functional requirements

REQ-300: The system shall introduce an `operatorMode` boolean in the Pinia store backed by `localStorage['meshsat_operator_mode']`.

REQ-301: When the SPA loads and `window.innerWidth > 1200`, the system shall default `operatorMode=false` (Engineer); when `window.innerWidth <= 1200` OR the URL contains `?shell=kiosk` OR the User-Agent contains `CrKiosk/`, the system shall default `operatorMode=true`.

REQ-302: The system shall add an Operator/Engineer toggle to the SPA header.

REQ-303: The primary navigation shall expose exactly 5 items: Compose, Inbox, Map, People, Radios, plus a `⋮ More` overflow.

REQ-304: When the operator activates `⋮ More`, the menu shall expose the 13 Engineer-only views (Dashboard, Bridge, Interfaces, Passes, Topology, Settings, Audit, Help, About, plus any added by other Phases).

REQ-305: When the viewport width drops below 768px, the navigation shall switch to a bottom-tab bar rendering the 5 primary items.

REQ-306: When the viewport width is between 768px and the `lg` Tailwind breakpoint, the navigation shall hide text labels and render icons-only.

REQ-307: The system shall use `v-show` (NOT `v-if`) on every view affected by the Operator/Engineer toggle so the toggling preserves component state without re-mount.

REQ-308: The new `ComposeView.vue` shall provide a contact-picker (typeahead against `/api/directory/contacts?q=`), precedence chips for the 6 STANAG 4406 levels, bearer-availability preview, and a Send button that calls `POST /api/messages/send-to-contact`.

REQ-309: When the operator switches to Engineer mode within `ComposeView.vue`, the SPA shall reveal a per-bearer override row letting the operator pick exact bearers and strategy_override.

REQ-310: The new `InboxView.vue` shall replace `MessagesView.vue` and shall render bearer-coloured message bubbles: Mesh=blue, SMS=green, Iridium=amber, APRS=teal, Reticulum=violet.

REQ-311: The `InboxView.vue` shall render per-bearer delivery ticks: `·`=queued → `✓`=sent → `✓✓`=delivered → `✓✓`-blue=read.

REQ-312: When the operator clicks a delivery tick in `InboxView.vue`, the SPA shall open a per-bearer breakdown popover listing each bearer's status.

REQ-313: The new `PeopleView.vue` shall list contacts from `/api/directory/contacts` with search, filter by team/role, and a Trust dots leading indicator (0..3 dots).

REQ-314: The `PeopleView.vue` detail pane shall show the contact's addresses, group memberships, and dispatch policy, plus an Engineer-only keys section.

REQ-315: The new `RadiosView.vue` shall consolidate the existing Interfaces view into per-bearer status rows with health dot, signal bars, and a Details action linking to the existing Interfaces detail page.

REQ-316: The persistent status strip shall be visible on every view showing mesh/sat/cell/Hub/battery/GPS/sync indicators.

REQ-317: The Settings view shall split into 5 Operator-mode tabs (Radio, Channels, Position, Satellite, Cellular) and an Engineer-mode drawer exposing the remaining 12 tabs.

REQ-318: The Settings tab strip at viewport width ≤720px shall horizontally scroll with a fade-on-right indicator when there are off-screen tabs.

REQ-319: The system shall implement a Night/NVIS theme palette per MIL-STD-3009 NVIS Green A: `#000000` background, `#00FF41` primary text, `#FFB000` accent, `#FF0000` for emergencies only.

REQ-320: When the operator toggles Night/NVIS theme, the SPA shall write to `POST /api/system/backlight {value:32}` so the panel hardware dims simultaneously.

REQ-321: When the Night/NVIS theme is active, the SPA shall disable motion (transitions, animations) globally.

REQ-322: The SPA shall apply the IQ-70 label translation table from `UX-AUDIT-AND-REDESIGN.md §1.4` via `web/src/i18n/operator.en.json` + `web/src/i18n/engineer.en.json` so Operator vs Engineer labels render correctly.

REQ-323: When Engineer mode is active and the operator hovers over an Operator label, the SPA shall show a tooltip exposing the underlying Engineer term.

REQ-324: The SPA shall implement Engineer-mode keyboard shortcuts: `g c`→Compose, `g i`→Inbox, `g m`→Map, `g p`→People, `g r`→Radios, `n`→new message, `/`→search, `Esc`→cancel.

REQ-325: The keyboard shortcuts shall NOT fire when Operator mode is active, to avoid touch-typing collisions on the kiosk display.
