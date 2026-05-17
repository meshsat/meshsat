Feature: Pair protocol (MeshSat Bridge Phase 8)

  Parent epic: MESHSAT-532. Subtasks: MESHSAT-595 (CA), MESHSAT-606 (touch UI), MESHSAT-607 (e2e test).

  Background:
    Given a freshly-booted bridge with an empty paired_client table
    And the internal CA root certificate has been generated and persisted
    And the bridge is in the idle state

  # REQ-001 — physical-touch arm
  Scenario: Operator arms pair mode from touch display
    When the operator taps the "Arm pair mode" button on the touch display
    Then the bridge state transitions from idle to armed
    And the armed window is 60 seconds
    And the touch UI renders a countdown ring starting at 60

  # REQ-002 — armed-state filter
  Scenario: Non-claim endpoints rejected while armed
    Given the bridge is in the armed state with 30 seconds remaining
    When a GET request to /api/pair/status arrives
    Then the response status is 200
    When a POST request to /api/pair/arm arrives WITHOUT a fresh touch event
    Then the response status is 409
    And the response body field "error" equals "not-armed"

  # REQ-003 — armed window expires
  Scenario: Armed window expires without a pair-claim
    Given the bridge has been in the armed state for 60 seconds
    When the 60-second timer expires
    Then the bridge state transitions from armed to idle
    And an audit_log entry of type "pair.arm.timeout" exists with event_at within the last 2 seconds

  # REQ-004 — re-arm refreshes
  Scenario: Operator re-arms during an existing armed window
    Given the bridge is in the armed state with 30 seconds remaining
    When the operator taps the "Arm pair mode" button again
    Then the armed window is reset to 60 seconds
    And the response field "refreshed" equals true
    And an audit_log entry of type "pair.arm.refresh" exists

  # REQ-005 — successful pair-claim
  Scenario: Pair-claim with valid CSR + HMAC succeeds
    Given the bridge is in the armed state
    And a client has generated a valid ECDH keypair + CSR + HMAC
    When the client POSTs to /api/pair/claim with the pair-csr.json payload
    Then the response status is 200
    And the response body contains "cert", "ca_bundle", "jwt", "expires_at", "client_id"
    And the JWT "sub" claim equals the new client_id
    And the JWT "exp" claim is approximately now + 90 days
    And a paired_client row exists with cert_fingerprint matching the issued cert
    And an audit_log entry of type "pair.completed" exists

  # REQ-006 — bad CSR rejected
  Scenario: Pair-claim with invalid CSR signature
    Given the bridge is in the armed state
    When the client POSTs to /api/pair/claim with a CSR whose signature does NOT match its public key
    Then the response status is 422
    And the response body field "error" equals "csr-invalid"
    And the bridge state remains armed
    And an audit_log entry of type "pair.claim.csr-invalid" exists

  # REQ-007 — bad HMAC rejected
  Scenario: Pair-claim with invalid HMAC
    Given the bridge is in the armed state
    When the client POSTs to /api/pair/claim with a valid CSR but a corrupted HMAC byte
    Then the response status is 422
    And the response body field "error" equals "hmac-invalid"
    And the bridge state remains armed
    And an audit_log entry of type "pair.claim.hmac-invalid" exists

  # REQ-008 — claim while not armed rejected
  Scenario: Pair-claim arrives while bridge is in idle state
    Given the bridge is in the idle state
    When a client POSTs to /api/pair/claim
    Then the response status is 409
    And the response body field "error" equals "not-armed"
    And an audit_log entry of type "pair.claim.rejected" exists

  # REQ-009 + REQ-010 — CA validity periods
  Scenario: Internal CA root and leaf validity periods
    Given the internal CA root certificate exists
    Then the CA root "NotAfter" minus "NotBefore" equals approximately 5 years
    And the CA root algorithm is "ECDSA-P256"
    When a pair-claim succeeds (REQ-005)
    Then the issued leaf cert "NotAfter" minus "NotBefore" equals approximately 90 days
    And the issued leaf algorithm is "ECDSA-P256"

  # REQ-011 — master-key wrapping
  Scenario: CA root private key is wrapped via master-key envelope
    Given the internal CA root row exists in ca_root
    When the ca_root.wrapped_private_key column is inspected
    Then the bytes are NOT a recognisable PEM or DER private key
    And the bytes can be unwrapped to a valid ECDSA P-256 private key using the master key + ca_root.wrap_nonce

  # REQ-012 — leaf-near-expiry warning
  Scenario: Daily warning while leaf cert is near expiry
    Given a paired_client cert with cert_not_after = now + 3 days
    When the daily cert-expiry check runs
    Then an audit_log entry of type "pair.ca.leaf-near-expiry" exists with expires_in_days = 3

  # REQ-013 — first-boot CA generation
  Scenario: First-boot generates a CA root if none exists
    Given the ca_root table is empty
    When the bridge starts and begins accepting pair-claims
    Then a ca_root row exists with a freshly-generated ECDSA P-256 root
    And the wrapped_private_key column is populated
    And the wrap_nonce column is 12 bytes

  # REQ-014 — Hub event emitted on completion
  Scenario: pair.completed event is published to Hub uplink
    Given a pair-claim has just succeeded (REQ-005)
    Then a message is queued on topic "meshsat/{bridge_id}/pair/completed"
    And the payload matches pair-event.json schema with event_type = "pair.completed"

  # REQ-015 — Hub-unreachable queues event
  Scenario: Hub unreachable at completion queues the event
    Given a pair-claim is about to succeed
    And the Hub MQTT connection is offline
    When the pair completes
    Then the pair.completed event is enqueued in message_deliveries with status = "queued"
    And the DeliveryWorker will retry per Constitution Article VI

  # REQ-016 — confirmation banner
  Scenario: Touch UI confirmation after successful pair
    Given the bridge is in the armed state
    When a pair-claim succeeds for client_label "Alice phone"
    Then the touch display shows "Paired with Alice phone" for exactly 5 seconds
    And after 5 seconds the touch display returns to the home view

  # REQ-017 — countdown render
  Scenario: Countdown ring renders every second
    Given the bridge is in the armed state with 60 seconds remaining
    When 5 seconds elapse
    Then the touch UI countdown ring renders "55" remaining

  # REQ-018 — rejection indicator
  Scenario: Touch UI shows rejection indicator on failed claim
    Given the bridge is in the armed state
    When a pair-claim fails with reason "csr-invalid"
    Then the touch display shows "Pair attempt rejected (csr-invalid)" for exactly 3 seconds
    And after 3 seconds the touch UI returns to the countdown view

  # REQ-019 — performance budget
  Scenario: Pair handshake completes in less than 800ms p99
    Given the bridge is in the armed state
    When 100 sequential pair-claims complete successfully (with re-arm between each)
    Then the p99 latency of POST /api/pair/claim is less than 800ms

  # REQ-020 — capacity enforced (TOCTOU-safe)
  Scenario: 11th paired client rejected with 429
    Given 10 paired_client rows already exist with revoked = 0
    And the bridge is in the armed state
    When a client POSTs a valid pair-claim
    Then the response status is 429
    And the response body field "error" equals "capacity-exceeded"
    And no audit_log entry of type "pair.claim.received" is created
    And no new paired_client row is created (atomic check-and-insert holds)

  # REQ-021 — claim-mid-expiry boundary
  Scenario: Claim arrives at second 59.9 and processing takes 500ms
    Given the bridge entered the armed state 59.5 seconds ago
    When a valid pair-claim is received and verification takes 500ms
    Then the pair-claim completes successfully (claim accepted at RECEIPT time, not completion)
    And the response status is 200
    And the bridge transitions to confirmed state after the response

  # REQ-022 — pair code rendered
  Scenario: Touch display renders a 6-char pair code on arm
    When the operator taps "Arm pair mode"
    Then the touch display shows a 6-character alphanumeric code matching pattern "^[A-HJ-NP-Z2-9]{6}$"
    And the code is also returned in GET /api/pair/status as field "pair_code"
    And the code is NEVER written to audit_log or any other log

  # REQ-022 — re-arm regenerates code
  Scenario: Re-arm produces a NEW pair code
    Given the bridge is in the armed state with code "ABCDEF"
    When the operator taps "Arm pair mode" again
    Then the touch display shows a different 6-character code
    And the old code "ABCDEF" no longer derives a valid HMAC for any claim

  # REQ-023 — remote arm via operator session
  Scenario: Operator with authenticated session arms remotely
    Given the operator is logged into the SPA with a valid session
    When the SPA POSTs to /api/pair/arm with body {"armed_by": "<session-id>"}
    Then the bridge transitions to armed state for 60 seconds
    And an audit_log entry of type "pair.arm.start" exists with armed_by_session matching the session ID
    And the touch display ALSO renders the countdown + pair code

  # REQ-024 — expired root refused, no auto-regen
  Scenario: Bridge boots with expired CA root
    Given the ca_root row exists with NotAfter in the past (5+ years ago)
    When the bridge starts
    Then the bridge accepts /api/pair/status requests
    But the bridge refuses /api/pair/arm with 503 ca-expired
    And the bridge refuses /api/pair/claim with 503 ca-expired
    And the bridge does NOT auto-generate a new root
    And an audit_log entry of type "pair.ca.expired" exists

  # REQ-024 — operator-approved regeneration invalidates clients
  Scenario: Operator approves regeneration after expired root
    Given the bridge has an expired CA root
    And 5 paired_client rows exist with revoked = 0
    When the operator POSTs to /api/pair/regenerate-root with confirm = "regenerate-and-invalidate-all-paired-clients"
    Then the response status is 200
    And response field "paired_clients_invalidated" equals 5
    And all 5 paired_client rows now have revoked = 1
    And a new ca_root row exists with NotAfter approximately now + 5y
    And an audit_log entry of type "pair.ca.regenerated" exists

  # REQ-025 — Prometheus metrics exposed
  Scenario: /metrics exposes pair counters
    When GET /metrics is called
    Then the response body contains "meshsat_pair_attempts_total"
    And the response body contains "meshsat_pair_handshake_duration_seconds"
    And the response body contains "meshsat_pair_armed_windows_total"
    And the response body contains "meshsat_paired_clients_current"
