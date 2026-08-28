# MeshSat

[![Stars](https://img.shields.io/github/stars/cubeos-app/meshsat?style=flat&logo=github&color=blue)](https://github.com/cubeos-app/meshsat)
[![Release](https://img.shields.io/github/v/release/cubeos-app/meshsat?color=blue)](https://github.com/cubeos-app/meshsat/releases)
[![License: GPL v3](https://img.shields.io/badge/license-GPLv3-green)](LICENSE)
![Go 1.24+](https://img.shields.io/badge/go-1.24+-blue)
![Tests](https://img.shields.io/badge/tests-1333-green)
[![Docker](https://img.shields.io/badge/docker-ghcr.io%2Fcubeos--app%2Fmeshsat-blue)](https://github.com/orgs/cubeos-app/packages/container/package/meshsat)

### Keep a message moving when the network is gone.

MeshSat is an open source gateway that gives an off-grid mesh a way out. It takes a message off a
Meshtastic LoRa network and finds a route to the outside world over whatever bearer is still alive:
Iridium satellite, cellular SMS, amateur packet radio, ZigBee, Bluetooth or a plain TCP link. A
Reticulum routing layer sits underneath and picks the path by cost, so traffic stays on the free
bearers and only reaches for the metered ones when nothing free is up.

It runs as a single Docker container on a Raspberry Pi. No cloud service, no account, no subscription
beyond whatever you already pay your satellite or cellular carrier.

<!-- DEMO GIF SLOT: a 15 second silent loop showing a message entering the mesh on one kit and
     arriving on the far side, goes here, directly under the intro and above Quick Start. -->

> **Status: pre-release.** This is a working prototype under active development, not a finished
> product. It has never been deployed to a real user and has never been used in an actual emergency.
> See [What is proven, and what is not](#what-is-proven-and-what-is-not) for an honest breakdown
> before you rely on it for anything.

## Quick start

Save this as `docker-compose.yml`:

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

Plug in a Meshtastic radio, a satellite modem, a cellular modem, or all three. MeshSat scans USB on
startup, identifies each device by VID:PID and protocol probe, and brings up whatever it finds.
Open `http://<your-ip>:6050` for the dashboard. A single device is enough to start; missing hardware
is logged as a warning, not an error.

The image is published to GHCR and is pullable anonymously. To
[build from source](#other-ways-to-install) instead, see below.

![MeshSat Dashboard](docs/images/meshsat_dashboard.png)
*The built-in dashboard: satellite modem status, mesh nodes, delivery queue, positioning and
per-channel health scores.*

If MeshSat is useful to you, a star helps other people find it.

## Why this exists

When a storm, an outage or a disaster takes the network down, the radios people already own mostly
cannot help. A Meshtastic mesh keeps a village talking to itself, but it has no way out of the
valley. A satellite modem can reach the far side of the planet, but it costs real money per message
and nobody carries one. A phone is useless without a tower.

MeshSat is the piece in between. It treats every radio you have as one possible route, and moves the
message along whichever one is up. The reason the routing layer exists at all is cost: satellite
bytes are expensive, so the design goal is to spend them only when there is nothing free left.

## What it bridges

Eight transport bearers, reachable across nine wired Reticulum interfaces.

| Bearer | Hardware | Notes |
|---|---|---|
| **Meshtastic LoRa** | ESP32 / nRF52 radios | Full protocol via the official `buf.build` protobuf bindings |
| **Iridium SBD** | RockBLOCK 9603N | 340 byte messages, AT commands, pass-aware scheduling |
| **Iridium IMT** | RockBLOCK 9704 | 100 KB messages, JSPR protocol |
| **Cellular SMS** | A7670E, SIM7600G-H, and similar | AT commands, SMS and data |
| **APRS / AX.25** | Any KISS TNC, Direwolf bundled | Direwolf runs supervised in-container on loopback |
| **ZigBee** | CC2652P | Z-Stack ZNP binary protocol |
| **BLE** | Host Bluetooth | GATT peripheral via BlueZ, with segmentation |
| **TCP** | Any IP link | Interoperates with upstream Python RNS |

Plus **TAK** (CoT XML), **MQTT** and **webhooks** as routing destinations, and multi-instance
gateways so two modems of the same type can run side by side with independent config and workers.

## What is proven, and what is not

Most projects skip this section. If you are deciding whether to trust this with anything that
matters, it is probably the most useful thing on the page.

| | State |
|---|---|
| Meshtastic serial, full protocol | Working on hardware, both kits |
| Iridium 9704 IMT, mobile-originated and mobile-terminated | Verified over a real satellite link, March 2026 |
| Iridium 9603 SBD | Working on hardware |
| Cellular SMS both directions | Working on hardware |
| APRS / AX.25 via bundled Direwolf | Working on hardware |
| Reticulum interoperability | Passes against upstream Python RNS 1.1.4 |
| HeMB bonding across LoRa, TCP and SMS | Three-bearer field test, April 2026, zero failures |
| HeMB over a paid satellite bearer | **Not validated.** Outstanding work |
| HeMB mixed free and paid allocation | **Undefined.** See the allocator note below |
| RTL-SDR jamming detection | Implemented and tested against ambient noise only, **never against a real jammer** |
| ZigBee gateway | Code complete, light field exposure |
| Deployment to a real end user | **Never.** No emergency service has used this |
| Use in an actual disaster | **Never** |

The test suite is 1,333 test functions across 129 files, and it gates every deploy. That says the
code does what the authors expect. It does not say the radio link will hold at 3am in the rain.

**On HeMB specifically:** the current symbol allocator is free-first. While any free bearer is
healthy, all source symbols go to the largest-MTU free bearer and paid bearers receive none. There
is no capacity model yet. Capacity-aware paid activation is specification work, not a shipped
feature, and the 1:900 latency ratio quoted in the design is a target, not a measured result.

## Hardware

![MeshSat Field Kit](docs/images/meshsat_field_kit.jpg)
*Field kit: a self-contained multi-transport gateway in a waterproof case. Meshtastic and cellular
over USB, the RockBLOCK 9603 wired to the Pi 5 GPIO UART. Everything is auto-detected on startup.*

| # | Component | Role |
|---|---|---|
| 1 | **Heltec LoRa V4** (ESP32-S3 + SX1262 + GPS) | Meshtastic mesh radio, 868/915 MHz |
| 2 | **RockBLOCK 9603** (Iridium 9603N) | Satellite, SBD, 340 byte messages, UART |
| 3 | **LILYGO T-Call A7670** (ESP32 + A7670E) | 4G LTE / 2G, AT commands, SMS and data |
| 4 | **INIU 25000mAh** (100W USB-C PD) | Power for the whole kit |
| 5 | **Raspberry Pi 5** (8 GB) | Host, standalone mode |

![MeshSat Compact Kit](docs/images/meshsat_compact_kit.jpg)
*Compact kit: mesh plus satellite only, in a pocket-sized waterproof case.*

| # | Component | Role |
|---|---|---|
| 1 | **XIAO ESP32-S3 + SX1262** | Meshtastic mesh radio, very small |
| 2 | **RockBLOCK 9704** (Iridium IMT) | Satellite, JSPR, 100 KB messages, USB |
| 3 | **Anker Prime 20,000mAh** (200W) | Power |
| 4 | **Raspberry Pi 5** (8 GB) | Host, standalone mode |

### Supported devices

| Category | Device | Status | Notes |
|---|---|---|---|
| **Meshtastic** | Heltec LoRa V4 (ESP32-S3 + SX1262 + GPS) | Tested | 868/915 MHz, OLED, 2 MB PSRAM |
| | XIAO ESP32-S3 + SX1262 | Tested | 868/915 MHz, WiFi + BLE, very compact |
| | Lilygo T-Echo (nRF52840) | Tested | 915 MHz, USB-C, e-ink |
| | Lilygo T-Deck | Tested | ESP32-S3, keyboard, screen |
| | Espressif / CH340 / CP2102 / Nordic | Should work | Auto-detected by USB VID:PID |
| **Satellite** | RockBLOCK 9603 (Iridium 9603N) | Tested | SBD, 340 byte MO, 19200 baud, UART or RS-232 |
| | RockBLOCK 9704 (Iridium IMT) | Tested | JSPR, 100 KB messages, 230400 baud, USB |
| **Cellular** | LILYGO T-Call A7670 (A7670E) | Tested | 4G LTE / 2G, SMS and data |
| | SIM7600G-H | Tested | USB modem, AT commands |
| | Huawei E220 (3G) | Tested | USB modem, AT commands |
| **ZigBee** | SONOFF ZigBee 3.0 Dongle Plus (CC2652P) | Code complete | Z-Stack ZNP, VID:PID plus ZNP probe |
| **Host** | Raspberry Pi 5 (8 GB) | Tested | ARM64 |
| | Raspberry Pi 4 (4 GB) | Tested | ARM64 |
| | BananaPi BPI-M4 Zero | Not recommended | Allwinner H618 USB proved unreliable |
| | Any x86_64 / ARM64 Linux | Should work | Docker plus USB serial |

## Features

### Routing

- **Reticulum-compatible routing** with Ed25519 identity, announce relay, link manager, keepalive,
  bandwidth tracking and chunked reliable resource transfer
- **Nine wired Reticulum interfaces:** LoRa mesh, TCP/HDLC (RNS interop), Iridium SBD, Iridium IMT,
  AX.25/APRS, MQTT, SMS, ZigBee, BLE
- **TransportNode** with cost-aware cross-interface forwarding, PathFinder route discovery and a
  30 minute route TTL
- **Dispatcher** with failover groups, a delivery ledger, per-channel workers and visited-set loop
  prevention
- **HeMB (Heterogeneous Media Bonding):** RLNC-coded bonding across heterogeneous bearer adapters,
  reassembling from any K of N coded symbols regardless of which bearer delivered them. Bearers do
  not need a shared IP stack. See the allocator caveat above.

### Compression and transforms

- **Three compression tiers:** SMAZ2 (lossless, sub-millisecond), llama-zip (LLM-based lossless,
  around 200 ms), MSVQ-SC (lossy semantic, rate-adaptive)
- **Per-interface transform pipelines:** compress (zstd, SMAZ2), encrypt (AES-256-GCM), encode
  (base64)

### Security

- **AES-256-GCM per channel**, with key exchange by QR bundle (`meshsat://key/` URI scheme)
- **v2 key bundles** carrying an Ed25519 signing public key for Trust On First Use. Android pins the
  bridge key on first scan; later scans are verified cryptographically
- **Master key envelope encryption** (HKDF plus AES-256-GCM wrapping), device-derived or passphrase
- **Ed25519 signing service** with a hash-chain audit log for tamper detection
- **Credential management:** ZIP/PEM upload, encrypted storage, expiry monitoring

### Field intelligence

- Dead Man's Switch, geofence alerts, per-channel health scores, satellite burst queue, mesh
  topology visualisation
- **Access rules engine** with object groups (node, portnum, sender, contact), rate limiting and
  implicit deny
- **DeviceSupervisor** with USB hotplug, a VID:PID identification cascade and claim-based port
  management
- **Satellite pass prediction** by SGP4/TLE propagation with signal correlation
- **RTL-SDR spectrum monitoring** across five bands with a multi-feature jamming classifier, feeding
  automatic transport failover
- **Config export and import** in YAML, in the style of `show running-config`, with diff preview

### Dashboard and API

- **Vue 3 dashboard**, 13 views, embedded in the binary
- **REST API**, over 300 endpoints, fully annotated for Swagger
- **Server-sent events** for live updates
- **Prometheus metrics** at `/metrics`

## Other ways to install

### From source

```bash
git clone https://github.com/cubeos-app/meshsat.git
cd meshsat
make build-with-web    # Vue SPA plus Go binary
```

## Setup guide

**1. Plug in your devices.** Meshtastic radio, satellite modem, cellular modem, in any combination.
Detection is automatic at startup by USB VID:PID table and protocol probe, using pure Go serial
(`go.bug.st/serial`), so there is no CGO anywhere in the build.

**2. Start the container.** MeshSat scans USB, probes each device (Meshtastic protobuf, Iridium AT,
ZNP for ZigBee), and starts the dashboard on port 6050.

**3. Open the dashboard** at `http://<your-ip>:6050`.

**4. Set up access rules** in the Interfaces tab to route between transports. Rules filter on source
and destination interface, direction, node, portnum, keyword and object group, and support SMS
contact selection, failover groups, transform overrides and rate limiting.

**5. Verify end to end.** Send a test message from your Meshtastic device. With rules configured it
should land on the destination interface, for example appearing in the RockBLOCK portal or arriving
as an SMS.

## Configuration

Everything is set by environment variable. Environment values are first-boot defaults; the dashboard
can override them afterwards.

**Core**

| Variable | Default | Description |
|---|---|---|
| `MESHSAT_MODE` | `cubeos` | Set to `direct` for standalone USB access |
| `MESHSAT_PORT` | `6050` | HTTP port for dashboard and API |
| `MESHSAT_DB_PATH` | `/data/meshsat.db` | SQLite database path |
| `MESHSAT_RETENTION_DAYS` | `30` | Days of history to keep |
| `MESHSAT_WEB_DIR` | *(empty)* | Override the embedded SPA path (development only) |

**Serial ports** (`auto` scans USB by VID:PID and protocol probe)

| Variable | Default | Description |
|---|---|---|
| `MESHSAT_MESHTASTIC_PORT` | `auto` | Meshtastic radio |
| `MESHSAT_IRIDIUM_PORT` | `auto` | Iridium 9603N (SBD) |
| `MESHSAT_IMT_PORT` | `auto` | RockBLOCK 9704 (IMT/JSPR) |
| `MESHSAT_CELLULAR_PORT` | `auto` | Cellular modem |
| `MESHSAT_ZIGBEE_PORT` | `auto` | ZigBee coordinator |

**Iridium 9603N**

| Variable | Default | Description |
|---|---|---|
| `MESHSAT_GPIO_CHIP` | `gpiochip4` | libgpiod chardev (Pi 5 is 4, Pi 4 is 0) |
| `MESHSAT_IRIDIUM_SLEEP_PIN` | `0` | Sleep/wake GPIO, BCM numbering, 0 disables |
| `MESHSAT_IRIDIUM_NETAV_PIN` | `0` | NetAv input, high means satellite visible |
| `MESHSAT_IRIDIUM_RI_PIN` | `0` | Ring indicator input, active low |
| `IRIDIUM_SBDIX_TIMEOUT` | `90` | SBDIX timeout in seconds |

**Rate limiting and routing**

| Variable | Default | Description |
|---|---|---|
| `MESHSAT_PAID_RATE_LIMIT` | `60` | Minimum seconds between paid satellite sends |
| `MESHSAT_MAX_HOPS` | `8` | Maximum interfaces a message may traverse |
| `MESHSAT_MESH_WATCHDOG_MIN` | `10` | Minutes of silence before a Meshtastic reconnect |
| `MESHSAT_MESH_MTU` | `100` | HeMB mesh bearer MTU in bytes, range 1 to 237 |

**Compression sidecars**

| Variable | Default | Description |
|---|---|---|
| `MESHSAT_LLAMAZIP_ADDR` | *(empty)* | llama-zip gRPC sidecar address |
| `MESHSAT_LLAMAZIP_TIMEOUT` | `30` | RPC timeout in seconds |
| `MESHSAT_MSVQSC_ADDR` | *(empty)* | MSVQ-SC gRPC sidecar address |
| `MESHSAT_MSVQSC_TIMEOUT` | `30` | RPC timeout in seconds |
| `MESHSAT_MSVQSC_CODEBOOK` | *(empty)* | Codebook path, enables pure-Go decode |

**Reticulum interfaces**

| Variable | Default | Description |
|---|---|---|
| `MESHSAT_TCP_LISTEN` | *(empty)* | TCP listen address for RNS interop, e.g. `:4242` |
| `MESHSAT_ANNOUNCE_INTERVAL` | `300` | Announce broadcast interval in seconds |
| `MESHSAT_AX25_KISS_ADDR` | *(empty)* | KISS TNC address. Bundled Direwolf binds `localhost:8001` |
| `MESHSAT_AX25_CALLSIGN` | *(empty)* | AX.25 source callsign, e.g. `MESHSAT-1` |
| `MESHSAT_MQTT_RETICULUM_BROKER` | *(empty)* | MQTT broker for Reticulum packets |
| `MESHSAT_MQTT_RETICULUM_PREFIX` | `reticulum/meshsat` | MQTT topic prefix |

**Key exchange**

| Variable | Default | Description |
|---|---|---|
| `MESHSAT_KEY_PASSPHRASE` | *(empty)* | Master key passphrase, empty means device-derived |
| `MESHSAT_BUNDLE_VERSION` | `v2` | Key bundle format, `v2` with signing pubkey or `v1` legacy |

### Key bundles and the TOFU trust model

QR-based key bundles (`meshsat://key/...`) carry AES-256 channel keys between the bridge and the
Android app. The bridge emits a signed binary bundle, which the user scans on the phone.

**v2 bundles** embed the bridge Ed25519 signing public key, which enables Trust On First Use:

1. **First scan:** Android pins the key and marks the bundle `NEW_TRUSTED`.
2. **Later scans:** the signature is verified against the pinned key. Valid gives
   `EXISTING_TRUSTED`; a changed key gives `KeyMismatch` and the user must accept it explicitly.
3. **v1 bundles** from older bridges import as `UNVERIFIED_V1` with a warning.

```
Version(1)=0x02 | BridgeHash(16) | Timestamp(4) | EntryCount(1) | SigningPubkey(32) | Signature(64) | Entries...
```

The signature covers every byte except the signature itself, so the public key cannot be swapped
without invalidating it. The key fingerprint is shown in Settings > About for visual comparison.

## Deployment modes

| | Standalone | CubeOS |
|---|---|---|
| Set by | `MESHSAT_MODE=direct` | `MESHSAT_MODE=cubeos` (default) |
| Serial access | Direct to `/dev/ttyACM0`, `/dev/ttyUSB0` | Through the HAL REST API |
| Deploy with | `docker-compose.standalone.yml` | CubeOS orchestrator |
| Who it is for | Any Linux machine | CubeOS installations |

## Architecture

```
USB / UART / TCP       MeshSat Container                              Clients
------------------     -----------------------------------------------  ----------------
                       |                                             |
/dev/ttyACM0 -------->-|  DirectMeshTransport (Meshtastic)            |
  (Meshtastic)         |    Protobuf binary framing (buf.build)       |->  Web Dashboard
                       |                                             |    (Vue 3 SPA,
/dev/ttyUSB0 -------->-|  DirectSatTransport (Iridium 9603N)          |     13 views)
  (Iridium SBD)        |    AT commands, SBDIX/SBDSX, sleep/wake GPIO |
                       |                                             |->  REST API
Pi UART GPIO -------->-|  DirectIMTTransport (RockBLOCK 9704)         |    (300+ endpoints)
  (Iridium IMT)        |    JSPR protocol, 230400 baud, 100 KB msgs   |
                       |                                             |->  SSE Events
/dev/ttyUSB1 -------->-|  DirectCellTransport (A7670E / SIM7600G)     |    (real-time)
  (Cellular)           |    AT commands, SMS, data                    |
                       |                                             |->  Prometheus
/dev/ttyUSB2 -------->-|  DirectZigBeeTransport (CC2652P)             |    (/metrics)
  (ZigBee)             |    Z-Stack ZNP binary protocol               |
                       |                                             |
                       |  DeviceSupervisor                            |
                       |    USB hotplug, VID:PID cascade, port claims |
                       |                                             |
                       |  Compression Pipeline                        |
                       |    SMAZ2 | llama-zip | MSVQ-SC               |
                       |                                             |
                       |  Reticulum Routing (9 interfaces)            |
                       |    Ed25519 identity, announce relay, links   |
                       |    TransportNode, PathFinder, cost-aware     |
                       |    mesh|tcp|sbd|imt|ax25|mqtt|sms|zigbee|ble |
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
                       |         HeMB Bonder (bond groups)            |
                       |        (RLNC coding, free-first allocation)  |
                       |              |                               |
                       |      TransformPipeline                       |
                       |        (zstd, smaz2, aes-256-gcm, b64)       |
                       |              |                               |
                       |  +--------+--------+--------+------+------+  |
                       |  |SBD     |IMT     |Cell    |MQTT  |APRS  |  |
                       |  |Gateway |Gateway |Gateway |GW    |GW    |  |
                       |  +--------+--------+--------+------+------+  |
                       |  |ZigBee  |TAK     |Wbook   |Fail- |      |  |
                       |  |Gateway |Gateway |GW      |over  |      |  |
                       |  +--------+--------+--------+------+------+  |
                       |                                             |
                       |  Field Intelligence                          |
                       |    Dead Man's Switch, Geofence Alerts,       |
                       |    Health Scores, Burst Queue, Topology      |
                       |                                             |
                       |  SpectrumMonitor (RTL-SDR jamming detection) |
                       |  KeyStore (QR bundles, master key envelope)  |
                       |  SigningService (Ed25519 hash chain)         |
                       |  CredentialManager (certs, expiry, mTLS)     |
                       |  Delivery Ledger (SQLite tracking)           |
                       |  SQLite DB (/data/meshsat.db, schema v53)    |
                       -----------------------------------------------
```

## Troubleshooting

**No devices detected on startup.** Check the devices are visible with
`ls /dev/ttyACM* /dev/ttyUSB*`. Try a different cable or port; USB cables are the usual culprit.

**Meshtastic connects but shows 0 nodes.** The config handshake takes 5 to 10 seconds. Wait for the
`config complete` line in the log.

**Iridium shows 0 bars.** Check the antenna connection. It needs a genuinely clear view of the sky,
not a window.

**ZigBee dongle detected as Meshtastic.** The SONOFF dongle shares a VID:PID with some Meshtastic
boards. Pin it explicitly with `MESHSAT_ZIGBEE_PORT`.

**A serial device goes silent and a reboot does not fix it.** On some hosts USB VBUS stays powered
across a warm reboot, so the device firmware never resets. Cut power properly.

## Roadmap

**v0.1.x** Iridium SBD plus Meshtastic bridging, rules engine, MQTT gateway, pass-aware scheduler,
dead letter queue with ISU-aware backoff, device management, SOS mode, dashboard.

**v0.2.0** Any-to-any routing fabric. Channel registry, unified rules engine, structured dispatcher,
cellular integration, SMAZ2 compression, ZigBee gateway, InterfaceManager with USB hotplug, object
groups, failover groups, transform pipelines, Ed25519 audit log, config export and import.

**v0.3.0** Three-tier compression. Reticulum-compatible routing with Ed25519 identity, announce
relay, link manager, keepalive, bandwidth tracking and TCP/HDLC RNS interop across nine wired
interfaces. RockBLOCK 9704 IMT transport. SBD and IMT decoupled into separate gateway types. APRS
and TAK gateways. DeviceSupervisor with USB hotplug and gateway lifecycle wiring. Full Meshtastic
protocol via the official protobuf bindings. Cross-platform key exchange. Credential management.
Multi-instance gateways. Field intelligence. Android companion app.

**Next, protocol work**

- DTN concepts: custody transfer, bundle fragmentation and late binding in the delivery ledger,
  taking cues from RFC 9171
- Forward error correction: a Reed-Solomon codec in the transform pipeline for noisy channels
- GPS-denied time synchronisation: mesh clock consensus with stratum tracking, Iridium MSSTM and an
  NTP fallback
- **HeMB:** formalising capacity-aware cost semantics and paid activation, then validating at larger
  latency ratios. RLNC encoding across bonded groups was confirmed on hardware in March 2026, and a
  three-bearer field test over LoRa, TCP and SMS completed in April 2026. Validation over a paid
  satellite bearer is still outstanding. An RFC submission through the Independent Submission Stream
  (`draft-papadopoulos-hemb-00`) is planned to follow that specification work and measurement.

**Next, spectrum**

- Offline characterisation of captured IQ, raw-signal evidence capture with SigMF sidecars, and
  panoramic survey sweeps. The classifier has not yet been validated against a real jammer.

**Future** Federated mesh-of-meshes, HF radio transport (NVIS/ALE), DMR, cognitive radio with
dynamic spectrum access, edge inference for message triage, HSM integration.

## Related projects

- **MeshSat Android**, a standalone mobile gateway with BLE mesh, SPP Iridium and SMS:
  [github.com/cubeos-app/meshsat-android](https://github.com/cubeos-app/meshsat-android)
- **MeshSat Hub**, multi-tenant fleet management. Currently a private beta on our own infrastructure
  with no public sign-up; access for demos and testing is arranged on request at
  `beta-access-hub@meshsat.net`
- **CubeOS**, a self-hosted OS for single-board computers: [cubeos.app](https://cubeos.app)

## Contributing

Issues and pull requests are welcome. Good places to start:

- Try it with hardware we have not tested and tell us what broke. The supported-devices table above
  is short because it only lists what we have physically run.
- The ZigBee and BLE paths have had far less field exposure than the rest.
- Documentation, especially anything you had to work out the hard way during setup.

Open an issue before a large change so we can agree the shape of it first.

## Funding

This project is supported by [SIDN fonds](https://www.sidnfonds.nl/projecten/meshsat-keeping-people-connected-when-the-network-is-not).

## License

Copyright 2026 Elli and Kyriakos. Licensed under the
[GNU General Public License v3.0](LICENSE).
