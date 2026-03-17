---
title: "MQTT"
weight: 7
---

# MQTT

MeshSat bridges mesh messages to an MQTT broker for integration with home automation, monitoring dashboards, and other IoT systems.

## Configuration

```json
{
  "broker": "tcp://mosquitto:1883",
  "topic_prefix": "msh/cubeos",
  "username": "",
  "password": "",
  "client_id": "meshsat"
}
```

## Topic Structure

MeshSat publishes to `{topic_prefix}/{channel}/{nodeHex}` for bridge MQTT (Meshtastic mesh bridging).

For MeshSat Hub, a separate namespace is used:

```
meshsat/{device_id}/mo/raw          # Raw MO payload (base64)
meshsat/{device_id}/mo/decoded      # Decoded MO message (JSON)
meshsat/{device_id}/mt/send         # Queue MT message
meshsat/{device_id}/position        # GPS position
meshsat/{device_id}/telemetry       # Sensor telemetry
meshsat/{device_id}/sos             # SOS events
```
