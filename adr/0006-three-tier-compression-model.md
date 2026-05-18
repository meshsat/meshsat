# 6. Three-tier compression model

Date: 2026-05-18

## Status
Accepted

## Context
SMAZ2 (lossless, <1ms, Meshtastic dictionary), llama-zip (LLM-based lossless, ~200ms), MSVQ-SC (Multi-Stage VQ Semantic Compression — lossy semantic, rate-adaptive).

## Decision
`internal/codec/Smaz2.kt`-equivalent in Go + `internal/compress/` codebook in lockstep with meshsat-hub per Article C-III.
