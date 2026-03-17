---
title: "Installation"
weight: 2
---

# Installation

MeshSat runs as a single Docker container. No complex orchestration required.

## Docker Compose (Recommended)

```bash
git clone https://github.com/cubeos-app/meshsat.git
cd meshsat
cp docker-compose.standalone.yml docker-compose.yml
```

Edit `docker-compose.yml` to match your hardware:

```yaml
services:
  meshsat:
    image: ghcr.io/cubeos-app/meshsat:latest
    restart: unless-stopped
    privileged: true
    ports:
      - "6050:6050"
    volumes:
      - /dev:/dev
      - /sys:/sys
      - meshsat-data:/cubeos/data
    environment:
      - MESHSAT_MODE=direct
      - MESHSAT_PORT=6050
      - MESHSAT_MESHTASTIC_PORT=auto
      - MESHSAT_IRIDIUM_PORT=auto

volumes:
  meshsat-data:
```

Start:

```bash
docker compose up -d
```

The dashboard is now available at `http://<your-ip>:6050`.

## Build from Source

```bash
# Prerequisites: Go 1.24+, Node.js 20+ (for web UI)
make build-with-web
./build/meshsat
```

## Environment Variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `MESHSAT_MODE` | `cubeos` | Set to `direct` for standalone mode |
| `MESHSAT_PORT` | `6050` | HTTP API + dashboard port |
| `MESHSAT_DB_PATH` | `/cubeos/data/meshsat.db` | SQLite database path |
| `MESHSAT_MESHTASTIC_PORT` | `auto` | Serial port or `auto` for USB detection |
| `MESHSAT_IRIDIUM_PORT` | `auto` | Serial port or `auto` |
| `MESHSAT_CELLULAR_PORT` | `auto` | Serial port or `auto` |
| `MESHSAT_RETENTION_DAYS` | `30` | Message/telemetry retention |
| `MESHSAT_PAID_RATE_LIMIT` | `60` | Min seconds between paid sends |
