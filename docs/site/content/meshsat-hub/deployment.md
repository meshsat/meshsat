---
title: "Deployment"
weight: 2
---

# Hub Deployment

MeshSat Hub runs the same Docker image as the Bridge, configured in hub mode.

## Docker Compose

```yaml
services:
  meshsat-hub:
    image: ghcr.io/cubeos-app/meshsat:latest
    restart: unless-stopped
    ports:
      - "6050:6050"
    volumes:
      - hub-data:/cubeos/data
    environment:
      - MESHSAT_MODE=direct
      - MESHSAT_PORT=6050
      - MESHSAT_DB_PATH=/cubeos/data/meshsat.db

  mosquitto:
    image: eclipse-mosquitto:2
    restart: unless-stopped
    ports:
      - "1883:1883"
    volumes:
      - mosquitto-data:/mosquitto/data
      - ./mosquitto.conf:/mosquitto/config/mosquitto.conf

volumes:
  hub-data:
  mosquitto-data:
```

## Requirements

- VPS with public IP (for field devices to reach)
- 1 CPU, 512MB RAM minimum
- Docker and Docker Compose
- MQTT broker (Mosquitto recommended)

<!-- TODO: Document hub-specific environment variables once hub mode is fully implemented -->
