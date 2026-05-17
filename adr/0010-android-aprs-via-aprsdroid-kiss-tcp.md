# ADR-0010 — Android APRS via APRSDroid KISS TCP client (Option A)

* Status: Accepted — codified after the fact 2026-05-17. Original decision document at `docs/ANDROID_APRS_DECISION.md` (created 2026-03-17). This ADR captures the same decision in the canonical `adr/` location so the project has one authoritative source for architectural decisions.
* Date: 2026-03-17 (decision); 2026-04-17 (Bridge-side update: Direwolf moved from host daemon to in-container subprocess, MESHSAT-514, keeps the pattern symmetric); 2026-05-17 (ADR recorded).
* Deciders: `ufwtqkgz@meshsat.net`
* Source document: `docs/ANDROID_APRS_DECISION.md`

## Context

The Android companion app needs APRS support for two reasons: (1) the user has an amateur radio license and wants RF APRS reachability for emergency comms; (2) feature parity with Bridge, which already speaks APRS via the bundled Direwolf KISS TNC.

On Android, APRS over RF requires: AIOC USB soundcard hardware, AFSK 1200 baud modulation/demodulation, AX.25 framing, and APRS packet codec. The choice is **where** these layers live.

Three options:

1. **Option A — connect to APRSDroid KISS TCP server**: APRSDroid (already a mature OSS APRS app for Android) runs as a separate app on the same phone, handles AIOC + AFSK + AX.25, exposes a KISS TCP server on localhost. MeshSat Android connects as a KISS TCP client.
2. **Option B — MeshSat Android drives AIOC USB audio directly**: MeshSat Android reimplements AFSK, AX.25, and USB audio handling internally.
3. **Option C — Hub-proxied APRS via APRS-IS only**: Android sends to Hub via MQTT; Hub's APRS-IS connection forwards. No local RF APRS.

## Decision

**Option A — APRSDroid KISS TCP client.**

MeshSat Android connects to APRSDroid's KISS TCP server on `localhost:8001`. APRSDroid handles AIOC USB audio + AFSK modulation + AX.25 framing. MeshSat Android implements only:

- A KISS TCP client (~200 lines of Kotlin — connect, send/receive KISS frames, exponential reconnect backoff).
- AX.25 + APRS packet encode/decode in Kotlin, ported from the Bridge's Go implementation.

This is the **same architectural pattern as Bridge → Direwolf** (also KISS TCP). After MESHSAT-514, Bridge bundles Direwolf inside its own container as a supervised in-container subprocess speaking KISS on loopback — Android using APRSDroid via KISS TCP is the same shape on a different platform.

## Consequences

**Positive**
- Proven stack — AIOC + APRSDroid is already validated by the amateur radio community. Reimplementing the TNC would be high-risk, low-reward.
- Architectural consistency — Bridge's APRS path and Android's APRS path share the KISS protocol. AX.25 + APRS codecs are well-defined and small; porting Go → Kotlin is tractable.
- Minimal code on MeshSat Android (~200 lines KISS client + Kotlin port of AX.25/APRS codec).
- Option C (Hub-proxied APRS-IS) remains available as automatic fallback when APRSDroid is not running OR AIOC is not connected. The two approaches are complementary, not exclusive.

**Negative**
- User must install + configure APRSDroid separately. UX cost.
- Two apps run simultaneously — additional battery drain.
- APRSDroid must be configured with the correct soundcard (AIOC) and KISS TCP enabled on port 8001 — configuration drift is a support burden.

## Alternatives considered (recap)

- **Option B (direct AIOC USB audio)**: rejected — enormous implementation effort to write AFSK 1200 baud modem in Kotlin/JNI; USB audio APIs on Android are complex and device-dependent; AIOC-on-Android is poorly documented; high risk of subtle audio timing bugs. Would essentially reimplement APRSDroid.
- **Option C (Hub-proxied APRS-IS only)**: rejected as **primary** path — no local RF APRS (internet-gated only); requires Hub online; not useful in the field without internet. **Accepted** as automatic fallback layer.

## Implementation path

### Phase 1 — Hub APRS-IS (no Android code)
- Hub connects to APRS-IS and injects satellite-originated positions.
- All MeshSat devices become visible on `aprs.fi` automatically.
- Android benefits without any changes.

### Phase 2 — Android KISS TCP client
1. Add `APRSChannel` to Android channel registry (same `ChannelDescriptor` shape as Bridge's `aprs` channel).
2. Implement `KissClient.kt` — TCP connect to `localhost:8001`, KISS frame encode/decode.
3. Implement `AprsPacket.kt` — AX.25 encode/decode, APRS position/message encode/decode (port from Go).
4. Wire into `InterfaceManager` as a new interface with auto-reconnect.
5. Wire into `Dispatcher` for message routing.

### Phase 3 — APRSDroid integration guide
- Document AIOC USB audio device selection.
- KISS TCP server enable on port 8001.
- Callsign + SSID configuration.

## Prerequisites for the user

1. AIOC flashed with correct firmware.
2. APRSDroid installed from F-Droid or Play Store.
3. APRSDroid configured: AIOC as audio device, KISS TCP enabled on port 8001.
4. Valid amateur radio callsign.
5. MeshSat Android APRS interface configured with matching callsign + SSID.

## Compliance

- MeshSat Android MUST connect to APRSDroid via KISS TCP, NOT attempt direct AIOC audio access.
- The AX.25 + APRS codec MUST be a direct port of the Bridge's Go implementation — same wire format, same well-defined protocol; tested by interoperating the two clients on the same channel.
- When APRSDroid is unreachable, MeshSat Android MUST fall back to Hub-proxied APRS-IS (Option C) — never display "APRS unavailable" without offering the fallback.
- This ADR supersedes (and re-locates) `docs/ANDROID_APRS_DECISION.md`. The `docs/` file remains for historical context; new edits go in this ADR.
