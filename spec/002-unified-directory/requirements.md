# Requirements — Unified Directory (meshsat Phase 1, EXECUTION-PLAN §6.1)

Source: `EXECUTION-PLAN.md` §6.1 (10 stories S1-01..S1-10), `UX-AUDIT-AND-REDESIGN.md` §4.2.1 (schema). Foundation phase — unblocks Phases 2, 3, 4, 5, 8.

Constitution invariants in scope: Article II (CGO=0 — uses modernc.org/sqlite), Article III (append-only migrations — current head v43; this adds v44-v48), Article IV (single entry point — wires in main.go), Article XIII (master-key envelope encryption irrecoverable).

Replaces the legacy `sms_contacts` table with a unified person/contact model. One contact has multiple addresses (SMS, Meshtastic, APRS, Iridium, cellular, TAK, Reticulum, ZigBee, BLE, webhook, email). Per-contact Ed25519 + X25519 signing keys. Tenant-scoped groups + per-contact dispatch policy. vCard 4.0 import/export. Hub-distributed authoritative directory via MQTT.

## Functional requirements

REQ-100: The system shall add migrations v44 through v48 to `internal/database/migrations.go` introducing `directory_contacts`, `directory_addresses`, `directory_contact_keys`, `directory_groups`, `directory_group_members`, `directory_dispatch_policy` tables per `UX-AUDIT-AND-REDESIGN.md §4.2.1`.

REQ-101: The system shall NOT modify any migration entry numbered ≤ v43 (Constitution Article III append-only).

REQ-102: The system shall create indexes `idx_dir_contacts_tenant`, `idx_dir_contacts_team`, `idx_dir_addr_contact`, `idx_dir_addr_kind` at migration time.

REQ-103: When migration v44 runs on a database with existing `contacts`/`contact_addresses` rows, the system shall backfill those rows into `directory_contacts` + `directory_addresses` preserving content.

REQ-104: When migration v44 runs on a database with existing `sms_contacts` rows, the system shall backfill those rows into `directory_contacts` + `directory_addresses` with `kind=SMS`.

REQ-105: The system shall introduce an `internal/directory/` Go package defining `Contact`, `Address`, `ContactKey`, `Group`, `DispatchPolicy` types.

REQ-106: The `Address.kind` field shall accept exactly the enum `{SMS, MESHTASTIC, APRS, IRIDIUM_SBD, IRIDIUM_IMT, CELLULAR, TAK, RETICULUM, ZIGBEE, BLE, WEBHOOK, EMAIL}`.

REQ-107: The `internal/directory/` package shall expose `Resolve(contactID)` and `FindByAddress(kind, value)` query methods backed by the SQLite store.

REQ-108: When a `Contact` has an X25519 long-term public key in `directory_contact_keys`, the system shall derive a per-contact AES-256 traffic key via X25519+HKDF on first use AND shall cache the derived key in-memory for the session.

REQ-109: The system shall migrate the existing `system_config.signing_private_key` from unwrapped hex to master-key-wrapped on first boot AFTER v44, AND shall clear the legacy plaintext entry once wrapping succeeds.

REQ-110: The transform pipeline's `encrypt` stage shall accept `params.key_ref="contact:<uuid>"` resolving against the per-contact derived key from REQ-108.

REQ-111: When a message has both a `sms:<addr>` legacy key_ref AND a `contact:<uuid>` key_ref valid, the transform pipeline shall prefer the `contact:` reference for the 30-day migration grace period.

REQ-112: When the Hub pushes a signed `directory_push` MQTT command, the bridge shall verify the Hub's ECDSA-P256 signature against the directory trust anchor stored in the bridge provisioning bundle AND shall apply the snapshot on signature success.

REQ-113: When directory signature verification fails on a `directory_push` payload, the bridge shall drop the payload, write an audit event `directory.signature-invalid`, AND alert in the Settings UI.

REQ-114: The bridge shall expose `POST /api/directory/import/vcard` accepting RFC 6350 vCard 4.0 input AND shall complete a 100-contact import in less than 2 seconds on a Pi 5.

REQ-115: The bridge shall expose `POST /api/directory/import/csv` accepting CSV input as a fallback to vCard.

REQ-116: The bridge shall expose `GET /api/directory/export/vcard` returning paginated RFC 6350 vCard 4.0 output for the operator's tenant.

REQ-117: The system shall accept the X-MESHSAT-* vCard extension fields for the mesh/iridium/reticulum address kinds AND shall document them in the export schema.

REQ-118: When the legacy `/api/sms/contacts` endpoint is called, the system shall respond with HTTP 301 redirecting to `/api/directory/contacts?kind=SMS` for one release cycle, AND shall remove the legacy endpoint in v50.

REQ-119: The legacy `sms_contacts` table shall remain read-only for one release after v44 lands AND shall be dropped in migration v50.

REQ-120: The `Message` and `Delivery` rows shall each gain a `precedence` field accepting the STANAG 4406 6-level enum `{Override, Flash, Immediate, Priority, Routine, Deferred}` defaulting to `Routine`.

REQ-121: The `precedence` field shall accept short-form input (Z/O/P/R/M and the 6-level form interchangeably) at API boundaries but shall persist the full-form value.

REQ-122: When a Hub-pushed signed directory snapshot arrives, the system shall stamp `directory_contacts.hub_version` AND `directory_contacts.hub_etag` columns from the snapshot metadata.

REQ-123: The bridge health payload shall include the current `directory_version` so Hub fleet views can see drift across the bridge fleet.

REQ-124: When the operator deletes a contact via `DELETE /api/directory/contacts/{contact_id}`, the system shall cascade-delete the contact's addresses, keys, and group memberships in a single transaction.
