# Requirements — Multi-Bridge + NAT Traversal + Hub Relay (meshsat Phase 9, EXECUTION-PLAN §6.9)

Source: `EXECUTION-PLAN.md` §6.9 (9 stories S9-01..S9-09). Cross-cuts `meshsat-android/spec/003-pair-shell/` for the Android UI surface. Depends on `spec/001-pair-protocol/` (pairing) + `spec/006-android-directory-sync/` (snapshot).

Constitution invariants in scope: Article X (Reticulum routing layer — 9 interfaces, RNS interop), Article XV (workflow exception). The Hub-relay layer crosses into `meshsat-hub/`.

Closes the 3-tier NAT-traversal story: LAN-direct (works on local WiFi) → Reticulum-fallback (works over LoRa/Iridium when off-LAN) → Hub WebSocket relay (works whenever the kit has any internet). Plus the Android multi-bridge UI for operators with several kits.

## Functional requirements

REQ-800: The Android app shall display a multi-bridge tile view at first-load showing every entry in the `paired_bridges` Room DB with a health dot (green=LAN reachable, amber=RNS only, red=unreachable).

REQ-801: The Android app shall support swipe-to-switch navigation between paired bridges.

REQ-802: When the operator switches to a different bridge, the SPA shall load that bridge's data scope (contacts, messages, settings) within 2 seconds.

REQ-803: The Android app shall publish unified notifications across all paired bridges, with each notification tagged with the source `bridge_id`.

REQ-804: When the operator taps a notification, the app shall navigate to the source bridge's relevant view (Inbox for a new message, Compose for a draft).

REQ-805: The Android app shall run a foreground service that holds an SSE connection to each paired bridge for real-time event delivery.

REQ-806: When the LAN connection to a bridge fails AND the bridge has a `rns_dest` in the paired record, the Android app shall fall back to a Reticulum link via the bridge's dest hash.

REQ-807: The Reticulum fallback shall carry HTTP/2-over-RNS one request/response at a time.

REQ-808: When the LAN connection AND the Reticulum link both fail AND the bridge has a Hub-relay configuration, the Android app shall fall back to a Hub WebSocket relay tunnel.

REQ-809: The Hub WebSocket relay shall expose a tunnel on `meshsat/relay/{bridge_id}/{client_id}` providing bidirectional opaque-bytes transport between the Android client and the bridge.

REQ-810: The Hub WebSocket relay shall be end-to-end mTLS-encrypted between the Android client and the bridge — the Hub shall NOT see plaintext.

REQ-811: The Hub relay shall enforce per-`client_id` isolation so one paired client cannot send to another client's tunnel.

REQ-812: The Hub relay shall rate-limit each `client_id` to 100 requests/min by default.

REQ-813: The bridge shall subscribe to its `meshsat/relay/{bridge_id}/+` topic AND shall forward inbound relay frames to its local HTTP server as if they came from a paired Android over LAN.

REQ-814: The bridge health payload shall include `directory_version` AND `last_sync` timestamps so Hub fleet views can detect drift.

REQ-815: When an Android client cannot reach the bridge via any of the 3 transports (LAN, RNS, Hub), the app shall display a "bridge unreachable — last seen at HH:MM" banner with the timestamp from the last successful contact.

REQ-816: The Android app shall log every transport fallback transition (LAN → RNS, RNS → Hub) to a local diagnostic ring buffer of the last 100 events.

REQ-817: The system shall provide an end-to-end integration test exercising the off-LAN scenario: Android phone (cellular only) controls a CG-NAT-bridge via the Hub WebSocket relay.

REQ-818: The Hub WebSocket relay shall expose a Prometheus counter `hub_relay_tunnels_active{bridge_id}` showing the current count of active tunnels per bridge.

REQ-819: The Hub WebSocket relay shall expose `hub_relay_bytes_total{bridge_id, direction}` showing total transported bytes per direction per bridge.

REQ-820: When the Android app starts a new pairing per `spec/001-pair-protocol/`, the resulting `paired_bridges` row shall record `rns_dest` + `hub_relay_url` so all 3 transports are immediately available.
