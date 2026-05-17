# Requirements — Compliance + Polish (meshsat Phase 6, EXECUTION-PLAN §6.6)

Source: `EXECUTION-PLAN.md` §6.6 (10 stories S6-01..S6-10).

Constitution invariants in scope: Article XIII (master-key envelope), Article XIV (parallel-dev gates), broader operator-anger rules from CLAUDE.md.

Hardens the SPA + backend against accessibility, security, and standards-compliance benchmarks: WCAG 2.1 AAA gate in CI, full USMTF template library (20+), MLS group encryption (RFC 9420 opt-in), FIPS-140-3 build target (BoringSSL), VPAT publication, Playwright operator-contract suite expansion, and operator-comfort improvements (team broadcast, map clustering, contextual help).

## Functional requirements

REQ-600: The CI pipeline shall add an `accessibility` stage running `axe-core` + `pa11y` against every primary view (Compose, Inbox, Map, People, Radios, Settings) AND shall fail on any WCAG 2.1 AAA violation.

REQ-601: The CI pipeline's `accessibility` stage shall produce an HTML report artifact per run for human review.

REQ-602: The Compose view shall offer a "Team broadcast" action that selects a `directory_group` and dispatches per the group's `DispatchPolicy` from spec/002.

REQ-603: When the operator picks a team broadcast group with no members, the SPA shall display an inline error AND shall NOT POST.

REQ-604: The Compose view shall expose 20+ USMTF templates beyond the 3 skeletons from spec/005 (SALUTE/MEDEVAC/SITREP), adding at minimum: NBC1, NBC4, SPOTREP, INTSUM, OPORD, FRAGO, WARNORD, AAR, JTAR, CHEMREP, MEDEVAC15, EOINCREP, PATROLREP, BDA, CALL_FOR_FIRE, ECHO, COMSPOT.

REQ-605: The MapView shall add cluster rendering via `leaflet-markercluster` so dense areas show counts instead of overlapping markers.

REQ-606: The system shall expose a contextual help tooltip on every Engineer-mode field in Settings describing the field's effect; no Engineer-mode field shall lack a tooltip.

REQ-607: The system shall implement optional MLS (RFC 9420) group encryption gated on a per-tenant feature flag `mls_enabled` defaulting to false.

REQ-608: When `mls_enabled=true` for a tenant, the system shall encrypt group messages per RFC 9420 AND the operator shall NOT be able to send unencrypted to group members.

REQ-609: The system shall add a FIPS-140-3 build target `make build-fips` that links against BoringSSL via `cgo` (only this build target uses CGO; default build remains CGO_ENABLED=0 per Constitution Article II).

REQ-610: The `make build-fips` target shall require an environment variable `MESHSAT_FIPS=1` to opt in AND shall print a clear notice when the FIPS build is selected.

REQ-611: The system shall publish a VPAT (Voluntary Product Accessibility Template) document under `docs/compliance/vpat.md` mapping every SPA view against EN 301 549 + Section 508 requirements.

REQ-612: The VPAT shall be regenerated automatically from axe-core findings via a `make vpat` target so it stays in sync with the actual accessibility posture.

REQ-613: The bridge shall add an Android-to-Android contact handoff via QR (parallel to the cross-bridge handoff from spec/006) for the case where two Android operators are in person AND want to share contacts without going through their bridges.

REQ-614: When the operator triggers the Android-to-Android handoff, the SPA shall present a QR with TTL=300 seconds embedding only the contact data + handoff source bridge pubkey (no bridge-to-bridge round trip).

REQ-615: The Playwright operator-contract suite shall add scenarios for every primary view: Compose, Inbox, People, Map, Radios (mirroring the kiosk-fitness specs from spec/008-kiosk-pwa-spa-hardening).

REQ-616: The Playwright operator-contract suite shall run in CI on every push AND shall produce video artifacts on failure for debugging.

REQ-617: The system shall publish accessibility metrics (`hub_accessibility_violations_total{view, severity}`) so the Hub-side Grafana dashboard can plot trends.

REQ-618: When the Compose view sends a "Team broadcast" to N recipients, the SendResult shall contain N grouped per-bearer delivery IDs so the InboxView can render the broadcast as one logical message with N sub-deliveries.
