# Constitution — MeshSat Bridge (project 27)

19 non-negotiable principles for the standalone multi-transport mesh + satellite + tactical gateway. These bind every contributor — human or agent. The merge-coordinator + validate-project-spec.py enforce them at commit time. Override only via formal RFC + ADR.

> Companion documents: vision/mission/scope live in [`PROJECT.md`](PROJECT.md). Decisions live in `adr/`. 9-phase roadmap in `EXECUTION-PLAN.md`. UX rationale in `UX-AUDIT-AND-REDESIGN.md` + `UX-MULTI-ACCESS-KIOSK-PAIRING.md`. Architecture deep-dive in `docs/ARCHITECTURE.md`. Production audit in `docs/PRODUCTION_READINESS_AUDIT_2026-04-04.md`. Transport-specific operational rules in `.claude/rules/transport-protocols.md` (gitignored). Reticulum details in `.claude/rules/reticulum-routing.md` (gitignored).

## Article I — Identity boundary (NON-NEGOTIABLE)

The system shall be the **standalone multi-transport mesh + satellite + tactical gateway** and NOTHING ELSE. It is a **first-class peer** in the MeshSat mesh (Bridge + Android + Hub) — none of those is primary; the Bridge operates fully autonomously without Hub or Android present (per `docs/ARCHITECTURE.md` L5). Its scope is the union of:

- **10 transport gateways**: Meshtastic LoRa, Iridium SBD (9603N), Iridium IMT (9704), Cellular SMS (A7670E/SIM7600G/Huawei), ZigBee 3.0 (CC2652P), MQTT, Webhooks, APRS (bundled Direwolf, in-container, KISS on loopback), TAK (CoT XML), direct serial. (Source: `README.md` L25–L36)
- **Reticulum-compatible routing layer with 9 cross-connected interfaces** (Article XVI / ADR-0007).
- **HeMB sub-IP bonding** for multi-bearer RLNC-coded transport (Article XVII / ADR-0008).
- **3-tier compression**: SMAZ2 + llama-zip + MSVQ-SC (Article XVIII / ADR-0009).
- **Three-shell SPA architecture**: one Vue codebase serves Kiosk + Browser + paired-Android (Article XIX / ADR-0004).
- **Field intelligence**: Dead Man's Switch, geofence alerts, channel health scores, satellite burst queue, mesh topology, RTL-SDR jamming detection, satellite pass prediction.
- **Pair protocol v1**: Bridge mints client certificates + JWT under its internal CA (ADR-0005, ADR-0006).

Hub features (OAuth2/OIDC, multi-tenancy, API keys, RBAC, NATS bus, MariaDB Galera, Cloudloop billing, tenant isolation, OWASP scans) belong in [`meshsat-hub/`](../meshsat-hub/) (project 35) and shall NEVER land in this repo. Companion-app features (Compose UI, BLE peripheral, Android-specific paths, hybrid native+WebView shell) belong in [`meshsat-android/`](../meshsat-android/) (project 31). Cross-platform shared logic uses the **adapter parity** pattern of `docs/ARCHITECTURE.md` §"Shared Design Patterns" — same conceptual interface, language-idiomatic implementations.

## Article II — `CGO_ENABLED=0` mandatory

The system shall compile pure-Go with `CGO_ENABLED=0` set everywhere — Makefile, CI, local build. Any `import "C"` is rejected by `.gitlab-ci.yml#lint:consistency` rule 2. Use `modernc.org/sqlite` for SQLite, not the CGO driver.

## Article III — Append-only migrations

The system shall never modify existing entries in `internal/database/migrations.go`. New schema changes append a new migration at the end. Current head: v41. CI rule 5 blocks any in-place edit.

## Article IV — Single entry point

The system shall wire all features in `cmd/meshsat/main.go` (2078 lines). `cmd/meshsat/app.go` (721 lines) is dead in production — `main.go` has zero references to it, so any feature wired only there does NOT run when the bridge boots. This has caused three production bugs. Note: `cmd/meshsat/app_test.go` exercises `app.Setup` as a test-fixture (5 callsites) — those calls are intentional and do not contradict the dead-in-production status. Delete `app.go` once the test-fixture is migrated to use main-path init, or treat it as inert reference.

