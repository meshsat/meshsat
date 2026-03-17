---
title: "Hardware"
weight: 1
---

# Hardware Requirements

## Minimum Setup (Meshtastic + Iridium)

| Component | Model | Purpose | Approx. Cost |
|-----------|-------|---------|-------------|
| SBC | Raspberry Pi 4B (2GB+) or BananaPi BPI-M4 Zero | Gateway host | ~$35-55 |
| LoRa radio | Heltec LoRa 32 V3 or any Meshtastic-compatible device | Mesh network interface | ~$20 |
| Satellite modem | RockBLOCK 9603N | Iridium SBD transceiver | ~$250 |
| USB cables | USB-A to Micro-USB / USB-C | Connect radios to Pi | ~$5 |
| Power supply | 5V 3A USB-C | Power the Pi | ~$10 |
| MicroSD card | 32GB+ Class 10 | Boot media | ~$10 |

## RockBLOCK 9603N Wiring

Connect the RockBLOCK to the Pi via a USB-to-serial adapter (FTDI or CP2102).

**IMPORTANT: The TX/RX labels on the RockBLOCK 9603N refer to the modem's perspective, NOT the host's perspective.** Connect:
- RockBLOCK **TX** → USB adapter **RX**
- RockBLOCK **RX** → USB adapter **TX**
- RockBLOCK **GND** → USB adapter **GND**
- RockBLOCK **5V IN** → 5V supply (or USB adapter 5V if rated)

The modem draws up to 1.5A during transmission. Ensure your power supply can handle the peak load.

## Optional Additions

| Component | Purpose |
|-----------|---------|
| AIOC (All-In-One Cable) | Connect Baofeng/Quansheng radio for APRS |
| Huawei E220 USB modem | Cellular SMS gateway |
| CC2652P ZigBee dongle | ZigBee 3.0 sensor network |
| GPS module (USB) | Position source for the gateway itself |

## USB Device Detection

MeshSat auto-detects USB devices by VID:PID. Plug devices in and MeshSat will find them. Override with environment variables if needed:

```bash
MESHSAT_MESHTASTIC_PORT=/dev/ttyACM0
MESHSAT_IRIDIUM_PORT=/dev/ttyUSB0
MESHSAT_CELLULAR_PORT=/dev/ttyUSB1
```
