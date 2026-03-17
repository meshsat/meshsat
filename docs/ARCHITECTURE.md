# MeshSat Architecture

_Created: 2026-03-17_

## Core Principle: Peer Mesh, Not Client/Server

MeshSat Bridge, MeshSat Android, and MeshSat Hub are **peers** in a communications mesh. None is primary. None requires the others to operate. Each is a full participant that can source and sink messages on any channel its hardware or software supports.

```
┌─────────────────────────────────────────────────────────────────┐
│                        MeshSat Mesh                             │
│                                                                 │
│  ┌──────────────┐     ┌──────────────┐     ┌──────────────┐    │
│  │  Bridge (Pi)  │────│  Hub (VPS)    │────│ Android (Phone)│   │
│  │              │     │              │     │              │    │
│  │ Meshtastic   │     │ MQTT broker  │     │ Meshtastic   │    │
│  │ Iridium 9603 │     │ TAK/CoT      │     │   (BLE)      │    │
│  │ Cellular SMS │     │ APRS-IS      │     │ Iridium SPP  │    │
│  │ APRS/Direwolf│     │ Webhook RX   │     │ Native SMS   │    │
│  │ TAK/CoT      │     │ SMS gateway  │     │              │    │
│  │ MQTT         │     │              │     │              │    │
│  │ ZigBee       │     │              │     │              │    │
│  │ Webhook      │     │              │     │              │    │
│  └──────────────┘     └──────────────┘     └──────────────┘    │
│         │                    │                    │              │
│         └──── MQTT/Tor/WG ──┴── MQTT/Tor/WG ────┘              │
│                                                                 │
│  Every node routes: [any inbound channel] → bus → [all outbound]│
└─────────────────────────────────────────────────────────────────┘
```

## Channel Availability Matrix

| Channel | Bridge (Pi) | Android (Phone) | Hub (VPS) |
|---------|:-----------:|:---------------:|:---------:|
| Meshtastic LoRa | USB serial | BLE | No |
| Iridium SBD | USB serial (RockBLOCK) | Bluetooth SPP (HC-05) | Webhook RX only |
| Astrocast | USB serial | No | No |
| Cellular SMS | USB modem (AT commands) | Native Android SMS | No (future: VoIP) |
| MQTT | TCP client | TCP client | Broker + client |
| Webhook HTTP | Send + receive | Send only | Receive (RockBLOCK webhook) |
| TAK/CoT | TCP/TLS to TAK server | **PLANNED**: via Hub relay | TCP/TLS to TAK server |
| APRS (Direwolf) | KISS TCP to Direwolf | **PLANNED**: via APRSDroid or Hub | APRS-IS TCP client |
| ZigBee 3.0 | USB dongle | No | No |
| Reticulum routing | Ed25519 identity | Ed25519 identity | **PLANNED** |

## Message Routing Flow

Every MeshSat node runs the same logical pipeline:

```
Inbound channel          Internal message bus         Outbound channels
─────────────           ──────────────────           ─────────────────
Meshtastic RX ──┐                               ┌──→ Iridium TX
Iridium MO ────┤       ┌──────────────┐        ├──→ TAK CoT TX
TAK CoT RX ────┤       │  Processor   │        ├──→ MQTT publish
APRS RX ───────┼──→────│  Dispatcher  │────→───┼──→ APRS TX
MQTT sub ──────┤       │  Rules eval  │        ├──→ Meshtastic TX
SMS inbound ───┤       │  Dedup       │        ├──→ SMS outbound
Webhook RX ────┘       │  Transform   │        └──→ Webhook TX
                       └──────────────┘
```

**Bridge** implements this in Go: `engine.Processor` → `engine.Dispatcher` → `rules.AccessEvaluator` → per-interface `gateway.Gateway.Forward()`.

**Android** implements the same in Kotlin: `GatewayService` → `Dispatcher` → `AccessEvaluator` → per-interface `DeliveryCallback`.

**Hub** runs the same MeshSat Go binary with different channels enabled. It has no serial hardware, but has persistent MQTT, webhook receivers, and internet-facing services (TAK, APRS-IS).

## How Hub Adds Value Without Being Required

Hub is **not a dependency**. It is an enhancement.

**Without Hub (fully offline):**
- Bridge operates all local channels autonomously (Meshtastic ↔ Iridium ↔ APRS ↔ SMS)
- Android operates BLE + SPP + SMS autonomously
- Bridge and Android can communicate directly via Meshtastic mesh RF
- All routing rules, delivery ledger, compression, and encryption work locally

**With Hub (internet available):**
- Hub receives RockBLOCK MO webhooks — provides a web-accessible receive endpoint
- Hub runs persistent TAK server connection — serves Bridge and Android nodes that lack direct TAK access
- Hub runs APRS-IS IGate — makes satellite-originated positions visible on aprs.fi
- Hub aggregates telemetry from all field nodes — central dashboard
- Hub enables device config versioning and remote management
- Hub relays messages between Bridge and Android nodes that are out of mesh RF range

**When Hub reconnects after outage:**
- MQTT retained messages ensure latest state is received
- Bridge/Android continue operating independently during Hub downtime
- No data loss — local delivery ledgers track all messages regardless of Hub availability

## Adapter Interface Parity

Bridge (Go) and Android (Kotlin) share the same conceptual adapter interface, adapted to each runtime:

