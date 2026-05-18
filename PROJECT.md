# Project Charter — MeshSat Bridge

> Component-scoped. Parent: `/home/claude-runner/gitlab/products/cubeos/docs/PROJECT.md` (Track B). CGC-grounded 2026-05-18.

## Role in the MeshSat family

`meshsat` (GitLab project 27) is the **Bridge** — the standalone Pi gateway that brokers messages between 10 transport types: Meshtastic LoRa, Iridium SBD (9603N), Iridium IMT (9704), Cellular SMS, ZigBee, MQTT, Webhooks, APRS, TAK/CoT XML, direct serial. Peers with `meshsat-android` + `meshsat-hub` over MQTT (meshsat-uplink/v1) + Reticulum + BLE.

## CGC-verified scope (2026-05-18)

- **529 files / 37672 functions / 1900 classes / 104 modules** — biggest of the MeshSat family
- 27 real internal/ packages
- Entry points: `cmd/meshsat/main.go` + `cmd/jspr-helper/main.go`
- Version 0.20.0
- 129 test files
- No CGO (CGC-verified — `grep -r "import \"C\""` returns 0)
- Reticulum-compatible routing layer with 9 cross-connected interfaces (per README)

## What this repo owns (CGC-verified packages)

| Package | Purpose |
|---|---|
| api/ | Local REST API |
| certpin/ | Outbound TLS pinning |
| channel/ | Channel registry + crypto |
| codec/ | SMAZ2 + canned codebooks + position codec + protocol version |
| compress/ | Compression dictionary (Article XII parity with meshsat-hub) |
| config/ | env-var loading |
| database/ | SQLite + migrations |
| dedup/ | Message deduplication |
| device/ | Connected-device registry |
| directory/ | Contacts + groups + dispatch policies |
| engine/ | Dispatcher + scheduler + telemetry |
| federation/ | Cross-bridge federation |
| gateway/ | Transport bridge layer |
| hemb/ | HeMB heterogeneous-media bonding |
| hubreporter/ | Bridge→Hub MQTT protocol (meshsat-uplink/v1) |
| keystore/ | Crypto key storage (Article IX) |
| pair/ | Android pair-protocol v1 |
| ratelimit/ | Token-bucket rate limiting |
| reticulum/ | Reticulum-compatible routing layer (9 interfaces) |
| routing/ | Per-rule message routing |
| rules/ | Access rules engine |
| spectrum/ | SDR spectrum scanning |
| sysinfo/ | System info exposure |
| timesync/ | NTP/RTC sync + GPS time |
| transport/ | Transport adapters (Meshtastic, Iridium, ZigBee, etc.) |
| types/ | Shared types |

## Constitutional inheritance

Inherits CubeOS project-level constitution + MeshSat sub-family. Component constitution adds 14 articles (CGO_ENABLED=0, single-Room-migration-per-version equivalent, Keystore-only keys, etc.).

## Source trace

- `meshsat/CLAUDE.md` (local-only)
- `meshsat/README.md` (CGC-confirmed)
- `meshsat/EXECUTION-PLAN.md` (9-phase plan)
- `meshsat/UX-MULTI-ACCESS-KIOSK-PAIRING.md` (kiosk + pair protocol design)
- Parent: `docs/PROJECT.md` Track B summary
