# MeshSat

A gateway that bridges Meshtastic mesh radios and Iridium SBD satellite modems. Send messages between off-grid mesh networks and global satellite coverage from a single box.

MeshSat runs as a standalone Docker container on any Linux machine with USB-connected devices. No cloud dependencies, no subscriptions beyond your Iridium SBD plan.

## What It Does

- Bridges Meshtastic mesh radio and Iridium satellite into a single message bus
- Routes messages between devices using configurable bridge rules (mesh-to-satellite, satellite-to-mesh, or both)
- Auto-detects USB devices on startup (no manual port configuration needed)
- Stores all messages, telemetry, GPS positions, and signal data in a local SQLite database
- Provides a built-in web dashboard for monitoring, sending messages, and managing devices
- Exposes a REST API for integration with other systems
- Runs on ARM64 (Raspberry Pi, Pine64) and x86_64 (Intel NUC, any PC)

## Tested Hardware

The following devices have been verified end-to-end (satellite to mesh and back):

| Device | Role | Interface | Notes |
|--------|------|-----------|-------|
| Lilygo T-Echo (nRF52840) | Meshtastic radio | `/dev/ttyACM0` | 915 MHz, USB-C |
| Iridium 9600 SBD Transceiver | Satellite modem | `/dev/ttyUSB0` | RS-232 via USB adapter, 19200 baud |
| Raspberry Pi 5 | Host | — | ARM64, 4 GB RAM, Debian Bookworm |

Other Meshtastic devices should work out of the box. MeshSat recognizes the standard Meshtastic USB vendor/product IDs (Espressif, CH340, CP2102, Nordic, Adafruit). If your device is not detected, you can pin the port manually with `MESHSAT_MESHTASTIC_PORT`.

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
docker compose -f docker-compose.standalone.yml up --build
```

## Setup Guide

### Step 1: Plug in your devices

Connect your Meshtastic radio and/or Iridium modem via USB. MeshSat will detect them automatically on startup. You can verify they appear:

```bash
ls /dev/ttyACM* /dev/ttyUSB*
```

Typical result: `/dev/ttyACM0` (Meshtastic) and `/dev/ttyUSB0` (Iridium). The exact names depend on your hardware and the order you plugged them in.

### Step 2: Start the container

Use one of the methods above (Docker one-liner or Docker Compose). MeshSat will:

1. Scan `/dev/ttyACM*` and `/dev/ttyUSB*` for known devices
2. Connect to each device it finds (Meshtastic handshake, Iridium AT probe)
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

- **Messages** — live message feed from all connected devices
- **Nodes** — mesh network nodes with signal quality and last-heard times
- **Map** — node positions on a Leaflet map (if GPS data is available)
- **Telemetry** — battery voltage, temperature, and other device metrics
- **Config** — radio settings, gateway configuration, and bridge rules

### Step 4: Set up bridge rules

To route messages between Meshtastic and Iridium, create bridge rules in the Config tab. A bridge rule specifies:

- **Source gateway** — where messages come from (e.g., `meshtastic`)
- **Destination gateway** — where messages go (e.g., `iridium`)
- **Direction** — outbound (mesh to satellite), inbound (satellite to mesh), or both
- **Filter** — optional: match specific channels, node IDs, or message types

Example: to forward all text messages from a specific Meshtastic node to Iridium SBD, create an outbound rule with the source node filter and destination set to your Iridium gateway.

### Step 5: Verify end-to-end

Send a test message from your Meshtastic device. If bridge rules are configured, it should appear in the RockBLOCK portal (or wherever your SBD messages are delivered). Send a message from the RockBLOCK portal back — it should arrive on your Meshtastic device.

## Configuration

All configuration is via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `MESHSAT_MODE` | `cubeos` | Set to `direct` for standalone USB access |
| `MESHSAT_PORT` | `6050` | HTTP port for dashboard and API |
| `MESHSAT_DB_PATH` | `/cubeos/data/meshsat.db` | SQLite database file path |
| `MESHSAT_MESHTASTIC_PORT` | `auto` | Serial port for Meshtastic (`auto` = scan USB) |
| `MESHSAT_IRIDIUM_PORT` | `auto` | Serial port for Iridium (`auto` = scan USB) |
| `MESHSAT_RETENTION_DAYS` | `30` | Days to keep historical data |

### Running with only one device

MeshSat works fine with just a Meshtastic radio (no Iridium) or just an Iridium modem (no Meshtastic). It will log a warning for the missing device and continue operating with whatever is connected.

### Pinning device ports

If auto-detection picks the wrong port (e.g., you have multiple USB-serial adapters), set the port explicitly:

```bash
-e MESHSAT_MESHTASTIC_PORT=/dev/ttyACM0
-e MESHSAT_IRIDIUM_PORT=/dev/ttyUSB1
```

### Why `--privileged`?

MeshSat needs raw access to USB serial devices (`/dev/ttyACM*`, `/dev/ttyUSB*`) and uses `stty` to configure baud rate and line discipline. The `--privileged` flag grants the necessary device permissions. The `/dev` and `/sys` bind mounts allow device enumeration and sysfs VID:PID lookups for auto-detection.

## API

MeshSat exposes a REST API on the same port as the dashboard:

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Health check |
| GET | `/api/messages` | Paginated message history |
| GET | `/api/messages/stats` | Message counts by transport and type |
| POST | `/api/messages/send` | Send a text message to the mesh |
| GET | `/api/telemetry` | Time-series device telemetry |
| GET | `/api/positions` | GPS position history |
| GET | `/api/nodes` | Mesh nodes with signal quality |
| GET | `/api/status` | Connection status for all transports |
| GET | `/api/events` | Server-Sent Events stream |
| GET | `/api/gateways` | Gateway status and configuration |
| GET | `/api/iridium/signal` | Current Iridium signal strength |
| GET | `/api/iridium/scheduler` | Pass scheduler status |
| POST | `/api/admin/reboot` | Reboot a remote mesh node |
| POST | `/api/admin/traceroute` | Traceroute to a mesh node |
| POST | `/api/config/radio` | Update radio configuration |
| POST | `/api/config/module` | Update module configuration |

Full API details are available at `http://<your-ip>:6050/api/` when MeshSat is running.

