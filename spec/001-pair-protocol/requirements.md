# Requirements — 001-pair-protocol (MeshSat Bridge Phase 8)

Parent epic: **MESHSAT-532** (Phase 8 — Pair protocol v1 + Android pair shell + browser-as-remote-control)

EARS-only requirements. Every line MUST match one of: ubiquitous / event-driven (When) / state-driven (While) / optional (Where) / unwanted-behaviour (If...then). Verified by `validate-project-spec.py --check ears_compliance`.

## Arming

REQ-001: When the operator physically taps the "Arm pair mode" button on the bridge touch display, the bridge shall enter the armed-pair state for exactly 60 seconds.

REQ-002: While the bridge is in the armed-pair state, the bridge shall accept POST /api/pair/claim requests and reject all other pair-protocol API requests with 409 not-armed.

REQ-003: When the 60-second armed window expires without a successful pair-claim, the bridge shall return to the idle state and log an audit event of type pair.arm.timeout.

REQ-004: If a second touch-arm request arrives while the bridge is already in the armed-pair state, then the bridge shall reset the 60-second countdown and log an audit event of type pair.arm.refresh.

## Pair-claim verification

REQ-005: When a pair-claim arrives with a CSR signed by the requester's ECDSA P-256 private key AND a valid HMAC-SHA256 over (csr_bytes || ecdh_pub || nonce) using the operator-typed pair code as the shared secret per ADR-0006, the bridge shall mint an X.509 client certificate from the internal CA AND a JWT signed by the existing bridge JWT key (MESHSAT-598) with claims sub=client_id, iss=bridge_id, iat=now, exp=now+90d.

REQ-006: If the pair-claim CSR signature verification fails (signature does not match the embedded public key), then the bridge shall return 422 csr-invalid and log an audit event of type pair.claim.csr-invalid.

REQ-007: If the pair-claim HMAC verification fails (HMAC does not match expected over csr_bytes || ecdh_pub || nonce using the operator pair code), then the bridge shall return 422 hmac-invalid and log an audit event of type pair.claim.hmac-invalid.

REQ-008: If a pair-claim arrives while the bridge is NOT in the armed-pair state, then the bridge shall return 409 not-armed and log an audit event of type pair.claim.rejected.

REQ-021: While a pair-claim is being verified, the bridge shall hold the armed-state until the claim resolves (success or rejection) even if the 60-second timer would otherwise expire during processing. The countdown timer is checked at RECEIPT, not at COMPLETION.

REQ-022: While the bridge is in the armed-pair state, the touch display shall render a 6-character alphanumeric pair code (uppercase, no I/O/0/1 to avoid confusion) per ADR-0006 that the client operator must type into the client app to derive the HMAC shared secret.

REQ-023: When an operator with an authenticated operator session POSTs to /api/pair/arm with body {"armed_by": "<session-id>"}, the bridge shall enter the armed-pair state identically to physical-touch arming (REQ-001) and emit an audit event of type pair.arm.start with armed_by_session populated. This enables remote arm via the SPA (for accessibility — e.g. operator with mobility limitations cannot reach the touch display).

## Internal CA (MESHSAT-595 surface)

REQ-009: The internal CA root certificate shall be valid for 5 years from creation and use ECDSA P-256.

REQ-010: The internal CA shall issue client leaf certificates with a 90-day validity period and ECDSA P-256 keys.

REQ-011: The internal CA root private key shall be wrapped via the master-key envelope encryption mechanism per Constitution Article XIII; the unwrapped key shall exist in memory only during the signing operation.

REQ-012: While the most recently issued CA leaf certificate is within 7 days of expiry, the bridge shall log a daily audit event of type pair.ca.leaf-near-expiry.

REQ-013: When the bridge starts and no internal CA root certificate exists in the database, the bridge shall generate a fresh root + wrapping nonce and persist both before accepting any pair-claim.

REQ-024: If the bridge starts and the internal CA root certificate's NotAfter is in the past, then the bridge shall refuse all pair-claims with 503 ca-expired AND log an audit event of type pair.ca.expired AND require explicit operator approval via /api/pair/regenerate-root (operator-session-authenticated) before generating a new root. Automatic re-generation is FORBIDDEN — root rotation invalidates all paired clients per ADR-0005.

## Audit + Hub uplink

REQ-014: When a pair-claim completes successfully, the bridge shall append a pair.completed entry to the audit log AND emit a pair.completed event on the Hub MQTT uplink with payload conforming to spec/001-pair-protocol/contracts/schemas/pair-event.json (event_type, event_at, bridge_id, client_id, client_label, cert_fingerprint).

REQ-015: If the Hub uplink is unreachable at the moment of pair completion, then the bridge shall queue the pair.completed event in the message_deliveries ledger for retry per Constitution Article VI.

## Touch UI confirmation (MESHSAT-606 surface)

REQ-016: When a pair-claim completes successfully, the touch display shall render a "Paired with <client_label>" confirmation for 5 seconds before returning to the home view.

REQ-017: While the bridge is in the armed-pair state, the touch display shall render a countdown showing remaining seconds (60 to 0) updated every 1 second.

REQ-018: If a pair-claim fails verification, then the touch display shall render a "Pair attempt rejected (<reason>)" indicator for 3 seconds.

## Non-functional

REQ-019: The system shall complete one full pair-claim handshake (verify CSR + verify HMAC + mint cert + mint JWT + persist + return response) in less than 800ms p99 on the production Pi 5 hardware.

REQ-020: The bridge shall support a maximum of 10 concurrent paired clients (rows in paired_client where revoked=0). Capacity check uses SELECT COUNT WITH SHARED LOCK on paired_client BEFORE the cert is minted; additional pair-claims while at-capacity shall return 429 capacity-exceeded WITHOUT consuming an audit_log slot. The check + insert are atomic within a single SQLite transaction to prevent TOCTOU races.

REQ-025: The bridge shall expose Prometheus counters at /metrics: meshsat_pair_attempts_total{result="completed|csr-invalid|hmac-invalid|not-armed|capacity-exceeded|ca-expired"}, meshsat_pair_handshake_duration_seconds (histogram, buckets aligned to REQ-019 budget), meshsat_pair_armed_windows_total{outcome="completed|timeout"}, meshsat_paired_clients_current (gauge).
