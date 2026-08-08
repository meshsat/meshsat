# Project Charter — MeshSat Bridge

> Component-scoped. Parent: `/home/claude-runner/gitlab/products/cubeos/docs/PROJECT.md` (Track B). CGC-grounded 2026-05-18.

## Role in the MeshSat family

`meshsat` (GitLab project 27) is the **Bridge** — the standalone Pi gateway that brokers messages
across **eight transport bearers, wired as nine Reticulum interfaces** in `cmd/meshsat/main.go`:
Meshtastic LoRa (`mesh_0`), Iridium (`iridium_0` SBD on the 9603N and `iridium_imt_0` IMT on the
9704 — one bearer, two modems and two protocols), APRS/AX.25 (`ax25_0`), Cellular SMS (`sms_0`),
ZigBee (`zigbee_0`), BLE GATT (`ble_0`), TCP (`tcp_0`) and MQTT (`mqtt_rns_0`). TAK/CoT and
webhooks are supported as **integrations**, not bearers. Peers with `meshsat-android` +
`meshsat-hub` over MQTT (meshsat-uplink/v1) + Reticulum + BLE.

## Measured scope (recounted from the tree 2026-08-08)

Superseding the CGC-derived block dated 2026-05-18, which was wrong by roughly 8× on functions
and counted "classes" in a language that has none. Every figure below is reproducible with the
command beside it.

- **404 Go files** — 275 non-test (`find . -name '*.go' -not -name '*_test.go'`) + 129 test
- **4.713 Go function declarations** — 3.067 non-test, 1.646 in test files
  (`grep -hE '^func '`). The previous "37672 functions" was out by a factor of eight.
- **Go has no classes.** The previous "1900 classes" described nothing.
- **26 packages under `internal/`** (`find internal -maxdepth 1 -mindepth 1 -type d`), not 27
- **126.747 total Go lines**, of which 85.995 are non-test
- **1.342 test functions** across 129 test files, gated in CI on main
- **23 Vue views** under `web/src/views/`
- Entry points: `cmd/meshsat/main.go` + `cmd/jspr-helper/main.go`
- No CGO — the tree contains no C imports and builds with `CGO_ENABLED=0`

Recount before quoting any of these externally — the audit found stale counts in every
repository's README, CLAUDE.md and PROJECT.md. Do not copy a number forward without rechecking.

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
