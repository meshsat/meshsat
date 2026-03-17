# TAK/CoT Integration Research — MeshSat

_Created: 2026-03-17_
_Author: Claude Code (research for MESHSAT TAK adapter)_

## 1. CoT Event Type Taxonomy Relevant to MeshSat

Cursor on Target (CoT) uses a hierarchical dot-separated type string to classify events.
The taxonomy relevant to MeshSat's multi-channel bridge:

### Position / PLI (Position Location Information)

| CoT Type | Meaning | MeshSat Use |
|----------|---------|-------------|
| `a-f-G-U-C` | Atom / Friend / Ground / Unit / Combat | Default friendly ground unit — used for all position reports from registered MeshSat devices |
| `a-f-G-U-C-I` | ...Infantry | Optional: mesh nodes carried by operators |
| `a-f-G-E-S` | Atom / Friend / Ground / Equipment / Sensor | Sensor/telemetry nodes (ZigBee, environmental) |
| `a-n-G` | Atom / Neutral / Ground | Unknown or unregistered nodes |

### Emergency / SOS

| CoT Type | Meaning | MeshSat Use |
|----------|---------|-------------|
| `a-f-G-U-C` + `<emergency>` detail | Friend unit with emergency detail block | SOS button press — Meshtastic `POSITION_APP` with `emergency=true`, or dedicated SOS MQTT topic |
| `b-a` | Bits / Alarm | Dead man's switch timeout alert |

The emergency detail block is the standard way to signal SOS in CoT:
```xml
<detail>
  <emergency type="911 Alert">SOS activated</emergency>
  <contact callsign="MESHSAT-42"/>
</detail>
```

Emergency `type` values: `911 Alert`, `Ring The Bell`, `In Contact`, `Cancel`.

### Sensor Telemetry

| CoT Type | Meaning | MeshSat Use |
|----------|---------|-------------|
| `t-x-d-d` | Tasking / Other / Data / Data | Generic telemetry data (temperature, humidity, battery) |
| `b-m-r` | Bits / Machined / Route | Track/route data |

### Chat / Messaging

| CoT Type | Meaning | MeshSat Use |
|----------|---------|-------------|
| `b-t-f` | Bits / Text / FreeText | GeoChat — text messages between TAK users and MeshSat devices |

## 2. CoT XML Structure

### Minimal Position Report (PLI)

```xml
<?xml version="1.0" encoding="UTF-8"?>
<event version="2.0" uid="MESHSAT-300234063904190" type="a-f-G-U-C"
       time="2026-03-17T12:00:00Z" start="2026-03-17T12:00:00Z"
       stale="2026-03-17T12:05:00Z" how="m-g">
  <point lat="52.3676" lon="4.9041" hae="0" ce="10" le="10"/>
  <detail>
    <contact callsign="MESHSAT-42"/>
    <__group name="Cyan" role="Team Member"/>
    <precisionlocation altsrc="GPS" geopointsrc="GPS"/>
    <track course="0" speed="0"/>
    <status battery="85"/>
    <remarks>Via Iridium SBD</remarks>
  </detail>
</event>
```

**Key fields:**
- `uid`: Globally unique — use `MESHSAT-{IMEI}` or `MESHSAT-{mesh_node_id}`
- `time`: When the event was generated (ISO 8601)
- `start`: When the event becomes valid (usually same as `time`)
- `stale`: When the event expires — typically `time + cot_stale_seconds` (default 300s)
- `how`: How the position was obtained — `m-g` (machine GPS), `h-e` (human estimated)
- `point`: WGS84 lat/lon, HAE (height above ellipsoid), CE (circular error 95%), LE (linear error 95%)

### SOS Event

Same as PLI but with `<emergency>` detail block:

```xml
<event version="2.0" uid="MESHSAT-300234063904190" type="a-f-G-U-C"
       time="2026-03-17T12:00:00Z" start="2026-03-17T12:00:00Z"
       stale="2026-03-17T12:10:00Z" how="m-g">
  <point lat="52.3676" lon="4.9041" hae="0" ce="10" le="10"/>
  <detail>
    <contact callsign="MESHSAT-42"/>
    <emergency type="911 Alert">SOS activated via Iridium</emergency>
    <remarks>Emergency beacon triggered on device 300234063904190</remarks>
  </detail>
</event>
```

### Dead Man's Switch Alert

