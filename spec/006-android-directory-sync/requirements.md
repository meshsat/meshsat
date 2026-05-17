# Requirements — Android Directory Sync (meshsat Phase 5, EXECUTION-PLAN §6.5)

Source: `EXECUTION-PLAN.md` §6.5 (3 stories S5-01..S5-03). Cross-cuts `meshsat-android/spec/002-directory-sync/` — this spec captures the **bridge side** of the sync contract; the android side captures the Room+Compose+QR-scan surface.

Constitution invariants in scope: Article XIII (master-key envelope encryption), Article VIII (audit hash chain).

When an Android app is paired with a bridge (per spec/001-pair-protocol), the Android app shall be able to pull a signed directory snapshot from the bridge so the operator's People/Compose flow on Android matches the bridge's view.

## Functional requirements

REQ-500: The bridge shall expose `GET /api/directory/snapshot?since={version}` returning the contact + address + group data the requesting client is authorised to see.

REQ-501: When the snapshot endpoint is called with `since=0` (or omitted), the bridge shall return the FULL contact set; when `since=N`, the bridge shall return only contacts whose `updated_at > N`.

REQ-502: The snapshot endpoint response shall be a signed JSON document with shape `{version: int, contacts: [...], addresses: [...], groups: [...], signature: <Ed25519 hex over the canonical JSON>}`.

REQ-503: The snapshot endpoint signature shall be produced by the bridge's Ed25519 signing key from spec/002 REQ-109.

REQ-504: When the Android app validates the snapshot signature, the bridge's signing pubkey shall be the same pubkey pinned in the Android `bridge_trust` Room table from `meshsat-android/spec/001-tofu-bundle-v2/`.

REQ-505: When the snapshot endpoint is called by an Android client whose pairing record is `revoked_at` non-null, the bridge shall return 403.

REQ-506: The snapshot endpoint response shall include a `Last-Modified` HTTP header equal to the highest `updated_at` of any returned contact.

REQ-507: When the Android client passes `If-Modified-Since` matching the bridge's current snapshot version, the bridge shall return 304 Not Modified.

REQ-508: When a contact is mutated on the bridge (POST/PUT/DELETE per spec/002), the bridge shall publish a MQTT topic notification on `meshsat/bridge/{bridge_id}/directory/changed` containing only the new `version` integer so Android clients can poll a fresh snapshot.

REQ-509: The bridge shall expose `POST /api/directory/contacts/{id}/qr/handoff` accepting `{target_bridge_id: string}` and returning a one-time-use signed CBOR contact card meant for scanning into another bridge's directory (cross-bridge handoff).

REQ-510: When the bridge receives `POST /api/directory/contacts/import-handoff` with a one-time-use CBOR card AND the card's signature verifies against a trusted source bridge AND the card's `expires_at` is in the future, the bridge shall add the contact to its directory_contacts table with `trust_level=1` (address-known, not in-person-verified).

REQ-511: When a handoff card is consumed (imported successfully), the bridge shall mark its `card_id` as consumed in an in-memory deduplication set for the card's TTL window.

REQ-512: The handoff CBOR card shall have a TTL of 600 seconds (10 minutes) by default; cards older than the TTL shall be rejected with 400.

REQ-513: The bridge shall write an audit_log entry of type `directory.handoff_exported` when a handoff card is generated AND `directory.handoff_imported` when one is consumed.

REQ-514: When the operator on the bridge revokes a contact's signing pubkey, the bridge shall ALSO emit `meshsat/bridge/{bridge_id}/directory/revoked` listing the revoked `contact_id` so paired Android clients can purge their copy.
