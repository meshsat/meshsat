---
title: "Operations Guide"
weight: 4
---

# MeshSat Hub — Operations Guide

This guide covers day-to-day Hub operation: managing field devices, monitoring channels, MQTT integration, and common administrative tasks.

## Device Management

### Registering Field Devices

Each field bridge registers with the Hub via its IMEI (Iridium) or a user-assigned device ID. Register devices through the API or dashboard:

```bash
# Register a new field device
curl -X POST https://hub.example.com/api/device-registry \
  -H "Content-Type: application/json" \
  -d '{
    "imei": "300234065012345",
    "label": "Field Unit Alpha",
    "type": "bridge",
    "notes": "Base camp, solar powered"
  }'
```

Devices appear on the dashboard with status indicators:

| Status | Meaning |
|--------|---------|
| **online** | Last seen within 24 hours |
| **offline** | Last seen more than 24 hours ago |
| **never_seen** | Registered but no data received yet |

### Configuration Versioning

Hub tracks every configuration change pushed to field devices. If a config change causes problems, roll back:

```bash
# List config versions for a device
curl https://hub.example.com/api/device-registry/1/config/versions

# Rollback to version 3
curl -X POST https://hub.example.com/api/device-registry/1/config/rollback/3
```

---

## MQTT Integration

### Hub MQTT Namespace

