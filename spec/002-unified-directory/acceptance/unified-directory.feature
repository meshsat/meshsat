Feature: Unified Directory (meshsat Phase 1, EXECUTION-PLAN §6.1)

  Foundation phase — unblocks Phases 2, 3, 4, 5, 8.

  Background:
    Given the bridge is running with database schema at version v43 (pre-migration)

  # REQ-100 + REQ-101 — migrations land + nothing edited
  Scenario: v44 through v48 land idempotently
    When the bridge starts
    Then the database schema version is v48
    And the file internal/database/migrations.go has no in-place edits to migrations 1..43

  # REQ-103 + REQ-104 — legacy backfill
  Scenario: Existing contacts + sms_contacts rows are backfilled into the unified model
    Given 5 rows exist in contacts/contact_addresses and 3 rows in sms_contacts before migration
    When the bridge starts and applies v44
    Then directory_contacts contains 8 rows (5 from contacts + 3 from sms_contacts)
    And directory_addresses contains at least 8 rows
    And every sms_contacts row is reachable via directory_addresses with kind=SMS

  # REQ-109 — signing key wrap
  Scenario: Legacy plaintext signing key is wrapped on first boot
    Given system_config.signing_private_key contained plaintext hex before migration
    When the bridge starts after v44
    Then system_config.signing_private_key_wrapped contains ciphertext
    And system_config.signing_private_key is empty
    And the SigningService can load + sign with the wrapped key

  # REQ-114 — vCard import
  Scenario: 100-contact vCard import completes under 2 seconds
    Given a vCard 4.0 file with 100 contacts
    When POST /api/directory/import/vcard is called with the file
    Then the response status is 200
    And the elapsed time is less than 2000ms
    And 100 directory_contacts rows exist after the import

  # REQ-118 — legacy SMS API redirects
  Scenario: GET /api/sms/contacts redirects to /api/directory/contacts?kind=SMS
    When GET /api/sms/contacts is called
    Then the response status is 301
    And the Location header is "/api/directory/contacts?kind=SMS"

  # REQ-112 + REQ-113 — Hub signature verify
  Scenario: directory_push with valid signature applies snapshot
    Given the bridge has a directory trust anchor for the Hub
    When the Hub publishes meshsat/bridge/{id}/cmd/directory_push with a snapshot signed by the trust anchor
    Then directory_contacts is updated to match the snapshot

  Scenario: directory_push with invalid signature is dropped + audited + alerted
    Given the bridge has a directory trust anchor for the Hub
    When the Hub publishes meshsat/bridge/{id}/cmd/directory_push with a payload signed by an unknown key
    Then directory_contacts is unchanged
    And an audit_log entry of type "directory.signature-invalid" exists
    And the Settings UI shows an alert badge

  # REQ-108 + REQ-110 — per-contact AES derivation
  Scenario: encrypt stage with contact: key_ref derives + caches the AES key
    Given a contact has an X25519 long-term pubkey in directory_contact_keys
    When the transform pipeline runs with params.key_ref="contact:<uuid>"
    Then the encrypt stage uses the derived AES-256 key
    And the derived key is cached in-memory for the session

  # REQ-120 + REQ-121 — STANAG 4406 precedence
  Scenario: Message + Delivery rows persist STANAG 4406 precedence
    Given API input with precedence="Flash"
    When the message is persisted
    Then the row precedence column equals "Flash"
    Given API input with precedence="F" (short form)
    When the message is persisted
    Then the row precedence column equals "Flash" (canonical full form)

  # REQ-124 — cascade delete
  Scenario: Deleting a contact cascades addresses + keys + group memberships
    Given contact_id="c-1" has 3 addresses, 2 keys, and is a member of 2 groups
    When DELETE /api/directory/contacts/c-1 is called
    Then the contact and all related rows are removed in a single transaction
    And no orphaned rows remain