## CubeOS Integration

MeshSat also runs as a managed service inside [CubeOS](https://cubeos.app), where it uses HAL (Hardware Abstraction Layer) for device access instead of direct serial. Set `MESHSAT_MODE=cubeos` (the default) and provide `HAL_URL` to use this mode. This is handled automatically when deployed via CubeOS.

## Architecture

```
USB Devices          MeshSat Container              Clients
-----------     ---------------------------     ----------------
                │                           │
/dev/ttyACM0 ──►│  DirectMeshTransport      │
  (Meshtastic)  │    ├── Serial framing     │──► Web Dashboard
                │    ├── Protobuf codec     │    (port 6050)
                │    └── Config handshake   │
                │              │            │──► REST API
                │         Processor +       │    (port 6050)
                │         Rule Engine       │
                │              │            │──► SSE Events
/dev/ttyUSB0 ──►│  DirectSatTransport       │    (port 6050)
  (Iridium)     │    ├── AT commands        │
                │    ├── SBD binary codec   │
                │    └── Signal polling     │
                │                           │
                │  SQLite (/data/meshsat.db)│
                ---------------------------
```

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

Check antenna connections. The Iridium 9600 requires a clear view of the sky and a properly connected antenna. If using an external antenna with a u.FL pigtail, verify the connector is seated firmly.

**SBDIX failures or timeouts**

Iridium SBD sessions (SBDIX) take 10-60 seconds and require signal strength of at least 2 bars. MeshSat rate-limits SBDIX to one session per 10 seconds. If messages are queuing in the dead letter queue, check signal strength and antenna placement.

## License

Copyright 2026 Nuclear Lighters Inc. Licensed under the [Apache License 2.0](LICENSE).
