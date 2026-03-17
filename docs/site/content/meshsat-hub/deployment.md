---
title: "Production Deployment"
weight: 2
---

# MeshSat Hub — Production Deployment Guide

This guide covers deploying MeshSat Hub on a VPS or dedicated server with TLS, MQTT, automated backups, and monitoring.

## Architecture Overview

```
Internet
  │
  ├── HTTPS (443) ──→ Caddy ──→ MeshSat Hub (:6050)
  │                     │              │
  │                     │         SQLite DB
  │                     │        /cubeos/data/
  │
  └── MQTTS (8883) ──→ Mosquitto (:1883 internal)
                           │
                      Field Bridges
```

| Component | Image | Port | Purpose |
|-----------|-------|------|---------|
| MeshSat Hub | `ghcr.io/cubeos-app/meshsat` | 6050 (internal) | REST API + Vue dashboard |
| Caddy | `caddy:2-alpine` | 80, 443 | Reverse proxy, auto TLS |
| Mosquitto | `eclipse-mosquitto:2` | 1883, 8883 | MQTT broker for field devices |

## Prerequisites

- **Server**: VPS or dedicated, 1+ CPU, 512MB+ RAM (see [Resource Sizing](#resource-sizing))
- **OS**: Any Linux with Docker Engine 24+ and Compose v2
- **DNS**: A record pointing your domain to the server IP
- **Firewall**: Ports 80, 443 open (and 8883 if using MQTT over TLS)

## Quick Start

```bash
# 1. Clone or copy the deploy/ directory to your server
scp -r deploy/ user@hub.example.com:/opt/meshsat/

# 2. Configure
cd /opt/meshsat
cp .env.example .env
nano .env  # set DOMAIN=hub.example.com

# 3. Launch
docker compose -f docker-compose.prod.yml up -d

# 4. Verify
curl https://hub.example.com/health
# → {"status":"healthy","service":"meshsat","database":true}
```

Caddy automatically obtains a Let's Encrypt certificate on first request. No manual TLS configuration needed.

## Configuration

### Environment Variables

Edit `.env` to customize. All variables have sensible defaults.

| Variable | Default | Description |
|----------|---------|-------------|
| `DOMAIN` | *(required)* | Public hostname for TLS certificate |
| `MESHSAT_TAG` | `latest` | Docker image tag |
| `MESHSAT_RETENTION_DAYS` | `90` | Days to keep messages/telemetry |
| `MESHSAT_BACKUP_INTERVAL_HOURS` | `12` | Auto-backup frequency |
| `MESHSAT_BACKUP_MAX_KEEP` | `14` | Maximum backup archives to retain |
| `MESHSAT_PAID_RATE_LIMIT` | `60` | Minimum seconds between satellite sends |
| `MESHSAT_ANNOUNCE_INTERVAL` | `300` | Routing announce broadcast interval (seconds) |

### Caddy (TLS / Reverse Proxy)

The `Caddyfile` configures:
- **Automatic HTTPS** via Let's Encrypt (ACME)
- **HTTP/3** (QUIC) support
- **Security headers** (HSTS, X-Frame-Options, CSP)
- **SSE streaming** support for `/api/events`
- **Gzip/Zstd compression** for API responses
- **Access logging** with rotation

To use a custom certificate instead of Let's Encrypt:

```caddyfile
hub.example.com {
    tls /path/to/cert.pem /path/to/key.pem
    reverse_proxy meshsat:6050
}
```

### Mosquitto (MQTT Broker)

Default configuration provides:
- **Port 1883**: Plaintext MQTT (internal Docker network only)
- **Port 8883**: TLS MQTT (commented out — enable for field devices over internet)

#### Enabling MQTT over TLS

1. Obtain certificates (e.g., from Let's Encrypt or your CA)
2. Mount them into the Mosquitto container
3. Uncomment the TLS listener block in `mosquitto.conf`
4. Create a password file: `docker exec mosquitto mosquitto_passwd -c /mosquitto/config/passwd fielduser`

#### Adding Authentication

```bash
# Enter the mosquitto container
docker compose -f docker-compose.prod.yml exec mosquitto sh

# Create password file
mosquitto_passwd -c /mosquitto/config/passwd bridge1
mosquitto_passwd /mosquitto/config/passwd bridge2

# Exit and update mosquitto.conf:
#   allow_anonymous false
#   password_file /mosquitto/config/passwd

# Restart
docker compose -f docker-compose.prod.yml restart mosquitto
```

## Backup & Restore

MeshSat has a built-in backup system accessible via the REST API and the web dashboard. For production, use the host-level scripts that also capture the SQLite database and Mosquitto state.

### Automated Backups

The `backup.sh` script creates three backup artifacts:

| Artifact | Contents | Method |
|----------|----------|--------|
| `meshsat-api-*.zip` | Config, rules, devices, contacts | REST API (`POST /api/backup/create`) |
| `meshsat-db-*.sqlite.gz` | Full database incl. messages/telemetry | SQLite `.backup` command |
| `mosquitto-data-*.tar.gz` | Retained messages, persistent sessions | Filesystem tar |

```bash
# Manual run
./backup.sh /mnt/backups/meshsat

# Cron (daily at 03:00 UTC)
echo "0 3 * * * /opt/meshsat/deploy/backup.sh /mnt/backups/meshsat 2>&1 | logger -t meshsat-backup" \
  | crontab -
```

Old backups are automatically pruned (default: keep last 14 of each type).

### Restore

```bash
# Preview what an API restore would change (non-destructive)
curl -X POST https://hub.example.com/api/backup/preview \
  -F "file=@meshsat-api-20260317-030000.zip"

# Restore application config (online, via API)
./restore.sh api backups/meshsat-api-20260317-030000.zip

# Restore full database (stops MeshSat during restore)
./restore.sh db backups/meshsat-db-20260317-030000.sqlite.gz

# Restore Mosquitto data (stops Mosquitto during restore)
./restore.sh mqtt backups/mosquitto-data-20260317-030000.tar.gz
```

Both scripts automatically create a pre-restore snapshot before applying changes.

### Backup via Web Dashboard

The MeshSat dashboard (Settings → Backup) also provides:
- One-click backup download
- Upload and preview diff before restore
- Auto-backup schedule configuration

## Monitoring

### Health Endpoints

| Endpoint | Response | Purpose |
|----------|----------|---------|
| `GET /health` | `{"status":"healthy","database":true}` | Container health check |
| `GET /api/interfaces/health` | Per-interface health scores | Channel status monitoring |
| `GET /api/burst/status` | Satellite burst queue depth | Backlog detection |
| `GET /api/loop-metrics` | Loop prevention counters | Routing health |

### Prometheus Integration

MeshSat does not expose a native `/metrics` endpoint. Use a blackbox exporter or scrape the health endpoint:

```yaml
# prometheus.yml — blackbox probe
scrape_configs:
  - job_name: meshsat-hub
    metrics_path: /probe
    params:
      module: [http_2xx]
    static_configs:
      - targets: ["https://hub.example.com/health"]
    relabel_configs:
      - source_labels: [__address__]
        target_label: __param_target
      - source_labels: [__param_target]
        target_label: instance
      - target_label: __address__
        replacement: blackbox-exporter:9115

  # JSON exporter for interface health scores
  - job_name: meshsat-interfaces
    metrics_path: /probe
    params:
      module: [meshsat_health]
    static_configs:
      - targets: ["https://hub.example.com/api/interfaces/health"]
    relabel_configs:
      - source_labels: [__address__]
        target_label: __param_target
      - source_labels: [__param_target]
        target_label: instance
      - target_label: __address__
        replacement: json-exporter:7979
```

### Docker Health Checks

All three services have built-in health checks. Monitor via Docker:

```bash
# Check all service health
docker compose -f docker-compose.prod.yml ps

# Watch health status
watch -n 10 'docker compose -f docker-compose.prod.yml ps --format "table {{.Name}}\t{{.Status}}"'
```

### Log Monitoring

```bash
# All services
docker compose -f docker-compose.prod.yml logs -f

# MeshSat only (JSON structured logs via zerolog)
docker compose -f docker-compose.prod.yml logs -f meshsat

# Caddy access logs
docker compose -f docker-compose.prod.yml exec caddy cat /data/access.log

# Mosquitto logs
docker compose -f docker-compose.prod.yml exec mosquitto cat /mosquitto/log/mosquitto.log
```

## Resource Sizing

### Minimum (1-5 field devices, light traffic)

| Resource | Value |
|----------|-------|
| CPU | 1 vCPU |
| RAM | 512 MB |
| Disk | 5 GB SSD |
| Bandwidth | 10 GB/month |

### Recommended (5-25 field devices, moderate traffic)

| Resource | Value |
|----------|-------|
| CPU | 2 vCPU |
| RAM | 1 GB |
| Disk | 20 GB SSD |
| Bandwidth | 50 GB/month |

### Large (25-100 field devices, high traffic, compression sidecars)

| Resource | Value |
|----------|-------|
| CPU | 4 vCPU |
| RAM | 4 GB |
| Disk | 50 GB SSD |
| Bandwidth | 100 GB/month |

**Notes:**
- SQLite database grows ~1-5 MB/day per active device (messages + telemetry)
- The llama-zip sidecar needs ~500 MB RAM for the LLM model
- The MSVQ-SC sidecar needs ~200 MB RAM for the ONNX encoder
- Adjust `MESHSAT_RETENTION_DAYS` to control database growth

### Disk Usage Estimates

| Data | Growth Rate | 90-day retention |
|------|-------------|------------------|
| Messages | ~500 KB/day/device | ~45 MB/device |
| Telemetry | ~200 KB/day/device | ~18 MB/device |
| Positions | ~100 KB/day/device | ~9 MB/device |
| Backups | ~2 MB/backup | ~28 MB (14 copies) |

## Optional: Compression Sidecars

For bandwidth-constrained satellite links, enable LLM-based or semantic compression:

```yaml
# Add to docker-compose.prod.yml services section:

  llamazip:
    build:
      context: ../
      dockerfile: sidecar/llama-zip/Dockerfile
    environment:
      - LLAMAZIP_LISTEN=0.0.0.0:50051
      - LLAMAZIP_MODEL_REPO=bartowski/SmolLM2-135M-Instruct-GGUF
      - LLAMAZIP_MODEL_FILE=SmolLM2-135M-Instruct-Q4_K_M.gguf
      - LLAMAZIP_MODEL_CACHE=/models
      - LLAMAZIP_WORKERS=2
      - LLAMAZIP_N_CTX=2048
    volumes:
      - llamazip-models:/models
    networks:
      - internal
    healthcheck:
      test: ["CMD", "python", "-c", "import grpc; ch = grpc.insecure_channel('localhost:50051'); grpc.channel_ready_future(ch).result(timeout=5)"]
      interval: 10s
      timeout: 10s
      retries: 6
      start_period: 60s
    restart: unless-stopped
    deploy:
      resources:
        limits:
          memory: 768M
```

Then uncomment `MESHSAT_LLAMAZIP_ADDR=llamazip:50051` in the meshsat environment block and add `llamazip-models:` to the volumes section.

## Upgrading

```bash
cd /opt/meshsat

# Pull latest images
docker compose -f docker-compose.prod.yml pull

# Create backup before upgrade
./backup.sh

# Rolling restart (zero-downtime for Caddy/Mosquitto)
docker compose -f docker-compose.prod.yml up -d

# Verify
curl https://hub.example.com/health
```

Database migrations run automatically on startup (append-only, forward-compatible).

## Troubleshooting

### MeshSat won't start

```bash
# Check logs
docker compose -f docker-compose.prod.yml logs meshsat

# Common issues:
# - "database is locked" → another instance running, check for stale containers
# - "bind: address already in use" → port 6050 conflict
```

### TLS certificate not provisioning

```bash
# Check Caddy logs
docker compose -f docker-compose.prod.yml logs caddy

# Common issues:
# - DNS not pointing to server → verify: dig hub.example.com
# - Port 80 blocked → Caddy needs it for ACME HTTP-01 challenge
# - Rate limited → check https://letsencrypt.org/docs/rate-limits/
```

### MQTT field devices can't connect

```bash
# Test local connectivity
docker compose -f docker-compose.prod.yml exec mosquitto \
  mosquitto_pub -t test -m "hello"

# Check listener status
docker compose -f docker-compose.prod.yml exec mosquitto \
  mosquitto_sub -t '$SYS/broker/clients/connected' -C 1

# Common issues:
# - Firewall blocking 1883/8883
# - TLS cert mismatch (if using MQTTS)
# - Authentication required but not configured on bridge
```

## File Reference

```
deploy/
├── docker-compose.prod.yml   # Production stack definition
├── Caddyfile                  # Reverse proxy + TLS config
├── mosquitto.conf             # MQTT broker config
├── .env.example               # Environment template
├── backup.sh                  # Host-level backup script
└── restore.sh                 # Restore from backup
```
