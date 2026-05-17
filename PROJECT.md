# Project Charter — MeshSat Bridge

> Operator-facing charter. Vision, mission, scope, deliverables, success metrics. Authored 2026-05-17 by drawing from the strategic documents listed in **Source Trace** at the foot of this file. Machine-readable companion: `PROJECT.json`. Hard rules: `constitution.md`. Decisions: `adr/`.

---

## Vision

A **multi-transport mesh + satellite + tactical gateway** that lets a single edge device (a Pi 5 in a Pelican case, on a vehicle dash, or on an operator's belt) bridge **any** message between **any** two of Meshtastic LoRa, Iridium satellite (SBD + IMT), cellular SMS, ZigBee, MQTT, webhooks, APRS, TAK/CoT, and direct serial — **without** a cloud, without a subscription beyond the carrier plan the operator already pays for.

Source: `README.md` L7–L9 ("Ten transport types... all available as routing destinations... No cloud dependencies, no subscriptions beyond your satellite or cellular plan").

## Mission

Be the **standalone field gateway** that operates fully without external dependencies — and the **peer node** in a MeshSat mesh (Bridge + Android + Hub) when those peers are present. Every node — Bridge, Android, Hub — is a **first-class peer**, not a client of any other; the mesh runs Bridge↔Bridge over LoRa, Bridge↔Android over BLE, and Bridge↔Hub over MQTT, but no single node is the root of trust or the source of state.

Source: `docs/ARCHITECTURE.md` L5 ("MeshSat Bridge, MeshSat Android, and MeshSat Hub are **peers** in a communications mesh. None is primary. None requires the others to operate.").

## Scope (in / out)

**In scope — what this Bridge does:**

1. **10 transport gateways** — Meshtastic LoRa, Iridium SBD (RockBLOCK 9603N), Iridium IMT (RockBLOCK 9704), Cellular SMS (A7670E/SIM7600G/Huawei), ZigBee 3.0 (CC2652P), MQTT, Webhooks, APRS (bundled Direwolf supervised in-container, KISS on loopback), TAK/CoT XML, direct serial. (Source: `README.md` L25–L36)
2. **Reticulum-compatible routing layer** — Ed25519 identity, announce relay, link manager, keepalive, bandwidth tracking, resource transfers with chunked reliable delivery. 9 cross-connected interfaces (mesh / TCP / iridium / iridium_imt / ax25 / mqtt / sms / zigbee / ble). Bridge is a real Reticulum endpoint (handles link requests, proofs, data locally). (Source: `.claude/rules/reticulum-routing.md` L10–L67)
3. **HeMB protocol — Heterogeneous Media Bonding** — RLNC-coded simultaneous multi-bearer bonding across N heterogeneous physical bearers (LoRa + Iridium + SMS + APRS + IPoUGRS), cost-weighted splitter (free bearers exhausted before paid), adaptive reassembly buffer tolerating 1:900 bearer latency ratio, per-bearer FEC profiles. Field-verified March 2026 on production hardware. IETF Independent Submission Stream RFC planned 2027-01 (`draft-papadopoulos-hemb-00`). (Source: `README.md` L36 + L384)
4. **3-tier compression** — SMAZ2 (lossless, <1ms, Meshtastic dictionary), llama-zip (LLM-based lossless, ~200ms), MSVQ-SC (Multi-Stage Vector Quantization Semantic Compression — lossy semantic, rate-adaptive). Transform pipelines per interface stack compress → encrypt → encode. (Source: `.claude/rules/features-subsystems.md` L52–L60)
5. **Access rules engine** — per-rule filtering, object groups (node/portnum/sender/contact), failover groups, transform pipelines, rate limiting, implicit deny. (Source: `README.md` L51–L52)
6. **Three-shell SPA architecture (Phase 7 active)** — One Vue 3 SPA serves three shells: (a) **Kiosk** on Pi Touch Display 2 lid via labwc + Chromium kiosk; (b) **Desktop browser** as PWA; (c) **Android native shell** (hybrid Compose + WebView). One codebase, one identity model, one deployment pipeline. (Source: `UX-MULTI-ACCESS-KIOSK-PAIRING.md` L31–L65)
7. **Pair protocol v1 (Phase 8 active)** — Philips-Hue-pushlink-style QR pairing: operator arms by physical touch on the 7" display → 60s single-use QR encodes ECDH+CSR challenge → bridge mints client cert (90-day, internal CA) + 90-day JWT. mTLS + Bearer for steady-state. Three NAT-traversal tiers: LAN → Reticulum → Hub WebSocket relay. (Source: `EXECUTION-PLAN.md` §2, `UX-MULTI-ACCESS-KIOSK-PAIRING.md` §4)
8. **Field intelligence** — Dead Man's Switch, geofence alerts, channel health scores, satellite burst queue, mesh topology visualization, satellite pass prediction (SGP4/TLE). (Source: `README.md` L50, `.claude/rules/features-subsystems.md` L111–L124)
9. **DeviceSupervisor** — USB hotplug detection, VID:PID identification cascade, protocol probing, claim-based port management. Auto-detects USB devices on startup. (Source: `README.md` L52–L53, L61)
10. **Web dashboard + REST API** — Vue 3 SPA (13 views currently, expanding to 5-tab Operator/Engineer reshape in Phase 3), 280+ REST endpoints, SSE for real-time updates. (Source: `README.md` L57–L61)

**Out of scope — what this Bridge is NOT:**

- **Not multi-tenant** — single-operator, single-host. Multi-tenant fleet management lives in [`meshsat-hub`](../meshsat-hub/) (project 35). Source: `constitution.md` Article I.
- **Not a mobile app** — Android-specific paths (BLE, native SMS, hybrid shell) live in [`meshsat-android`](../meshsat-android/) (project 31). Bridge exposes the API shape the Android app consumes. Source: `constitution.md` Article I.
- **Not a cloud service** — runs as a single Docker container with `--network host` + `--privileged` on any Linux machine; SQLite for persistence; no external dependencies for operation. Source: `README.md` L9, `constitution.md` Article XII.
- **Not Meshtastic firmware** — consumes the official `buf.build/gen/go/meshtastic/protobufs` bindings; never replaces or modifies Meshtastic itself. Source: `constitution.md` Article VI (forbids hand-rolled protobuf — MESHSAT-242).

## Current production state (as of 2026-05-17)

| Dimension | State | Source |
|---|---|---|
| Version | **v0.3.0 — current shipping line.** v0.1 = Iridium SBD + Meshtastic bridge; v0.2 = any-to-any routing fabric; v0.3 = 3-tier compression + Reticulum + IMT (9704) + APRS + TAK + DeviceSupervisor + Hub MQTT mTLS + Android companion. | `README.md` L373–L378 |
| Deployment | **2 field kits** — `tesseract01` (RockBLOCK 9603 SBD) + `parallax01` (RockBLOCK 9704 IMT). 99% identical kits, differ only in satellite modem family. Both Ubuntu Server 24.04 + Docker. | `PROJECT.json` `deployment_targets`, `.claude/rules/ecosystem-fleet.md` L42–L67 |
| Ecosystem grade | **84/100 (B+)** per 2026-04-04 audit. Highest test density in the ecosystem (1,058 tests, 35% LOC ratio). | `docs/PRODUCTION_READINESS_AUDIT_2026-04-04.md` §2.1 |
| Schema | **v43** (Zigbee IAS Zone status). Next free **v44** (directory_contacts — Phase 1). | `EXECUTION-PLAN.md` §1, `internal/database/migrations.go` |
| Reticulum interfaces | **9/9 wired** (mesh/tcp/iridium_sbd/iridium_imt/ax25/mqtt/sms/zigbee/ble); BLE shipped 2026-04-15 (MESHSAT-406). | `.claude/rules/reticulum-routing.md` L59–L67 |
| HeMB | **3-bearer RLNC verified on production hardware, March 2026.** Field validation NL→GR pending IPoUGRS hardware (Apr 2026). | `README.md` L384, `PRODUCTION_READINESS_AUDIT` §3.2 |
| RTL-SDR jamming detection | Implemented (MESHSAT-412), "To Verify" status — graceful dormant when `rtl_power` not present. | `.claude/rules/features-subsystems.md` L140–L165 |

## Active priorities (next 4–8 weeks)

The `EXECUTION-PLAN.md` (2026-04-18) is the **authoritative active roadmap**, 9 phases / 83 stories / ~95 engineer-days / ~19 weeks solo. Three locked-in product decisions (2026-04-18): landscape kiosk 1280×720, Android "Both" mode in v1, Hub WS relay in v1. Critical-path summary:

| Phase | Goal | Stories | Effort (solo) | Status |
|---|---|---|---|---|
| **1** | Unified directory (contacts/addresses/keys/groups/dispatch-policy, schema v44-v48) | 10 | 2.5w | Foundation — unblocks 2,3,4,5,8 |
| **2** | Contact-aware Dispatcher + STANAG 4406 precedence | 5 | 1.5w | After Phase 1 |
| **3** | UI reshape (Operator/Engineer modes, 5-item nav, Compose/Inbox/People/Map/Radios) | 10 | 3w | After Phase 1, 2 |
| **4** | Trust levels + SIDC/MIL-STD-2525D symbology + SOS | 5 | 1.5w | After Phase 3 |
| **5** | Android directory sync + contact QR handoff | 3 | 1w | After Phase 4 |
| **6** | Compliance + accessibility + standards polish (WCAG 2.1 AAA gate, MLS, FIPS) | 10 | 2.5w | After Phase 3,4,5 |
| **7** | Kiosk (Pi Touch Display 2) + PWA + responsive-touch SPA hardening | 16 | 2.5w | Parallel-safe with 1-2 |
| **8** | **Pair protocol v1** (pair-protocol feature spec at `spec/001-pair-protocol/`) + Android pair shell | 15 | 3w | After Phase 7 |
| **9** | Multi-bridge + NAT traversal (LAN/RNS/Hub) | 9 | 2w | After Phase 8 |

**Why phases 7-8 are the canary for parallel-dev:** Phase 8 produced the first spec/<feature> directory in the meshsat repo (`spec/001-pair-protocol/`) — 25 EARS REQs, OpenAPI + AsyncAPI + 3 JSON Schemas + Gherkin acceptance scenarios. It's the proving ground for the bootstrap-pack methodology before broader phases adopt it.

Source: `EXECUTION-PLAN.md` §3, §6.1–§6.9, `spec/001-pair-protocol/`.

## Success metrics

| Dimension | Metric | Source |
|---|---|---|
| Reliability | DeliveryWorker queue depth stays bounded under sustained load; failover resolver promotes alternates within 5s of primary failure | `constitution.md` Article VI, `internal/engine/dispatcher.go` |
| Iridium 9603 SBD | Serial mutex held for full SBDIX cycle (11–62s); `mo_status=32/36` triggers ≥3 min backoff — zero registration death-spiral incidents | `.claude/rules/transport-protocols.md` L9–L19, `constitution.md` Article VII |
| Iridium 9704 IMT | MO+MT both flow end-to-end at 230400 baud through Docker container; JSPR handshake completes 100% of cold starts (post-MESHSAT-334) | `.claude/rules/transport-protocols.md` L24–L30 |
| Reticulum | Bridge identity persistent across restarts; announce relay dedup-rate ≥99%; PathFinder converges <30s for 3-hop topology | `.claude/rules/reticulum-routing.md` L16–L20 |
| HeMB | Field validation NL→GR ≥99.5% delivery across 3-bearer bond (LoRa + Iridium SBD + SMS) | `README.md` L384 (Apr 2026 pending hardware) |
| Pair protocol v1 | Pair handshake p99 <800ms; capacity-exceeded enforced TOCTOU-safe; 6-char operator pair-code never written to audit log | `spec/001-pair-protocol/acceptance/pair-protocol.feature` REQ-019, REQ-020, REQ-022 |
| Production audit | Maintain **≥84/100 B+** grade; close MESHSAT-154 (Hub MQTT reconnect) for +2 points | `docs/PRODUCTION_READINESS_AUDIT_2026-04-04.md` §1, §8 |

## Non-goals

- **No multi-tenancy in this repo** — Bridge is single-operator. Multi-tenant features (OAuth2/OIDC, RBAC, tenant isolation, NATS bus, MariaDB Galera) live in `meshsat-hub/`. Source: `constitution.md` Article I.
- **No hand-rolled Meshtastic protobuf** — MESHSAT-242 explicitly removed all hand-rolled bindings in favor of `buf.build/gen/go/meshtastic/protobufs`. Source: `.claude/rules/features-subsystems.md` L17, `constitution.md` Article VI.
- **No CGO** — pure-Go everywhere, including SQLite (`modernc.org/sqlite`) and serial (`go.bug.st/serial`). This is the load-bearing constraint that keeps Bridge cross-compilable to ARM64 and FIPS-140-3 capable (when paired with BoringSSL build target in Phase 6). Source: `constitution.md` Article II, ADR-0002.
- **No replacement of Meshtastic itself** — bridge speaks Meshtastic protocol via official protobuf bindings; never participates in firmware decisions for Heltec/T-Echo/T-Deck/XIAO/etc.
- **No central PKI across the bridge fleet** — each Bridge is its own trust root via internal CA. Cross-bridge sharing happens at the Android-multi-bridge layer, NOT at the certificate level. Source: ADR-0005, `UX-MULTI-ACCESS-KIOSK-PAIRING.md` §13.2.
- **No backward compatibility for the Iridium serial mutex** — Article VII is the load-bearing rule. Aggressive retries brick the modem; we refuse code that bypasses the mutex.

## Stakeholders

- **Owner / operator:** `ufwtqkgz@meshsat.net` (sole operator; same ID owns `meshsat-hub` + `meshsat-android`).
- **Field kits:** `tesseract01` (SBD) + `parallax01` (IMT). Procurement complete 2026-04-13. Assembly blocked on AliExpress IP67 bulkhead delivery (~2-3w).
- **Carrier dependencies:** RockBLOCK (Ground Control) for Iridium SBD MO/MT webhooks; Cloudloop for Iridium MT REST API; user-supplied KPN SIM for cellular; any APRS-IS gateway for APRS internet path.
- **Adjacent projects:**
  - [`meshsat-hub`](../meshsat-hub/) — multi-tenant fleet management SaaS.
  - [`meshsat-android`](../meshsat-android/) — companion + standalone mobile gateway.
  - [`meshsat-website`](../meshsat-website/) — Hugo + VitePress docs site at `meshsat.net`.
  - [Meshtastic](https://meshtastic.org) — upstream LoRa mesh stack (consumed via protobuf).
  - [Reticulum Network Stack](https://reticulum.network) — upstream wire-format reference; cross-component interop verified 2026-03-25.
  - [Direwolf](https://github.com/wb2osz/direwolf) — bundled in-container as APRS modem, supervised, KISS on loopback (MESHSAT-514).
- **Compliance targets:** WCAG 2.1 AAA (Phase 6), MIL-STD-2525D / APP-6D / STANAG 4677 symbology (Phase 4), STANAG 4406 6-level precedence (Phase 2), MIL-STD-3009 NVIS Green A theme (Phase 3), FIPS-140-3 BoringSSL build target (Phase 6, deferred).

## Architectural pillars (linked artifacts)

Each pillar has a citable artifact — strategic document, ADR, or constitution article. **Read these before changing the surface area.**

| Pillar | Authoritative source |
|---|---|
| Identity boundary (what is/isn't Bridge) | `constitution.md` Article I + `docs/ARCHITECTURE.md` (peer mesh diagram) |
| `CGO_ENABLED=0` mandatory | `constitution.md` Article II + `adr/0002-cgo-disabled-mandatory.md` |
| Append-only migrations (v43 current head) | `constitution.md` Article III + `EXECUTION-PLAN.md` §1 |
| Single entry point — wire everything in `main.go`, NOT `app.go` | `constitution.md` Article IV |
| DeliveryWorker is the sole outbound path | `constitution.md` Article VI |
| Iridium 9603 serial-mutex + 3-min backoff | `constitution.md` Article VII + `.claude/rules/transport-protocols.md` L9–L19 |
| JSPR (9704 IMT) JSON-with-spaces format | `constitution.md` Article VIII + `.claude/rules/transport-protocols.md` L33–L43 |
| GPIO via libgpiod chardev (NOT sysfs) | `constitution.md` Article IX |
| Pi 5 field-kit hardware contract (PSU_MAX_CURRENT=5000, UART boot fix, EEPROM) | `constitution.md` Article X + `adr/0012-pi5-field-kit-hardware-contract.md` |
| Single trusted container (host network, privileged) | `constitution.md` Article XI |
| No cloud dependencies | `constitution.md` Article XII |
| Master-key envelope encryption (AES-256-GCM, irrecoverable) | `constitution.md` Article XIII |
| Three-shell SPA architecture (Kiosk + Browser + Android, one Vue codebase) | `adr/0004-three-shell-spa-architecture.md` + `UX-MULTI-ACCESS-KIOSK-PAIRING.md` §1 |
| Reticulum routing layer (Ed25519 + 9 interfaces + transport node + pathfinder) | `adr/0007-reticulum-routing-layer.md` + `.claude/rules/reticulum-routing.md` |
| HeMB Heterogeneous Media Bonding (RLNC, multi-bearer) | `adr/0008-hemb-heterogeneous-media-bonding.md` + `README.md` L36, L384 |
| 3-tier compression (SMAZ2 + llama-zip + MSVQ-SC) | `adr/0009-three-tier-compression-model.md` + `.claude/rules/features-subsystems.md` L52–L60 |
| Android APRS via APRSDroid KISS TCP | `adr/0010-android-aprs-via-aprsdroid-kiss-tcp.md` + `docs/ANDROID_APRS_DECISION.md` |
| Android TAK via Hub proxy | `adr/0011-android-tak-via-hub-proxy.md` + `docs/ANDROID_TAK_DECISION.md` |
| Pair protocol v1 (bridge-issued certs + operator-typed pair code) | `adr/0005-pair-cert-bridge-issued-not-hub-issued.md` + `adr/0006-hmac-shared-secret-is-operator-typed-pair-code.md` + `UX-MULTI-ACCESS-KIOSK-PAIRING.md` §4 + `spec/001-pair-protocol/` |
| Parallel-dev workflow override | `adr/0003-parallel-dev-workflow-override.md` + `constitution.md` Article XV |

## Where decisions live

| Type of change | Where it gets recorded |
|---|---|
| New architectural commitment | New `adr/NNNN-<slug>.md` (MADR); link from this charter's pillar table |
| Tightening / loosening a security rule | New ADR + edit to relevant `constitution.md` Article |
| Pair-protocol feature change | Edit `spec/001-pair-protocol/{requirements,design,...}` + bump `spec_revision` |
| Phase 1-9 roadmap update | Edit `EXECUTION-PLAN.md` §6 (per-phase story manifest) + this charter's "Active priorities" if reordered |
| UX redesign rationale | `UX-AUDIT-AND-REDESIGN.md` (design) + `UX-MULTI-ACCESS-KIOSK-PAIRING.md` (multi-access); NOT this charter |
| Field-kit hardware change | `docs/hardware/MeshSat-Field-Kit-*.docx` (operator-facing BOM) + `.claude/rules/ecosystem-fleet.md` (gitignored ops notes) |
| Transport-protocol gotcha | `.claude/rules/transport-protocols.md` (gitignored) |
| Reticulum / routing change | `.claude/rules/reticulum-routing.md` (gitignored) + ADR-0007 if invariant |

## What this charter is NOT

- **Not** the roadmap — see `EXECUTION-PLAN.md` (phase-by-phase, story-by-story).
- **Not** the UX rationale — see `UX-AUDIT-AND-REDESIGN.md` + `UX-MULTI-ACCESS-KIOSK-PAIRING.md`.
- **Not** the architecture deep-dive — see `docs/ARCHITECTURE.md` (peer mesh + adapter parity).
- **Not** the production audit — see `docs/PRODUCTION_READINESS_AUDIT_2026-04-04.md`.
- **Not** the operational ops notes — see `CLAUDE.md` (gitignored) + `.claude/rules/*.md` (gitignored).

This charter is the load-bearing summary that lets an incoming engineer (human or agent) understand WHAT the project is, WHY it exists, and WHERE the canonical source for each operational decision lives — without re-reading 5,000 lines of strategic docs.

---

## Source Trace

| Statement in charter | Source file | Line range |
|---|---|---|
| Vision: 10-transport multi-modal gateway, no cloud | `README.md` | L7–L9 |
| Mission: peer mesh (Bridge + Android + Hub) | `docs/ARCHITECTURE.md` | L5–L8 |
| 10 transports enumerated | `README.md` | L25–L36 |
| Reticulum routing details | `.claude/rules/reticulum-routing.md` | L8–L67 |
| HeMB protocol + RFC 2027-01 plan | `README.md` | L36, L380–L384 |
| 3-tier compression | `.claude/rules/features-subsystems.md` | L52–L60 |
| 3-shell SPA architecture | `UX-MULTI-ACCESS-KIOSK-PAIRING.md` | §1 (L31–L65) |
| Pair protocol v1 + 3 NAT tiers | `EXECUTION-PLAN.md` | §2, `UX-MULTI-ACCESS-KIOSK-PAIRING.md` §4 |
| Field intelligence catalogue | `.claude/rules/features-subsystems.md` | L111–L124 |
| DeviceSupervisor + USB hotplug | `README.md` | L52–L53, L61 |
| v0.1 → v0.3 timeline | `README.md` | L372–L378 |
| Field kits: tesseract + parallax | `.claude/rules/ecosystem-fleet.md` | L42–L67, `PROJECT.json` |
| 84/100 grade + 1,058 tests | `docs/PRODUCTION_READINESS_AUDIT_2026-04-04.md` | §1, §2.1 |
| Schema v43 head + v44 next | `EXECUTION-PLAN.md` | §1, L8 |
| 9 Reticulum interfaces wired | `.claude/rules/reticulum-routing.md` | L59–L67 |
| HeMB 3-bearer verified | `README.md` | L384 |
| 9-phase plan + critical path | `EXECUTION-PLAN.md` | §3, §6, §7 |
| Three locked-in decisions (2026-04-18) | `EXECUTION-PLAN.md` | §0 (L13–L20) |
| Pair-protocol Phase 8 feature spec exists | `spec/001-pair-protocol/` | (directory) |
| Iridium SBD operational rules | `.claude/rules/transport-protocols.md` | L9–L19 |
| Iridium IMT (9704) JSPR rules | `.claude/rules/transport-protocols.md` | L24–L43 |
| Pi 5 hardware contract (EEPROM, UART) | `.claude/rules/ecosystem-fleet.md` | L60–L66 |
| Android APRS via APRSDroid decision | `docs/ANDROID_APRS_DECISION.md` | (full doc) |
| Android TAK via Hub decision | `docs/ANDROID_TAK_DECISION.md` | (full doc) |
| Carrier dependencies (RockBLOCK, Cloudloop) | `README.md` | L78–L80, L104 |
| WCAG/MIL-STD compliance targets | `EXECUTION-PLAN.md` | §6.6 (S6-01, S6-08) + §6.4 (S4-01) |
