---
title: "APRS"
weight: 4
---

# APRS (Direwolf)

MeshSat bridges to APRS via Direwolf, a software TNC that runs on the Pi. An AIOC (All-In-One Cable) connects a Baofeng or Quansheng handheld radio to the Pi via USB-C, providing both a soundcard (for 1200 baud AFSK) and a serial port.

## Hardware Chain

```
Baofeng/Quansheng Radio ←→ AIOC USB-C ←→ Raspberry Pi
                                              ↓
                                         Direwolf (software TNC)
                                              ↓
                                         KISS TCP :8001
                                              ↓
                                         MeshSat APRS Gateway
```

## Prerequisites

### 1. AIOC Setup

The AIOC enumerates as a USB soundcard + serial port. Install the AIOC firmware from [AIOC GitHub](https://github.com/skuep/AIOC).

### 2. Direwolf Installation

```bash
sudo apt install direwolf
```

Create `/etc/direwolf.conf`:

```
ADEVICE plughw:1,0   # AIOC soundcard — adjust device number
CHANNEL 0
MYCALL PA3XYZ-10     # Your callsign and SSID
MODEM 1200           # Standard APRS
KISSPORT 8001        # KISS TCP port for MeshSat
```

Start Direwolf:

```bash
direwolf -t 0 -c /etc/direwolf.conf
```

### 3. MeshSat APRS Configuration

In the dashboard under Bridge > Interfaces, create an APRS interface:

```json
{
  "callsign": "PA3XYZ",
  "ssid": 10,
  "kiss_host": "localhost",
  "kiss_port": 8001,
  "frequency_mhz": 144.800,
  "aprs_is_enabled": false
}
```

## EU APRS Frequency

The default frequency is **144.800 MHz** (EU APRS). North America uses 144.390 MHz.

## APRS-IS IGate

Optionally forward decoded packets to APRS-IS:

```json
{
  "aprs_is_enabled": true,
  "aprs_is_server": "euro.aprs2.net:14580",
  "aprs_is_passcode": "12345"
}
```

Generate your APRS-IS passcode from your callsign using standard APRS-IS verification.

## Supported Packet Types

| APRS Type | Decode | Encode |
|-----------|--------|--------|
| Position (uncompressed) | Yes | Yes |
| Message | Yes | Yes |
| Object | Partial | No |
| Telemetry | Partial | No |
