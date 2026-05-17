# ADR-0006 — HMAC shared secret is the operator-typed pair code

## Status

Accepted — 2026-05-17 (for spec/001-pair-protocol/)

## Context

The pair-claim payload (`contracts/schemas/pair-csr.json`) includes an `hmac` field — HMAC-SHA256 over `csr || ecdh_pub || nonce`. **The shared secret used to compute this HMAC must come from somewhere.** Without specifying its provenance, the protocol is underspecified and unsafe.

Three viable models exist:

**A. No HMAC, arm is the sole gate (Hue-pushlink-pure).** Bridge accepts any pair-claim during the 60-second armed window. Security = physical access to arm.
- Pro: minimal client UX (no typing).
- Con: ANYONE on the same network can race to pair during a 60s window. A drive-by attacker who observes someone press the arm button can steal the pairing. Real attack vector for bridges with public DHCP / open WiFi.

**B. Operator-typed pair code displayed on touch UI.** Bridge generates a fresh 6-char code on arm; displays on touch UI; client operator types into client app; client computes HMAC using code as the shared secret.
- Pro: secure against drive-by. Requires the attacker to physically read the bridge's display.
- Con: extra UX step on the client. Code-typing friction.

**C. ECDH-derived shared secret (HMAC is technically redundant).** Client and bridge ECDH; HMAC over body using ECDH-derived key proves freshness but not authorization.
- Pro: zero UX friction.
- Con: same drive-by attack as Option A — ECDH doesn't authenticate the operator, only freshness. Worse, it makes the security model OPAQUE (developers will think it's authenticated when it's not).

## Decision

**Option B — operator-typed pair code.** When the bridge enters the armed state (REQ-001/REQ-023), the touch display renders a 6-character alphanumeric code (uppercase, no I/O/0/1 to avoid confusion — 32 chars per position = ~30 bits of entropy). The client operator reads the code and types it into the client app. The HMAC shared secret is derived from this code via:

```
shared_secret = HKDF-SHA256(salt=bridge_id, ikm=pair_code, info="meshsat-pair-v1", length=32)
```

The HMAC is then `HMAC-SHA256(shared_secret, csr_bytes || ecdh_pub || nonce)`.

This is codified in:
- REQ-005 (HMAC verification logic)
- REQ-007 (HMAC-invalid → 422)
- REQ-022 (touch UI renders the code)

## Consequences

**Positive:**
- **Security model is explicit.** Operator + physical sight of bridge = both required. No drive-by attacks.
- **Operator UX is recoverable.** If operator typo's the code, claim is rejected (422 hmac-invalid), bridge stays armed for retry. Tap "Arm" again to refresh both the window AND the code.
- **30-bit entropy is sufficient** for a 60-second window with ≤4 incorrect-attempts-then-armed-revoke policy (future hardening). Brute force is infeasible.

**Negative:**
- **Adds client-side UX.** "Type this 6-character code" is friction. Mitigated by code being short, unambiguous (no I/O/0/1), and one-time-per-pair (rare event for end-users).
- **Touch display is now load-bearing.** Bridges without a functioning touch display can't pair. Acceptable tradeoff — bridges without displays use remote-arm (REQ-023) where the operator copy/pastes the code via SPA.
- **Requires HKDF (not raw HMAC of code).** Adds a small derivation step on both sides; standard primitive in `golang.org/x/crypto/hkdf`.

## Alternatives considered

- **Option A (no HMAC, arm-only)** — rejected. Drive-by attack vector unacceptable for a bridge that handles SOS / dead-man-switch traffic.
- **Option C (ECDH-only)** — rejected. Opaque security model; ECDH proves freshness but not operator authorization. Worst of both worlds.
- **6-digit numeric code (PIN-style)** — considered. 1M combinations = 20-bit entropy. With 4-attempt lockout, brute force is ~250k attempts to crack the 50%-likely code — feasible in 60s with network amplification. Rejected for entropy.
- **QR code with embedded secret** — considered. Requires camera, which client apps must already support for QR scanning (MESHSAT-600). Could be an enhancement: bridge displays BOTH the code (for type-in) AND a QR (for scan). Out of scope for v1 — code is sufficient.
- **Bluetooth Low-Energy proximity gate** — considered. Adds hardware dependency (Pi5 has BLE; client must have BLE pairing). Way more complex. Future enhancement (Phase 9-ish).
- **Pre-shared key (operator distributes out-of-band)** — rejected. Doesn't work for first-pair from a fresh client; requires existing trusted channel.

## Operational corollaries

- Pair code is REGENERATED on every arm event (so re-arm = new code, REQ-004 + REQ-022).
- Code is displayed on touch UI ONLY. Never logged. Never sent over the network from bridge.
- Bridges WITHOUT a touch display return code in the response of POST /api/pair/arm — operator reads from SPA, types into client. Same security model (operator must have access to BOTH SPA + client).
- After successful pair, code is discarded. Cannot be reused.

## References

- spec/001-pair-protocol/requirements.md REQ-005, REQ-007, REQ-022
- spec/001-pair-protocol/contracts/schemas/pair-csr.json (hmac field definition)
- Constitution Article III (zero-trust input validation)
- HKDF-SHA256: RFC 5869
- Hue pushlink security analysis (Option A baseline) — what we explicitly chose NOT to do because of bridge's safety-critical role
