# MeshSat Ecosystem — Production Readiness Audit & Roadmap

**Date:** 2026-04-04
**Scope:** meshsat (bridge), meshsat-hub, meshsat-android, meshsat-website
**Auditor:** Claude Code (comprehensive codebase + documentation + YouTrack analysis)

---

## 1. Executive Summary

The MeshSat ecosystem is a **remarkably ambitious and well-executed** project spanning 4 repositories, ~217K lines of code, 2,550+ test functions, and 10+ communication transports (Meshtastic LoRa, Iridium SBD/IMT, Cellular SMS, APRS, TAK, ZigBee, MQTT, Webhooks, Reticulum TCP). It was built primarily by a single senior infrastructure engineer over ~6 weeks with AI-assisted development.

### Overall Score: **B+ (85/100)**

| Repo | LOC | Tests | Score | Grade |
|------|----:|------:|:-----:|:-----:|
| Bridge | 100K | 1,058 | 84/100 | B+ |
| Hub | 67K | 915 | 88/100 | A- |
| Android | 49K | 539 | 82/100 | B+ |
| Website | 1.3K docs | — | 80/100 | B |
| **Ecosystem** | **217K** | **2,512** | **85/100** | **B+** |

**To reach A+++ (97+/100):** 14 targeted improvements across CI/CD, testing, security hardening, documentation, and operational maturity. Estimated effort: ~40-60 hours of focused work.

---

## 2. Per-Repo Production Readiness Scorecards

### 2.1 MeshSat Bridge (meshsat/) — 84/100

| Dimension | Score | Notes |
|-----------|:-----:|-------|
| Architecture | 95 | 21 clean packages, transport-agnostic dispatcher, delivery ledger, transform pipeline |
| Code Quality | 90 | Consistent error handling (1,405 err checks), zerolog structured logging, no dead code |
| Test Coverage | 85 | 1,058 tests (35% LOC ratio), 23 Playwright E2E, 3 integration tests |
| CI/CD | 70 | **CRITICAL GAP: no `go test` in CI pipeline** — 7 consistency checks but tests only run locally |
| Security | 82 | No hardcoded secrets, credential redaction, cert pinning — but no rate limiting on API |
| Documentation | 88 | 89% Swagger coverage (211/236 endpoints), comprehensive CLAUDE.md |
| Deployment | 90 | Multi-arch Docker, parallel deploy to 2 devices, rollback support |
| Monitoring | 78 | SSE events, health endpoints, Hub telemetry — no Prometheus/metrics endpoint |
| Resilience | 88 | DLQ with ISU-aware backoff, failover resolver, delivery retries, graceful shutdown |
| DB Health | 92 | 41 append-only migrations, FK constraints, proper indexing, CI lint |

**Key Strengths:**
- Highest test density in the ecosystem (3.4 tests per file)
- 9 Reticulum transport interfaces operational
- HeMB protocol verified in field test (3-bearer, 0 failures)
- DeviceSupervisor USB hot-plug with auto-identification cascade

**Critical Gaps:**
1. `go test` not in CI pipeline (tests pass locally but never gated)
2. No API rate limiting (local network assumed, but still a risk)
3. No Prometheus metrics endpoint (Hub has 25+ metrics, bridge has 0)
4. 25 API handlers lack Swagger annotations

---

### 2.2 MeshSat Hub (meshsat-hub/) — 88/100

| Dimension | Score | Notes |
|-----------|:-----:|-------|
| Architecture | 93 | 52 packages, multi-tenant, MQTT bus, Galera cluster, NATS leaf nodes |
| Code Quality | 90 | Consistent patterns, 915 tests, structured logging |
| Test Coverage | 88 | 915 tests across 46 packages, 13 Playwright E2E fleet tests |
| CI/CD | **95** | **Best in ecosystem:** 11 stages incl. gosec, govulncheck, Galera health gate, OWASP |
| Security | **92** | JWT/OIDC, RBAC, tenant isolation, SAST+SCA in CI, webhook signature verification |
| Documentation | 85 | 375+ Swagger endpoints, detailed CLAUDE.md (633 lines) |
| Deployment | 90 | Dual-site Galera, garbd arbitrator, pre-deploy health gate, rolling updates |
| Monitoring | 85 | 12 observability features, 3 Grafana dashboards, OTel scaffold, Prometheus metrics |
| Resilience | 82 | MQTT reconnect, store-and-forward, fragmentation — **P0 bug: MESHSAT-154** |
| DB Health | 88 | Schema v24, MariaDB Galera + SQLite, proper tenant isolation |

