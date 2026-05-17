# ADR-0008 — HeMB (Heterogeneous Media Bonding): RLNC-coded multi-bearer bonding

* Status: Accepted — codified after the fact 2026-05-17. RLNC encoding verified on production hardware March 2026; 3-bearer field validation pending IPoUGRS hardware arrival (April 2026). IETF Independent Submission Stream submission planned 2027-01 (`draft-papadopoulos-hemb-00`).
* Date: Originally designed 2026-Q1; recorded as ADR 2026-05-17.
* Deciders: `ufwtqkgz@meshsat.net`
* Source: `README.md` L36 (feature description) + L380–L384 (roadmap)

## Context

A field operator routinely has **multiple bearers simultaneously available**, each with mutually incompatible characteristics:

| Bearer | Latency | Cost | Bandwidth | Loss profile |
|---|---|---|---|---|
| LoRa (Meshtastic) | ~50 ms | free | ~50 kbps shared | bursty, RF-fading |
| Iridium SBD | 30–90 s | $0.05/msg | 340 B per pass | per-pass, deterministic windows |
| Cellular SMS | 1–5 s | $0.01/msg | ~140 B | provider-dependent |
| APRS | ~10 s | free | ~1.2 kbps shared | RF-collision-prone |
| IPoUGRS (GSM ring signals — experimental) | ms | metered | bits | provider-dependent |

Routing protocols (including Reticulum, ADR-0007) **pick one** bearer per packet by cost/availability heuristics. That's the right default for control-plane traffic. But for **payload bonding**, exclusive-OR-style bearer selection underuses the available capacity: an operator with LoRa + Iridium + SMS active is paying for satellite latency she could partially eliminate by also using SMS in parallel.

Network coding via Random Linear Network Codes (RLNC) lets us split a payload into N coded symbols, send across the available bearers in cost-weighted proportion, and reassemble at the receiver as soon as **any K of N symbols arrive — regardless of which bearer carried which symbol**. That's HeMB.

## Decision

The Bridge ships **HeMB (Heterogeneous Media Bonding)** as a sub-IP bonding layer:

### Wire model
- Operator declares a **bond group** containing 2+ physical bearer interfaces (e.g. `lora_0`, `iridium_0`, `sms_0`).
- Outbound payloads are split via **Random Linear Network Coding** (RLNC) into N coded symbols.
- A **cost-weighted splitter** allocates symbols to bearers — free bearers exhaust their bandwidth allotment first, paid bearers only fill what's left.
- Receiver reassembles when any **K of N** symbols arrive (where K depends on RLNC matrix rank).

### Operational guarantees
- **Below IP**: HeMB doesn't depend on bearers being IP-routable (Iridium SBD and AX.25 are not). Operates on raw payload bytes.
- **Routing-protocol agnostic**: Works above Reticulum, above Meshtastic protocol, above any other routing layer — HeMB sees opaque payloads.
- **TUN-wrappable as a standard Linux network interface**: Operator can `ip tuntap add hemb0 mode tun` and route IP through it — HeMB invisible to upper layers.
- **Adaptive reassembly buffer** tolerates a 1:900 bearer latency ratio (LoRa 50 ms vs Iridium 45 s = 1:900 in the worst case). Tunable per bond group.
- **Per-bearer FEC profiles** — different bearers get different forward-error-correction strength based on observed loss characteristics.

### Concrete commitments
- RLNC encoding is **confirmed running on production hardware** as of March 2026.
- Field validation with LoRa + Iridium SBD + SMS + IPoUGRS pending IPoUGRS hardware arrival April 2026.
- **IETF RFC submission planned January 2027** via Independent Submission Stream as `draft-papadopoulos-hemb-00`.

## Consequences

**Positive**
- Bonded throughput across heterogeneous bearers — operator gets MTU and latency floor of the **fastest** bearer for K-of-N completion, while still paying the cost of only what was actually sent on paid bearers.
- Robust to per-bearer failures — losing the Iridium link mid-bond doesn't break the message if LoRa + SMS together carry ≥K symbols.
- Single-bearer fallback works degenerately — if only LoRa is up, K=N and HeMB reduces to plain delivery on LoRa.
- IETF RFC submission is a major credibility lever for the project (novel protocol with real-world validation, not a research prototype).

**Negative**
- Adds RLNC encode/decode CPU cost. Mitigation: on Pi 5 with `vc4-kms-v3d` GPU and 8 GB RAM, RLNC matrix ops are sub-millisecond for typical 340-byte chunks.
- Bond-group configuration is operator-facing complexity — wrong cost-weighting could exhaust the cheap bearer and force everything to satellite. Mitigation: UI shows cost-projection-per-bond-group before commit; default templates ship sensible weightings for common bond combinations.
- Reassembly buffer holds partial bonds for up to the worst-case bearer's RTT. With Iridium in the bond, that's ~90 s of memory commitment per in-flight bond. Mitigation: buffer is fixed-size and bounded by bond group config; oldest partial bond evicted on overflow with audit event.
- Not (yet) implemented in Hub or Android — pure Bridge feature today. Hub + Android implementations are on the roadmap but unstarted.

**Forward direction**
- January 2027: IETF Independent Submission Stream draft published — gathers external review.
- Hub HeMB integration: receive-side reassembly when Bridge sends a bonded payload over MQTT-fanned bearers.
- Android HeMB: complementary use case for phone-as-gateway with BLE + SMS + paired-Iridium bearers.

## Alternatives considered

- **Single-bearer routing only (no bonding)** — already implemented via Reticulum (ADR-0007). HeMB is **complementary**, not a replacement. Single-bearer remains the default for control traffic; HeMB opt-in for payload-heavy bonds.
- **MPTCP (Multipath TCP)** — IP-only, requires TCP semantics, doesn't work over Iridium SBD or AX.25.
- **Erasure-coded FEC at the application layer (e.g. Raptor)** — same idea minus the network-coding generality. Raptor codes are patent-encumbered (Qualcomm); RLNC is patent-free.
- **Custom message replication on each bearer** — wastes the cheap bearer's bandwidth and triples paid-bearer cost.

## Compliance

- HeMB bond groups MUST be operator-declared via UI; never auto-spawned for unmodeled bearer combos.
- Cost-weighted splitter MUST default to free-bearer-first, paid-bearer-on-overflow only.
- Reassembly buffer MUST be bounded and emit audit events on eviction.
- Wire format changes are BREAKING for active field bonds — version field in the HeMB header MUST be bumped if anything in the symbol-layout changes.
- The IETF RFC draft is the source-of-truth wire spec from January 2027 onwards — pre-RFC versions are internal-only.
