---
title: "APRS"
weight: 4
---

# APRS (Direwolf)

MeshSat bridges to APRS via Direwolf, a software TNC that runs on the Pi. An AIOC (All-In-One Cable) connects a Baofeng or Quansheng handheld radio to the Pi via USB-C, providing both a soundcard (for 1200 baud AFSK) and a serial port.

## Hardware Chain

```
Baofeng/Quansheng Radio <-> AIOC USB-C <-> Raspberry Pi
                                              |
                                      MeshSat container
                                  (bundled Direwolf subprocess)
                                              |
                                     loopback KISS :8001
                                              |
                                      MeshSat APRS gateway
```

As of MESHSAT-514 Direwolf is bundled inside the MeshSat container and
supervised as a subprocess. The KISS port binds to `127.0.0.1` inside the
container and is **not** published outside. No host-side `direwolf`
package, `/etc/direwolf.conf`, systemd unit, or udev rule is needed.

## Prerequisites

### 1. AIOC Setup

The AIOC enumerates as a USB soundcard + serial port. Install the AIOC firmware from [AIOC GitHub](https://github.com/skuep/AIOC).

### 2. MeshSat APRS Configuration

In the dashboard under Bridge > Interfaces, create an APRS interface:

```json
{
  "callsign": "PA3XYZ",
  "ssid": 10,
  "audio_card": "AllInOneCable",
  "ptt_device": "/dev/ttyACM1",
  "ptt_line": "RTS",
  "modem_baud": 1200,
  "frequency_mhz": 144.800,
  "aprs_is_enabled": false
}
```

MeshSat renders Direwolf's config from these fields on every APRS gateway
start. To opt back into the legacy external daemon path, set
`"external_direwolf": true` and run your own Direwolf on the host.

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
