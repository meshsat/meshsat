---
title: "MeshSat Hub"
weight: 1
---

# MeshSat Hub

MeshSat Hub is the cloud/VPS component that aggregates data from multiple MeshSat Bridge instances deployed in the field. It provides a centralized dashboard, device registry, configuration management, and MQTT-based telemetry ingestion.

## Architecture

```
Field Device 1 (Pi + radios) ──MQTT──→ MeshSat Hub (VPS)
Field Device 2 (Pi + radios) ──MQTT──→     ↓
Field Device 3 (Pi + radios) ──MQTT──→  Dashboard
                                           ↓
                                     Device Registry
                                     Config Versioning
                                     Telemetry Store
```

## MQTT Topic Namespace

Hub topics use the `meshsat/` prefix (separate from bridge MQTT's `msh/` prefix):

```
meshsat/{device_id}/position        # GPS position
meshsat/{device_id}/telemetry       # Sensor data
meshsat/{device_id}/sos             # Emergency events
meshsat/{device_id}/config/current  # Active config
meshsat/hub/status                  # Hub health
meshsat/hub/credits                 # Satellite credit balance
```
