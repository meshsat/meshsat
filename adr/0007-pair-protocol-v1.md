# 7. Pair protocol v1 (Phase 8)

Date: 2026-05-18

## Status
Accepted (planned)

## Context
Philips-Hue-pushlink-style QR pairing: operator arms by physical touch on 7" display → 60s single-use QR → ECDH+CSR challenge → bridge mints client cert (90-day, internal CA) + 90-day JWT. mTLS + Bearer for steady-state. Three NAT-traversal tiers: LAN → Reticulum → Hub WebSocket relay.

## Decision
Implement in `internal/pair/` per UX-MULTI-ACCESS-KIOSK-PAIRING.md §4.
