# Android APRS Integration — Decision Document

_Created: 2026-03-17_

## Options Evaluated

### Option A — MeshSat Android connects to APRSDroid TNC server (RECOMMENDED)

APRSDroid runs as a separate app on the same phone. It manages the AIOC USB soundcard and runs a software TNC. MeshSat Android connects to APRSDroid's KISS TCP server on localhost.

**Pros:**
- Clean separation of concerns — APRSDroid handles AFSK modulation, MeshSat handles routing
- Leverages APRSDroid's mature, battle-tested TNC implementation (1200 baud AFSK)
- APRSDroid already handles AIOC USB audio on Android
- MeshSat Android's KISS TCP client code would be minimal (~200 lines of Kotlin)
- Same architecture pattern as Bridge → Direwolf (KISS TCP)
- APRSDroid has its own APRS-IS IGate capability as a bonus

**Cons:**
- Requires user to install and configure APRSDroid separately
- Two apps running simultaneously — additional battery drain
- APRSDroid must be configured with the correct soundcard (AIOC) and KISS TCP enabled

### Option B — MeshSat Android drives AIOC USB audio directly

MeshSat Android handles USB audio, AFSK modulation/demodulation, and AX.25 framing internally.

**Pros:**
- Single-app experience
- No dependency on APRSDroid

**Cons:**
- Enormous implementation effort — AFSK 1200 baud modem in Kotlin/JNI
- USB audio APIs on Android are complex and device-dependent
- Would essentially reimplement APRSDroid's core functionality
- AIOC USB audio handling on Android is not well-documented
- High risk of subtle audio timing bugs

### Option C — Hub-proxied APRS (fallback)

Android sends messages to Hub via MQTT. Hub's APRS-IS connection handles injection into the APRS network.

**Pros:**
- Zero APRS code on Android
- Works without any radio hardware on the phone

**Cons:**
- No local RF APRS — only APRS-IS (internet-gated)
- Requires Hub to be online
- Not useful in the field without internet

## Decision: Option A — APRSDroid KISS TCP Client

**Rationale:**

1. **Proven stack**: The AIOC + APRSDroid combination is already validated by the amateur radio community. Reimplementing the TNC would be high-risk, low-reward.

2. **Architectural consistency**: Bridge uses Direwolf via KISS TCP. Android using APRSDroid via KISS TCP is the same pattern on a different platform. The KISS protocol is identical.

3. **Minimal code**: MeshSat Android only needs a KISS TCP client (connect to `localhost:8001`, send/receive KISS frames). The AX.25 and APRS packet encoding/decoding can be ported from the Bridge's Go implementation to Kotlin — the protocol is well-defined and small.

4. **Option C as automatic fallback**: When APRSDroid is not running or AIOC is not connected, Hub-proxied APRS-IS is still available. The two approaches are complementary, not exclusive.

## Implementation Path

### Phase 1 — Hub APRS-IS (no Android code needed)
- Hub connects to APRS-IS and injects satellite-originated positions
- All MeshSat devices become visible on aprs.fi automatically
- This is purely a Hub feature — Android benefits without changes

### Phase 2 — Android KISS TCP client
1. Add `APRSChannel` to Android channel registry (same descriptor as Bridge's `aprs` channel)
2. Implement `KissClient.kt` — TCP connect to `localhost:8001`, KISS frame encode/decode
3. Implement `AprsPacket.kt` — AX.25 encode/decode, APRS position/message encode/decode (port from Go)
4. Wire into InterfaceManager as a new interface with auto-reconnect
5. Wire into Dispatcher for message routing

### Phase 3 — APRSDroid integration guide
- Document how to configure APRSDroid for MeshSat Android
- AIOC USB audio device selection
- KISS TCP server enable on port 8001
- Callsign + SSID configuration

## Prerequisites for User

1. AIOC flashed with correct firmware
2. APRSDroid installed from F-Droid or Play Store
3. APRSDroid configured: AIOC as audio device, KISS TCP enabled on port 8001
4. Valid amateur radio callsign
5. MeshSat Android APRS interface configured with matching callsign + SSID
