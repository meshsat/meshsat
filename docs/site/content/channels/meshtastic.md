---
title: "Meshtastic"
weight: 1
---

# Meshtastic LoRa

Meshtastic is MeshSat's primary local network interface. It connects to a Meshtastic-compatible LoRa radio via USB serial and provides bidirectional mesh access.

## Configuration

MeshSat auto-detects Meshtastic devices by USB VID:PID. Override with:

```bash
MESHSAT_MESHTASTIC_PORT=/dev/ttyACM0
```

## Supported Devices

Any Meshtastic-compatible device with USB serial:
- Heltec LoRa 32 V3 (recommended)
- LilyGo T-Beam
- RAK WisBlock
- Any ESP32 with SX1262/SX1276

## Message Types

| Meshtastic PortNum | MeshSat Handling |
|-------------------|-----------------|
| TEXT_MESSAGE_APP (1) | Routed as text to all enabled channels |
| POSITION_APP (3) | Stored in positions table, available for TAK/CoT |
| TELEMETRY_APP (67) | Stored in telemetry table |
| PRIVATE_APP (256) | Used for Reticulum routing protocol |

## Technical Details

- Protocol: Meshtastic protobuf over serial
- Max payload: 237 bytes
- Connection: USB serial at 115200 baud (auto-detected)
- Node discovery via protobuf `FromRadio` messages