Hub uses the `meshsat/` topic prefix (separate from bridge MQTT's `msh/` prefix). Both coexist on the same Mosquitto broker.

```
meshsat/{device_id}/mo/raw          # Raw MO payload (base64, QoS 1)
meshsat/{device_id}/mo/decoded      # Decoded MO message (JSON, QoS 1)
meshsat/{device_id}/mt/send         # Queue MT message (JSON, QoS 1)
meshsat/{device_id}/mt/status       # MT delivery status (JSON, QoS 1)
meshsat/{device_id}/status/signal   # Signal quality per channel (QoS 0, retained)
meshsat/{device_id}/status/health   # Device health summary (QoS 0, retained)
meshsat/{device_id}/position        # GPS position (QoS 1, retained)
meshsat/{device_id}/telemetry       # Sensor telemetry (QoS 0, retained)
meshsat/{device_id}/sos             # SOS events (QoS 2)
meshsat/{device_id}/config/current  # Current config snapshot (QoS 1, retained)
meshsat/{device_id}/config/update   # Config update command (QoS 1)
meshsat/hub/status                  # Hub health (QoS 0, retained)
meshsat/hub/events                  # Hub system events (QoS 1)
meshsat/hub/credits                 # Satellite credit balance (QoS 0, retained)
```

### Subscribing to Device Data

Monitor all field devices from an external system:

```bash
# All decoded messages from all devices
mosquitto_sub -h hub.example.com -p 8883 \
  --cafile ca.crt -u bridge1 -P secret \
  -t 'meshsat/+/mo/decoded'

# All positions
mosquitto_sub -t 'meshsat/+/position'

# SOS events (highest QoS)
mosquitto_sub -t 'meshsat/+/sos' --qos 2
```

### Sending MT Messages

Queue a Mobile-Terminated (MT) message to a field device:

```bash
mosquitto_pub -t 'meshsat/300234065012345/mt/send' \
  -m '{"text":"Check in ASAP","priority":"high"}' \
  --qos 1
```

### Channel Type to Topic Mapping

| Channel | Topics Used |
|---------|-------------|
| Iridium | `mo/raw`, `mo/decoded`, `mt/send`, `mt/status` |
| Cellular | `mo/decoded`, `mt/send` (text only, no raw) |
| Meshtastic | `mo/decoded`, `position`, `telemetry` (via bridge relay) |
| ZigBee | `mo/decoded`, `telemetry` (sensor data) |
| Webhook | Not on MQTT — HTTP-native |

---

## Monitoring

### Health Endpoints

Poll these endpoints for monitoring and alerting:

```bash
# Overall health (use for container health check)
curl https://hub.example.com/health
# → {"status":"healthy","service":"meshsat","database":true}

# Per-interface health scores (0-100)
curl https://hub.example.com/api/interfaces/health
# → [{"id":"iridium-1","score":92,"state":"online"},...]

# Satellite burst queue (detect message backlog)
curl https://hub.example.com/api/burst/status
# → {"queued":3,"in_flight":1,"max_queue":100}

# Loop prevention metrics
curl https://hub.example.com/api/loop-metrics
# → {"loops_detected":0,"messages_dropped":0}
```

### Real-Time Event Stream

Connect to SSE for live monitoring:

```bash
curl -N https://hub.example.com/api/events
```

Events include message receipt, delivery status, interface state changes, SOS activations, geofence alerts, and dead man's switch triggers.

### Delivery Statistics

```bash
# Overall delivery stats
curl https://hub.example.com/api/deliveries/stats
# → {"total":1523,"delivered":1489,"failed":12,"pending":22,"avg_latency_ms":4200}
```

### Log Analysis

MeshSat uses structured JSON logging (zerolog):

```bash
# Follow all logs
docker compose -f docker-compose.prod.yml logs -f meshsat

# Filter for errors
docker compose -f docker-compose.prod.yml logs meshsat 2>&1 | \
  python3 -c "import sys,json;[print(json.loads(l)['message']) for l in sys.stdin if '\"level\":\"error\"' in l]"
```

---

## Satellite Credit Management

### Monitoring Credits

```bash
# Check remaining Iridium credits
curl https://hub.example.com/api/iridium/credits

# Set budget alert (warns when credits drop below threshold)
curl -X POST https://hub.example.com/api/iridium/credits/budget \
  -H "Content-Type: application/json" \
  -d '{"warning_threshold": 100, "critical_threshold": 20}'
```

### Rate Limiting

Per-device satellite rate limiting prevents runaway costs:

```bash
# Set 120-second minimum between satellite sends for a device
curl -X PUT https://hub.example.com/api/satellite/rate-limits/300234065012345 \
  -H "Content-Type: application/json" \
  -d '{"min_interval_seconds": 120}'

# View satellite usage across all devices
curl https://hub.example.com/api/satellite/usage
```

---

## Access Rules

MeshSat uses Cisco ASA-style access rules with implicit deny. Rules are evaluated top-to-bottom.

### Example: Allow mesh-to-satellite, deny all else

```bash
# Create a permit rule for mesh → iridium
curl -X POST https://hub.example.com/api/access-rules \
  -H "Content-Type: application/json" \
  -d '{
    "action": "permit",
    "source_interface": "mesh-1",
    "dest_interface": "iridium-1",
    "direction": "egress",
    "description": "Allow mesh messages to satellite"
  }'
```

All traffic not explicitly permitted is denied (implicit deny at the bottom of the rule set).

---

## Configuration Export/Import

MeshSat supports Cisco-style `running-config` export and import:

```bash
# Export current running config as YAML
curl https://hub.example.com/api/config/export > running-config.yaml

# Preview what an import would change (non-destructive)
curl -X POST https://hub.example.com/api/config/diff \
  -H "Content-Type: application/yaml" \
  --data-binary @modified-config.yaml

# Apply config
curl -X POST https://hub.example.com/api/config/import \
  -H "Content-Type: application/yaml" \
  --data-binary @modified-config.yaml
```

---

## Field Intelligence Features

### Dead Man's Switch

Configurable check-in timer. If no message is received within the window, alerts fire on all channels:

```bash
curl -X POST https://hub.example.com/api/deadman \
  -H "Content-Type: application/json" \
  -d '{"enabled": true, "interval_minutes": 60, "alert_channels": ["sms", "mqtt"]}'
```

### Geofence Alerts

Define polygon boundaries. When a tracked device crosses one, an alert fires:

```bash
curl -X POST https://hub.example.com/api/geofences \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Base Camp Perimeter",
    "type": "polygon",
    "coordinates": [[52.0, 5.0], [52.1, 5.0], [52.1, 5.1], [52.0, 5.1]],
    "alert_on": "exit"
  }'
```

### SOS

```bash
# Activate SOS (broadcasts on all channels)
curl -X POST https://hub.example.com/api/sos/activate

# Check SOS status
curl https://hub.example.com/api/sos/status

# Cancel SOS
curl -X POST https://hub.example.com/api/sos/cancel
```

---

## Routine Maintenance

### Database Retention

MeshSat automatically prunes old messages based on `MESHSAT_RETENTION_DAYS` (default: 90). No manual action needed.

### Backup Verification

Periodically verify backups are being created:

```bash
# List recent backups
curl https://hub.example.com/api/backup/list

# Verify backup schedule
curl https://hub.example.com/api/backup/schedule
```

### TLE Refresh

Satellite pass predictions use TLE data from Celestrak, refreshed daily. Force a refresh if passes seem inaccurate:

```bash
curl -X POST https://hub.example.com/api/iridium/passes/refresh
```

### Audit Log

MeshSat maintains an Ed25519-signed, hash-chained audit log for non-repudiation:

```bash
# View recent audit entries
curl https://hub.example.com/api/audit

# Verify chain integrity
curl https://hub.example.com/api/audit/verify
# → {"valid":true,"entries":1523,"chain_intact":true}
```
