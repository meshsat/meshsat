# Design — Pair protocol v1 (spec/001 — RETROSPECTIVE)

CGC-grounded against `internal/pair/pair.go` + `pair_test.go` 2026-05-18.

## Real shape (CGC-verified)

```go
// Package pair implements the bridge side of the pair-mode protocol.
// [MESHSAT-594 / MESHSAT-598]

package pair

const PinLength = 6
const DefaultArmTTL = 90 * time.Second
const JWTTTL = 1 * time.Hour

func GeneratePairingKey() ([]byte, error)
func DeriveSharedSecret(pairingKey []byte, pin string) ([]byte, error)
// ... + HMAC verify + cert sign + JWT issue/verify
```

Uses:
- `crypto/ed25519` for client identity
- `crypto/hmac` + `crypto/sha256` for claim authenticity
- `golang.org/x/crypto/hkdf` for shared-secret derivation
- `encoding/base64` + `encoding/hex` for wire encoding
- JSON wire format (NOT CBOR — deliberately consistent with `directory/qr.go` precedent)

## Wire diagram

```
Operator taps "Arm pair mode" on touch display
       │
       ▼
Bridge: GeneratePairingKey() → 32-byte random
        store {pin, pairing_key, armed_at} in pair_mode_rows table
        show 6-digit PIN on display
        90s TTL armed
       │
       ▼ (operator enters PIN on remote device)
Remote: Generate Ed25519 keypair
        Compute shared_secret = HKDF-SHA256(pairing_key || pin, "meshsat-pair-v1", 32)
        Compute hmac = HMAC-SHA256(shared_secret, ed25519_pub)
        POST /api/v2/pair/claim {pin, client_ed25519_pub: base64, hmac: hex}
       │
       ▼
Bridge: lookup armed row by PIN
        if not found OR expired → HTTP 401
        else:
          shared_secret = DeriveSharedSecret(row.pairing_key, pin)
          expected_hmac = HMAC-SHA256(shared_secret, decoded_client_pub)
          if !hmac.Equal(expected_hmac, decoded_client_hmac) → HTTP 401, consume row
          else:
            if internal_CA configured:
              cert = signLeafCert(client_pub, validity=...)
              response = {cert, client_id, ca_chain, rns_announce?, hub_url?}
            else:
              jwt = issueJWT(client_id, ttl=1h)
              response = {jwt, client_id, rns_announce?, hub_url?}
            persist paired_clients row {client_id, public_key, created_at}
            consume armed row (one-shot)
            return 200 with response
       │
       ▼ (steady state)
Remote: present JWT signed by Ed25519 private key
Bridge: middleware verifies against paired_clients.public_key
        refresh via POST /api/v2/pair/refresh before expiry
```

## Real file paths (CGC-verified)

| File | Status |
|---|---|
| `internal/pair/pair.go` | EXISTS — full bridge-side implementation |
| `internal/pair/pair_test.go` | EXISTS — `TestDeriveSharedSecret_Agrees` + others |
| `internal/api/` | needed — claim handler routes /api/v2/pair/claim → pair.HandleClaim() |
| `internal/database/` | needed — pair_mode_rows + paired_clients schema |

## Anti-pattern (CGC-confirmed absences)

- NO CBOR encoding — JSON only (deliberate per package docstring)
- NO BLE pairing in this spec — out of scope (REQ-025)

## Out of scope

- Multi-bridge NAT traversal (spec/009)
- Android-specific UX (covered by meshsat-android/spec/003-pair-shell)
