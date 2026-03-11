# MeshSat

[![Pipeline](https://gitlab.nuclearlighters.net/products/cubeos/meshsat/badges/main/pipeline.svg)](https://gitlab.nuclearlighters.net/products/cubeos/meshsat/-/pipelines)
![Go 1.24+](https://img.shields.io/badge/go-1.24+-blue)
[![License: GPL v3](https://img.shields.io/badge/license-GPLv3-green)](LICENSE)
![Docker: ghcr.io/cubeos-app/meshsat](https://img.shields.io/badge/docker-ghcr.io%2Fcubeos--app%2Fmeshsat-blue)

MeshSat bridges Meshtastic mesh networks to satellite, cellular, and data channels from a single gateway. Iridium SBD, Astrocast LEO, cellular SMS, MQTT, ZigBee, and webhooks are all available as routing destinations. The access rules engine routes messages between any interface with per-rule filtering, failover groups, and transform pipelines.

MeshSat runs as a standalone Docker container on any Linux machine with USB-connected devices. No cloud dependencies, no subscriptions beyond your satellite or cellular plan.

## Dashboard

![MeshSat Dashboard](docs/images/meshsat_dashboard.png)
*Built-in web dashboard showing Iridium status, mesh nodes, SOS controls,
SBD message queue, and GPS/satellite positioning*

![MeshSat Pass Predictor](docs/images/meshsat_passes.png)
*Satellite pass predictor with signal correlation --
optimizes transmission timing in obstructed environments*

## What It Does

- Bridges Meshtastic mesh radio to satellite, cellular, and data channels via configurable access rules
- Routes messages between any pair of interfaces using a unified rules engine with object groups and failover
- Auto-detects USB devices on startup via VID:PID tables and protocol probing (no manual port configuration)
- Stores all messages, telemetry, GPS positions, and signal data in a local SQLite database
- Provides a built-in web dashboard for monitoring, sending messages, and managing devices
- Predicts satellite passes using SGP4/TLE propagation and schedules transmissions around optimal windows
- Manages a delivery queue with per-interface retry, backoff, and failover (ISU-aware for Iridium)
- Applies per-interface transform pipelines: zstd/SMAZ2 compression, AES-256-GCM encryption, base64
- Cryptographically signs audit log entries with Ed25519 hash chains for tamper detection
- Supports config export/import in YAML format (Cisco `show running-config` style)
- Exposes a REST API with 106 endpoints for integration with other systems
- Runs on ARM64 (Raspberry Pi, BPI-M4 Zero) and x86_64 (Intel NUC, any PC)

## Deployment Modes

| | Standalone mode | CubeOS mode |
|---|---|---|
| Set via | `MESHSAT_MODE=direct` | `MESHSAT_MODE=cubeos` (default) |
| Serial access | Direct to /dev/ttyACM0, /dev/ttyUSB0 | Via HAL REST API |
| Deploy with | `docker-compose.direct.yml` | CubeOS orchestrator |
| Who it's for | Any Linux machine | CubeOS installations |

This README covers standalone mode. For CubeOS mode, see [CubeOS docs](https://cubeos.app).

## Hardware

![MeshSat Lab - Host and Meshtastic radio](docs/images/meshsat_lab_01.jpg)
*Lilygo T-Echo (Meshtastic) connected to the MeshSat host with USB GPS dongle*

![MeshSat Lab - RockBLOCK 9603 satellite modem](docs/images/meshsat_lab_02.jpg)
*RockBLOCK 9603 Iridium modem with patch antenna -- needs sky view for satellite access*

### Supported Devices

| Category | Device | Status | Notes |
|----------|--------|--------|-------|
| Meshtastic | Lilygo T-Echo (nRF52840) | Tested | 915 MHz, USB-C, end-to-end verified |
| Meshtastic | Lilygo T-Deck | Tested | ESP32-S3, keyboard, screen |
| Meshtastic | Espressif / CH340 / CP2102 / Nordic devices | Should work | Auto-detected via USB VID:PID |
| Satellite | RockBLOCK 9603 (Iridium 9603N) | Tested | RS-232 via USB adapter, 19200 baud |
| Satellite | Astrocast Astronode S | Code complete | Awaiting hardware for integration testing |
| Cellular | Huawei E220 (3G HSDPA) | Tested | USB modem, AT commands, SMS + data |
| ZigBee | SONOFF ZigBee 3.0 USB Dongle Plus (CC2652P) | Code complete | Z-Stack ZNP protocol, VID:PID auto-detect with ZNP probe |
| Host | Raspberry Pi 5 | Tested | ARM64, 4 GB RAM, Debian Bookworm |
| Host | Raspberry Pi 4 | Should work | Same platform as Pi 5 |
| Host | BPI-M4 Zero | Planned | Armbian base, pending hardware verification |
| Host | Any x86_64 / ARM64 Linux | Should work | Docker + USB serial required |

## Quick Start

### Option A: One-liner with Docker

Pull the pre-built multi-arch image from GHCR and run it:

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

Save the following as `docker-compose.yml`:

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
      # Auto-detects USB devices by default.
      # To pin specific ports, uncomment and set:
      # - MESHSAT_MESHTASTIC_PORT=/dev/ttyACM0
      # - MESHSAT_IRIDIUM_PORT=/dev/ttyUSB0
      # - MESHSAT_CELLULAR_PORT=/dev/ttyUSB1
      # - MESHSAT_ZIGBEE_PORT=/dev/ttyUSB2
    volumes:
      - meshsat-data:/data
      - /dev:/dev
      - /sys:/sys:ro

volumes:
  meshsat-data:
```

Then run:

```bash
docker compose up -d
```

The dashboard will be available at `http://<your-ip>:6050`.

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

Connect your Meshtastic radio and/or satellite modem and/or cellular modem via USB. MeshSat will detect them automatically on startup using USB VID:PID tables and protocol probing (pure Go serial via `go.bug.st/serial`). You can verify they appear:

```bash
ls /dev/ttyACM* /dev/ttyUSB*
```

Typical result: `/dev/ttyACM0` (Meshtastic) and `/dev/ttyUSB0` (Iridium). The exact names depend on your hardware and the order you plugged them in.

### Step 2: Start the container

Use one of the methods above (Docker one-liner or Docker Compose). MeshSat will:

1. Scan `/dev/ttyACM*` and `/dev/ttyUSB*` for known devices via VID:PID + protocol probing
2. Connect to each device it finds (Meshtastic protobuf handshake, Iridium AT probe, ZNP probe for ZigBee)
3. Start the web dashboard and API on port 6050

Watch the startup logs to confirm detection:

```bash
docker logs meshsat
```

You should see lines like:

```
INF using direct serial transport mode=direct
INF meshtastic: connected to /dev/ttyACM0 nodes=12 myNode=0xABCD1234
INF iridium: connected imei=300434067943980 model=IRIDIUM 9600 Family SBD Transceiver
INF server started port=6050
```

### Step 3: Open the dashboard

Navigate to `http://<your-ip>:6050` in any browser. The dashboard shows:

- **Dashboard** -- live status of all interfaces, signal strength, connection state
- **Messages** -- live message feed from all connected devices
- **Nodes** -- mesh network nodes with signal quality and last-heard times
- **Map** -- node positions on a Leaflet map (if GPS data is available)
- **Passes** -- satellite pass predictions with signal correlation
- **Interfaces** -- access rules, object groups, failover groups, transform pipelines
- **Settings** -- radio config, gateway settings, channels, export/import

### Step 4: Set up access rules

To route messages between interfaces, create access rules in the Interfaces tab. Rules support per-interface ingress/egress evaluation with:

- **Source/destination interface** -- which interfaces the rule applies to
- **Direction** -- ingress, egress, or both
- **Filters** -- match by node ID (with names from mesh), portnum (with labels), keyword, or object groups
- **SMS contacts** -- per-rule phone number selection for cellular destinations
- **Forward options** -- TTL, failover group, transform overrides
- **Rate limiting** -- per-rule rate limits

Example: to forward text messages from a specific Meshtastic node to Iridium SBD, create an egress rule on the Iridium interface with the source node filter set to that node.

### Step 5: Verify end-to-end

Send a test message from your Meshtastic device. If access rules are configured, it should appear in the RockBLOCK portal (or wherever your SBD messages are delivered). Send a message from the RockBLOCK portal back -- it should arrive on your Meshtastic device.

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
| `HAL_URL` | `http://10.42.24.1:6005` | HAL endpoint (CubeOS mode only) |

### Running with only one device

MeshSat works fine with just a Meshtastic radio (no satellite/cellular) or any single device. It will log a warning for missing devices and continue operating with whatever is connected.

### Pinning device ports

If auto-detection picks the wrong port (e.g., you have multiple USB-serial adapters), set the port explicitly:

```bash
-e MESHSAT_MESHTASTIC_PORT=/dev/ttyACM0
-e MESHSAT_IRIDIUM_PORT=/dev/ttyUSB1
-e MESHSAT_CELLULAR_PORT=/dev/ttyUSB2
-e MESHSAT_ZIGBEE_PORT=/dev/ttyUSB3
```

### Why `--privileged`?

MeshSat needs raw access to USB serial devices (`/dev/ttyACM*`, `/dev/ttyUSB*`) and uses sysfs for VID:PID lookups during auto-detection. Serial configuration is handled in-process via pure Go (`go.bug.st/serial`). The `/dev` and `/sys` bind mounts allow device enumeration and USB device identification.

## API

MeshSat exposes a REST API on the same port as the dashboard. The major endpoint groups are listed below.

| Group | Endpoints | Description |
|-------|-----------|-------------|
| Health | `GET /health` | Health check |
| Messages | `GET/POST/DELETE /api/messages`, `GET /api/messages/stats` | Message history, send, purge |
| Telemetry | `GET /api/telemetry`, `GET /api/positions` | Time-series device telemetry, GPS positions |
| Nodes | `GET /api/nodes`, `DELETE /api/nodes/{num}` | Mesh nodes with signal quality |
| Events | `GET /api/events` | Server-Sent Events stream |
| Gateways | `GET/PUT/DELETE /api/gateways/{type}`, `POST .../start\|stop\|test` | Gateway config and lifecycle |
| Iridium | `/api/iridium/signal\|passes\|scheduler\|mailbox\|credits\|queue\|geolocation\|locations` | Iridium SBD management |
| Astrocast | `/api/astrocast/passes` | Astrocast LEO pass predictions |
| Cellular | `/api/cellular/signal\|status\|info\|sms\|contacts\|sim-cards\|broadcasts\|data\|dyndns\|pin` | Cellular modem management |
| ZigBee | `/api/zigbee/devices\|status` | ZigBee coordinator and devices |
| Webhooks | `POST /api/webhooks/inbound`, `GET /api/webhooks/log` | Inbound webhooks |
| Interfaces | `GET/POST/PUT/DELETE /api/interfaces`, `POST .../bind\|unbind`, `GET /api/devices` | Interface management and USB device scan |
| Access Rules | `GET/POST/PUT/DELETE /api/access-rules` | Access rules with filters and forward options |
| Object Groups | `GET/POST/PUT/DELETE /api/object-groups` | Node groups, portnum groups, contact groups |
| Failover | `GET/POST/DELETE /api/failover-groups` | Failover group management |
| Deliveries | `GET /api/deliveries`, `GET .../stats`, `POST .../{id}/cancel\|retry` | Delivery ledger tracking |
| Config | `GET /api/config/export`, `POST /api/config/import` | YAML config export/import |
| Audit | `GET /api/audit`, `GET /api/audit/verify\|signer` | Signed audit log with tamper detection |
| Crypto | `POST /api/crypto/generate-key\|validate-transforms` | Encryption key management |
| Admin | `POST /api/admin/reboot\|factory_reset\|traceroute` | Remote mesh node administration |
| Radio | `POST /api/config/radio\|module`, `POST /api/channels` | Radio and module configuration |
| Position | `POST /api/position/send\|fixed`, `DELETE /api/position/fixed` | Position sharing |
| Presets | `GET/POST/PUT/DELETE /api/presets`, `POST /api/presets/{id}/send` | Preset message management |
| SOS | `POST /api/sos/activate\|cancel`, `GET /api/sos/status` | Emergency SOS mode |
| Misc | `/api/neighbors`, `/api/store-forward/request`, `/api/range-test`, `/api/canned-messages`, `/api/waypoints`, `/api/transport/channels` | Neighbor info, S&F, range test, waypoints |

Total: 106 endpoints.

## CubeOS Integration

MeshSat also runs as a managed service inside [CubeOS](https://cubeos.app), where it uses HAL (Hardware Abstraction Layer) for device access instead of direct serial. Set `MESHSAT_MODE=cubeos` (the default) and provide `HAL_URL` to use this mode. This is handled automatically when deployed via CubeOS.

## Architecture

```
USB Devices             MeshSat Container                        Clients
-----------      -----------------------------------------      ----------------
                 |                                         |
/dev/ttyACM0 -->-|  DirectMeshTransport                     |
  (Meshtastic)   |    Protobuf binary framing               |-->  Web Dashboard
                 |                                         |     (Vue 3 SPA, port 6050)
/dev/ttyUSB0 -->-|  DirectSatTransport (Iridium 9603N)      |
  (Iridium)      |    AT commands, SBDIX/SBDSX              |-->  REST API
                 |                                         |     (106 endpoints)
/dev/ttyUSB1 -->-|  DirectCellTransport (Huawei E220)       |
  (Cellular)     |    AT commands, SMS, data                |-->  SSE Events
                 |                                         |     (real-time updates)
/dev/ttyUSB2 -->-|  DirectZigBeeTransport (CC2652P)         |
  (ZigBee)       |    Z-Stack ZNP binary protocol           |
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
                 |  SQLite DB (/data/meshsat.db, v21)       |
                 -----------------------------------------
```

Each gateway implements a common interface and is managed by the InterfaceManager. The InterfaceManager maintains a state machine per interface (unbound -> offline -> binding -> online/error) with USB hotplug scanning. The AccessEvaluator evaluates per-interface ingress/egress rules with object groups, rate limiting, and implicit deny. The Dispatcher routes messages through delivery workers with per-interface transform pipelines and failover resolution.

## Dynamic DNS

MeshSat includes a built-in DynDNS updater for keeping a hostname pointed at the device's cellular IP. Configured in Settings > Cellular > Dynamic DNS.

Supported providers: **DuckDNS**, **No-IP**, **Dynu**, **Cloudflare**, **Custom URL**.

Cloudflare requires a Zone ID (from domain overview) and an API Token with DNS edit permissions. The DNS Record ID is auto-resolved on first update.

## Troubleshooting

**No devices detected on startup**

Check that your USB devices are visible to the host:
```bash
ls -la /dev/ttyACM* /dev/ttyUSB*
```
If nothing shows up, the USB cable or adapter may be faulty. Try a different port or cable.

**Meshtastic connects but shows 0 nodes**

The config handshake takes 5-10 seconds. Wait for the "config complete" log line. If nodes still don't appear, verify the radio is joined to a mesh network (configure via the Meshtastic app first).

**Iridium signal shows 0 bars**

Check antenna connections. The RockBLOCK 9603 requires a clear view of the sky and a properly connected antenna. If using an external antenna with a u.FL pigtail, verify the connector is seated firmly.

**SBDIX failures or timeouts**

Iridium SBD sessions (SBDIX) take 10-60 seconds and require signal strength of at least 2 bars. MeshSat rate-limits SBDIX to one session per 10 seconds. If messages are queuing in the dead letter queue, check signal strength and antenna placement. The DLQ uses ISU-aware backoff: mo_status 32/36 triggers a 3-minute minimum wait per Iridium spec.

**ZigBee dongle detected as Meshtastic**

The SONOFF ZigBee dongle (CP2102/CP210x) shares VID:PID with some Meshtastic devices. MeshSat uses ZNP protocol probing to distinguish them. If misdetected, pin the port explicitly with `MESHSAT_ZIGBEE_PORT`.

## Roadmap

**v0.1.x** -- Iridium SBD + Meshtastic bridge with configurable rules engine, MQTT gateway, pass-aware scheduler with SGP4/TLE prediction, dead letter queue with ISU-aware backoff, device management, SOS mode, and full Vue.js SPA dashboard with REST API.

**v0.2.0** -- Any-to-any routing fabric. Channel registry with self-describing adapters, unified rules engine supporting directional routes between 6 channel types, structured dispatcher with per-channel delivery workers, Astrocast and cellular gateway integration, SMAZ2 compression.

**v0.3.0 (current)** -- Interface-based architecture with InterfaceManager state machine, USB hotplug, AccessEvaluator with object groups (node, portnum, sender, contact), per-rule SMS contact selection, failover groups, transform pipelines (zstd, SMAZ2, AES-256-GCM, base64), Ed25519 signing service with hash-chain audit log, YAML config export/import, ZigBee gateway (SONOFF CC2652P / Z-Stack ZNP), and per-interface delivery workers with hold/unhold on offline/online transitions.

**Future** -- Semantic compression using rate-adaptive multi-stage vector quantization (MSVQ-SC) for satellite payload efficiency. Reticulum-inspired routing with cryptographic announce broadcasting and path discovery.

## Community

- GitHub: [github.com/cubeos-app/meshsat](https://github.com/cubeos-app/meshsat)
- Issues: Use GitHub Issues for bugs and feature requests
- Discord: Coming soon

PRs welcome. See open issues for where help is needed.

## License

Copyright 2026 Nuclear Lighters Inc. Licensed under the [GNU General Public License v3.0](LICENSE).
