Feature: Trust + SIDC + SOS (meshsat Phase 4, EXECUTION-PLAN §6.4)

  Background:
    Given Phase 1 (directory) and Phase 3 (UI reshape) have shipped
    And directory_contacts has the sidc + trust_level + trust_verified_at + trust_verified_by columns

  # REQ-401 + REQ-403 — milsymbol rendering on MapView
  Scenario: MapView renders milsymbol pins with the contact's SIDC
    Given contact "alice" has sidc="SFGPUCI----D--"
    When the operator opens Map
    Then alice's pin is rendered via milsymbol using SIDC "SFGPUCI----D--"

  Scenario: Contact with no SIDC renders default neutral pin
    Given contact "bob" has sidc=null
    When the operator opens Map
    Then bob's pin uses the default CoT type "a-f-G-U-C"

  # REQ-406 — trust hard-block on Flash
  Scenario: Sending Flash to unverified contact requires explicit "Send anyway"
    Given alice's trust_level=1
    When the operator opens Compose, picks alice, selects precedence="Flash"
    Then a blocking modal warns about the trust gap
    When the operator taps Cancel
    Then the message is NOT sent

  Scenario: "Send anyway" sends + audits
    Given alice's trust_level=1
    When the operator opens Compose, picks alice, selects precedence="Flash"
    And taps "Send anyway"
    Then the message is sent via POST /api/messages/send-to-contact
    And an audit_log entry of type "trust.gap_overridden" exists

  # REQ-407 + REQ-408 — verify in person via QR
  Scenario: Successful in-person scan bumps trust to 3
    Given alice's trust_level=1
    When the operator opens alice's detail pane and taps "Verify in person"
    And scans a meshsat://contact/... QR signed by alice's known signing_pubkey
    Then alice's trust_level becomes 3
    And alice's trust_verified_at equals approximately now()
    And alice's trust_verified_by equals the operator's session ID

  # REQ-409 + REQ-410 — KeyMismatch warning
  Scenario: Mismatched signing pubkey on rescan triggers KeyMismatch
    Given alice's stored signing_pubkey is PK1
    When the operator scans a contact QR for alice with signing_pubkey=PK2
    Then a KeyMismatch warning is displayed
    And alice's trust_level is NOT bumped

  Scenario: Operator-accepted key rotation resets trust to 0
    Given alice's previous trust_level=3 with signing_pubkey=PK1
    When the operator accepts a new signing_pubkey=PK2 for alice
    Then alice's trust_level resets to 0
    And precedence ≥ Immediate triggers the hard-block until re-verified

  # REQ-411 + REQ-412 + REQ-413 — contact QR endpoint
  Scenario: GET /api/directory/contacts/{id}/qr returns a signed CBOR card
    Given alice exists with addresses + signing_pubkey
    When GET /api/directory/contacts/alice/qr is called
    Then the response body parses as CBOR with v=1, contact_id="alice", addresses[], signing_pubkey
    And the Ed25519 signature over the canonical CBOR (sans signature) verifies against alice's signing_pubkey

  # REQ-414 + REQ-415 + REQ-416 + REQ-417 + REQ-418 — SOS button
  Scenario: Single accidental tap does NOT fire SOS
    When the operator taps the SOS button once
    Then no SOS message is sent

  Scenario: 3-second hold fires SOS with Flash precedence + HEMB bond
    When the operator holds the SOS button for 3 seconds
    Then a Flash-precedence message containing current location + last-48h contact briefing is sent
    And the dispatch strategy is HEMB_BONDED
    And an audit_log entry of type "sos.fired" exists (immutable hash-chain entry)
    And the counter meshsat_sos_fired_total{operator_session_id} is incremented by 1

  Scenario: Double-tap within 2 seconds fires SOS
    When the operator taps SOS twice within 2 seconds
    Then a Flash-precedence message is sent (same as 3s hold path)

  # REQ-419 + REQ-420 — SALUTE template
  Scenario: SALUTE template renders 6 labelled fields + packs to slash format
    When the operator opens Compose and picks Templates → SALUTE
    Then 6 labelled fields appear: Size, Activity, Location, Unit, Time, Equipment
    When the operator fills the fields and submits
    Then the message body equals "S=<size>/A=<activity>/L=<location>/U=<unit>/T=<time>/E=<equipment>"

  # REQ-423 — TAK destination + XML option
  Scenario: USMTF template + TAK destination + XML option emits MIL-STD-6040 XML
    Given the operator filled a SALUTE template with destination=TAK and Format=XML
    When the operator sends
    Then the message body is MIL-STD-6040 XML alongside the slash form
