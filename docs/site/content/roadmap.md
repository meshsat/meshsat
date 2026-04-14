---
title: "Roadmap"
---

# Roadmap

## v0.1.x — Foundation (Complete)

- Meshtastic ↔ Iridium SBD bridge
- MQTT, Webhook, Cellular SMS gateways
- Web dashboard (Vue 3 SPA)
- Standalone mode for direct Pi deployment
- CI/CD pipeline (GitLab → Docker → Pi)

## v0.2.0 — Full Routing Fabric (Complete)

- Channel registry with self-registration
- Any-to-any message routing
- Delivery ledger with full lifecycle tracking
- Unified access rules (Cisco ASA-style implicit deny)
- Dispatcher with failover resolution
- ZigBee 3.0 gateway
- SMAZ2 compression
- Reticulum-inspired routing (Ed25519, announce relay, link management)
- MSVQ-SC lossy semantic compression
- Field intelligence (dead man's switch, geofence, health scores, burst queue)
- Config export/import (Cisco `show running-config` style)

## v0.3.0 — Multi-Channel Expansion (In Progress)

- TAK/CoT integration (bidirectional bridge to TAK server)
- APRS via Direwolf (AIOC + Baofeng/Quansheng)
- MeshSat Hub (centralized device management)
- Device configuration versioning
- Documentation site (Hugo + PaperMod)
- Android companion app parity

## Future

- LoRa WAN (Lacuna Space)
- HF radio integration
- Starlink direct
- Multi-constellation satellite support
- Web-based config editor with live diff
