# MeshSat

[![Pipeline](https://gitlab.nuclearlighters.net/products/cubeos/meshsat/badges/main/pipeline.svg)](https://gitlab.nuclearlighters.net/products/cubeos/meshsat/-/pipelines)
![Go 1.24+](https://img.shields.io/badge/go-1.24+-blue)
[![License: GPL v3](https://img.shields.io/badge/license-GPLv3-green)](LICENSE)
![Docker: ghcr.io/cubeos-app/meshsat](https://img.shields.io/badge/docker-ghcr.io%2Fcubeos--app%2Fmeshsat-blue)

MeshSat is a multi-transport mesh and satellite gateway that bridges Meshtastic LoRa networks to satellite, cellular, and data channels. Eight transport types -- Meshtastic LoRa, Iridium SBD, Astrocast LEO, Cellular SMS, ZigBee, MQTT, Webhooks, and direct serial -- are all available as routing destinations. Access rules route messages between any pair of interfaces with per-rule filtering, failover groups, and transform pipelines.

MeshSat runs as a standalone Docker container on any Linux machine with USB-connected devices. No cloud dependencies, no subscriptions beyond your satellite or cellular plan.

## Dashboard

![MeshSat Dashboard](docs/images/meshsat_dashboard.png)
*Built-in web dashboard showing Iridium status, mesh nodes, SOS controls,
SBD message queue, and GPS/satellite positioning*

![MeshSat Pass Predictor](docs/images/meshsat_passes.png)
*Satellite pass predictor with signal correlation --
optimizes transmission timing in obstructed environments*

## Features

- **8 transports:** Meshtastic LoRa, Iridium SBD, Astrocast LEO, Cellular SMS, ZigBee (Z-Stack ZNP), MQTT, Webhooks, direct serial
- **3 compression tiers:** SMAZ2 (lossless, <1ms), llama-zip (LLM-based lossless, ~200ms), MSVQ-SC (lossy semantic, rate-adaptive)
- **Reticulum-inspired routing** with Ed25519 identity, cryptographic announce broadcasting, link management, and keepalive
- **Transform pipelines** per interface: compress (zstd, SMAZ2) + encrypt (AES-256-GCM) + encode (base64)
- **Channel registry** with self-describing adapters and MTU awareness
- **Dispatcher** with failover groups, delivery ledger, per-channel workers, and visited-set loop prevention
- **Access rules engine** with object groups (node, portnum, sender, contact), rate limiting, and implicit deny
- **Config export/import** in YAML format (Cisco `show running-config` style)
- **Web dashboard** (Vue.js SPA, 11 views) for monitoring, sending messages, and managing devices
- **REST API** with 106+ endpoints for integration
- **Ed25519 signing service** with hash-chain audit log for tamper detection
- **Auto-detects** USB devices on startup via VID:PID tables and protocol probing
- **Satellite pass prediction** using SGP4/TLE propagation with signal correlation
- **Android companion app** ([meshsat-android](https://github.com/cubeos-app/meshsat-android)) with BLE mesh, SPP Iridium, SMS, MSVQ-SC, and AES-GCM
- Runs on ARM64 (Raspberry Pi 5/4) and x86_64

## Hardware

![MeshSat Field Kit](docs/images/meshsat_field_kit.jpg)
*MeshSat field kit -- a self-contained, portable multi-transport gateway in a waterproof hard case.
Meshtastic and cellular are USB-connected; the RockBLOCK 9603 is UART-wired to the Pi 5 GPIO.
All devices are auto-detected on startup.*

| # | Component | Description |
|---|-----------|-------------|
| 1 | **Heltec LoRa V4** (ESP32-S3 + SX1262 + GPS) | Meshtastic mesh radio -- 915 MHz LoRa, OLED display, 2 MB PSRAM, 16 MB flash |
| 2 | **RockBLOCK 9603** (Iridium 9603N, SMA) | Iridium satellite modem -- SBD protocol, 340-byte MO buffer, UART via Pi 5 GPIO |
| 3 | **LILYGO T-Call A7670** (ESP32 + A7670E) | 4G LTE / 2G GSM cellular modem -- AT commands, SMS + data |
| 4 | **INIU 25000mAh** (100W USB-C PD) | Portable power bank -- powers all components via USB |
| 5 | **Raspberry Pi 5** (8 GB RAM) | MeshSat Bridge host -- standalone mode, Debian Bookworm |

### Supported Devices

| Category | Device | Status | Notes |
|----------|--------|--------|-------|
| Meshtastic | Lilygo T-Echo (nRF52840) | Tested | 915 MHz, USB-C, end-to-end verified |
| Meshtastic | Lilygo T-Deck | Tested | ESP32-S3, keyboard, screen |
| Meshtastic | Espressif / CH340 / CP2102 / Nordic devices | Should work | Auto-detected via USB VID:PID |
| Satellite | RockBLOCK 9603 (Iridium 9603N) | Tested | RS-232 via USB adapter, 19200 baud |
| Satellite | Astrocast Astronode S | Code complete | Binary frame protocol, fragmentation, pass prediction |
| Cellular | SIM7600G-H (4G LTE) | Tested | USB modem, AT commands, SMS + data |
| Cellular | Huawei E220 (3G HSDPA) | Tested | USB modem, AT commands, SMS + data |
| ZigBee | SONOFF ZigBee 3.0 USB Dongle Plus (CC2652P) | Code complete | Z-Stack ZNP protocol, VID:PID auto-detect with ZNP probe |
| Host | Raspberry Pi 5 | Tested | ARM64, 4 GB RAM, Debian Bookworm |
| Host | Any x86_64 / ARM64 Linux | Should work | Docker + USB serial required |

## Quick Start

### Option A: One-liner with Docker

```bash
docker run -d \
  --name meshsat \
  --privileged \
  --network host \
  -e MESHSAT_MODE=direct \
  -e MESHSAT_PORT=6050 \
  -e MESHSAT_DB_PATH=/data/meshsat.db \
  -v meshsat-data:/data \
  -v /dev:/dev \
  -v /sys:/sys:ro \
  --restart unless-stopped \
  ghcr.io/cubeos-app/meshsat:latest
```

Open `http://<your-ip>:6050` in a browser to access the dashboard.

### Option B: Docker Compose

```yaml
services:
  meshsat:
    image: ghcr.io/cubeos-app/meshsat:latest
    container_name: meshsat
    restart: unless-stopped
    privileged: true
    network_mode: host
    environment:
      - MESHSAT_MODE=direct
      - MESHSAT_PORT=6050
      - MESHSAT_DB_PATH=/data/meshsat.db
    volumes:
      - meshsat-data:/data
      - /dev:/dev
      - /sys:/sys:ro

volumes:
  meshsat-data:
```

```bash
docker compose up -d
```

### Option C: Build from Source

```bash
git clone https://github.com/cubeos-app/meshsat.git
cd meshsat
make build-with-web    # Builds Vue SPA + Go binary
# Or with Docker:
docker compose -f docker-compose.direct.yml up --build
```

## Setup Guide

### Step 1: Plug in your devices

Connect your Meshtastic radio and/or satellite modem and/or cellular modem via USB. MeshSat will detect them automatically on startup using USB VID:PID tables and protocol probing (pure Go serial via `go.bug.st/serial`).

### Step 2: Start the container

Use one of the methods above. MeshSat will scan USB devices, connect to each one it finds via protocol-specific probing (Meshtastic protobuf, Iridium AT, ZNP for ZigBee), and start the web dashboard on port 6050.

### Step 3: Open the dashboard

Navigate to `http://<your-ip>:6050`. The dashboard provides 11 views: Dashboard, Messages, Nodes, Map, Passes, Bridge, Interfaces, Settings, Audit, Help, and About.

### Step 4: Set up access rules

Create access rules in the Interfaces tab to route messages between transports. Rules support source/destination interface filtering, direction (ingress/egress/both), node/portnum/keyword/object group matching, SMS contact selection, failover groups, transform overrides, and rate limiting.

### Step 5: Verify end-to-end

Send a test message from your Meshtastic device. If access rules are configured, it should be delivered to the destination interface (e.g., appear in the RockBLOCK portal for Iridium, or arrive as an SMS for cellular).

## Configuration

All configuration is via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `MESHSAT_MODE` | `cubeos` | Set to `direct` for standalone USB access |
| `MESHSAT_PORT` | `6050` | HTTP port for dashboard and API |
| `MESHSAT_DB_PATH` | `/data/meshsat.db` | SQLite database file path |
| `MESHSAT_MESHTASTIC_PORT` | `auto` | Serial port for Meshtastic (`auto` = scan USB) |
| `MESHSAT_IRIDIUM_PORT` | `auto` | Serial port for Iridium (`auto` = scan USB) |
| `MESHSAT_CELLULAR_PORT` | `auto` | Serial port for cellular modem (`auto` = scan USB) |
| `MESHSAT_ZIGBEE_PORT` | `auto` | Serial port for ZigBee coordinator (`auto` = scan USB) |
| `MESHSAT_RETENTION_DAYS` | `30` | Days to keep historical data |
| `MESHSAT_PAID_RATE_LIMIT` | `60` | Minimum seconds between paid gateway sends |
| `MESHSAT_WEB_DIR` | *(empty)* | Override embedded SPA path (development only) |

MeshSat works fine with just a single device connected. Missing devices are logged as warnings.

## Deployment Modes

| | Standalone mode | CubeOS mode |
|---|---|---|
| Set via | `MESHSAT_MODE=direct` | `MESHSAT_MODE=cubeos` (default) |
| Serial access | Direct to /dev/ttyACM0, /dev/ttyUSB0 | Via HAL REST API |
| Deploy with | `docker-compose.direct.yml` | CubeOS orchestrator |
| Who it's for | Any Linux machine | CubeOS installations |

For CubeOS mode, see [CubeOS docs](https://cubeos.app).

## Architecture

```
USB Devices             MeshSat Container                        Clients
-----------      -----------------------------------------      ----------------
                 |                                         |
/dev/ttyACM0 -->-|  DirectMeshTransport                     |
  (Meshtastic)   |    Protobuf binary framing               |-->  Web Dashboard
                 |                                         |     (Vue 3 SPA, 11 views)
/dev/ttyUSB0 -->-|  DirectSatTransport (Iridium 9603N)      |
  (Iridium)      |    AT commands, SBDIX/SBDSX              |-->  REST API
                 |                                         |     (106+ endpoints)
/dev/ttyUSB1 -->-|  DirectCellTransport (SIM7600G-H)        |
  (Cellular)     |    AT commands, SMS, data                |-->  SSE Events
                 |                                         |     (real-time updates)
/dev/ttyUSB2 -->-|  DirectZigBeeTransport (CC2652P)         |
  (ZigBee)       |    Z-Stack ZNP binary protocol           |
                 |                                         |
                 |  Compression Pipeline                    |
                 |    SMAZ2 | llama-zip | MSVQ-SC           |
                 |                                         |
                 |  Reticulum Routing                       |
                 |    Ed25519 identity, announce, links      |
                 |                                         |
                 |         InterfaceManager                 |
                 |           (state machine, USB hotplug)   |
                 |              |                           |
                 |         AccessEvaluator                  |
                 |           (rules, object groups, rates)  |
                 |              |                           |
                 |         Dispatcher                       |
                 |           (delivery workers per iface)   |
                 |              |                           |
                 |      TransformPipeline                   |
                 |        (zstd, smaz2, aes-256-gcm, b64)   |
                 |              |                           |
                 |  +---------+---------+---------+------+  |
                 |  |Iridium  |MQTT     |Cell     |Wbook |  |
                 |  |Gateway  |Gateway  |Gateway  |GW    |  |
                 |  +---------+---------+---------+------+  |
                 |  |Astrocast|ZigBee   |Failover         |  |
                 |  |Gateway  |Gateway  |Resolver         |  |
                 |  +---------+---------+-----------------+  |
                 |                                         |
                 |  SigningService (Ed25519 hash chain)     |
                 |  Delivery Ledger (SQLite tracking)       |
                 |  SQLite DB (/data/meshsat.db, v25)       |
                 -----------------------------------------
```

## Troubleshooting

**No devices detected on startup** -- Check that USB devices are visible (`ls /dev/ttyACM* /dev/ttyUSB*`). Try a different cable or port.

**Meshtastic connects but shows 0 nodes** -- Config handshake takes 5-10 seconds. Wait for "config complete" log line.

**Iridium signal shows 0 bars** -- Check antenna connections. Requires clear sky view.

**ZigBee dongle detected as Meshtastic** -- SONOFF ZigBee dongle shares VID:PID with some Meshtastic devices. Pin the port with `MESHSAT_ZIGBEE_PORT`.

## Roadmap

**v0.1.x** -- Iridium SBD + Meshtastic bridge with configurable rules engine, MQTT gateway, pass-aware scheduler, dead letter queue with ISU-aware backoff, device management, SOS mode, and full dashboard.

**v0.2.0** -- Any-to-any routing fabric. Channel registry, unified rules engine, structured dispatcher, Astrocast and cellular integration, SMAZ2 compression, ZigBee gateway, InterfaceManager with USB hotplug, object groups, failover groups, transform pipelines, Ed25519 audit log, config export/import.

**v0.3.0 (current)** -- 3-tier compression (SMAZ2 lossless, llama-zip LLM lossless, MSVQ-SC lossy semantic with rate-adaptive codebook). Reticulum-inspired routing with Ed25519 identity, announce relay, link manager, keepalive, bandwidth tracking. Android companion app.

**Future** -- meshsat-android beta release, community templates, Grafana dashboard for transport metrics.

## Community

- GitHub: [github.com/cubeos-app/meshsat](https://github.com/cubeos-app/meshsat)
- Android: [github.com/cubeos-app/meshsat-android](https://github.com/cubeos-app/meshsat-android)
- Issues: Use GitHub Issues for bugs and feature requests

PRs welcome. See open issues for where help is needed.

## License

Copyright 2026 Nuclear Lighters Inc. Licensed under the [GNU General Public License v3.0](LICENSE).
