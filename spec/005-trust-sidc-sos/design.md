# Design — Trust + SIDC + SOS (Phase 4)

## Goal

Operators interacting with contacts need three things this phase delivers:
1. **Symbology**: contacts + pins render in MIL-STD-2525D / APP-6D symbols (everyone in TAK-adjacent ops knows these instantly).
2. **Trust**: who has been verified, and what counts as verification. Plus a hard-block on sending Flash to unverified contacts.
3. **Emergency**: a SOS button that's always-visible AND hard-to-trigger-by-accident AND fans out maximally on confirm.

Plus the USMTF template skeletons (SALUTE / MEDEVAC 9-line / SITREP) so the operator types into structured forms instead of free-text.

## Trust state machine

```
unverified (0) ──address-add──> address-known (1)
                                       │
                          ───address-verify──> address-verified (2)
                                                       │
                                          ───in-person-scan-QR──> in-person-verified (3)
                                                                          │
                                                                          ▼
                                                          [key rotation event]
                                                                          │
                                                          ───────────reset to 0
```

`trust_level` columns from spec/002 (REQ-405) record the level + when + by whom.

## Hard-block UX (REQ-406)

When the operator picks Flash/Immediate/Override on a contact with trust_level < 2, the SPA renders a blocking modal:

```
⚠ Trust gap for alice (trust_level=1)
   You're about to send a Flash/Immediate/Override precedence message
   to an address you haven't verified.
   
   [Cancel]   [Send anyway]
```

`Send anyway` is logged in the audit chain so operator decisions are auditable.

## Contact QR card (REQ-411..413)

`GET /api/directory/contacts/{id}/qr` returns:

```cbor
{
  "v": 1,
  "contact_id": "alice-uuid",
  "display_name": "Alice",
  "addresses": [
    {"kind":"MESHTASTIC","value":"!aabbccdd"},
    {"kind":"SMS","value":"+1-555-0100"}
  ],
  "signing_pubkey": h'...32 bytes...'
}
```

Ed25519-signed by the contact's holding bridge. TTL-less (REQ-413) — contact cards age slowly, no point in TTL.

## SOS confirmation (REQ-415)

Single accidental taps must NOT fire SOS. Two confirmation strategies:
- **3-second hold** — operator presses + holds the button; haptic pulse at 1s and 2s; fire at 3s.
- **Double-tap within 2s** — second tap of the same button.

Either path counts. Audit entry (REQ-417) captures which path triggered.

## USMTF templates (REQ-419..423)

Three v1 templates (full library lands in Phase 6 per `EXECUTION-PLAN.md §6.6`):

| Template | Lines | Use case |
|---|---|---|
| SALUTE | Size / Activity / Location / Unit / Time / Equipment | Reconnaissance report |
| MEDEVAC 9-line | Lines 1-9 per MEDEVAC standard | Casualty evacuation |
| SITREP | Per SITREP standard format | Situation update |

Default wire format: slash-delimited string ("S=BTN/A=patrol/L=52.16,4.51/..."). When the operator sets destination=TAK and asks for XML, the SPA also generates MIL-STD-6040 XML alongside the slash form.

## Out of scope

- Full USMTF template library (20+) — Phase 6.
- TAK plugin / external CoT injection — Phase 9.
- Pubkey rotation negotiation flow — operator does manual revoke+rescan today.
