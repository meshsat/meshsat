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

## A Meshtastic daemon over TCP

Where the radio is driven by Meshtastic's Linux daemon (`meshtasticd`) rather than by a USB node, the bridge talks to the daemon's TCP API. The framing is the same as over serial; there are no modem lines, so the serial reset rungs (DTR/RTS reboot, USB reset, hub port power cycle) do not apply and the device supervisor leaves the port alone.

```bash
MESHSAT_MODE=direct
MESHSAT_MESHTASTIC_PORT=tcp://127.0.0.1:4403
```

`tcp://host` without a port uses 4403, the daemon's default. This is how the PinePhone with the Pine64 LoRa back cover runs the bridge against its own daemon (see [meshsat-lora-backplate](https://github.com/meshsat/meshsat-lora-backplate)). The daemon keeps one TCP client at a time: a Meshtastic app or CLI connecting to the same daemon disconnects the bridge, which reconnects with its usual backoff.

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
