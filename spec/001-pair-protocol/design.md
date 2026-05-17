# Design — 001-pair-protocol

Parent epic: MESHSAT-532 (Phase 8). Subtasks: MESHSAT-595 (CA), MESHSAT-606 (touch UI), MESHSAT-607 (e2e test).

## Overview

The pair protocol implements a Hue-pushlink-inspired pattern: the operator physically arms pairing on the bridge (60-second window); the client claims pairing during the window with a CSR + HMAC; the bridge mints a client cert + JWT and returns a bundle the client uses for all subsequent mTLS calls.

This design assumes Phase 8 foundations are already merged (per CLAUDE.md L222 + dossier):
- MESHSAT-593: schema v49 (pair-related tables)
- MESHSAT-594: `internal/pair/` package (state machine + CBOR codec, 316 lines)
- MESHSAT-596: mTLS API skeleton
- MESHSAT-598: JWT middleware
- MESHSAT-605: browser-as-remote-control surface

This spec only adds: internal CA (T-001), touch-arm UI (T-002), and end-to-end test (T-003).

## State machine

```
                   ┌──────────────────┐
                   │      idle        │
                   └────────┬─────────┘
                            │ operator-touch arm-button
                            ▼
                   ┌──────────────────┐
                   │     armed        │◀────── operator-touch (refresh, REQ-004)
                   │  (countdown 60s) │
                   └────────┬─────────┘
                            │
              ┌─────────────┼─────────────┐
              │             │             │
   pair-claim │  60s timer  │             │ invalid claim
   arrives    │  expires    │             │ (CSR/HMAC fail)
              ▼             ▼             ▼
       ┌────────────┐  ┌─────────┐  ┌──────────┐
       │ verifying  │  │  idle   │  │  armed   │ (stays armed,
       └─────┬──────┘  │ (REQ-3) │  └──────────┘  REQ-018)
             │         └─────────┘
             ├─────────────┐
   verify    │ verify      │
   passes    │ fails       │
             ▼             ▼
       ┌──────────┐  ┌──────────┐
       │ issuing  │  │  armed   │ (stays armed,
       └─────┬────┘  └──────────┘  REQ-006/007)
             │
             ├─────────────┐
   issue     │ issue       │
   succeeds  │ fails       │
             ▼             ▼
       ┌──────────┐  ┌──────────┐
       │confirmed │  │  failed  │
       │  (5s UI) │  │  (3s UI) │
       └────┬─────┘  └─────┬────┘
            │              │
            └──────┬───────┘
                   ▼
            ┌──────────────┐
            │     idle     │
            └──────────────┘
```

State transitions are atomic via the existing `internal/pair/state.go` (MESHSAT-594).

## Pair-claim handshake (sequence)

```
Client (Android/browser)              Bridge
─────────────────────────────────────────────────────────────
operator arms bridge ──physical touch──▶ idle → armed (start 60s timer)
                                          │
QR scan / discover bridge URL             │
                                          │
generate ECDH ephemeral keypair           │
compute HMAC = HMAC(shared_secret,        │
                    csr_bytes + nonce)    │
                                          │
POST /api/pair/claim                      │
  body: {csr, ecdh_pub, hmac, nonce}      │
  ───────────────────────────────────────▶│
                                          │ verify ECDH (derive shared)
                                          │ verify HMAC over body
                                          │ → if fail: 422 + audit + stay armed
                                          │
                                          │ verify CSR signature
                                          │ → if fail: 422 + audit + stay armed
                                          │
                                          │ mint client cert (CA leaf, 90d)
                                          │ mint JWT (sub=client_id, exp=90d)
                                          │
                                          │ persist paired_client row
                                          │ append audit log (pair.completed)
                                          │ emit Hub event (pair.completed)
                                          │ → if Hub unreachable: queue in
                                          │   message_deliveries for retry
                                          │
                                          │ touch UI: "Paired with <label>" 5s
                                          │ state: armed → confirmed → idle
                                          │
  ◀───────────────────────────────────────│
  200 {cert, ca_bundle, jwt, expires_at}  │
                                          │
client trusts ca_bundle, uses cert+jwt    │
for all subsequent mTLS calls             │
```

## Internal CA hierarchy (MESHSAT-595)

