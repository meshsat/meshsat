# Constitution — MeshSat Bridge

Inherits CubeOS project-level constitution + meshsat-family conventions. Component-specific Articles below. CGC-grounded 2026-05-18.

## Article C-I — `CGO_ENABLED=0` everywhere

The system shall NEVER `import "C"` (CGO_ENABLED=0 mandatory). CGC-verified: grep returns 0 hits across the repo. SQLite via `modernc.org/sqlite`.

## Article C-II — Reticulum identity is the mesh root of trust

The system shall use the Reticulum Ed25519 identity (`internal/reticulum/`) as the canonical mesh-side identity. Identity rotation requires explicit operator action — automatic rotation is forbidden.

## Article C-III — SMAZ2 dictionary byte-for-byte parity with meshsat-hub

The system shall maintain `internal/compress/dict_meshtastic.go` byte-for-byte identical to `meshsat-hub/internal/compress/`. Drift = decompression failures for every cross-node message.

## Article C-IV — Keystore-only crypto keys

The system shall store all crypto private keys in `internal/keystore/`. Raw key bytes NEVER appear in SharedPreferences-equivalent, Room columns, JSON exports, log statements, or backup payloads.

## Article C-V — meshsat-uplink/v1 protocol parity with Hub

The system shall keep `internal/hubreporter/protocol.go` in lockstep with `meshsat-hub/internal/protocol/protocol.go` via the `ProtocolVersion` constant. Breaking changes require a new `meshsat-uplink/v2` namespace.

## Article C-VI — 10 transport adapters, each in its own package under `internal/transport/`

The system shall isolate each transport adapter (Meshtastic, Iridium SBD/IMT, Cellular SMS, ZigBee, MQTT, Webhooks, APRS, TAK, direct serial) in its own subpackage. Cross-adapter coupling must go via the routing engine (`internal/routing/`).

## Article C-VII — Reticulum 9-interface routing core

The system shall maintain the Reticulum-compatible routing layer with 9 cross-connected interfaces (per README). Adding a 10th interface requires Operator review + ADR.

## Article C-VIII — Per-rule routing + failover groups + transform pipelines

The system shall route messages via the rules engine (`internal/rules/`) supporting: per-rule filtering, object groups, failover groups, transform pipelines. Defaults: implicit deny.

## Article C-IX — Pair protocol v1 single-use QR (`internal/pair/`)

The system shall implement the pair protocol v1 per `UX-MULTI-ACCESS-KIOSK-PAIRING.md` §4: single-use QR shown on the 7" touch display after operator-armed physical touch; encodes ECDH+CSR challenge; bridge mints client cert (90-day) + JWT (90-day).

## Article C-X — Standalone-Docker deployment

The system shall run as a standalone Docker container on any Linux machine with USB-connected devices. No cloud dependencies. No subscriptions beyond satellite/cellular plan.

## Article C-XI — Local REST API on 127.0.0.1 (Bridge-local automation)

The system shall expose `internal/api/` REST endpoints bound to 127.0.0.1 only — for local automation / scripting on the Bridge itself. Operator-facing UI goes via Hub or paired Android.

## Article C-XII — Reticulum mesh address never an IP

The system shall reference mesh peers by Reticulum identity (32-byte hash), NEVER IP. IPs are transport-implementation details.

## Article C-XIII — Multi-bridge federation via Hub relay (Phase 9)

The system shall, for multi-bridge deployments behind NAT, federate via Hub WebSocket relay (`internal/federation/`). Direct Reticulum P2P preferred where reachable; Hub-relay is the fallback.

## Article C-XIV — Parallel-dev workflow override

Same as the CubeOS-family-wide ADR (parent ADR-0008). merge/<feature_id> branches + 1 MR per feature + auto-delete on merge.
