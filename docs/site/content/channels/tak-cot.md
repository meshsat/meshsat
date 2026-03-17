---
title: "TAK/CoT"
weight: 5
---

# TAK / Cursor on Target

MeshSat bridges to TAK (Team Awareness Kit) ecosystems via the Cursor on Target (CoT) XML protocol. It connects to an OpenTAKServer or TAK Server instance over TCP (port 8087) or TLS (port 8089).

## Configuration

```json
{
  "tak_host": "tak.example.com",
  "tak_port": 8087,
  "tak_ssl": false,
  "callsign_prefix": "MESHSAT",
  "cot_stale_seconds": 300
}
```

For TLS connections (port 8089), provide client certificates:

```json
{
  "tak_host": "tak.example.com",
  "tak_port": 8089,
  "tak_ssl": true,
  "cert_file": "/path/to/client.pem",
  "key_file": "/path/to/client.key",
  "ca_file": "/path/to/ca.pem"
}
```

## CoT Event Type Mapping

| MeshSat Event | CoT Type | Detail |
|---------------|----------|--------|
| Position report | `a-f-G-U-C` | Friendly ground unit with PLI |
| SOS / Emergency | `a-f-G-U-C` | Same type + `<emergency>` detail block |
| Dead man's switch | `b-a` | Alarm event with remarks |
| Sensor telemetry | `t-x-d-d` | Data event with sensor readings in remarks |
| Text message | `b-t-f` | GeoChat freetext |

## Recommended TAK Server

[OpenTAKServer](https://github.com/brian7704/OpenTAKServer) is recommended for self-hosted deployments. It provides TCP CoT (8087), TLS CoT (8089), and DataPackage API (8443) in a Docker Compose setup.

## Bidirectional Routing

- **Outbound**: MeshSat position/message/telemetry → CoT XML → TAK server
- **Inbound**: TAK client position/chat → CoT XML → MeshSat internal message → routed to mesh/satellite/SMS
