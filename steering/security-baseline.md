# Steering — Security baseline (MeshSat Bridge)

## Secrets

- NEVER commit secrets. `.env*` is gitignored. Master key + signing keys live in keystore (`internal/keystore/`), wrapped with AES-256-GCM, never on filesystem in plaintext.
- Pasted Hub mTLS cert + key: stored in DB (NOT filesystem path) per CLAUDE.md guidance.
- If a secret leaks via commit: rotate the master key (full keystore rotation procedure), force-push the cleaned commit (operator-only escalation).

## Crypto

- Ed25519 for signing, X25519 for ECDH, AES-256-GCM for envelope encryption — stdlib + `golang.org/x/crypto`. No custom primitives.
- Master-key envelope encryption is the chain-of-trust root (Article XIII). Treat as a CA private key.
- TOFU verification of v2 bundles (`bridge_trust` model). First-seen pubkey is trusted; subsequent diffs are rejected.

## Network

- Hub uplink: WSS + mTLS client cert verification. NEVER plain TCP.
- Bridge listens on Pi5 host network — assume operator deploys with `iptables` / `nftables` rules limiting external access (their responsibility, not the bridge's, but document).
- API uses chi router with rate-limit middleware (`internal/ratelimit/`).

## Input validation

- All HTTP inputs JSON-Schema-validated at handler edge. No raw `json.Unmarshal()` from request body.
- All inbound mesh packets parsed against the official Meshtastic protobuf bindings (`buf.build/gen/go/meshtastic/protobufs`). No hand-rolled parsers — MESHSAT-242 explicitly removed all hand-rolled protobuf.

## Pair protocol (Phase 8 — NEW)

- 60-second armed window (physical touch on bridge UI; Hue-pushlink-style).
- Internal CA: 5-year self-signed root, 90-day leaf, in-DB store (Article XIII applies to CA private key too).
- mTLS for all post-pair traffic.

## Dependency posture

- Every new third-party Go dep requires an ADR (license check, OSV-DB CVE count, maintenance signal, transitive count). No silent additions to `go.mod`.
- `govulncheck` runs in CI; HIGH severity blocks merge.

## Audit

- CI log retention via GitLab.
- Bridge-side audit chain (mesh + satellite delivery ledger) lives in `internal/database/` tables; never modified post-write.
