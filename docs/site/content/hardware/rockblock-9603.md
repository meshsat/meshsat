---
title: "RockBLOCK 9603N"
weight: 1
---

# RockBLOCK 9603N

The RockBLOCK 9603N by Ground Control is a compact Iridium SBD transceiver. It provides global two-way satellite messaging with 340-byte MO (mobile originated) and 270-byte MT (mobile terminated) payloads.

## Specifications

| Parameter | Value |
|-----------|-------|
| MO payload | 340 bytes |
| MT payload | 270 bytes |
| Serial | 19200 baud, 8N1 |
| Protocol | AT commands (ISU AT reference) |
| Power | 5V, up to 1.5A during TX |
| Antenna | SMA, requires clear sky view |

## Wiring

**TX/RX labels are from the modem's perspective.** See [Hardware](/getting-started/hardware/) for wiring details.

## AT Command Reference

MeshSat uses these key AT commands:

| Command | Purpose |
|---------|---------|
| `AT` | Check modem alive |
| `AT+SBDWB` | Write binary MO buffer (with checksum) |
| `AT+SBDIX` | Initiate SBD session (send MO, check MT) |
| `AT+SBDSX` | Lightweight status check (no session) |
| `AT+SBDRB` | Read binary MT buffer |
| `AT+CSQ` | Signal quality (0-5) |

## MO Buffer Checksum

The MO buffer uses a uint16 big-endian sum of all payload bytes as a checksum, appended after the payload in the `AT+SBDWB` command.