## Article V — Test-first imperative

The system shall ship every functional change with a test that fails before the change and passes after. Workers that delete or disable tests to make CI green VIOLATE this article — merge-coordinator's `go test -count=1 -timeout 900s ./...` gate catches it.

## Article VI — DeliveryWorker is the sole outbound path

The system shall send all outbound messages through `Dispatcher.QueueDirectSend()` or `DispatchAccess()` → `message_deliveries` ledger → `DeliveryWorker.processBatch()` → `gw.Forward()`. Direct `gw.Forward()` from API handlers blocks HTTP for minutes and bypasses retries. NEVER bypass the pipeline.

## Article VII — Iridium serial-mutex + 3-min backoff

The system shall hold the Iridium serial mutex during SBDIX exchanges. On `mo_status=32` or `mo_status=36`, the system shall back off for ≥3 minutes before retry. Aggressive retries trigger registration death-spiral and brick the modem.

## Article VIII — JSPR (RockBLOCK 9704) JSON formatting

The system shall format JSPR JSON requests WITH SPACES (`{"key": value}`). The firmware rejects compact JSON (`{"key":value}`) with `407 BAD_JSON`. Test fixtures must preserve this.

## Article IX — GPIO via libgpiod chardev

The system shall access GPIO via `github.com/warthog618/go-gpiocdev` (chardev `/dev/gpiochip*`), NOT sysfs. The container mounts `/sys:/sys:ro` (sysfs read-only). Pi 5 gpiochip bases start at 512.

## Article X — Pi 5 field-kit hardware requirements

The system shall assume the target hardware is a Raspberry Pi 5 running Ubuntu Server 24.04, kernel `6.8.0-1051-raspi`. Required EEPROM: `PSU_MAX_CURRENT=5000`. UART2 on BCM pins 4/5 (NOT UART0). MT7612U for P2P WiFi (NOT MT7921U).

## Article XI — Single trusted container

The system shall run as the sole privileged Docker container on its Pi (host network, privileged). The standalone-mode pragma `// host-ops: allowed-in-standalone` is required for any direct `nsenter` / `systemctl exec` from Go.

## Article XII — No cloud dependencies, no subscriptions

The system shall function fully offline. Per README.md L9: "no cloud dependencies, no subscriptions beyond your satellite or cellular plan." Hub uplink is OPTIONAL — bridges work without it.

## Article XIII — Master-key envelope encryption is irrecoverable

The system shall wrap signing private keys + keystore entries via AES-256-GCM with a master key. Lost master key = unrecoverable signing key (same operational contract as losing a CA private key). Any change to the keystore code path requires explicit operator review.

## Article XIV — Files-owned non-overlap (parallel-dev gate)

The system shall enforce that no two parallelizable tasks share `files_owned` entries within the same dependency wave. Validated by `bootstrap-pack/scripts/validate-project-spec.py` at the Phase F gate, BEFORE any worker launches.

## Article XV — Parallel-dev workflow exception

The repo's default workflow ("push directly to main, no branches, no MRs" per CLAUDE.md L374) is overridden for parallel-dev waves. Each wave gets ONE short-lived branch `merge/<feature_id>`, ONE merge-coordinator MR per feature, and auto-delete the branch on merge. Workers' `parallel-dev/<feature>/<task>` branches are intermediates and never merged directly — they're squashed via patch-apply into the merge branch by `merge-coordinator.sh`. See ADR-0003.

## Article XVI — Reticulum routing layer (9 interfaces, Bridge IS an RNS endpoint)

The system shall implement a Reticulum-compatible routing layer with exactly **9 cross-connected interfaces** (`mesh_0`, `tcp_0`, `iridium_0`, `iridium_imt_0`, `ax25_0`, `mqtt_rns_0`, `sms_0`, `zigbee_0`, `ble_0`) wired in `main.go`. The Bridge is a **real Reticulum endpoint** — it handles link requests, proofs, and data locally, not just relay. Cross-interop with the upstream Python `rns` reference implementation MUST remain green (verified 2026-03-25 via `mule01:4242` TCP/HDLC handshake; regressions in `internal/reticulum/` or `internal/routing/tcp_interface.go` break this guarantee). Paid bearers (`cost > 0`) default to `floodable=false` so PathFinder doesn't burn $0.05/packet hunting unknown destinations. Identity persistence: `routing_identities` table (schema v24); never derived purely in-memory after first boot. See ADR-0007 + `.claude/rules/reticulum-routing.md`.