```
                   ┌─────────────────────────────┐
                   │  Root CA (5y, ECDSA P-256)  │
                   │  Subject: CN=Bridge-Root,   │
                   │           O=MeshSat,        │
                   │           CN=<bridge_id>    │
                   │  Stored: ca_root row,       │
                   │   private key wrapped via   │
                   │   master-key envelope       │
                   └────────────┬────────────────┘
                                │
                  ┌─────────────┼─────────────┐
                  │             │             │
                  ▼             ▼             ▼
              ┌──────┐      ┌──────┐      ┌──────┐
              │ Leaf │      │ Leaf │      │ Leaf │  (90d, ECDSA P-256)
              │ #1   │      │ #2   │      │ #N   │  Subject: CN=<client_id>
              └──────┘      └──────┘      └──────┘  Stored: paired_client row
                  │             │             │
                  ▼             ▼             ▼
              client #1     client #2     client #N
              (Android,     (browser,     (custom)
               MESHSAT-     MESHSAT-
               601)         605)
```

Rotation policy: leaf certs expire at 90d; clients must re-pair to renew (no automatic renewal in v1 — explicitly out of scope). REQ-012 logs a warning 7 days before leaf expiry.

Root cert rotation: 5-year period. Out of scope for v1; covered by ADR-0005 as future work.

## Touch UI (MESHSAT-606)

The pair-mode screen is a single Vue component (`web/src/views/PairArmingView.vue`) added to the existing Settings → Devices navigation tree. Implements:
- Arm button: triggers POST /api/pair/arm
- Countdown ring (60→0 seconds, REQ-017)
- Confirmation banner (5s, REQ-016)
- Rejection indicator (3s, REQ-018)

No new design tokens needed; uses existing Tailwind palette. Kiosk-mode compatible (touch-only, no hover states).

## End-to-end test (MESHSAT-607)

`test/integration/pair_e2e_test.go` exercises the full handshake:

1. Setup: spin up bridge in test mode (in-memory SQLite, no Hub uplink), trigger arm via test-hook (bypasses physical touch — operator-pattern simulated by HTTP call from test-fixture port)
2. Generate synthetic ECDH + CSR + HMAC in the test client
3. POST /api/pair/claim
4. Assert: 200 response with valid cert + JWT + ca_bundle
5. Assert: paired_client row in SQLite, audit_log entry, Hub event queued (since Hub uplink is offline in test mode)
6. Re-call protected endpoint with new cert + JWT, assert 200 (proves cert is trusted)
7. Tear down

Also includes a Playwright spec (`web/e2e/pair-arming.spec.js`) that exercises the touch-arm UI (countdown render, confirmation banner, rejection indicator).

## Error handling

- 409 not-armed: pair-claim outside armed window (REQ-008)
- 422 csr-invalid: CSR signature verification failed (REQ-006)
- 422 hmac-invalid: HMAC verification failed (REQ-007)
- 429 capacity-exceeded: ≥10 concurrent paired clients (REQ-020)
- 500: anything else (CA signing failure, DB write failure) — surface in audit_log with full error context

## Performance budget

REQ-019: ≤800ms p99 for one full handshake. Breakdown estimate:
- CSR signature verify: ~50ms (ECDSA P-256)
- HMAC verify: ~5ms
- ECDH compute: ~30ms
- Mint cert (sign with CA private key): ~80ms (ECDSA P-256 sign + DER encode)
- Mint JWT: ~10ms
- Master-key unwrap + re-wrap: ~50ms (AES-256-GCM)
- DB writes (paired_client + audit_log): ~30ms (SQLite WAL mode)
- Hub event queue: ~10ms

Total: ~265ms in the happy path; 800ms p99 leaves 535ms headroom for SQLite contention, GC pauses, request parsing. Comfortable.

## Out-of-scope (v1)

- Automatic leaf-cert renewal (clients re-pair to renew)
- Root CA rotation tooling (manual operator procedure documented in `docs/runbooks/ca-rotation.md`, to be authored)
- Client revocation (no CRL; bridge restart drops all paired clients — explicit operator UX choice for v1)
- Multi-bridge pair-relay (Phase 9 territory, MESHSAT-533)

## References

- Constitution Articles VI (DeliveryWorker is sole outbound path), VII (Iridium serial-mutex — unrelated, mentioned for general respect), XI (single trusted container), XIII (master-key envelope encryption)
- ADR-0004 (CSR transport: JSON-body vs raw protobuf)
- ADR-0005 (bridge-issued vs hub-issued client certs)
- Hue pushlink pattern (60s pairing window) — operator UX precedent
- Existing `internal/pair/state.go` (MESHSAT-594, 316 lines)
- `internal/database/migrations.go` v49 (MESHSAT-593, pair tables)
