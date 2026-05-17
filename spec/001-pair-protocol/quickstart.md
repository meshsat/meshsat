# Quickstart — 001-pair-protocol

Smoke-test the pair protocol end-to-end against a local bridge in test mode.

## Prerequisites

- Bridge running in test mode: `make build-with-web && ./meshsat --test-mode --port 8443`
- Test client tooling: `curl`, `jq`, `openssl` (for CSR generation)
- The bridge's CA bundle has been fetched once (auto on first arm)

## Steps

### 1. Arm pair mode (operator-side)

```bash
# Operator physically taps "Arm pair mode" on the touch UI, OR (test mode only):
curl -X POST http://localhost:8443/api/pair/arm \
  -H "Content-Type: application/json" \
  -d '{"armed_by": "touch"}'
# Response: {"armed_at": 1747486800, "expires_at": 1747486860, "refreshed": false}
```

### 2. Generate client CSR + HMAC (client-side)

```bash
# Generate ECDSA P-256 keypair + CSR
openssl ecparam -genkey -name prime256v1 -out client.key
openssl req -new -key client.key -out client.csr -subj "/CN=test-client"

# Generate X25519 ECDH ephemeral pubkey (for HMAC derivation)
openssl genpkey -algorithm X25519 -out ecdh.key
openssl pkey -in ecdh.key -pubout -outform DER \
  | tail -c 32 | base64 > ecdh_pub.b64

# Random 16-byte nonce
openssl rand 16 | base64 > nonce.b64

# HMAC over csr || ecdh_pub || nonce (shared secret out-of-band for test)
CSR_BODY=$(cat client.csr | tail -n +2 | head -n -1 | tr -d '\n' | base64 -d)
SHARED_SECRET="test-shared-secret-replace-in-prod"
printf '%s%s%s' "$CSR_BODY" "$(cat ecdh_pub.b64 | base64 -d)" "$(cat nonce.b64 | base64 -d)" \
  | openssl dgst -sha256 -hmac "$SHARED_SECRET" -binary \
  | base64 > hmac.b64
```

### 3. POST pair-claim

```bash
curl -X POST http://localhost:8443/api/pair/claim \
  -H "Content-Type: application/json" \
  -d "{
    \"csr\": \"$(cat client.csr | tr -d '\n' | sed 's/\"/\\\"/g')\",
    \"ecdh_pub\": \"$(cat ecdh_pub.b64)\",
    \"hmac\": \"$(cat hmac.b64)\",
    \"nonce\": \"$(cat nonce.b64)\",
    \"client_label\": \"smoke test\"
  }" | jq .

# Expected response:
# {
#   "client_id": "8e3b2a1f-4d5c-4e6f-9a0b-1c2d3e4f5a6b",
#   "cert": "-----BEGIN CERTIFICATE-----\n...",
#   "ca_bundle": "-----BEGIN CERTIFICATE-----\n...",
#   "jwt": "eyJ...",
#   "expires_at": 1755262800,
#   "bridge_id": "parallax01"
# }
```

### 4. Verify cert + use it

```bash
# Save the issued cert + ca_bundle
jq -r .cert response.json > client.crt
jq -r .ca_bundle response.json > ca.crt
JWT=$(jq -r .jwt response.json)

# Verify cert is signed by the bridge CA
openssl verify -CAfile ca.crt client.crt
# Expected: client.crt: OK

# Call a protected endpoint with cert + JWT
curl --cert client.crt --key client.key --cacert ca.crt \
  -H "Authorization: Bearer $JWT" \
  https://localhost:8443/api/devices
# Expected: 200 with device list
```

### 5. Verify audit log + Hub event

```bash
sqlite3 meshsat.db "SELECT event_type, event_at, client_id FROM audit_log WHERE event_type LIKE 'pair.%' ORDER BY event_at DESC LIMIT 5;"

# In Hub-offline test mode:
sqlite3 meshsat.db "SELECT topic, status, payload FROM message_deliveries WHERE topic LIKE '%/pair/%' ORDER BY created_at DESC LIMIT 5;"
```

## What to watch for

- **Performance:** REQ-019 budgets the full handshake at <800ms p99. `pair_perf_test.go` runs 100 iterations; manual smoke should feel snappy.
- **Touch UI:** the countdown ring should redraw every 1s (REQ-017). The "Paired with <label>" banner should render for 5s post-success (REQ-016).
- **Failure paths:** if you tamper with the HMAC (flip a byte), expect 422 + bridge stays armed (REQ-007 + bridge stays receptive to retries until 60s expires).

## Where this fits

After paired, the client uses `cert + JWT` for all subsequent mTLS calls to the bridge API. JWT expires at +90d, cert at +90d — re-pair to renew. CA bundle pinned in client's trust store (TOFU per ADR-0005).

For end-to-end test automation: `test/integration/pair_e2e_test.go` (T-003) does all of the above programmatically + asserts every REQ.
