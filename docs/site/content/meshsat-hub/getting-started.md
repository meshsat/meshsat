---
title: "Getting Started with Hub"
weight: 1.5
---

# Getting Started with MeshSat Hub

MeshSat Hub aggregates data from multiple field bridges, providing centralized monitoring, device management, and configuration control. This guide walks you through setting up a Hub instance.

## What You Need

| Component | Requirement |
|-----------|-------------|
| Server | VPS or dedicated, 1+ vCPU, 512+ MB RAM |
| OS | Any Linux with Docker Engine 24+ and Compose v2 |
| Domain | A record pointing to the server IP |
| Firewall | Ports 80, 443 open (8883 for MQTT TLS) |

## Step 1 — Deploy the Hub

```bash
# Copy deploy files to your server
scp -r deploy/ user@hub.example.com:/opt/meshsat/

# SSH in and configure
ssh user@hub.example.com
cd /opt/meshsat
cp .env.example .env
nano .env  # set DOMAIN=hub.example.com
```

## Step 2 — Start Services

```bash
docker compose -f docker-compose.prod.yml up -d
```

This starts three services:

| Service | Purpose |
|---------|---------|
| **MeshSat** | REST API + Vue dashboard (port 6050 internal) |
| **Caddy** | Reverse proxy with automatic TLS (ports 80, 443) |
| **Mosquitto** | MQTT broker for field devices (port 1883/8883) |

Caddy automatically obtains a Let's Encrypt certificate — no manual TLS setup needed.

## Step 3 — Verify

```bash
curl https://hub.example.com/health
# → {"status":"healthy","service":"meshsat","database":true}
```

Open `https://hub.example.com` in a browser to see the dashboard.

## Step 4 — Register Field Devices

Each field bridge needs to be registered in Hub's device registry:

```bash
curl -X POST https://hub.example.com/api/device-registry \
  -H "Content-Type: application/json" \
  -d '{
    "imei": "300234065012345",
    "label": "Field Unit Alpha",
    "type": "bridge"
  }'
```

Or use the dashboard: navigate to **Devices** and click **Add Device**.

## Step 5 — Connect Field Bridges

On each field bridge, configure MQTT to point to your Hub:

```yaml
# In the bridge's gateway config (via API or dashboard)
{
  "type": "mqtt",
  "enabled": true,
  "config": {
    "broker": "mqtts://hub.example.com:8883",
    "topic_prefix": "meshsat",
    "username": "bridge1",
    "password": "your-mqtt-password"
  }
}
```

Once connected, the bridge publishes positions, telemetry, and messages to Hub's MQTT namespace (`meshsat/{device_id}/...`).

## Step 6 — Configure Routing Rules

By default, all traffic is denied (implicit deny). Create access rules to allow message flow:

```bash
# Allow all inbound MQTT messages to be processed
curl -X POST https://hub.example.com/api/access-rules \
  -H "Content-Type: application/json" \
  -d '{
    "action": "permit",
    "source_interface": "mqtt-hub",
    "direction": "ingress",
    "description": "Accept messages from field bridges"
  }'
```

## What's Next?

- **[Deployment Guide](/meshsat-hub/deployment/)** — TLS, MQTT auth, backups, monitoring, resource sizing
- **[Operations Guide](/meshsat-hub/operations/)** — Day-to-day management, MQTT integration, satellite credits
- **[API Reference](/meshsat-hub/api-reference/)** — Full REST API documentation
- **[Troubleshooting](/meshsat-hub/troubleshooting/)** — Common issues and fixes
- **[Architecture](/architecture/)** — How Hub, Bridge, and Android work together
