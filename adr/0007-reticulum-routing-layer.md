# ADR-0007 — Reticulum-compatible routing layer with 9 cross-connected interfaces

* Status: Accepted — codified after the fact 2026-05-17. Shipped: 9/9 interfaces wired; RNS interop verified 2026-03-25 (MESHSAT-199, MESHSAT-336).
* Date: Originally decided 2025-Q4 (v0.3.0 line); recorded as ADR 2026-05-17.
* Deciders: `ufwtqkgz@meshsat.net`
* Source documents: `.claude/rules/reticulum-routing.md`, `README.md` L32–L36

## Context

Field operators need messages to flow between **any pair** of available bearers — a Meshtastic node sending text to an APRS station; an Iridium MO arriving and fanning to TAK; a ZigBee sensor event reaching the operator's phone via cellular. Hard-coded N×N routing across 9 bearer types is unmaintainable, and a custom routing protocol would re-invent identity, addressing, link establishment, link state, and pathfinding from scratch.

[Reticulum Network Stack](https://reticulum.network) (RNS) is an open networking protocol designed for "lossy, low-bandwidth, low-cost networks." It provides Ed25519 identity, X25519-derived link keys with forward secrecy, announce-based topology discovery, and small (~500-byte MTU) packets — designed for exactly the bearer mix the Bridge has. Adopting RNS gives MeshSat:

- A pre-existing, documented wire format.
- Cross-interop with the Python `rns` reference implementation.
- A community ecosystem (Reticulum-based mesh radio, Nomad messaging app).
- Identity primitives we'd have to invent anyway.

## Decision

The Bridge implements a **Reticulum-compatible routing layer** with **9 cross-connected interfaces**, structured as two packages:

| Package | Role |
|---|---|
| `internal/reticulum/` | Wire format library — packet marshal/unmarshal, identity, announce, ECDH link, HKDF-SHA256 + AES-256-CBC+HMAC encryption, HDLC framing for serial/TCP interop, resource transfer (chunked + bitmap + SHA-256 verify) |
| `internal/routing/` | High-level logic — app-level identity, announce relay (with bandwidth limiting + dedup), link state machine, keepalive, per-interface bandwidth allocation, destination table, transport node, pathfinder |

### Registered Reticulum interfaces — all 9 wired

| Interface | Cost | MTU | Underlying transport |
|---|---|---|---|
| `mesh_0` | free | 230 B | Meshtastic LoRa, PRIVATE_APP portnum 256 |
| `tcp_0` | free | 65535 B | TCP/HDLC for RNS interop (dynamic peers via UI; `MESHSAT_TCP_LISTEN`) |
| `iridium_0` | $0.05 | 340 B | Iridium SBD (RockBLOCK 9603N) via SatInterface |
| `iridium_imt_0` | $0.05 | 100 KB | Iridium IMT (RockBLOCK 9704) via SatInterface |
| `ax25_0` | free | 256 B | AX.25/APRS via KISS TNC (bundled Direwolf) |
| `mqtt_rns_0` | free | 65535 B | MQTT raw binary pub/sub (`MESHSAT_MQTT_RETICULUM_BROKER`) |
| `sms_0` | $0.01 | ~140 B | Cellular SMS via CellularGateway wrap (MESHSAT-404) |
| `zigbee_0` | free | ~100 B | ZigBee mesh via ZigBeeGateway wrap (MESHSAT-405) |
| `ble_0` | free | 500 B | BLE GATT via BlueZ D-Bus, SAR segmentation (MESHSAT-406, shipped 2026-04-15) |

### Transport node + pathfinder behavior

- **InterfaceRegistry** manages all 9 interfaces, exposes `Send()` and `Floodable()` filtering.
- **TransportNode** does cross-interface packet forwarding with a 30-minute route TTL and cost-aware path selection (free bearers preferred before paid).
- **PathFinder** is flooding-based for unknown destinations, but ONLY on `floodable=true` interfaces — paid bearers (iridium, sms) default to `floodable=false` so the Bridge doesn't burn $0.05 per packet hunting for a route.
- **Bridge is a real Reticulum endpoint** — it handles link requests, proofs, and data locally, not just relay.

### Hub telemetry, fully populated

Hub health payload includes route count, link count, announces relayed — all sourced from `internal/routing/` counters.

## Consequences

**Positive**
- Bridge interops with stock Python `rns` over TCP (verified 2026-03-25 — Python RNS 1.1.4 connects to `mule01:4242`, HDLC frames accepted, announces exchanged bidirectionally).
- Adding a new bearer is one new file implementing `ReticulumInterface` + one registration in `main.go`.
- Cost-aware routing means free bearers exhaust before paid — no per-packet operator surprise.
- 284 tests across wire edge cases, TCP multi-node, and E2E scenarios (MESHSAT-279).
- Future interoperability with Reticulum-ecosystem nodes (Nomad messenger, Sideband, dedicated RNS mesh radios) for free.

**Negative**
- The Reticulum wire format is not an Internet standard — we depend on the upstream project's stability. Mitigation: we own a vetted in-repo implementation (`internal/reticulum/`) so an upstream regression doesn't break us.
- Small MTU (500 B) constrains application payloads. Mitigation: resource transfer (`internal/routing/resource.go`) does chunked reliable delivery with bitmap + SHA-256 verify, abstracting the MTU constraint from callers.
- Adds an Ed25519 identity surface alongside Bridge's existing master-key keystore (Article XIII). Two crypto worlds to keep mentally separate. Mitigation: routing identity is persisted in DB (`routing_identities` table v24), wrapped just like keystore entries are.

**Forward direction**
- Reticulum will become the de-facto cross-platform router for MeshSat as Android (MESHSAT-63) and Hub (MESHSAT-199 future) gain RNS identity. Currently Bridge is the only RNS-native node in the mesh.
- The HeMB protocol (ADR-0008) sits **below** Reticulum (or alongside via TUN-wrap), splitting per-packet across bonded bearers. Reticulum + HeMB compose cleanly.

## Alternatives considered

- **Custom hand-rolled cross-bearer router**: rejected — re-invents identity, link state, announce, pathfinding. Zero leverage of community ecosystem.
- **IP-over-everything (e.g., bonded WireGuard tunnels)**: rejected — assumes all bearers carry IP (Iridium SBD and AX.25 do NOT); WireGuard adds tunnel-setup latency unsuitable for satellite handshake economics.
- **Pure Meshtastic protocol cross-bearer**: rejected — Meshtastic protocol is LoRa-shaped (small fixed packets, mesh-RF assumptions); doesn't generalize to TCP/MQTT/satellite without contortion.
- **Babel / OLSRv2 routing protocols**: rejected — IP-routing protocols, not applicable to bearer-agnostic small-packet routing across high-latency links.

## Compliance

- New bearer interfaces MUST implement `ReticulumInterface` in `internal/routing/iface.go`; registration in `main.go` is the ONLY way they're surfaced to the routing layer.
- A new bearer with `cost > 0` MUST default to `floodable=false` — operator can override in UI with explicit warning.
- Cross-component changes to wire format require coordinated changes to `internal/reticulum/` AND verification against the Python `rns` reference (the TCP interop test catches regressions automatically).
- HDLC framing constants (FLAG=0x7E, ESC=0x7D) MUST match Reticulum spec — don't redefine.
- All Reticulum identities persisted in `routing_identities` table (schema v24); never derived purely in-memory after first boot.
- Hub telemetry MUST keep populating route count + link count + announces relayed — this is the operator's visibility into routing health.