**Key Strengths:**
- Best CI/CD pipeline (11 stages, mandatory security gates)
- Most mature security posture (JWT/OIDC, RBAC, gosec, govulncheck)
- Dual-site HA with Galera cluster and garbd quorum
- 12 observability features operational

**Critical Gaps:**
1. **MESHSAT-154 (P0):** MQTT subscriptions lost on broker reconnect — causes silent message loss
2. CustodyManager TODO in main.go (DTN custody not wired)
3. v1.2 partial: email gateway, WireGuard, .onion API still pending
4. Tech debt issues tracked: MESHSAT-155 through MESHSAT-161 (k8s deps, API tests, Swagger, OWASP)

---

### 2.3 MeshSat Android (meshsat-android/) — 82/100

| Dimension | Score | Notes |
|-----------|:-----:|-------|
| Architecture | 90 | 28 modules, 5-phase parity (A-E), clean Compose-only UI |
| Code Quality | 85 | 539 tests, proper coroutine scoping, Room DB v10 |
| Test Coverage | 78 | 539 tests concentrated in phases A-C; Phase E (routing) thin |
| CI/CD | 65 | **Only 3 stages (build, release)** — no lint, no security scan, no test gate |
| Security | 85 | AES-256-GCM, MSVQ-SC compression, mTLS Hub connection |
| Documentation | 80 | CLAUDE.md comprehensive (314 lines), in-code docs adequate |
| Deployment | 72 | Debug APK only (129M), no release signing, no Play Store pipeline |
| Monitoring | 70 | Local API server (localhost:6051), Hub telemetry — no crash reporting |
| Resilience | 80 | Delivery ledger, failover groups, dead man switch — no background service recovery |
| APK Health | 68 | 129M debug APK, no R8/ProGuard optimization, no build shrinking |

