# 4. Reticulum-compatible routing layer

Date: 2026-05-18

## Status
Accepted

## Context
9 cross-connected interfaces per README. Ed25519 identity + announce relay + link manager + keepalive + bandwidth tracking + resource transfers.

## Decision
`internal/reticulum/` implements the Reticulum-compatible routing layer as the canonical mesh-side address resolution. Identity is the root of trust (Article C-II).
