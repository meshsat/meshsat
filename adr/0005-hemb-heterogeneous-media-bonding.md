# 5. HeMB heterogeneous media bonding

Date: 2026-05-18

## Status
Accepted

## Context
RLNC-coded simultaneous multi-bearer bonding across N heterogeneous physical bearers (LoRa + Iridium + SMS + APRS + IPoUGRS).

## Decision
`internal/hemb/` implements cost-weighted splitter (free bearers exhausted before paid) + adaptive reassembly buffer + per-bearer FEC profiles.
