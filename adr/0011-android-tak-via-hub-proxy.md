# ADR-0011 — Android TAK/CoT via Hub proxy (Option B)

* Status: Accepted — codified after the fact 2026-05-17. Original decision document at `docs/ANDROID_TAK_DECISION.md` (created 2026-03-17). This ADR captures the same decision in the canonical `adr/` location.
* Date: 2026-03-17 (decision); 2026-05-17 (ADR recorded).
* Deciders: `ufwtqkgz@meshsat.net`
* Source document: `docs/ANDROID_TAK_DECISION.md`

## Context

The Android companion app needs TAK/CoT support so positions, telemetry, and SOS events originating on a paired Bridge (or on Android itself) reach an OpenTAKServer / ATAK ecosystem. Three options:

1. **Option A — Direct TAK from Android**: Android opens a persistent TCP/TLS CoT stream to OpenTAKServer (mirroring Bridge's `TAKGateway`).
2. **Option B — Hub-proxied TAK**: Android publishes to Hub MQTT topics; Hub's TAK gateway converts to CoT XML and forwards to OpenTAKServer.
3. **Option C — ATAK Plugin**: Extend the official `meshtastic/ATAK-Plugin` Java plugin to receive satellite-originated messages.

## Decision

**Option B — Hub-proxied TAK.**

Android sends position/SOS/telemetry to Hub via MQTT (over Tor or WireGuard). Hub's TAK gateway subscribes to `meshsat/+/position`, `meshsat/+/sos`, `meshsat/+/telemetry`, converts each to CoT XML, and forwards to OpenTAKServer. Android NEVER connects to TAK directly. Reverse direction (TAK → Android): Hub receives CoT events, converts to MQTT messages on `meshsat/{device_id}/tak/cot/in`, Android subscribes.

## Consequences

**Positive**
- Zero TAK-specific code on Android — leverages the MQTT channel Android already speaks for Hub integration.
- Hub's persistent TAK connection serves Bridge + Android nodes; one TCP connection to TAK server per Hub, not per Android.
- Battery-efficient — MQTT (with WebSocket keep-alive) is cheaper than a persistent TCP to TAK server.
- Hub handles CoT XML generation centrally — Go code already written and shared with Bridge.
- Simpler security — Android only needs MQTT credentials, not TAK server certs.
- Works even when Android cannot reach TAK server directly (behind NAT, no VPN to the TAK deployment).
- Same MQTT topics already carry the data needed for CoT generation:
  - `meshsat/{device_id}/position` → CoT PLI (`a-f-G-U-C`)
  - `meshsat/{device_id}/sos` → CoT emergency detail (`b-a`)
  - `meshsat/{device_id}/telemetry` → CoT `t-x-d-d`
  - `meshsat/{device_id}/mo/decoded` → CoT text/chat (`b-t-f`)

**Negative**
- Requires Hub to be online for TAK integration. **But TAK requires a server by definition — there is no "offline TAK"** — so this is an inherent constraint, not an Option B regression.
- Additional latency (Android → MQTT → Hub → TAK) vs direct Android → TAK. Acceptable for the operational TAK use case (CoT events are not real-time chat).

## Alternatives considered (recap)

- **Option A (direct TAK from Android)**: rejected — persistent TCP to TAK server drains mobile battery; TAK server reachability requires public IP or VPN on the mobile network; duplicates TAK connection management across Bridge + Android + Hub; CoT XML generation needs Kotlin reimplementation.
- **Option C (ATAK Plugin extension)**: rejected — ATAK plugin is Java + tightly coupled to ATAK's plugin API; MeshSat Android is a standalone gateway, not an ATAK plugin; would require running ATAK (200MB+) alongside MeshSat Android; plugin only handles Meshtastic protobuf, not MeshSat's internal message format; different architectural model.

## Future consideration

If a use case emerges where Android needs direct TAK connectivity without Hub (e.g. tactical mesh-only deployment with a local ATAK Mesh SA endpoint), a Kotlin TAK adapter can be implemented following the same pattern as the Go adapter. The decision to use Hub-proxy does not preclude this — it simply defers the Kotlin implementation until there is a concrete need.

## Compliance

- Android MUST publish position/SOS/telemetry to `meshsat/{device_id}/...` MQTT topics, NOT to a TAK server directly.
- Hub's TAK gateway MUST subscribe to the wildcarded topics (`meshsat/+/position` etc.) and emit CoT to whatever OpenTAKServer it's configured for.
- Reverse-direction TAK → Android MUST go via `meshsat/{device_id}/tak/cot/in`. Android subscribes per its `device_id`, not via wildcard.
- This ADR supersedes (and re-locates) `docs/ANDROID_TAK_DECISION.md`. The `docs/` file remains for historical context; new edits go in this ADR.
