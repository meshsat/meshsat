# ADR-0009 — Three-tier compression model: SMAZ2 + llama-zip + MSVQ-SC

* Status: Accepted — codified after the fact 2026-05-17. All three tiers shipped in v0.3.0 line.
* Date: Originally decided 2026-Q1 (v0.2 SMAZ2 → v0.3 llama-zip + MSVQ-SC); recorded as ADR 2026-05-17.
* Deciders: `ufwtqkgz@meshsat.net`
* Source: `.claude/rules/features-subsystems.md` §"3 compression tiers" (L52–L60), `internal/compress/`

## Context

Different transports have radically different bandwidth and latency budgets:

- **Meshtastic LoRa**: 230 B MTU, ~50 ms latency, shared spectrum — compression saves airtime which is the gating resource.
- **Iridium SBD**: 340 B MTU, 30–90 s latency, $0.05 per message — every byte costs literal money.
- **Iridium IMT (9704)**: 100 KB MTU, lower per-byte cost — bandwidth less constrained but cost-per-message still matters.
- **APRS**: ~256 B per packet, free, RF-collision-prone — compression reduces collision risk.

A single compression algorithm cannot serve all three regimes. SMAZ-class dictionary coders win on tiny English-text payloads (<200 B); LLM-based coders win on longer text where statistical modeling pays for its latency; semantic compression with bounded rate budget wins on payloads where lossy reconstruction beats truncation.

## Decision

The Bridge ships **three** compression tiers as composable stages in the transform pipeline. Operators select per-interface (via access rules / transform pipelines) which tier(s) apply.

| Tier | Algorithm | Type | Latency | Use case |
|---|---|---|---|---|
| 1 | **SMAZ2** | Lossless, dictionary-coded | < 1 ms | Short text, Meshtastic dictionary (`internal/compress/dict_meshtastic.go`) |
| 2 | **llama-zip** | LLM-based, lossless | ~200 ms (sidecar) | Longer text, maximum compression for satellite where cost ≫ latency |
| 3 | **MSVQ-SC** | Multi-Stage Vector Quantization + Semantic Compression, lossy, rate-adaptive | Rate-adaptive | Satellite payloads under hard byte budget; sender accepts lossy reconstruction |

### Why three, not one

| Algorithm picked alone | What breaks |
|---|---|
| **SMAZ2 only** | LLM-grade compression unreachable; MSVQ-SC's lossy-under-budget use case unserved. |
| **llama-zip only** | 200 ms latency on every LoRa packet is unacceptable; sidecar dependency mandatory in every deployment. |
| **MSVQ-SC only** | Lossy compression on every operational text message ("SOS at coords X,Y") = data corruption. Lossy must be opt-in per channel + per rule. |

### Architectural commitments

- **Composable transform pipeline**: `internal/engine/TransformPipeline` chains stages — typical stack is `compress(<tier>) → encrypt(AES-256-GCM) → encode(base64)`. The compression tier is configurable per access rule.
- **Sidecars for tiers 2 + 3**: llama-zip and MSVQ-SC run as separate Docker containers communicating via gRPC. `MESHSAT_LLAMAZIP_ADDR` and `MESHSAT_MSVQSC_ADDR` env vars wire them in; absence is graceful (tier dormant).
- **MSVQ-SC pure-Go decode path**: `MESHSAT_MSVQSC_CODEBOOK` lets Bridge decode received MSVQ-SC payloads without the gRPC sidecar — receiver-side is always available, encoder-side is sidecar-dependent.
- **SMAZ2 dictionary mirror with Hub**: The dictionary in `internal/compress/dict_meshtastic.go` MUST stay byte-for-byte identical to the dictionary in `meshsat-hub/internal/compress/dict_meshtastic.go`. Independent modification = decompression failures for every field-originated message. (Source: meshsat-hub `constitution.md` Article VI; this is the cross-repo invariant.)

## Consequences

**Positive**
- Operators get the right tradeoff for the right link automatically — short LoRa text takes SMAZ2; longer Iridium text takes llama-zip; satellite-budget-constrained sensor payloads take MSVQ-SC.
- Sidecar architecture means heavy dependencies (LLM inference, ONNX runtime for vector quantization) don't bloat the Bridge container — operator only deploys the sidecars they actually use.
- All three tiers run on Pi 5 with acceptable latency (SMAZ2 sub-millisecond; llama-zip sidecar fits in 2 GB RAM; MSVQ-SC sidecar uses ONNX Runtime CPU-only).

**Negative**
- Three implementations to maintain. Mitigation: SMAZ2 is in-repo Go (small); llama-zip + MSVQ-SC are upstream-maintained — Bridge ships the gRPC client only.
- Sidecar gRPC adds inter-container latency. Mitigation: tier 1 (SMAZ2) is in-process, so the latency hit only applies when an operator opted into tier 2 or 3.
- MSVQ-SC requires per-tenant codebook training for optimal compression — out-of-the-box codebook is generic, less efficient than a tuned one. Future work: per-deployment codebook training pipeline.
- Lossy compression in tier 3 means operator must understand which message types tolerate it. Mitigation: UI shows lossy/lossless badge per rule; default channels use lossless tiers only.

**Forward direction**
- LLM-class compression evolution will likely produce better tier-2 algorithms over time. The pipeline is generic — swapping `llamazip` for a successor is a gRPC endpoint change, not a Bridge code change.
- MSVQ-SC per-deployment codebook training is the next quality lever for tier 3.

## Alternatives considered

- **Single tier (zstd)**: rejected — zstd is the industry default but doesn't win on the <300-byte regime where the bridge spends most of its airtime. (zstd shines on multi-KB blobs.)
- **gzip / deflate**: rejected — same reason, plus older + slower.
- **Custom dictionary-only coder**: rejected — would re-invent SMAZ2 with worse documentation.
- **No compression**: rejected — Iridium SBD at $0.05 per 340-byte message makes every saved byte literal money.

## Compliance

- The SMAZ2 dictionary (`internal/compress/dict_meshtastic.go`) MUST stay byte-for-byte identical with the Hub's copy (`meshsat-hub/internal/compress/dict_meshtastic.go`). Verify before any compression-related merge.
- Sidecars (`MESHSAT_LLAMAZIP_ADDR`, `MESHSAT_MSVQSC_ADDR`) MUST degrade gracefully when unset — Bridge logs warning and the tier becomes unavailable, not the Bridge container dying.
- MSVQ-SC receive-side decode MUST work without the sidecar if `MESHSAT_MSVQSC_CODEBOOK` is provided (pure-Go decoder).
- Access rules selecting tier 3 (MSVQ-SC) MUST display a lossy badge in the dashboard UI; default rule templates use lossless tiers only.
- Adding a fourth tier (e.g. better-than-llama-zip LLM) follows the same pattern — new sidecar, new env var, new transform-pipeline stage; no breaking change to existing rules.