**Key Strengths:**
- Only Android app supporting Iridium + Meshtastic in one app
- ONNX Runtime MSVQ-SC semantic compression (unique capability)
- All 16 backend parity phases complete
- Pure Kotlin codebook decoder (RX path doesn't need ML runtime)

**Critical Gaps:**
1. CI has no test execution, no lint, no security scanning
2. Debug-only APK (129M) — no release build, no ProGuard/R8
3. No crash reporting (Firebase Crashlytics, Sentry, etc.)
4. TOFU key pinning TODO in KeyBundleImporter.kt
5. Phase E routing tests incomplete

---

### 2.4 MeshSat Website (meshsat-website/) — 80/100

| Dimension | Score | Notes |
|-----------|:-----:|-------|
| Content | 85 | 192 docs pages, full transport documentation, install guide |
| Build System | 88 | Hugo + VitePress + Nginx, multi-container, AWX deployment |
| CI/CD | 78 | 4 stages, consistency checks, Docker build |
| Completeness | 75 | No broken link validation, no API doc auto-sync from repos |
| SEO/Accessibility | 70 | No meta tags audit, no Lighthouse score, no sitemap validation |
| Install Script | 82 | One-command curl install, but no checksum verification |

**Critical Gaps:**
1. No automated link validation in CI
2. No API doc auto-sync (Swagger from bridge/hub could auto-publish)
3. Install script lacks signature/checksum verification
4. No Lighthouse/accessibility audit

---

## 3. Cross-Ecosystem Analysis

### 3.1 Architecture — STRONG (93/100)

The three-repo separation (Bridge/Hub/Android) is the correct architecture:
- Bridge: embedded device firmware (Pi, ARM64) — no auth needed, direct hardware
- Hub: SaaS platform (server) — multi-tenant, full auth stack, HA database
- Android: mobile app — standalone, BLE/SPP transports, offline-first

The CLAUDE.md boundary rules are well-enforced and have prevented cross-contamination.

### 3.2 Protocol Completeness — STRONG (90/100)

| Transport | Bridge | Hub | Android | Status |
|-----------|:------:|:---:|:-------:|--------|
| Meshtastic LoRa | YES | relay | YES (BLE) | Production |
| Iridium 9603 SBD | YES | webhook | YES (SPP) | Production |
| Iridium 9704 IMT | YES | webhook | YES (SPP) | Production |
| Cellular SMS | YES | relay | YES (native) | Production |
| APRS (AX.25) | YES | — | YES (KISS) | Production |
| TAK (CoT XML) | YES | relay | YES | Production (verified 2026-04-04) |
| ZigBee | YES | — | — | Bridge only |
| MQTT | YES | YES | YES | Production |
| Webhooks | YES | YES | — | Production |
| Reticulum TCP | YES | YES | YES | Production (9 interfaces) |
| BLE Reticulum | — | — | — | MESHSAT-406 (open) |
| HeMB bonding | YES | planned | planned | Bridge verified, Hub/Android pending |

### 3.3 CI/CD Maturity — MIXED (76/100)

| Capability | Bridge | Hub | Android | Website |
|------------|:------:|:---:|:-------:|:-------:|
| Automated tests in CI | **NO** | YES | **NO** | N/A |
| SAST (gosec) | NO | YES | NO | N/A |
| CVE scanning | NO | YES | NO | N/A |
| Swagger validation | YES | YES | N/A | N/A |
| Consistency checks | YES | YES | NO | YES |
| Multi-arch build | YES | partial | N/A | N/A |
| Automated deploy | YES | YES | NO | YES |
| Pre-deploy health gate | NO | YES (Galera) | N/A | NO |
| OWASP ZAP | NO | YES (optional) | NO | NO |
| Release automation | NO | YES (changelog) | YES (APK) | NO |

**The Hub is the gold standard. Bridge and Android need to catch up.**

### 3.4 Test Coverage — GOOD (82/100)

| Metric | Bridge | Hub | Android | Total |
|--------|:------:|:---:|:-------:|:-----:|
| Test functions | 1,058 | 915 | 539 | 2,512 |
| Test files | 92 | 108 | 44 | 244 |
| Test/file ratio | 3.4x | 2.9x | 2.3x | — |
| E2E tests | 23 (Playwright) | 13 (Playwright) | 0 | 36 |
| Integration tests | 3 | integration tag | 0 | — |
| **Gated in CI** | **NO** | **YES** | **NO** | — |

### 3.5 Security Posture — GOOD (84/100)

| Control | Bridge | Hub | Android |
|---------|:------:|:---:|:-------:|
| No hardcoded secrets | YES | YES | YES |
| Input validation | YES | YES | YES |
| Parameterized queries | YES | YES | YES (Room) |
| Auth middleware | N/A (local) | YES (JWT/OIDC) | N/A (local) |
| RBAC | N/A | YES | N/A |
| Tenant isolation | N/A | YES | N/A |
| Webhook signature verify | N/A | YES | N/A |
| SAST in CI | NO | YES | NO |
| CVE scanning | NO | YES | NO |
| Rate limiting | NO | YES | NO |
| Credential redaction | YES | YES | YES |
| E2E encryption | AES-256-GCM | AES-256-GCM | AES-256-GCM |
| Certificate pinning | YES | YES | mTLS |

---

## 4. Gap Analysis

### 4.1 CRITICAL (Must fix for production confidence)

| # | Gap | Repo | Impact | Effort |
|---|-----|------|--------|--------|
| C1 | **No `go test` in bridge CI** | bridge | Regressions ship to production devices undetected | 1h |
| C2 | **No test/lint in Android CI** | android | Same — regressions ship in APK | 2h |
| C3 | **MESHSAT-154: MQTT reconnect loses subscriptions** | hub | Silent message loss in production | 4-8h |
| C4 | **No API rate limiting on bridge** | bridge | DoS risk even on local network (mesh nodes can flood) | 3h |

### 4.2 MAJOR (Should fix before v1.0 release)

| # | Gap | Repo | Impact | Effort |
|---|-----|------|--------|--------|
| M1 | No SAST/CVE scanning in bridge CI | bridge | Security vulnerabilities undetected | 2h |
| M2 | No Prometheus metrics on bridge | bridge | No observability parity with Hub | 4-6h |
| M3 | Android APK is 129M debug-only | android | No release signing, huge download, no optimization | 3h |
| M4 | No crash reporting on Android | android | Field failures invisible | 2h |
| M5 | 25 API handlers lack Swagger docs | bridge | Incomplete API contract | 3h |
| M6 | Install script has no checksum verification | website | Supply chain risk on curl-pipe-bash | 2h |
| M7 | Hub v1.2 incomplete (email gw, WireGuard, .onion) | hub | Feature gap in notification/tunneling | 8-12h |
| M8 | E2E full-stack validation never completed (MESHSAT-338) | all | Integration risk between components | 8h |

### 4.3 MINOR (Polish for A+++ score)

| # | Gap | Repo | Impact | Effort |
|---|-----|------|--------|--------|
| m1 | No link validation in website CI | website | Broken docs links | 1h |
| m2 | CustodyManager TODO in Hub main.go | hub | DTN custody not wired | 2h |
| m3 | TOFU key pinning TODO in Android | android | First-scan key not verified | 2h |
| m4 | Android Phase E routing tests thin | android | Routing regression risk | 4h |
| m5 | No Lighthouse/accessibility audit on website | website | SEO/a11y gaps | 2h |
| m6 | Bridge pre-deploy health check missing | bridge | Deploy to unhealthy device possible | 2h |

---

## 5. Strengths (What's Already Excellent)

1. **Transport diversity** — 10+ transports across 3 repos, more than any comparable open-source project
2. **HeMB protocol** — Novel sub-IP bonding protocol, field-tested, IETF RFC planned (unique IP)
3. **Test discipline** — 2,512 test functions across 217K LOC (1.16 tests per 100 LOC)
4. **Hub CI/CD** — 11-stage pipeline with mandatory security gates is production-grade
5. **Reticulum integration** — 9 transport interfaces, TCP interop with Python RNS verified
6. **Architecture boundaries** — Clean repo separation enforced by CLAUDE.md rules
7. **Delivery pipeline** — message_deliveries table + DeliveryWorker + DLQ with ISU-aware backoff
8. **Database discipline** — Append-only migrations enforced in CI, no schema corruption risk
9. **Multi-arch builds** — ARM64 + AMD64 Docker images, cross-compilation working
10. **Documentation** — 633-line Hub CLAUDE.md, 192 VitePress pages, 89% Swagger coverage

---

## 6. Weaknesses (Areas Needing Work)

1. **CI/CD inconsistency** — Hub is gold standard but bridge/android lack test+security gates
2. **Observability gap** — Hub has 12 features + Grafana; bridge has SSE events only
3. **Android maturity** — Debug-only APK, no crash reporting, thin CI pipeline
4. **No integration test suite** — Components tested individually but MESHSAT-338 (full-stack E2E) still open
5. **Single developer risk** — 217K LOC maintained by one person; bus factor = 1
6. **Hardware-blocked features** — BLE (MESHSAT-406) waiting on hardware
7. **HeMB not yet in Hub/Android** — Only bridge has implementation; Hub+Android planned
8. **No formal security audit** — OWASP ZAP is optional in Hub, absent elsewhere

---

## 7. YouTrack Open Issues Summary

11 unresolved MESHSAT issues as of 2026-04-04:

| Issue | Summary | Priority | Category |
|-------|---------|----------|----------|
| MESHSAT-154 | Hub MQTT reconnect loses subscriptions | **P0** | Bug |
| MESHSAT-338 | E2E full-stack validation | P1 | Testing |
| MESHSAT-354 | Hub Reticulum cross-component validation | P1 | Testing |
| MESHSAT-403 | Field kit assembly | P2 | Hardware |
| MESHSAT-406 | BLE Reticulum interface | P3 | Feature |
| MESHSAT-412 | RTL-SDR jamming detection | P3 | Feature |
| MESHSAT-415 | HeMB Master issue | P1 | Feature |
| MESHSAT-441 | HeMB Phase FIELD validation (NL-GR) | P2 | Testing |
| MESHSAT-442 | HeMB RFC submission | P3 | Documentation |
| MESHSAT-461 | IPsec S2S tunnel health monitoring | P2 | Infrastructure |

---

## 8. Roadmap to A+++ (97+/100)

### Phase 1: CI/CD Parity (Week 1) — +6 points

**Goal:** Bring bridge and Android CI to Hub's level.

| Task | Repo | Effort | Impact |
|------|------|--------|--------|
| Add `go test ./...` to bridge validate stage | bridge `.gitlab-ci.yml` | 30min | +2 |
| Add `gosec` + `govulncheck` to bridge CI | bridge `.gitlab-ci.yml` | 1h | +1 |
| Add `./gradlew test` + `./gradlew lint` to Android CI | android `.gitlab-ci.yml` | 1h | +2 |
| Add pre-deploy health check to bridge deploy | bridge `.gitlab-ci.yml` | 1h | +1 |

### Phase 2: Critical Bug Fix (Week 1) — +2 points

| Task | Repo | Effort | Impact |
|------|------|--------|--------|
| Fix MESHSAT-154: MQTT reconnect subscription recovery | hub | 4-8h | +2 |

**Approach:** On Paho MQTT OnConnect callback, re-subscribe all topic filters. Add integration test with broker restart simulation.

### Phase 3: Observability Parity (Week 2) — +3 points

| Task | Repo | Effort | Impact |
|------|------|--------|--------|
| Add Prometheus metrics endpoint to bridge (`/metrics`) | bridge `internal/api/` | 4h | +1.5 |
| Add key metrics: messages sent/received, delivery latency, gateway health, error rates | bridge | 2h | +1 |
| Add Grafana dashboard template for bridge | bridge `docs/` | 1h | +0.5 |

### Phase 4: Android Hardening (Week 2) — +3 points

| Task | Repo | Effort | Impact |
|------|------|--------|--------|
| Configure R8/ProGuard, release signing, build shrinking | android `build.gradle` | 2h | +1 |
| Add crash reporting (Sentry or Firebase Crashlytics) | android | 2h | +1 |
| Wire TOFU key pinning in KeyBundleImporter | android | 1h | +0.5 |
| Complete Phase E routing test coverage | android | 4h | +0.5 |

### Phase 5: API & Documentation Completeness (Week 3) — +2 points

| Task | Repo | Effort | Impact |
|------|------|--------|--------|
| Add Swagger annotations to remaining 25 bridge handlers | bridge `internal/api/` | 3h | +1 |
| Add API rate limiting middleware to bridge router | bridge `internal/api/` | 3h | +0.5 |
| Add checksum verification to install script | website `install/` | 1h | +0.25 |
| Add link validation to website CI | website `.gitlab-ci.yml` | 1h | +0.25 |

### Phase 6: Integration Testing (Week 3-4) — +2 points

| Task | Repo | Effort | Impact |
|------|------|--------|--------|
| Complete MESHSAT-338: full-stack E2E validation | all | 8h | +1 |
| Complete MESHSAT-354: Reticulum cross-component validation | all | 4h | +0.5 |
| Add bridge-to-Hub integration test in CI | bridge + hub | 4h | +0.5 |

### Phase 7: Remaining Features (Week 4+) — +2 points

| Task | Repo | Effort | Impact |
|------|------|--------|--------|
| Complete Hub v1.2 (email gateway, WireGuard, .onion) | hub | 12h | +1 |
| Wire CustodyManager for Hub DTN | hub | 2h | +0.5 |
| HeMB Hub integration (MESHSAT-415 Phase 2) | hub | 8h | +0.5 |

### Total Score Progression

| Phase | Cumulative Score | Grade |
|-------|:----------------:|:-----:|
| Current | 85/100 | B+ |
| After Phase 1 (CI parity) | 91/100 | A- |
| After Phase 2 (P0 fix) | 93/100 | A |
| After Phase 3 (observability) | 96/100 | A+ |
| After Phase 4 (Android) | 99/100 | A++ |
| After Phases 5-7 (polish) | **100/100** | **A+++** |

---

## 9. Recommended Priority Order

```
IMMEDIATE (this week):
  1. Add go test to bridge CI                     [30 min, +2 points]
  2. Add test+lint to Android CI                  [1 hour, +2 points]
  3. Add gosec+govulncheck to bridge CI           [1 hour, +1 point]
  4. Fix MESHSAT-154 (Hub MQTT reconnect)         [4-8 hours, +2 points]

SHORT TERM (next 2 weeks):
  5. Bridge Prometheus metrics endpoint            [4 hours, +1.5 points]
  6. Bridge API rate limiting                      [3 hours, +0.5 points]
  7. Android R8/release build                      [2 hours, +1 point]
  8. Android crash reporting                       [2 hours, +1 point]
  9. Remaining Swagger annotations                 [3 hours, +1 point]

MEDIUM TERM (next month):
  10. MESHSAT-338 full-stack E2E validation        [8 hours, +1 point]
  11. Hub v1.2 completion                          [12 hours, +1 point]
  12. HeMB Hub integration                         [8 hours, +0.5 points]
  13. Website polish (links, checksum, Lighthouse) [4 hours, +0.5 points]

LONG TERM (next quarter):
  14. MESHSAT-441 HeMB field validation NL->GR     [hardware + 16h]
  15. MESHSAT-442 HeMB IETF RFC submission         [20h writing]
  16. MESHSAT-406 BLE Reticulum interface           [8h, needs hardware]
  17. MESHSAT-412 RTL-SDR jamming detection         [16h, needs field kit]
```

---

## 10. Verification Plan

After implementing the roadmap phases, verify with:

1. **CI/CD:** Push a commit with a deliberate test failure to bridge/android — CI must reject it
2. **MESHSAT-154 fix:** Kill and restart MQTT broker while Hub is processing — verify no subscription loss
3. **Metrics:** `curl http://bridge:6050/metrics` returns Prometheus text format with message counters
4. **Rate limiting:** Send 1000 requests/sec to bridge API — verify 429 responses after threshold
5. **Android:** Release APK < 50M, no crash on any transport activation, Sentry reports visible
6. **E2E:** Bridge sends SBD MO -> Hub receives -> Hub sends MT -> Bridge receives -> Dashboard shows both
7. **Website:** `linkchecker docs.meshsat.net` returns 0 broken links
8. **Security:** `gosec ./...` on bridge returns 0 HIGH findings

---

## Appendix A: File Inventory

| Repo | Go Files | Test Files | Vue Components | Total LOC |
|------|:--------:|:----------:|:--------------:|:---------:|
| Bridge | 322 | 92 | 14 views + 9 components | 100,858 |
| Hub | 317 | 108 | 37 components | 67,055 |
| Android | 236 (Kotlin) | 44 | Compose-only | 48,577 |
| Website | — | — | — | 1,286 (docs) |
| **Total** | **875** | **244** | **60+** | **217,776** |

## Appendix B: Database Schema Versions

| Repo | Current Version | Key Tables | Migration Strategy |
|------|:--------------:|:----------:|-------------------|
| Bridge | v41 | messages, deliveries, dead_letters, bond_groups, hemb_events, interfaces, gateway_config | Append-only in Go code |
| Hub | v24 | messages, devices, positions, deliveries, audit_logs, rules, credentials | Append-only, Galera-replicated |
| Android | Room v10 | 13 entities (Messages, NodePosition, ForwardingRule, ConversationKey, etc.) | Room auto-migration |

## Appendix C: Deployment Topology

```
                    Internet
                       |
            +----------+----------+
            |                     |
     NL-DMZ (Leiden)       GR-DMZ (Thessaloniki)
     192.168.192.x         192.168.15.x
            |                     |
    +-------+-------+    +-------+-------+
    |       |       |    |       |       |
   Hub    OTS    NATS   Hub    OTS    NATS
   (NL)   (NL)   leaf   (GR)   (GR)   leaf
            |                     |
            +---Galera cluster----+
            +---NATS leaf nodes---+
            |                     |
     mule01 (Pi 5)        rocket01 (x86)
     Bridge+9704           Bridge+9704
     Standalone            Standalone
            |
     Android phone
     BLE+SPP+SMS
```
