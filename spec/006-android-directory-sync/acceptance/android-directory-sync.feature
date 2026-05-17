Feature: Android Directory Sync (meshsat Phase 5, bridge side)

  Background:
    Given Phase 1 (unified directory) has shipped on the bridge
    And an Android client is paired with the bridge per spec/001-pair-protocol

  # REQ-500 + REQ-501 — full + delta snapshot
  Scenario: GET /api/directory/snapshot returns full snapshot when since=0
    Given the bridge has 10 contacts with various updated_at timestamps
    When GET /api/directory/snapshot?since=0 is called
    Then the response body contains all 10 contacts

  Scenario: since=N returns only contacts with updated_at > N
    Given since timestamp T excludes 7 contacts but includes 3
    When GET /api/directory/snapshot?since=T is called
    Then the response body contains exactly 3 contacts

  # REQ-502 + REQ-503 — signed payload
  Scenario: Snapshot is Ed25519-signed by the bridge signing key
    When the snapshot is fetched
    Then the response body has a `signature` field
    And the signature verifies against the bridge's signing_private_key public counterpart

  # REQ-505 — revoked pairing returns 403
  Scenario: Revoked Android pairing cannot read snapshot
    Given the Android client's paired_clients row has revoked_at set
    When GET /api/directory/snapshot is called by that client
    Then the response status is 403

  # REQ-507 — 304 Not Modified
  Scenario: If-Modified-Since matching current version returns 304
    Given the bridge's current snapshot version is V
    When GET /api/directory/snapshot with If-Modified-Since matching V is called
    Then the response status is 304

  # REQ-508 — change notification
  Scenario: Bridge publishes directory/changed on contact mutation
    Given an Android client is connected to the bridge's MQTT
    When a contact is updated on the bridge
    Then within 1 second a MQTT message on meshsat/bridge/{id}/directory/changed is observed with a new version integer

  # REQ-509 + REQ-510 + REQ-513 — handoff export + import + audit
  Scenario: Operator A exports a handoff card; operator B imports it
    Given operator A's bridge has contact alice
    When operator A calls POST /api/directory/contacts/alice/qr/handoff with target_bridge_id=B
    Then the response body contains a CBOR card with TTL=600s
    And an audit_log entry of type "directory.handoff_exported" exists on bridge A
    When operator B calls POST /api/directory/contacts/import-handoff with the CBOR bytes
    Then alice is added to bridge B's directory_contacts with trust_level=1
    And an audit_log entry of type "directory.handoff_imported" exists on bridge B

  # REQ-511 — replay rejected
  Scenario: Replaying a consumed handoff card is rejected
    Given a handoff card was just consumed
    When the same card is POSTed to /api/directory/contacts/import-handoff again within the TTL
    Then the response status is 400
    And an audit_log entry of type "directory.handoff_replay_rejected" exists

  # REQ-512 — TTL expiry
  Scenario: Expired handoff card returns 400
    Given a handoff card with expires_at 700 seconds ago
    When the card is POSTed to /api/directory/contacts/import-handoff
    Then the response status is 400
    And the error mentions TTL expiry

  # REQ-514 — revoke broadcast
  Scenario: Contact revocation publishes directory/revoked
    Given alice exists with a signing pubkey
    When the operator revokes alice's signing pubkey on the bridge
    Then within 1 second a MQTT message on meshsat/bridge/{id}/directory/revoked is observed listing alice's contact_id
