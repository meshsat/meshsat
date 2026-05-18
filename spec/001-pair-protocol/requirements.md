# Requirements — Pair protocol v1 (spec/001 — PARTIALLY RETROSPECTIVE)

Source: `internal/pair/pair.go` + `pair_test.go` (CGC-verified 2026-05-18). MESHSAT-594 / MESHSAT-598.

> Partially retrospective. The bridge-side pair-mode implementation has shipped: `pair.go` exists with the PIN + pairing-key + HKDF derivation + HMAC verification + cert signing logic. ID convention: 001-block (`001..099`).

## PIN + arming

REQ-001: The system shall use a fixed 6-digit PIN length (`pair.PinLength = 6`), giving 1,000,000 combinations.
REQ-002: When the operator arms pair mode from the touch display, the system shall generate a 32-byte cryptographic random pairing key via `GeneratePairingKey()`.
REQ-003: While a pair-mode row is armed, the system shall enforce a 90-second TTL (`pair.DefaultArmTTL`) after which claims are rejected.
REQ-004: The system shall store the PIN + pairing key + armed-at timestamp in the bridge database (touch-display-controlled lifecycle).

## Claim endpoint

REQ-005: The system shall expose `POST /api/v2/pair/claim` accepting `{pin: string, client_ed25519_pub: base64, hmac: hex}`.
REQ-006: When a claim request arrives, the system shall lookup the armed pair-mode row matching the PIN.
REQ-007: If no armed row matches the PIN OR the row has expired (> 90s old), then the system shall return HTTP 401 with body explaining the mismatch.
REQ-008: When a matching armed row is found, the system shall derive the shared secret via `DeriveSharedSecret(pairing_key, pin)` using HKDF-SHA256.
REQ-009: The system shall verify the client's HMAC by computing `HMAC-SHA256(shared_secret, client_ed25519_pub)` and comparing with `hmac.Equal()` for constant-time comparison.
REQ-010: If the HMAC does not match, then the system shall return HTTP 401 and consume the pair-mode row (one-shot).

## Cert signing + response

REQ-011: When HMAC verification succeeds, the system shall mint an Ed25519 leaf certificate signed by the bridge's internal CA (MESHSAT-595) if configured.
REQ-012: The system shall return `{cert: PEM, client_id: string, ca_chain: PEM, rns_announce: optional, hub_url: optional}` on successful claim.
REQ-013: While the internal CA is not configured, the system shall return a JWT instead of a cert (graceful fallback).
REQ-014: The system shall persist the new `paired_clients` row with `client_id`, `public_key`, `created_at`.
REQ-015: When a successful claim is processed, the system shall consume the armed pair-mode row so it cannot be reused.

## JWT lifecycle

REQ-016: The system shall issue JWTs with a 1-hour TTL (`pair.JWTTTL`).
REQ-017: When the client presents a JWT signed by its Ed25519 private key, the middleware shall verify against `paired_clients.public_key`.
REQ-018: The system shall expose `POST /api/v2/pair/refresh` for clients to refresh JWTs before expiry.

## Wire format

REQ-019: The system shall use JSON for all pair-claim wire payloads (NOT CBOR, despite issue spec mention — consistent with signed-artefact convention across the repo per `directory/qr.go` precedent).
REQ-020: The system shall base64-encode binary fields in the JSON payload (`client_ed25519_pub`).
REQ-021: The system shall hex-encode the HMAC field in the JSON payload.

## Test coverage

REQ-022: The system shall include `pair_test.go` covering: shared-secret derivation agreement, PIN mismatch, expired arm row, HMAC verification, cert signing.
REQ-023: The system shall test the HKDF derivation determinism (same pairing_key + same pin → same shared secret).

## Out of scope (Phase 9)

REQ-024: The system shall NOT implement multi-bridge NAT traversal in this spec — covered by spec/009-multi-bridge-nat.
REQ-025: The system shall NOT implement BLE-based pairing — pair-mode is HTTPS-only over LAN.
