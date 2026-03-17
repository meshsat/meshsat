# Android TAK/CoT Integration — Decision Document

_Created: 2026-03-17_

## Options Evaluated

### Option A — Direct TAK connection from Android

Android connects directly to OpenTAKServer via TCP/TLS CoT stream, mirroring the Bridge's TAKGateway.

**Pros:**
- Full autonomy — works without Hub
- Low latency — direct TCP connection
- Same interface pattern as Bridge

**Cons:**
- Requires persistent TCP connection — expensive on mobile battery
- TAK server must be reachable from mobile network (requires public IP or VPN)
- Duplicates TAK connection management (Android + Bridge + Hub all connecting independently)
- CoT XML generation needs Kotlin implementation (not trivial)

### Option B — Hub-proxied TAK (RECOMMENDED)

Android sends position/messages to Hub via MQTT (over Tor or WireGuard). Hub's TAK gateway forwards to OpenTAKServer as CoT events. Android never connects to TAK directly.

**Pros:**
- No TAK-specific code needed on Android — leverages existing MQTT channel
- Hub's persistent TAK connection serves both Bridge and Android nodes
- Battery-efficient — MQTT is cheaper than persistent TCP to TAK server
- Hub handles CoT XML generation centrally (Go code already written)
- Simpler security model — Android only needs MQTT credentials, not TAK server certs
- Works even when Android cannot reach TAK server directly (behind NAT, no VPN)

**Cons:**
- Requires Hub to be online for TAK integration
- Additional latency (Android → MQTT → Hub → TAK)
- No TAK when fully offline (but this is acceptable — TAK requires a server anyway)

### Option C — ATAK Plugin extension

Extend the official meshtastic/ATAK-Plugin to receive satellite-originated messages.

**Pros:**
- Leverages existing ATAK plugin ecosystem
- Native ATAK integration on Android

**Cons:**
- The ATAK plugin is Java, tightly coupled to ATAK's plugin API
- MeshSat Android is a standalone gateway, not an ATAK plugin
- Would require running ATAK alongside MeshSat Android — heavy (ATAK is 200MB+)
- The plugin only handles Meshtastic protobuf, not MeshSat's internal message format
- Different architectural model — plugin is ATAK-centric, MeshSat is channel-agnostic

## Decision: Option B — Hub-Proxied TAK

**Rationale:**

1. TAK requires a server by definition. There is no "offline TAK". If the TAK server is unreachable, TAK integration is impossible regardless of which option we choose.

2. Android already has MQTT capability planned (the Hub MQTT namespace `meshsat/{device_id}/...` is designed for this). Adding TAK via Hub is zero marginal code on Android.

3. The Bridge already implements the TAK gateway (tak.go). The same binary runs on Hub. Reusing the Go TAK implementation on Hub avoids duplicating CoT XML generation in Kotlin.

4. The Hub MQTT topics already carry all the data needed for CoT generation:
   - `meshsat/{device_id}/position` → CoT PLI (a-f-G-U-C)
   - `meshsat/{device_id}/sos` → CoT emergency detail
   - `meshsat/{device_id}/telemetry` → CoT t-x-d-d
   - `meshsat/{device_id}/mo/decoded` → CoT text/chat

5. Battery: a persistent TCP connection to a TAK server drains mobile battery significantly more than periodic MQTT publishes.

## Implementation Path

1. **No Android code changes required for TAK** — Android publishes to Hub MQTT topics (position, SOS, telemetry), which it will do regardless of TAK integration.

2. **Hub receives MQTT messages and generates CoT** — Hub's TAK gateway subscribes to `meshsat/+/position`, `meshsat/+/sos`, `meshsat/+/telemetry` and converts each to CoT XML for the TAK server connection.

3. **Reverse direction (TAK → Android)** — Hub's TAK gateway receives CoT events, converts to MQTT messages on `meshsat/{device_id}/tak/cot/in`, which Android subscribes to and displays as incoming messages.

## Future Consideration

If a use case emerges where Android needs direct TAK connectivity without Hub (e.g., tactical mesh-only deployment with a local ATAK Mesh SA endpoint), a Kotlin TAK adapter can be implemented following the same pattern as the Go adapter. The decision to use Hub-proxy does not preclude this — it simply defers the Kotlin implementation until there is a concrete need.
