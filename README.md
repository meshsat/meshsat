# MeshSat

![Go 1.24+](https://img.shields.io/badge/go-1.24+-blue)
[![License: GPL v3](https://img.shields.io/badge/license-GPLv3-green)](LICENSE)
![Docker: ghcr.io/cubeos-app/meshsat](https://img.shields.io/badge/docker-ghcr.io%2Fcubeos--app%2Fmeshsat-blue)

MeshSat is a multi-transport mesh and satellite gateway that bridges Meshtastic LoRa networks to satellite, cellular, and tactical data channels. Eleven transport types -- Meshtastic LoRa, Iridium SBD (9603N), Iridium IMT (9704), Astrocast LEO, Cellular SMS, ZigBee, MQTT, Webhooks, APRS, TAK, and direct serial -- are all available as routing destinations. Access rules route messages between any pair of interfaces with per-rule filtering, failover groups, and transform pipelines.

MeshSat runs as a standalone Docker container on any Linux machine with USB-connected devices. No cloud dependencies, no subscriptions beyond your satellite or cellular plan.

For multi-tenant fleet management, see [MeshSat Hub](https://hub.meshsat.net).

## Dashboard

![MeshSat Dashboard](docs/images/meshsat_dashboard.png)
*Built-in web dashboard showing Iridium status, mesh nodes, SOS controls,
SBD message queue, and GPS/satellite positioning*

![MeshSat Pass Predictor](docs/images/meshsat_passes.png)
*Satellite pass predictor with signal correlation --
optimizes transmission timing in obstructed environments*

## Features

- **11 transports:** Meshtastic LoRa, Iridium SBD (9603N), Iridium IMT (9704, 100 KB messages), Astrocast LEO, Cellular SMS, ZigBee (Z-Stack ZNP), MQTT, Webhooks, APRS (Direwolf KISS), TAK (CoT XML), direct serial
- **3 compression tiers:** SMAZ2 (lossless, <1ms), llama-zip (LLM-based lossless, ~200ms), MSVQ-SC (lossy semantic, rate-adaptive)
- **Reticulum-inspired routing** with Ed25519 identity, announce relay, link manager, keepalive, bandwidth tracking, TCP/HDLC interface for RNS interop, and resource transfers with chunked reliable delivery
- **Transform pipelines** per interface: compress (zstd, SMAZ2) + encrypt (AES-256-GCM) + encode (base64)
- **Channel registry** with self-describing adapters and MTU awareness
- **Dispatcher** with failover groups, delivery ledger, per-channel workers, and visited-set loop prevention
- **Access rules engine** with object groups (node, portnum, sender, contact), rate limiting, and implicit deny
- **DeviceSupervisor** with USB hotplug detection, VID:PID identification cascade, and claim-based port management
- **Field intelligence:** Dead Man's Switch, geofence alerts, channel health scores, satellite burst queue, mesh topology visualization
- **Config export/import** in YAML format (Cisco `show running-config` style)
- **Web dashboard** (Vue.js SPA, 13 views) for monitoring, sending messages, radio configuration, mesh topology, and device management
- **REST API** with 280+ endpoints for integration
- **Ed25519 signing service** with hash-chain audit log for tamper detection
- **Auto-detects** USB devices on startup via VID:PID tables and protocol probing
- **Satellite pass prediction** using SGP4/TLE propagation with signal correlation
- **Android companion app** ([meshsat-android](https://github.com/cubeos-app/meshsat-android)) with BLE mesh, SPP Iridium, SMS, MSVQ-SC, and AES-GCM
- **Multi-tenant fleet management** via [MeshSat Hub](https://hub.meshsat.net) (separate product)
- Runs on ARM64 (Raspberry Pi 5/4, BananaPi) and x86_64

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

![MeshSat Compact Kit](docs/images/meshsat_compact_kit.jpg)
*MeshSat compact kit -- minimal two-transport gateway (mesh + satellite) in a pocket-sized waterproof case.*

| # | Component | Description |
|---|-----------|-------------|
| 1 | **XIAO ESP32-S3 + SX1262 LoRa Module** | Meshtastic mesh radio -- 868/915 MHz, WiFi + BLE, ultra-compact form factor |
| 2 | **RockBLOCK 9704** (Iridium IMT, SMA) | Iridium satellite modem -- JSPR protocol, 100 KB messages, FTDI USB |
| 3 | **Anker Prime 20,000mAh** (200W, 2x USB-C + USB-A) | Portable power bank -- powers all components via USB-C |
| 4 | **BananaPi BPI-M4 Zero** (4 GB RAM + 32 GB eMMC) | MeshSat Bridge host -- Allwinner H618, metal case, Pi Zero 2W alternative |

### Supported Devices

| Category | Device | Status | Notes |
|----------|--------|--------|-------|
| **Meshtastic** | Heltec LoRa V4 (ESP32-S3 + SX1262 + GPS) | Tested | 915 MHz, OLED, 2 MB PSRAM, 16 MB flash |
| | XIAO ESP32-S3 + SX1262 LoRa Module | Tested | 868/915 MHz, ultra-compact, WiFi + BLE |
| | Lilygo T-Echo (nRF52840) | Tested | 915 MHz, USB-C, e-ink display |
| | Lilygo T-Deck | Tested | ESP32-S3, keyboard, screen |
| | Espressif / CH340 / CP2102 / Nordic devices | Should work | Auto-detected via USB VID:PID |
| **Satellite** | RockBLOCK 9603 (Iridium 9603N) | Tested | SBD protocol, 340-byte MO, 19200 baud, UART or RS-232 |
| | RockBLOCK 9704 (Iridium IMT) | Tested | JSPR protocol, 100 KB messages, 230400 baud, FTDI USB |
| | Astrocast Astronode S | Code complete | ASCII hex frame protocol, fragmentation, pass prediction |
| **Cellular** | LILYGO T-Call A7670 (A7670E LTE) | Tested | 4G LTE / 2G GSM, AT commands, SMS + data |
| | SIM7600G-H (4G LTE) | Tested | USB modem, AT commands, SMS + data |
| | Huawei E220 (3G HSDPA) | Tested | USB modem, AT commands, SMS + data |
| **ZigBee** | SONOFF ZigBee 3.0 USB Dongle Plus (CC2652P) | Code complete | Z-Stack ZNP protocol, VID:PID auto-detect with ZNP probe |
| **Host** | Raspberry Pi 5 (8 GB) | Tested | ARM64, Debian Bookworm |
| | Raspberry Pi 4 (4 GB) | Tested | ARM64, Debian Bookworm |
| | BananaPi BPI-M4 Zero (4 GB + 32 GB eMMC) | Tested | Allwinner H618, ARM64, Ubuntu |
| | Any x86_64 / ARM64 Linux | Should work | Docker + USB serial required |

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

Navigate to `http://<your-ip>:6050`. The dashboard provides 13 views: Dashboard, Messages, Nodes, Map, Passes, Bridge, Interfaces, Radio Config, Topology, Settings, Audit, Help, and About.

### Step 4: Set up access rules

Create access rules in the Interfaces tab to route messages between transports. Rules support source/destination interface filtering, direction (ingress/egress/both), node/portnum/keyword/object group matching, SMS contact selection, failover groups, transform overrides, and rate limiting.

### Step 5: Verify end-to-end

Send a test message from your Meshtastic device. If access rules are configured, it should be delivered to the destination interface (e.g., appear in the RockBLOCK portal for Iridium, or arrive as an SMS for cellular).

## Configuration

All configuration is via environment variables. MeshSat works fine with just a single device connected -- missing devices are logged as warnings.

**Core:**

| Variable | Default | Description |
|----------|---------|-------------|
| `MESHSAT_MODE` | `cubeos` | Set to `direct` for standalone USB access |
| `MESHSAT_PORT` | `6050` | HTTP port for dashboard and API |
| `MESHSAT_DB_PATH` | `/data/meshsat.db` | SQLite database file path |
| `MESHSAT_RETENTION_DAYS` | `30` | Days to keep historical data |
| `MESHSAT_WEB_DIR` | *(empty)* | Override embedded SPA path (development only) |

**Serial ports** (`auto` = scan USB via VID:PID + protocol probing):

| Variable | Default | Description |
|----------|---------|-------------|
| `MESHSAT_MESHTASTIC_PORT` | `auto` | Meshtastic radio serial port |
| `MESHSAT_IRIDIUM_PORT` | `auto` | Iridium 9603N (SBD) serial port |
| `MESHSAT_IMT_PORT` | `auto` | RockBLOCK 9704 (IMT/JSPR) serial port |
| `MESHSAT_CELLULAR_PORT` | `auto` | Cellular modem serial port |
| `MESHSAT_ASTROCAST_PORT` | `auto` | Astrocast Astronode serial port |
| `MESHSAT_ZIGBEE_PORT` | `auto` | ZigBee coordinator serial port |

**Iridium 9603N:**

| Variable | Default | Description |
|----------|---------|-------------|
| `MESHSAT_IRIDIUM_SLEEP_PIN` | `0` | GPIO pin for 9603N sleep/wake (0 = disabled) |
| `IRIDIUM_SBDIX_TIMEOUT` | `90` | SBDIX AT command timeout in seconds |

**Rate limiting & routing:**

| Variable | Default | Description |
|----------|---------|-------------|
| `MESHSAT_PAID_RATE_LIMIT` | `60` | Minimum seconds between paid satellite sends |
| `MESHSAT_MAX_HOPS` | `8` | Maximum interfaces a message may traverse |
| `MESHSAT_MESH_WATCHDOG_MIN` | `10` | Minutes of silence before Meshtastic serial reconnect (0 = disabled) |

**Compression sidecars:**

| Variable | Default | Description |
|----------|---------|-------------|
| `MESHSAT_LLAMAZIP_ADDR` | *(empty)* | llama-zip gRPC sidecar address (empty = disabled) |
| `MESHSAT_LLAMAZIP_TIMEOUT` | `30` | llama-zip RPC timeout in seconds |
| `MESHSAT_MSVQSC_ADDR` | *(empty)* | MSVQ-SC gRPC sidecar address (empty = disabled) |
| `MESHSAT_MSVQSC_TIMEOUT` | `30` | MSVQ-SC RPC timeout in seconds |
| `MESHSAT_MSVQSC_CODEBOOK` | *(empty)* | Path to MSVQ-SC codebook file (enables pure-Go decode) |

**Reticulum TCP interface:**

| Variable | Default | Description |
|----------|---------|-------------|
| `MESHSAT_TCP_LISTEN` | *(empty)* | TCP listen address for RNS interop (e.g. `:4242`) |
| `MESHSAT_TCP_CONNECT` | *(empty)* | TCP remote RNS node address |
| `MESHSAT_ANNOUNCE_INTERVAL` | `300` | Routing announce broadcast interval in seconds |

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
USB / UART / TCP       MeshSat Container                              Clients
------------------     -----------------------------------------------  ----------------
                       |                                             |
/dev/ttyACM0 -------->-|  DirectMeshTransport (Meshtastic)            |
  (Meshtastic)         |    Protobuf binary framing                   |->  Web Dashboard
                       |                                             |    (Vue 3 SPA,
/dev/ttyUSB0 -------->-|  DirectSatTransport (Iridium 9603N)          |     13 views)
  (Iridium SBD)        |    AT commands, SBDIX/SBDSX, sleep/wake GPIO |
                       |                                             |->  REST API
Pi UART GPIO -------->-|  DirectIMTTransport (RockBLOCK 9704)         |    (280+ endpoints)
  (Iridium IMT)        |    JSPR protocol, 230400 baud, 100 KB msgs  |
                       |                                             |->  SSE Events
/dev/ttyUSB1 -------->-|  DirectCellTransport (A7670E / SIM7600G)     |    (real-time)
  (Cellular)           |    AT commands, SMS, data                    |
                       |                                             |
/dev/ttyUSB2 -------->-|  DirectAstrocastTransport (Astronode S)      |
  (Astrocast)          |    ASCII hex frames, CRC-16, fragmentation  |
                       |                                             |
/dev/ttyUSB3 -------->-|  DirectZigBeeTransport (CC2652P)             |
  (ZigBee)             |    Z-Stack ZNP binary protocol              |
                       |                                             |
                       |  DeviceSupervisor                            |
                       |    USB hotplug, VID:PID cascade, port claims |
                       |                                             |
                       |  Compression Pipeline                        |
                       |    SMAZ2 | llama-zip | MSVQ-SC              |
                       |                                             |
                       |  Reticulum Routing                           |
                       |    Ed25519 identity, announce relay, links   |
                       |    TCP/HDLC interface, path discovery        |
                       |                                             |
                       |         InterfaceManager                     |
                       |           (state machine, bind/unbind)       |
                       |              |                               |
                       |         AccessEvaluator                      |
                       |           (rules, object groups, rates)      |
                       |              |                               |
                       |         Dispatcher                           |
                       |           (delivery workers per iface)       |
                       |              |                               |
                       |      TransformPipeline                       |
                       |        (zstd, smaz2, aes-256-gcm, b64)       |
                       |              |                               |
                       |  +--------+--------+--------+------+------+  |
                       |  |Iridium |MQTT    |Cell    |Wbook |APRS  |  |
                       |  |Gateway |Gateway |Gateway |GW    |GW    |  |
                       |  +--------+--------+--------+------+------+  |
                       |  |Astro   |ZigBee  |TAK     |Failover     |  |
                       |  |Gateway |Gateway |Gateway |Resolver     |  |
                       |  +--------+--------+--------+-------------+  |
                       |                                             |
                       |  Field Intelligence                          |
                       |    Dead Man's Switch, Geofence Alerts,       |
                       |    Health Scores, Burst Queue, Topology      |
                       |                                             |
                       |  SigningService (Ed25519 hash chain)         |
                       |  Delivery Ledger (SQLite tracking)           |
                       |  SQLite DB (/data/meshsat.db, v30)           |
                       -----------------------------------------------
```

## Troubleshooting

**No devices detected on startup** -- Check that USB devices are visible (`ls /dev/ttyACM* /dev/ttyUSB*`). Try a different cable or port.

**Meshtastic connects but shows 0 nodes** -- Config handshake takes 5-10 seconds. Wait for "config complete" log line.

**Iridium signal shows 0 bars** -- Check antenna connections. Requires clear sky view.

**ZigBee dongle detected as Meshtastic** -- SONOFF ZigBee dongle shares VID:PID with some Meshtastic devices. Pin the port with `MESHSAT_ZIGBEE_PORT`.

## Roadmap

**v0.1.x** -- Iridium SBD + Meshtastic bridge with configurable rules engine, MQTT gateway, pass-aware scheduler, dead letter queue with ISU-aware backoff, device management, SOS mode, and full dashboard.

**v0.2.0** -- Any-to-any routing fabric. Channel registry, unified rules engine, structured dispatcher, Astrocast and cellular integration, SMAZ2 compression, ZigBee gateway, InterfaceManager with USB hotplug, object groups, failover groups, transform pipelines, Ed25519 audit log, config export/import.

**v0.3.0 (current)** -- 3-tier compression (SMAZ2 lossless, llama-zip LLM lossless, MSVQ-SC lossy semantic with rate-adaptive codebook). Reticulum-inspired routing with Ed25519 identity, announce relay, link manager, keepalive, bandwidth tracking, TCP/HDLC RNS interop. RockBLOCK 9704 IMT transport (100 KB messages). APRS and TAK gateways. DeviceSupervisor with USB hotplug. Field intelligence (dead man's switch, geofence, health scores, burst queue, topology). Android companion app.

**Future** -- meshsat-android beta release, community templates, Grafana dashboard for transport metrics.

## Related Projects

- **MeshSat Hub** -- Multi-tenant fleet management platform: [hub.meshsat.net](https://hub.meshsat.net)
- **MeshSat Android** -- Standalone mobile gateway app: [github.com/cubeos-app/meshsat-android](https://github.com/cubeos-app/meshsat-android)
- **CubeOS** -- Self-hosted OS for SBCs and edge devices: [cubeos.app](https://cubeos.app)

## Community

- GitHub: [github.com/cubeos-app/meshsat](https://github.com/cubeos-app/meshsat)
- Issues: Use GitHub Issues for bugs and feature requests

PRs welcome. See open issues for where help is needed.

## License

Copyright 2026 Nuclear Lighters Inc. Licensed under the [GNU General Public License v3.0](LICENSE).
