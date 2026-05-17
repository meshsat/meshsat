# Design — Unified Directory (Phase 1)

## Goal

Per `EXECUTION-PLAN.md §3` Phase 1 is the foundation — unblocks Phases 2, 3, 4, 5, 8. The legacy `sms_contacts` table treats every phone number as an island; operators need a person-shaped contact with multiple addresses across the 12 transports the bridge speaks. This phase introduces that unified model + the migration off the legacy schema.

## Schema delta (v44 through v48)

```sql
-- v44: directory_contacts (one row per person)
CREATE TABLE directory_contacts (
  contact_id   TEXT PRIMARY KEY,
  tenant_id    TEXT NOT NULL,
  team         TEXT,
  display_name TEXT NOT NULL,
  sidc         TEXT,                         -- MIL-STD-2525D symbol (set by Phase 4)
  trust_level  INTEGER NOT NULL DEFAULT 0,
  trust_verified_at INTEGER,
  trust_verified_by TEXT,
  hub_version  INTEGER,
  hub_etag     TEXT,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
CREATE INDEX idx_dir_contacts_tenant ON directory_contacts(tenant_id);
CREATE INDEX idx_dir_contacts_team   ON directory_contacts(team);

-- v45: directory_addresses (many addresses per contact)
CREATE TABLE directory_addresses (
  address_id   TEXT PRIMARY KEY,
  contact_id   TEXT NOT NULL REFERENCES directory_contacts(contact_id) ON DELETE CASCADE,
  kind         TEXT NOT NULL,                -- SMS|MESHTASTIC|APRS|...
  value        TEXT NOT NULL,                -- phone, !aabbccdd, IMEI, etc.
  created_at   INTEGER NOT NULL
);
CREATE INDEX idx_dir_addr_contact ON directory_addresses(contact_id);
CREATE INDEX idx_dir_addr_kind    ON directory_addresses(kind);

-- v46: directory_contact_keys (per-contact crypto material)
CREATE TABLE directory_contact_keys (
  key_id       TEXT PRIMARY KEY,
  contact_id   TEXT NOT NULL REFERENCES directory_contacts(contact_id) ON DELETE CASCADE,
  kind         TEXT NOT NULL,                -- ED25519_SIGN|X25519_ENC|AES256_GCM_SHARED
  pubkey       BLOB NOT NULL,
  created_at   INTEGER NOT NULL
);

-- v47: directory_groups + directory_group_members
CREATE TABLE directory_groups (
  group_id     TEXT PRIMARY KEY,
  tenant_id    TEXT NOT NULL,
  name         TEXT NOT NULL,
  created_at   INTEGER NOT NULL
);
CREATE TABLE directory_group_members (
  group_id   TEXT NOT NULL REFERENCES directory_groups(group_id) ON DELETE CASCADE,
  contact_id TEXT NOT NULL REFERENCES directory_contacts(contact_id) ON DELETE CASCADE,
  PRIMARY KEY (group_id, contact_id)
);

-- v48: directory_dispatch_policy (per-contact OR per-group OR default)
CREATE TABLE directory_dispatch_policy (
  policy_id    TEXT PRIMARY KEY,
  tenant_id    TEXT NOT NULL,
  scope_type   TEXT NOT NULL,                -- contact|group|default
  scope_id     TEXT,                         -- contact_id OR group_id; NULL for default
  precedence   TEXT,                         -- optional precedence-level scoping
  strategy     TEXT NOT NULL,                -- PRIMARY_ONLY|ANY_REACHABLE|ORDERED_FALLBACK|HEMB_BONDED|ALL_BEARERS
  bearer_order TEXT,                         -- JSON array of bearer kinds (for ORDERED_FALLBACK)
  created_at   INTEGER NOT NULL
);
```

## Backfill semantics (REQ-103 + REQ-104)

- Existing `contacts.id` → `directory_contacts.contact_id` (UUID)
- Existing `contact_addresses` → `directory_addresses` with `kind` mapped from the legacy `type` column
- Existing `sms_contacts.phone` → `directory_addresses(kind=SMS, value=<phone>)` with a freshly-minted `directory_contacts` row per phone

Idempotent: re-running the migration is a no-op (rows already present detected by `contact_id` presence).

## Master-key wrapping for signing key (REQ-109)

The existing `system_config.signing_private_key` was committed as plaintext hex in earlier versions — known security gap. v44 boot adds a one-shot wrapper:
1. Read existing plaintext hex.
2. Wrap via `keystore.WrapData(plaintext, masterKey)` → ciphertext + 12-byte nonce.
3. Write wrapped form into `signing_private_key_wrapped` column.
4. Clear `signing_private_key` plaintext.
5. SigningService loads from wrapped column going forward.

If migration interrupted mid-wrap (e.g. power loss), recovery: detect plaintext-still-present + retry the wrap. Idempotent.

## Per-contact AES derivation (REQ-108 + REQ-110)

The legacy per-channel AES key (`sms:<addr>`) doesn't generalize to other bearers. Per-contact derivation:

```
contact_aes = HKDF-SHA256(
  ikm=X25519(bridge_x25519_priv, contact_x25519_pub),
  salt="meshsat-contact-v1",
  info=contact_id,
  length=32
)
```

The transform pipeline accepts `key_ref="contact:<uuid>"` (REQ-110), looking up the cached derived key. Per-channel `sms:<addr>` references continue working during the 30-day grace per REQ-111.

## Hub-distributed authoritative directory (REQ-112 + REQ-113)

The Hub holds the canonical directory; bridges pull signed snapshots via MQTT:

1. Hub publishes `meshsat/bridge/{bridge_id}/cmd/directory_push` with payload `{snapshot: <bytes>, sig: <ECDSA-P256>, version: <uint>}`.
2. Bridge verifies `sig` against the directory trust anchor (provisioned at first-pair).
3. On success: apply the snapshot, stamp `hub_version` + `hub_etag` (REQ-122).
4. On failure: drop + audit `directory.signature-invalid` + alert in Settings UI.

Equivalent `directory_delta` and `directory_revoke` MQTT commands deferred to a follow-up spec.

## Out of scope

- Per-contact UI for Phase 3 (covered by `spec/004-ui-reshape/`).
- Phase 2 dispatcher integration (covered by `spec/003-contact-aware-dispatcher/`).
- Trust dots + verify flow (covered by `spec/005-trust-sidc-sos/`).
- Android directory sync (covered by `spec/006-android-directory-sync/`).