```xml
<event version="2.0" uid="MESHSAT-DEADMAN-300234063904190" type="b-a"
       time="2026-03-17T12:00:00Z" start="2026-03-17T12:00:00Z"
       stale="2026-03-17T12:30:00Z" how="h-e">
  <point lat="52.3676" lon="4.9041" hae="0" ce="100" le="100"/>
  <detail>
    <contact callsign="MESHSAT-42"/>
    <remarks>Dead man's switch timeout — no check-in for 3600s</remarks>
  </detail>
</event>
```

### Sensor Telemetry

```xml
<event version="2.0" uid="MESHSAT-SENSOR-zigbee-0x1234" type="t-x-d-d"
       time="2026-03-17T12:00:00Z" start="2026-03-17T12:00:00Z"
       stale="2026-03-17T12:05:00Z" how="m-g">
  <point lat="52.3676" lon="4.9041" hae="0" ce="50" le="50"/>
  <detail>
    <contact callsign="MESHSAT-SENSOR-1"/>
    <remarks>temperature=22.5C humidity=65% battery=92%</remarks>
  </detail>
</event>
```

## 3. Recommended Go Library for CoT Generation

### Option A: NERVsystems/cotlib (Recommended)

**Repository:** https://github.com/NERVsystems/cotlib
**License:** Apache 2.0 (compatible with GPLv3)
**Language:** Pure Go, no CGO
**Features:**
- `Event` struct with full CoT field support (uid, type, time, start, stale, how, point, detail)
- XML marshaling/unmarshaling via `encoding/xml`
- Detail block support with extensible child elements
- Both generation and parsing

**Assessment:** This is the right choice for MeshSat. It provides the core CoT struct and XML serialization. The detail blocks (contact, emergency, group, track, status) need to be defined as additional structs — cotlib provides the envelope, MeshSat adds the domain-specific details. This is the correct separation of concerns.

**Usage pattern:**
```go
import "github.com/NERVsystems/cotlib"

event := cotlib.Event{
    Version: "2.0",
    UID:     "MESHSAT-" + device.IMEI,
    Type:    "a-f-G-U-C",
    Time:    time.Now().UTC(),
    Start:   time.Now().UTC(),
    Stale:   time.Now().UTC().Add(5 * time.Minute),
    How:     "m-g",
    Point: cotlib.Point{
        Lat: pos.Latitude,
        Lon: pos.Longitude,
        HAE: pos.Altitude,
        CE:  10.0,
        LE:  10.0,
    },
}
xmlBytes, _ := xml.Marshal(event)
```

### Option B: Custom implementation with encoding/xml

If cotlib proves too rigid or unmaintained, the CoT XML schema is simple enough to implement with Go's `encoding/xml` directly. The entire schema is ~100 lines of struct definitions.

**Recommendation:** Start with cotlib, fall back to custom if it blocks progress.

### Option C: TAK Protocol Buffers (protobuf)

TAK Server also supports protobuf wire format (`takproto`) for better performance. Since MeshSat already uses protobuf (google.golang.org/protobuf in go.mod), this could be a future optimization. XML is correct for initial implementation — it's what all TAK clients and reference implementations use.

## 4. Proposed CoT Adapter Interface for MeshSat

The TAK adapter fits MeshSat's existing `Gateway` interface pattern:

```go
// internal/gateway/tak.go
type TAKGateway struct {
    config    TAKConfig
    db        *database.DB
    inCh      chan InboundMessage
    outCh     chan *transport.MeshMessage
    conn      net.Conn        // TCP connection to TAK server
    connected atomic.Bool
    // ... standard gateway fields
}

// Implements gateway.Gateway interface:
// Start()   — dial TAK server TCP/TLS, start read/write goroutines
// Stop()    — close connection, drain channels
// Forward() — convert MeshMessage to CoT XML, write to TCP stream
// Receive() — return inCh (fed by TCP reader goroutine)
// Status()  — return GatewayStatus with connected state
// Type()    — return "tak"
```

### Data Flow

**Outbound (MeshSat → TAK Server):**
1. Dispatcher calls `Forward(ctx, msg)` with a MeshMessage
2. TAK gateway inspects message source/content to determine CoT event type
3. Builds CoT XML using cotlib + MeshSat detail structs
4. Writes XML + newline delimiter to TCP stream
5. Logs delivery to DB

**Inbound (TAK Server → MeshSat):**
1. TCP reader goroutine reads CoT XML from stream (newline-delimited)
2. Parses CoT event, extracts position/text/emergency
3. Converts to InboundMessage with source="tak"
4. Pushes to inCh for processor to route to other channels

### CoT Event Type Selection Logic