## Article XVII — HeMB (Heterogeneous Media Bonding) protocol invariants

The system shall implement HeMB as a sub-IP RLNC-coded multi-bearer bonding layer: cost-weighted splitter exhausts free bearers before paid, adaptive reassembly buffer tolerates 1:900 bearer latency ratio (LoRa 50 ms vs Iridium 45 s), per-bearer FEC profiles. HeMB operates **below IP, routing-protocol-agnostic, TUN-wrappable as a standard Linux network interface** — it sees opaque payloads and composes with Reticulum (Article XVI) above it. RLNC encoding is confirmed running on production hardware (March 2026). The HeMB wire format version field MUST be bumped on any symbol-layout change; from January 2027 onwards the IETF `draft-papadopoulos-hemb-00` Independent Submission becomes the canonical wire spec — pre-RFC versions are internal-only. See ADR-0008 + `README.md` L36, L380–L384.

## Article XVIII — 3-tier compression model + SMAZ2 dictionary parity with Hub

The system shall offer exactly **three compression tiers** in the transform pipeline: SMAZ2 (lossless, < 1 ms, in-process), llama-zip (LLM lossless, ~200 ms, gRPC sidecar via `MESHSAT_LLAMAZIP_ADDR`), MSVQ-SC (lossy semantic, rate-adaptive, gRPC sidecar via `MESHSAT_MSVQSC_ADDR`). Sidecars MUST degrade gracefully when unset (tier dormant, Bridge logs warning, Bridge container does NOT die). MSVQ-SC receive-side decode MUST work without the sidecar when `MESHSAT_MSVQSC_CODEBOOK` is provided (pure-Go decoder). Access rules selecting tier 3 (lossy MSVQ-SC) MUST display a lossy badge in the dashboard UI; default rule templates use lossless tiers only.

**Cross-repo invariant (CRITICAL):** The SMAZ2 dictionary in `internal/compress/dict_meshtastic.go` MUST stay byte-for-byte identical to the dictionary in `meshsat-hub/internal/compress/dict_meshtastic.go`. Independent modification = decompression failures for every field-originated message. Verify byte-for-byte before any compression-related merge. See ADR-0009 + `meshsat-hub/constitution.md` Article VI (the Hub-side mirror of this invariant).

## Article XIX — Three-shell SPA architecture (one Vue codebase)

The system shall serve exactly three UI shells from **one** Vue 3 Single-Page Application in `web/src/`:

- **Shell A — Kiosk**: labwc Wayland + Chromium `--kiosk --app=http://localhost:6050/` under dedicated `kiosk` user (NOT `pi`); enterprise-policy lockdown (`/etc/chromium/policies/managed/meshsat-lockdown.json` restricts URLAllowlist to localhost only); embedded `simple-keyboard` JS OSK (NOT compositor OSK — squeekboard-under-Chromium-kiosk is broken on labwc).
- **Shell B — Desktop browser**: PWA-installable; `sessionStorage` for JWT (dies on tab close); IndexedDB `CryptoKey` with `extractable:false` for client cert.
- **Shell C — Android**: native Kotlin hybrid — Compose for hot paths (Compose/Inbox/Map/People/Pair list), WebView wrapping the Bridge's Vue SPA for Settings/Engineer surfaces. Android Keystore for cert + JWT; OkHttp `CertificatePinner` for SPKI pin; foreground service for SSE.

Native code per shell is the **minimum required** to bridge to that platform's particular capabilities (OS-level boot for kiosk; PWA install for browser; hardware Keystore for Android). Anything else MUST go in the Vue SPA. Three concurrent breakpoints (400 wide / 720×1280 / ≥1200) MUST work without per-shell branching of Vue components. See ADR-0004 + `UX-MULTI-ACCESS-KIOSK-PAIRING.md` §1.
