Feature: Pair protocol v1 (spec/001 — PARTIALLY RETROSPECTIVE)

  # Covers: REQ-001, REQ-002, REQ-003, REQ-004, REQ-005, REQ-006, REQ-007, REQ-008, REQ-009, REQ-010, REQ-011, REQ-012, REQ-013, REQ-014, REQ-015, REQ-016, REQ-017, REQ-018, REQ-019, REQ-020, REQ-021, REQ-022, REQ-023, REQ-024, REQ-025

  Background:
    Given the bridge has internal CA configured and pair package compiled

  # REQ-001 + REQ-002 + REQ-003 — arm
  Scenario: Arming pair mode generates 32-byte key + shows 6-digit PIN with 90s TTL
    When the operator taps "Arm pair mode" on the touch display
    Then the bridge generates a 32-byte random pairing key via GeneratePairingKey()
    And a 6-digit PIN is shown on the display
    And a row is stored in pair_mode_rows with armed_at timestamp
    And the row expires 90 seconds later (pair.DefaultArmTTL)

  # REQ-005 + REQ-006 + REQ-008 + REQ-009 — happy path claim
  Scenario: Valid claim with correct PIN + HMAC succeeds
    Given an armed pair row with pin="123456" and pairing_key=<random32>
    When the remote device POSTs /api/v2/pair/claim with pin, client_ed25519_pub base64, hmac hex over the shared-secret-derived MAC
    Then the bridge derives the shared secret via HKDF-SHA256
    And verifies the HMAC with hmac.Equal() (constant-time)
    And returns HTTP 200 with cert + client_id + ca_chain

  # REQ-007 — expired arm row
  Scenario: Claim against expired arm row returns 401
    Given an armed pair row whose armed_at is 91 seconds ago
    When a valid claim arrives
    Then HTTP 401 is returned with body explaining the TTL expiry

  # REQ-010 — HMAC mismatch consumes row
  Scenario: HMAC mismatch returns 401 and consumes the row (one-shot)
    Given an armed pair row with pin="123456"
    When a claim arrives with the right PIN but wrong HMAC
    Then HTTP 401 is returned
    And the pair_mode_rows row is consumed (deleted)
    And a subsequent valid claim with the same PIN also fails

  # REQ-011 + REQ-012 — cert minting
  Scenario: Successful claim returns leaf cert signed by internal CA
    Given internal CA is configured
    When a valid claim is made
    Then the response contains cert (PEM), client_id, ca_chain (PEM)
    And cert.Subject contains client_id
    And cert is signed by the internal CA

  # REQ-013 — graceful JWT fallback
  Scenario: No internal CA returns JWT instead of cert
    Given internal CA is NOT configured
    When a valid claim is made
    Then the response contains jwt + client_id (no cert)
    And the JWT is signed with the bridge's signing key

  # REQ-014 — persist paired_clients
  Scenario: Successful claim persists paired_clients row
    When a valid claim succeeds
    Then a row in paired_clients is inserted with client_id, public_key, created_at

  # REQ-016 + REQ-017 — JWT lifecycle
  Scenario: JWT signed by client Ed25519 verifies via paired_clients lookup
    Given a paired_clients row with public_key=K1 for client_id=C1
    When the client presents a JWT signed by the matching private key
    Then the middleware verifies via paired_clients lookup
    And the request is authorized

  # REQ-018 — refresh
  Scenario: Client refreshes JWT before expiry
    Given a JWT with 5 min remaining
    When the client POSTs /api/v2/pair/refresh with the JWT
    Then a fresh JWT with full 1h TTL is returned

  # REQ-019 — JSON wire format (NOT CBOR)
  Scenario: Wire format is JSON not CBOR
    When inspecting any pair-claim payload
    Then Content-Type is application/json
    And no CBOR encoding is used

  # REQ-022 + REQ-023 — test coverage
  Scenario: pair_test.go covers shared-secret derivation determinism
    When `go test -v ./internal/pair/ -run TestDeriveSharedSecret_Agrees` runs
    Then it passes
    And the test proves DeriveSharedSecret(same_key, same_pin) returns the same output twice

  # REQ-024 + REQ-025 — out of scope assertions
  Scenario: Spec does NOT cover multi-bridge NAT
    Then multi-bridge NAT traversal is explicitly scoped to spec/009-multi-bridge-nat

  Scenario: Spec does NOT cover BLE pairing
    Then BLE-based pairing is explicitly out of scope (HTTPS-only over LAN)
