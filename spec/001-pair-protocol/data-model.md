# Data model — 001-pair-protocol

## Existing tables (already in schema v49 via MESHSAT-593)

`paired_client`:
- `client_id` TEXT PRIMARY KEY (operator-chosen or auto-generated UUID)
- `client_label` TEXT (operator-visible name, e.g. "Alice's phone")
- `cert_pem` TEXT (issued cert PEM)
- `cert_fingerprint` TEXT (SHA-256 of cert DER, indexed)
- `cert_not_before` INTEGER (unix epoch)
- `cert_not_after` INTEGER (unix epoch)
- `paired_at` INTEGER (unix epoch)
- `last_seen_at` INTEGER (nullable, updated on mTLS handshake)
- `revoked` INTEGER DEFAULT 0 (operator can flag without deleting row)

`pair_arm`:
- `id` INTEGER PRIMARY KEY AUTOINCREMENT
- `armed_at` INTEGER (unix epoch)
- `armed_by` TEXT (operator session ID; "touch" if physical-only)
- `expires_at` INTEGER (armed_at + 60)
- `result` TEXT CHECK (result IN ('completed', 'timeout', 'rejected', 'pending'))
- `result_at` INTEGER (nullable)
- `claim_count` INTEGER DEFAULT 0 (how many claims arrived during this window)

## NEW tables (T-001 adds)

`ca_root`:
- `id` INTEGER PRIMARY KEY AUTOINCREMENT (typically only 1 row exists)
- `cert_pem` TEXT (root cert PEM, public)
- `wrapped_private_key` BLOB (private key wrapped via master-key envelope per Article XIII)
- `wrap_nonce` BLOB (AES-256-GCM nonce, 12 bytes)
- `created_at` INTEGER (unix epoch)
- `valid_until` INTEGER (cert NotAfter)
- `algorithm` TEXT DEFAULT 'ECDSA-P256'

Note: T-001's migration is `v50` (next sequential per Constitution Article III append-only rule). Does NOT modify existing migrations.

## Data shapes (in-memory + JSON contracts)

### CSR (pair-claim request body)

See `contracts/schemas/pair-csr.json`. Summary:
- `csr` (string, PEM-encoded CSR per RFC 2986)
- `ecdh_pub` (string, base64-encoded compressed X25519 public key)
- `hmac` (string, base64-encoded HMAC-SHA256 over `csr || ecdh_pub || nonce`)
- `nonce` (string, base64-encoded 16-byte nonce, anti-replay)
- `client_label` (string, operator-visible name 3-50 chars)

### Cert bundle (pair-claim response body)

See `contracts/schemas/pair-cert-bundle.json`. Summary:
- `client_id` (string, UUID)
- `cert` (string, PEM-encoded leaf cert)
- `ca_bundle` (string, PEM-encoded root cert chain)
- `jwt` (string, JWT with sub=client_id, iss=bridge_id, exp=now+90d)
- `expires_at` (integer, unix epoch, leaf cert NotAfter)
- `bridge_id` (string, UUID of the bridge)

### Audit event shape

See `contracts/schemas/pair-event.json`. Summary:
- `event_type` (enum: pair.arm.start | pair.arm.refresh | pair.arm.timeout | pair.claim.received | pair.claim.csr-invalid | pair.claim.hmac-invalid | pair.claim.rejected | pair.completed | pair.ca.leaf-near-expiry)
- `event_at` (integer, unix epoch)
- `client_id` (string, nullable)
- `cert_fingerprint` (string, nullable)
- `reason` (string, nullable, for failure events)

## Storage assumptions

- SQLite via `modernc.org/sqlite` per Constitution Article V (CGO_ENABLED=0)
- WAL mode enabled in existing config (already true)
- The `paired_client` table is queried on every mTLS handshake to verify the client cert fingerprint — needs to be fast. Index on `cert_fingerprint` per the schema.
- Audit log inserts are append-only per Constitution; no UPDATE/DELETE allowed.

## Master-key envelope encryption (Article XIII)

The CA root private key is wrapped using the existing master-key mechanism in `internal/keystore/`:

```go
// Wrap on CA generation:
nonce := make([]byte, 12); rand.Read(nonce)
ciphertext := AESGCMSeal(masterKey, nonce, privKeyDER, nil)
db.Exec("INSERT INTO ca_root ... wrapped_private_key=?, wrap_nonce=? ...", ciphertext, nonce)

// Unwrap for each signing operation (CA leaf mint):
row := db.QueryRow("SELECT wrapped_private_key, wrap_nonce FROM ca_root LIMIT 1")
privKeyDER := AESGCMOpen(masterKey, nonce, ciphertext, nil)
defer Zeroize(privKeyDER) // explicit memory clear per Article XIII
sign(privKeyDER, ...)
```

Lost master key = unrecoverable CA root key = all paired clients invalidated. Same operational contract as losing a CA private key. This is Constitution Article XIII writ explicit.
