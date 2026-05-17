# ADR-0005 — Pair-cert: bridge-issued, not Hub-issued

## Status

Accepted — 2026-05-17 (for spec/001-pair-protocol/)

## Context

When a client pairs with a bridge, it needs an X.509 client cert it can present in subsequent mTLS handshakes. Two architectural models:

1. **Bridge-issued (per-bridge internal CA)** — bridge generates its own root + signs leaf certs locally. Client must trust each bridge's distinct root.
2. **Hub-issued (Hub is the CA)** — bridge forwards the CSR to Hub, Hub signs and returns. All bridges share Hub's root.

## Decision

**Bridge-issued.** Each bridge runs its own internal CA (5y root, 90d leaves). Implemented in T-001 (MESHSAT-595).

## Consequences

**Positive:**
- **Works fully offline.** Constitution Article XII: "no cloud dependencies, no subscriptions." A bridge in a satellite kit at sea must be able to pair its operator's phone WITHOUT Hub connectivity. Hub-issued certs would block this.
- **Bridge sovereignty.** The bridge owns its trust boundary. A compromised Hub can't impersonate or revoke clients of an offline bridge.
- **Aligns with the "single trusted container per Pi" model** (Constitution Article XI). The bridge is already the authority for its mesh; extending that to client identity is natural.
- **Simpler operational model.** Lost CA private key = re-pair all clients (acceptable since bridges typically have ≤10 paired clients per REQ-020). Hub-issued model would entangle Hub key-management with bridge auth.
- **TOFU pinning works.** The first time a client pairs, it stores the bridge's CA bundle. Subsequent connections verify against this pinned root — Bridge becomes the trust anchor.

**Negative:**
- **Client must trust N distinct roots** (one per paired bridge). For a typical operator with 1-2 bridges this is fine; for multi-bridge fleets it's friction.
  - Mitigation: Phase 9 (MESHSAT-533, multi-bridge UX) can offer Hub-mediated bridge-of-bridges trust as an OPTIONAL overlay. The bridge-issued root remains the offline fallback.
- **No cross-bridge cert validity.** A client paired with bridge A cannot use the same cert against bridge B. Per Article XII this is correct; Hub-mediated cross-trust is an opt-in extension, not the default.
- **CA root rotation is a per-bridge operator task.** 5-year cadence — manual procedure documented in `docs/runbooks/ca-rotation.md` (to be authored). Cannot be automated because the rotation invalidates all paired clients (they must re-pair).

## Alternatives considered

- **Hub-issued:** rejected — breaks offline pairing (Constitution Article XII violation). Also entangles bridge auth with Hub uptime.
- **External CA (Let's Encrypt-style):** rejected — bridges are not internet-reachable for ACME challenges; satellite-only kits have no inbound IP.
- **Self-signed per-client (no CA)**: rejected — client cert cannot be verified by the bridge without trusting individual cert pins, scales poorly.
- **Hybrid (bridge-issued + optional Hub cross-sign):** considered, deferred to Phase 9 (MESHSAT-533). v1 ships bridge-only; cross-trust extension lands later if multi-bridge UX demands it.

## Operational corollaries

- **CA root rotation** requires re-pairing all clients. Operators see a UI warning 30 days before root expiry (extension of REQ-012's leaf warning).
- **Bridge factory-reset** generates a fresh CA root (REQ-013). All previously paired clients are invalidated. This is the correct behaviour — factory reset == fresh trust boundary.
- **Lost master key** = lost CA private key = re-pair all clients. Same operational contract as Constitution Article XIII (master-key envelope encryption is irrecoverable).

## References

- spec/001-pair-protocol/design.md section "Internal CA hierarchy (MESHSAT-595)"
- spec/001-pair-protocol/data-model.md section "Master-key envelope encryption"
- Constitution Articles XI (single trusted container), XII (no cloud dependencies), XIII (master-key envelope encryption is irrecoverable)
- TOFU bundle precedent: README.md L267 (v2 bundles use Trust On First Use verification)
- MESHSAT-533 (Phase 9 — multi-bridge + NAT traversal) — future home of optional Hub-mediated cross-trust
