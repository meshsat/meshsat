---
title: "Architecture"
weight: 5
---

# Architecture

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
| Iridium SBD | USB serial | Bluetooth SPP | Webhook RX only |
| Cellular SMS | USB modem | Native Android SMS | No |
| MQTT | TCP client | TCP client | Broker + client |
| Webhook HTTP | Send + receive | Send only | Receive |
| TAK/CoT | TCP/TLS | Via Hub relay | TCP/TLS |
| APRS (Direwolf) | KISS TCP | Via APRSDroid | APRS-IS TCP |
| ZigBee 3.0 | USB dongle | No | No |

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

## How Hub Adds Value Without Being Required

Hub is **not a dependency**. It is an enhancement.

**Without Hub (fully offline):**
- Bridge operates all local channels autonomously
- Android operates BLE + SPP + SMS autonomously
- Bridge and Android communicate directly via Meshtastic mesh RF
- All routing rules, delivery ledger, compression, and encryption work locally

**With Hub (internet available):**
- Hub receives RockBLOCK MO webhooks
- Hub runs persistent TAK server connection — serves all field nodes
- Hub runs APRS-IS IGate — makes satellite positions visible on aprs.fi
- Hub aggregates telemetry from all field nodes
- Hub enables remote device configuration management