```go
func cotTypeForMessage(msg *transport.MeshMessage) string {
    // SOS/emergency
    if msg.PortNum == 3 && hasEmergencyFlag(msg) {  // POSITION_APP with emergency
        return "a-f-G-U-C"  // Same type, emergency detail block distinguishes
    }
    // Normal position
    if msg.PortNum == 3 {  // POSITION_APP
        return "a-f-G-U-C"
    }
    // Telemetry
    if msg.PortNum == 67 {  // TELEMETRY_APP
        return "t-x-d-d"
    }
    // Text message → GeoChat
    if msg.PortNum == 1 {  // TEXT_MESSAGE_APP
        return "b-t-f"
    }
    // Default
    return "a-f-G-U-C"
}
```

## 5. MQTT Topic → CoT Event Mapping

For MeshSat Hub integration, Hub MQTT topics map to CoT events as follows:

| Hub MQTT Topic | CoT Event Type | CoT Detail |
|----------------|---------------|------------|
| `meshsat/{id}/position` | `a-f-G-U-C` | `<point>` with lat/lon, `<track>` with course/speed |
| `meshsat/{id}/sos` | `a-f-G-U-C` | `<emergency type="911 Alert">` |
| `meshsat/{id}/mo/decoded` | `b-t-f` (text) or `a-f-G-U-C` (position) | Depends on payload content |
| `meshsat/{id}/telemetry` | `t-x-d-d` | `<remarks>` with sensor readings |
| `meshsat/{id}/status/health` | `a-f-G-E-S` | `<status battery="X"/>` |
| Dead man's switch alert | `b-a` | `<remarks>` with timeout details |

### New MQTT Topics for TAK Integration

```
meshsat/{device_id}/tak/cot/out      # CoT XML generated from device data (QoS 1)
meshsat/{device_id}/tak/cot/in       # CoT XML received from TAK server for this device (QoS 1)
meshsat/hub/tak/status               # TAK gateway connection status (QoS 0, retained)
```

## 6. OpenTAKServer vs FreeTAKServer

### OpenTAKServer (Recommended)

- **Repository:** https://github.com/brian7704/OpenTAKServer
- **License:** Eclipse Public License 2.0
- **Stack:** Python/Flask, PostgreSQL
- **Protocols:** TCP CoT (8087), TLS CoT (8089), DataPackage API (8443)
- **Deployment:** Docker Compose available
- **Why:** Active development, cleaner codebase, better Docker support, maintained by the same developer who built TAK_Meshtastic_Gateway — familiar with the Meshtastic/satellite use case.

### FreeTAKServer

- **Repository:** https://github.com/FreeTAKTeam/FreeTakServer
- **License:** Eclipse Public License 2.0
- **Stack:** Python/Flask, SQLite
- **Protocols:** TCP CoT (8087), REST API
- **Why not:** Development has slowed, more complex configuration, less Docker-friendly. The SQLite backend is a concern for multi-client scenarios.

### TAK Server (official, closed-source)

- **Maintained by TAK Product Center (US DoD)**
- **Not open source — requires CaC or DEVCOM access**
- **Protocol-compatible** with OpenTAKServer on the CoT TCP/TLS ports
- **MeshSat should target protocol compatibility, not server coupling**

**Recommendation:** Document OpenTAKServer as the recommended integration target for self-hosted MeshSat Hub deployments. MeshSat's TAK adapter should be protocol-level (CoT XML over TCP/TLS), not server-specific — it will work with any TAK server implementation.

## 7. Reference Project Analysis

### meshtastic/ATAK-Plugin

- Java/Kotlin ATAK plugin that renders Meshtastic mesh nodes on the TAK map
- Converts Meshtastic `POSITION_APP` (portnum 3) to CoT PLI events
- Uses `a-f-G-U-C` as the default type for all mesh nodes
- Maps Meshtastic node long name to CoT `<contact callsign="...">`
- Maintains a node cache to avoid spamming TAK with duplicate positions
- **Key insight:** Uses `how="m-g"` (machine-generated GPS) for positions with GPS lock, `how="h-e"` for estimated positions
- **Key insight:** Emergency handling uses the same `a-f-G-U-C` type with an `<emergency>` detail block — the type does NOT change for SOS

### snstac/inrcot

