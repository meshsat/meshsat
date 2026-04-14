---
title: "MeshSat"
---

# MeshSat

**One gateway, every channel. If a message can travel, MeshSat will send it.**

MeshSat is an open-source multi-channel communications bridge that connects Meshtastic LoRa mesh networks to satellite, cellular, APRS, TAK, and IP-based channels. It runs on a Raspberry Pi (or any ARM64/x86 Linux box) as a single Docker container.

## Channel Support

| Channel | Status | Direction | Paid | Max Payload |
|---------|--------|-----------|------|-------------|
| Meshtastic LoRa | Implemented | Bidirectional | No | 237 bytes |
| Iridium SBD (9603N) | Implemented | Bidirectional | Yes | 340 bytes |
| Cellular SMS | Implemented | Bidirectional | Yes | 160 chars |
| MQTT | Implemented | Bidirectional | No | Unlimited |
| Webhook HTTP | Implemented | Bidirectional | No | Unlimited |
| ZigBee 3.0 | Implemented | Bidirectional | No | 100 bytes |
| TAK/CoT | Implemented | Bidirectional | No | Unlimited |
| APRS (Direwolf) | Implemented | Bidirectional | No | 256 bytes |

## Quickstart

```bash
# 1. Clone
git clone https://github.com/cubeos-app/meshsat.git
cd meshsat

# 2. Configure
cp docker-compose.standalone.yml docker-compose.yml
# Edit docker-compose.yml — set serial ports for your hardware

# 3. Run
docker compose up -d
```

The web dashboard is at `http://<your-pi>:6050`.

## Key Features

- **Any-to-any routing** — every channel can be a source and a destination
- **Access rules** — Cisco ASA-style implicit deny with per-interface ingress/egress rules
- **Delivery ledger** — full lifecycle tracking for every message across all channels
- **3-tier compression** — SMAZ2 (lossless, <1ms), llama-zip (LLM lossless), MSVQ-SC (lossy semantic)
- **Satellite-aware scheduling** — SGP4 TLE pass prediction for Iridium
- **Dead man's switch** — configurable check-in timer with multi-channel alerting
- **Geofence alerts** — polygon-based boundary crossing detection
- **Reticulum routing** — Ed25519 identity, cryptographic announces, link management
- **Web dashboard** — Vue 3 SPA with real-time SSE updates

## Links

- [GitHub](https://github.com/cubeos-app/meshsat) — source code (GPLv3)
- [YouTrack](https://youtrack.nuclearlighters.net/projects/MESHSAT) — issue tracker
