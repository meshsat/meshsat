# MeshSat

Meshtastic + Iridium SBD Gateway for CubeOS.

MeshSat is a Go coreapp that sits on top of [HAL](../hal/) and provides:

- **Persistent storage** — messages, telemetry, and GPS positions survive restarts (SQLite)
- **Mesh admin** — remote reboot, factory reset, traceroute, radio/module config, waypoints
- **Signal quality** — RSSI/SNR-based assessment (Good/Fair/Bad) with diagnostic notes
- **Gateway bridging** — MQTT and Iridium satellite bridges (Phase 4)
- **REST API** — paginated history, time-series telemetry, SSE event stream
- **Standalone mode** — designed to run as a single Docker container without CubeOS

## Architecture

```
Dashboard (:6011) ──► CubeOS API (:6010) ──► MeshSat (:6050) ──► HAL (:6005) ──► Radio
                                                  │
                                                  ├── SQLite (persistence)
                                                  ├── MQTT Gateway (Phase 4)
                                                  └── Iridium Bridge (Phase 4)
```

MeshSat subscribes to HAL's Meshtastic SSE event stream, persists all data, and re-broadcasts events to its own subscribers. The transport abstraction (`MeshTransport` / `SatTransport` interfaces) allows the same codebase to run against HAL (CubeOS mode) or directly against serial devices (standalone mode).

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Service health check |
| GET | `/api/messages` | Paginated message history |
| GET | `/api/messages/stats` | Message counts by transport/portnum |
| POST | `/api/messages/send` | Send a text message |
| GET | `/api/telemetry` | Time-series telemetry data |
| GET | `/api/positions` | GPS position history / tracks |
| GET | `/api/nodes` | Mesh nodes with signal quality |
| GET | `/api/status` | Meshtastic connection status |
| GET | `/api/events` | SSE event stream |
| GET | `/api/gateways` | Gateway status |
| POST | `/api/admin/reboot` | Reboot a remote node |
| POST | `/api/admin/factory_reset` | Factory reset a remote node |
| POST | `/api/admin/traceroute` | Traceroute to a node |
| POST | `/api/config/radio` | Set radio configuration |
| POST | `/api/config/module` | Set module configuration |
| POST | `/api/waypoints` | Send a waypoint |

## Build

```bash
make build          # Build for current platform
make build-arm64    # Cross-compile for Raspberry Pi
make test           # Run tests
make docker         # Build Docker image
make fmt            # Format code
```

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `MESHSAT_PORT` | `6050` | HTTP listen port |
| `MESHSAT_DB_PATH` | `/cubeos/data/meshsat.db` | SQLite database path |
| `HAL_URL` | `http://cubeos-hal:6005` | HAL service endpoint |
| `HAL_API_KEY` | — | HAL authentication key |
| `MESHSAT_MODE` | `cubeos` | `cubeos` (HAL transport) or `standalone` (direct serial) |
| `MESHSAT_RETENTION_DAYS` | `30` | Days to retain historical data |

## Deployment

Deployed as a Docker Swarm stack via CubeOS coreapps:

```bash
docker stack deploy -c coreapps/meshsat/appconfig/docker-compose.yml --resolve-image=never meshsat
```

## License

Copyright Nuclear Lighters. All rights reserved.
