# Design — Android Directory Sync (Phase 5, bridge side)

## Goal

Per `EXECUTION-PLAN.md §6.5`, Android needs the directory data so operators see the same People list on phone + on the bridge SPA. This spec captures the bridge side; `meshsat-android/spec/002-directory-sync/` captures the Room+Compose surface on the phone.

The signed-snapshot model mirrors how the Hub-to-bridge directory_push works (spec/002 T-003) — bridge becomes the authoritative source for paired Android phones, Hub remains the authoritative source for the bridge.

## Wire diagram

```
   Android (paired)              Bridge
        │                          │
        │ GET /api/directory/      │
        │ snapshot?since=N         │
        │ Authorization: Bearer    │
        ├─────────────────────────▶│
        │                          │  load contacts where updated_at > N
        │                          │  build {version, contacts, addresses,
        │                          │         groups, signature}
        │                          │  sign with signing_private_key
        │                          │
        │  signed JSON +           │
        │  Last-Modified header    │
        │◀─────────────────────────┤
        │                          │
        │ verify signature against │
        │ bridge_trust pinned pk   │
        │ apply to Room DB         │
        │                          │
        │   ...later...            │
        │                          │
        │ bridge MQTT pub:         │
        │ meshsat/bridge/{id}/     │
        │ directory/changed        │
        │                          │
        │ Android polls again      │
        │ with since=current       │
        │ → delta snapshot         │
```

## Cross-bridge contact handoff (REQ-509..513)

Two operators at different sites want to swap a contact:

1. Operator A's bridge: `POST /api/directory/contacts/alice/qr/handoff?target_bridge_id=B` returns CBOR card.
2. Operator A shows the QR to Operator B (face-to-face OR via a printed card).
3. Operator B's bridge: `POST /api/directory/contacts/import-handoff` with the CBOR bytes.
4. Bridge B validates signature against source bridge B's known signing pubkey (operator must pre-pair the bridges OR accept first-import TOFU).
5. Imported contact lands with `trust_level=1`; operator B must verify in person (per spec/005) to bump to 3.

TTL=600s + one-time consumed marker (REQ-511/512) prevents replay attacks.

## Tables touched

- No new tables. Snapshot endpoint reads existing directory_contacts/addresses/groups.
- audit_log gains 2 new event types: `directory.handoff_exported`, `directory.handoff_imported`, `directory.handoff_replay_rejected`.

## Out of scope

- Android Room schema (lives in meshsat-android/spec/002).
- Android Compose People view (meshsat-android/spec/002).
- Android QR scanner (meshsat-android/spec/003 pair-shell).
- Full bridge-mesh discovery (the operator manually picks the target_bridge_id).
