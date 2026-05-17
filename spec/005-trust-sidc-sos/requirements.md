# Requirements — Trust Levels + SIDC + SOS (meshsat Phase 4, EXECUTION-PLAN §6.4)

Source: `EXECUTION-PLAN.md` §6.4 (5 stories S4-01..S4-05). Depends on `spec/002-unified-directory/` (contact + trust columns), `spec/004-ui-reshape/` (PeopleView + Compose).

Constitution invariants in scope: Article XIII (master-key envelope encryption), Article VIII (audit log SHA-256 hash chain for trust-state changes).

Adds MIL-STD-2525D / APP-6D / STANAG 4677 symbology to every contact + map pin, Threema-style 0..3 trust dots with verify flow, contact-QR scan for in-person verification, full-width SOS button with panic flow, and a skeleton of USMTF templates (SALUTE, MEDEVAC 9-line, SITREP) in Compose.

## Functional requirements

REQ-400: The system shall populate `directory_contacts.sidc` (nullable, MIL-STD-2525D SIDC string) for every contact created by the operator.

REQ-401: The `MapView.vue` shall use the `milsymbol` npm library (MIT) to render map pins from the `sidc` field of each pin's underlying contact.

REQ-402: The `PeopleView.vue` contact detail pane shall include a Symbol picker exposing the STANAG 4677 reduced set so an operator can pick a SIDC without typing the raw code.

REQ-403: When a contact has no `sidc` set, the `MapView.vue` shall render a default neutral CoT pin (`a-f-G-U-C`).

REQ-404: The `directory_contacts.trust_level` column shall accept integer 0..3 where 0=unverified, 1=address-known, 2=address-verified, 3=in-person-verified.

REQ-405: The system shall add `directory_contacts.trust_verified_at` and `directory_contacts.trust_verified_by` columns persisting when + by whom verification was performed.

REQ-406: When the operator opens Compose and selects a precedence of `Immediate`, `Flash`, or `Override` for a contact with `trust_level < 2`, the SPA shall display a blocking confirmation modal warning about the trust gap AND shall require an explicit "Send anyway" tap.

REQ-407: When the operator clicks "Verify in person" in `PeopleView.vue` detail pane, the SPA shall open a camera viewfinder to scan the contact's QR card.

REQ-408: When a `meshsat://contact/...` QR is scanned successfully AND its Ed25519 signature matches the contact's known signing pubkey, the system shall bump the contact's `trust_level` to 3 AND shall record `trust_verified_at = now()` and `trust_verified_by = operator_session_id`.

REQ-409: When a `meshsat://contact/...` QR is scanned AND its signing pubkey does NOT match the stored pubkey, the SPA shall display a KeyMismatch warning AND shall NOT bump trust level.

REQ-410: When a contact's signing pubkey changes (operator-accepted via the KeyMismatch flow), the system shall reset `trust_level` to 0 AND shall require a fresh verify cycle before precedence ≥ Immediate is permitted.

REQ-411: The system shall expose `GET /api/directory/contacts/{id}/qr` returning a CBOR-encoded, Ed25519-signed contact card per the `meshsat://contact/...` URI scheme described in `UX-AUDIT-AND-REDESIGN.md §9`.

REQ-412: The contact QR card payload shall include `contact_id`, `display_name`, addresses, `signing_pubkey`, AND a Ed25519 signature over the canonical CBOR encoding of the payload (excluding the signature bytes).

REQ-413: The contact QR card payload shall be TTL-less (long-lived, no expiry) — verification is one-time-per-pubkey.

REQ-414: The status strip from spec/004 REQ-316 shall include an always-visible full-width SOS button at the bottom edge.

REQ-415: When the operator taps the SOS button, the SPA shall require a confirmation gesture of either a 3-second-hold OR a second tap within 2 seconds to fire — single accidental taps shall NOT fire SOS.

REQ-416: When the SOS button confirms-fires, the system shall send a Flash-precedence message containing current location + the last-48h contact briefing AND shall fan it out via HEMB to every reachable bearer.

REQ-417: When the SOS button fires, the system shall write an audit_log entry of type `sos.fired` that is immutable (append-only chain entry; no UPDATE allowed).

REQ-418: When the SOS button fires, the system shall increment the `meshsat_sos_fired_total` Prometheus counter labelled by `operator_session_id`.

REQ-419: The Compose view shall offer a Templates dropdown with three USMTF skeletons: SALUTE, MEDEVAC 9-line, SITREP.

REQ-420: When the operator picks the SALUTE template, the SPA shall render 6 labelled fields (Size, Activity, Location, Unit, Time, Equipment) that pack into a slash-delimited body string before sending.

REQ-421: When the operator picks the MEDEVAC 9-line template, the SPA shall render the 9 lettered fields (Line 1..9) per the standard MEDEVAC format that pack into the body.

REQ-422: When the operator picks the SITREP template, the SPA shall render the SITREP fields per the standard SITREP format.

REQ-423: When a USMTF-templated message is bound for a TAK destination, the SPA shall optionally encode the body as MIL-STD-6040 XML in addition to the slash-delimited form.