- Python, uses PyTAK framework for CoT TCP/UDP transport
- Converts Garmin inReach (Iridium) positions to CoT
- **Architecturally closest to MeshSat's TAK adapter** — satellite position → CoT pipeline
- Data flow: inReach MapShare KML feed → parse → CoT Event → PyTAK CoT sender → TAK server
- Uses `a-f-G-U-C` for all positions, `a-f-G-U-C-I` for infantry specifically
- Stale time: configurable, default 600s (10 min) — appropriate for satellite update intervals
- CoT UID: `inrcot-{device_name}` — MeshSat should use `meshsat-{imei}` or `meshsat-{node_id}`
- **Key insight:** Stale time should be 2-3x the expected update interval. For Iridium (5-15 min intervals), use 1800s (30 min) default.

### NERVsystems/cotlib

- Pure Go, Apache 2.0 license
- Clean `Event`, `Point`, `Detail` structs with XML tags
- Supports marshal/unmarshal
- No CGO dependencies
- Small, focused — exactly what's needed for CoT XML generation
- **Assessment:** Suitable for MeshSat. Use this.

### brian7704/TAK_Meshtastic_Gateway

- Python standalone gateway between Meshtastic and TAK Server
- Bidirectional: Meshtastic positions → CoT PLI, TAK chat → Meshtastic messages
- Connects to Meshtastic via serial or TCP interface
- Connects to TAK server via TCP CoT stream
- **Key insight:** Uses newline (`\n`) as CoT XML message delimiter on the TCP stream
- **Key insight:** TAK server sends a keepalive/ping CoT event periodically — the gateway must handle (and ignore) these
- **Key insight:** Bidirectional routing requires callsign-to-node mapping maintained in a local cache

## 8. Open Questions

1. **Protobuf vs XML:** TAK Server supports both CoT XML and TAK Protobuf (`takproto`). XML is universal and simpler. Protobuf is more efficient for bandwidth-constrained links. Start with XML, consider protobuf as a future optimization for satellite channels.

2. **TLS certificate management:** TAK Server TLS (port 8089) uses mutual TLS (client cert + server cert). MeshSat needs a config path for client cert/key/CA bundle. Self-signed certs are common in field deployments.

3. **Callsign management:** Each MeshSat device needs a TAK callsign. Options:
   - Auto-generate from IMEI: `MESHSAT-{last4digits}`
   - User-configured per device in the device registry
   - Configurable prefix: `{prefix}-{device_label}` (recommended)

4. **Rate limiting:** TAK servers can be overwhelmed by high-frequency position updates. MeshSat should coalesce positions (e.g., max 1 PLI per device per 30s) before forwarding to TAK.

5. **GeoChat bridging:** Should MeshSat bridge TAK GeoChat messages to/from Meshtastic text messages? This would enable TAK operators to communicate with mesh users through the MeshSat bridge. Architecturally straightforward (GeoChat CoT `b-t-f` ↔ MeshMessage portnum 1) but needs careful UX design for the callsign mapping.

6. **Multi-device CoT UIDs:** When a single MeshSat bridge has multiple devices (mesh + Iridium + cellular), each needs a distinct CoT UID. Proposed scheme: `meshsat-{channel}-{device_id}` (e.g., `meshsat-iridium-300234063904190`, `meshsat-mesh-!aabbccdd`).

## 9. How Hub's TAK Connection Serves Bridge and Android

Hub runs an always-on TAK gateway connected to OpenTAKServer. This single connection serves all field nodes:

**Bridge nodes** that have direct TAK connectivity use their own TAKGateway. Bridge nodes without direct TAK access (no route to TAK server) benefit from Hub: Bridge publishes to Hub MQTT → Hub forwards to TAK.

**Android nodes** use Hub-proxied TAK exclusively (see ANDROID_TAK_DECISION.md). Android publishes position/SOS/telemetry to Hub MQTT topics → Hub converts to CoT → OpenTAKServer → ATAK clients. Zero TAK-specific code on Android.

**Message flow (Hub as TAK relay):**
```
Android/Bridge → MQTT → Hub → CoT XML → OpenTAKServer → ATAK
ATAK → CoT XML → OpenTAKServer → Hub → MQTT → Android/Bridge
```

## 10. OpenTAKServer Deployment for Hub

Recommended: Run OpenTAKServer as a Docker Compose service alongside Hub.

```yaml
# In meshsat-hub/docker-compose.yml (tak profile)
opentakserver:
  image: ghcr.io/brian7704/opentakserver:latest
  ports:
    - "8087:8087"   # TCP CoT
    - "8089:8089"   # TLS CoT
    - "8443:8443"   # HTTPS DataPackage
  profiles:
    - tak
```

Enable with: `docker compose --profile tak up -d`

Hub's Go service connects internally on `opentakserver:8087` (Docker network, no TLS needed). External ATAK clients connect on the host's public IP ports.