### Bridge Gateway Interface (Go)

```go
type Gateway interface {
    Start(ctx context.Context) error
    Stop() error
    Forward(ctx context.Context, msg *transport.MeshMessage) error
    Receive() <-chan InboundMessage
    Status() GatewayStatus
    Type() string
}
```

Implementations: MQTTGateway, IridiumGateway, CellularGateway, WebhookGateway, AstrocastGateway, ZigBeeGateway, TAKGateway, APRSGateway.

### Android Equivalent (Kotlin)

Android does not use a single `Gateway` interface. Instead, the equivalent is distributed across:

- **InterfaceManager** — lifecycle state machine (Offline/Connecting/Online/Error/Disabled) with auto-reconnect backoff. Equivalent to Gateway.Start/Stop/Status.
- **Dispatcher.DeliveryCallback** — `suspend fun deliver(interfaceId, payload, textPreview): String?`. Equivalent to Gateway.Forward.
- **Transport-specific classes** (MeshtasticBle, IridiumSpp, SmsSender) — handle hardware specifics.

The channel registry (`ChannelDescriptor`) is identical across both platforms — same field names, same semantics, same defaults.

### Shared Design Patterns

| Pattern | Bridge (Go) | Android (Kotlin) |
|---------|-------------|-------------------|
| Channel registry | `channel.Registry` + `ChannelDescriptor` | `ChannelRegistry` + `ChannelDescriptor` |
| Access rules | `rules.AccessEvaluator` | `rules.AccessEvaluator` |
| Dispatcher | `engine.Dispatcher` | `engine.Dispatcher` |
| Failover | `engine.FailoverResolver` | `engine.FailoverResolver` |
| Dedup | `dedup.Tracker` | `dedup.Deduplicator` |
| Rate limiting | Per-rule token bucket | `ratelimit.TokenBucket` |
| Transform pipeline | `engine.TransformPipeline` | `engine.TransformPipeline` |
| Delivery ledger | `database.MessageDelivery` | `data.MessageDeliveryEntity` |
| Interface state machine | `engine.InterfaceManager` | `engine.InterfaceManager` |
| Ed25519 identity | `routing.Identity` | `routing.Identity` |
| Dead man's switch | `engine.DeadManSwitch` | `engine.DeadManSwitch` |
| Geofence | `engine.GeofenceMonitor` | `engine.GeofenceMonitor` |
| Health scores | `engine.HealthScorer` | `engine.HealthScorer` |
| Burst queue | `engine.BurstQueue` | `engine.BurstQueue` |
| Compression | SMAZ2, llama-zip, MSVQ-SC | MSVQ-SC (ONNX encoder + codebook decoder) |

## Fully Offline Mode

When no internet is available and no Hub is reachable:

1. **Bridge** routes between all locally-connected channels:
   - Meshtastic mesh user sends text → Bridge receives via serial → Dispatcher evaluates rules → forwards to Iridium (if satellite visible) and/or APRS (if Direwolf running) and/or SMS (if cellular modem connected)
   - All messages stored in local SQLite with full delivery ledger

2. **Android** routes between BLE mesh + Bluetooth SPP Iridium + native SMS:
   - Same Dispatcher logic, same rules engine, same delivery tracking
   - Room database stores everything locally

3. **Bridge ↔ Android** communicate via Meshtastic mesh RF (no internet needed):
   - Bridge has USB serial to Meshtastic radio
   - Android has BLE to same or nearby Meshtastic radio
   - Messages traverse the LoRa mesh between them

## Hub Reconnect Behavior

When Hub comes back online:

1. **MQTT reconnect** — Paho MQTT client (Bridge) and Android MQTT client auto-reconnect with exponential backoff
2. **Retained messages** — Hub's MQTT broker serves retained `status/health`, `position`, `config/current` messages immediately
3. **Message sync** — **PLANNED**: Currently no explicit sync protocol. Messages sent during Hub downtime that were delivered via local channels (Iridium, APRS, SMS) are tracked in local delivery ledger but not retroactively synced to Hub. Future: delivery ledger sync via MQTT.
4. **TAK update** — Hub's TAK gateway picks up latest positions from reconnecting nodes and pushes CoT PLI updates to TAK server

## Implementation Status

| Component | Status |
|-----------|--------|
| Bridge Gateway interface + 8 gateways | Implemented |
| Bridge TAK/CoT gateway | Implemented (tak.go, tak_cot.go, tak_config.go) |
| Bridge APRS gateway | Implemented (aprs.go, aprs_kiss.go, aprs_packet.go, aprs_config.go) |
| Bridge channel registry (9 channels) | Implemented |
| Bridge Dispatcher + rules + delivery ledger | Implemented |
| Android channel registry (3 channels) | Implemented |
| Android Dispatcher + rules + delivery ledger | Implemented |
| Android InterfaceManager state machine | Implemented |
| Android TAK/CoT support | **PLANNED** |
| Android APRS support | **PLANNED** |
| Hub MO webhook receiver | Implemented |
| Hub MT delivery (Cloudloop API) | Implemented |
| Hub MQTT broker | Implemented |
| Hub TAK/CoT gateway | **PLANNED** |
| Hub APRS-IS IGate | **PLANNED** |
| Hub OpenTAKServer Docker integration | **PLANNED** |
| Delivery ledger sync (Hub ↔ Bridge/Android) | **PLANNED** |
