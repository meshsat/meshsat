---
title: "API Reference"
weight: 3
---

# API Reference

MeshSat exposes a REST API on port `6050` (configurable via `MESHSAT_PORT`). All endpoints return JSON. No authentication is required by default — secure access at the reverse proxy layer (see [Deployment Guide](/meshsat-hub/deployment/)).

The full OpenAPI 3.0 specification is available at [`/docs/swagger/swagger.json`](https://github.com/cubeos-app/meshsat/blob/main/docs/swagger/swagger.json) in the repository.

---

## Health & Status

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/health` | Container health check — returns `{"status":"healthy","database":true}` |
| GET | `/api/events` | SSE stream of real-time events (messages, telemetry, status changes) |

---

## Messages

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/messages` | Paginated message history with filters (node, time range, transport, direction) |
| GET | `/api/messages/stats` | Message count and size statistics |
| POST | `/api/messages/send` | Send a text message via mesh radio |
| DELETE | `/api/messages` | Purge message history |

### Query Parameters — `GET /api/messages`

| Parameter | Type | Description |
|-----------|------|-------------|
| `node` | string | Filter by node ID (`!hex` format) |
| `since` | string | Start time (RFC3339) |
| `until` | string | End time (RFC3339) |
| `portnum` | int | Filter by Meshtastic port number |
| `transport` | string | Filter by transport: `radio`, `mqtt`, `satellite` |
| `direction` | string | Filter by direction: `rx`, `tx` |
| `limit` | int | Results per page (default 50, max 1000) |
| `offset` | int | Offset for pagination |

---

## Telemetry & Positions

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/telemetry` | Telemetry history (battery, voltage, temperature) |
| GET | `/api/positions` | Position history with optional node/time filters |
| POST | `/api/position/send` | Broadcast current position to mesh |
| POST | `/api/position/fixed` | Set a fixed position (for stationary nodes) |
| DELETE | `/api/position/fixed` | Remove fixed position |
| POST | `/api/waypoints` | Send a waypoint to the mesh |

---

## Mesh Nodes

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/nodes` | List all known mesh nodes with metadata |
| DELETE | `/api/nodes/{num}` | Remove a node from the database |
| GET | `/api/neighbors` | Neighbor info from mesh |
| GET | `/api/topology` | Mesh topology graph (nodes + links with SNR) |

---

## Gateways

Gateways bridge MeshSat to external channels (MQTT, Iridium, Cellular, etc.).

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/gateways` | List all configured gateways |
| GET | `/api/gateways/{type}` | Get gateway config by type (`mqtt`, `iridium`, `cellular`, etc.) |
| PUT | `/api/gateways/{type}` | Create or update gateway configuration |
| DELETE | `/api/gateways/{type}` | Remove a gateway |
| POST | `/api/gateways/{type}/start` | Start a gateway |
| POST | `/api/gateways/{type}/stop` | Stop a gateway |
| POST | `/api/gateways/{type}/test` | Test gateway connectivity |

---

## Interfaces (v0.3.0)

Interfaces represent physical or logical communication endpoints with a state machine (unbound → offline → binding → online/error).

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/interfaces` | List all interfaces with status |
| GET | `/api/interfaces/{id}` | Get interface details |
| POST | `/api/interfaces` | Create a new interface |
| PUT | `/api/interfaces/{id}` | Update interface configuration |
| DELETE | `/api/interfaces/{id}` | Delete an interface |
| POST | `/api/interfaces/{id}/bind` | Bind interface to hardware |
| POST | `/api/interfaces/{id}/unbind` | Unbind interface |
| POST | `/api/interfaces/{id}/enable` | Enable an interface |
| POST | `/api/interfaces/{id}/disable` | Disable an interface |
| GET | `/api/interfaces/health` | Health scores for all interfaces |

---

## Access Rules

Cisco ASA-style access control with implicit deny. Rules are evaluated top-to-bottom per interface.

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/access-rules` | List all access rules |
| POST | `/api/access-rules` | Create a new rule |
| PUT | `/api/access-rules/{id}` | Update a rule |
| DELETE | `/api/access-rules/{id}` | Delete a rule |
| POST | `/api/access-rules/{id}/enable` | Enable a rule |
| POST | `/api/access-rules/{id}/disable` | Disable a rule |
| POST | `/api/access-rules/reorder` | Reorder rule evaluation priority |
| GET | `/api/access-rules/{id}/stats` | Hit counters for a rule |

### Object Groups

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/object-groups` | List object groups |
| POST | `/api/object-groups` | Create an object group |
| PUT | `/api/object-groups/{id}` | Update an object group |
| DELETE | `/api/object-groups/{id}` | Delete an object group |

### Failover Groups

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/failover-groups` | List failover groups |
| POST | `/api/failover-groups` | Create a failover group |
| PUT | `/api/failover-groups/{id}` | Update a failover group |
| DELETE | `/api/failover-groups/{id}` | Delete a failover group |

---

## Iridium Satellite

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/iridium/signal` | Current signal strength (0-5 bars) |
| GET | `/api/iridium/signal/fast` | Quick signal check (cached) |
| GET | `/api/iridium/signal/history` | Signal strength over time |
| GET | `/api/iridium/credits` | Remaining satellite credits |
| POST | `/api/iridium/credits/budget` | Set credit budget/alerts |
| GET | `/api/iridium/passes` | Predicted satellite passes (SGP4) |
| POST | `/api/iridium/passes/refresh` | Force TLE refresh from Celestrak |
| GET | `/api/iridium/scheduler` | Pass scheduler state |
| POST | `/api/iridium/mailbox/check` | Force mailbox check (SBDIX) |
| GET | `/api/iridium/geolocation` | Iridium-derived geolocation |
| GET | `/api/iridium/geolocation/history` | Geolocation history |

### Iridium Queue

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/iridium/queue` | View outbound message queue |
| POST | `/api/iridium/queue` | Enqueue a message for satellite TX |
| POST | `/api/iridium/queue/{id}/cancel` | Cancel a queued message |
| DELETE | `/api/iridium/queue/{id}` | Remove from queue |
| POST | `/api/iridium/queue/{id}/priority` | Change queue priority |

---

## Astrocast LEO

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/astrocast/passes` | Predicted Astrocast passes |
| POST | `/api/astrocast/passes/refresh` | Force TLE refresh |

---

## Cellular / SMS

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/cellular/signal` | Current cellular signal strength |
| GET | `/api/cellular/signal/history` | Signal history |
| GET | `/api/cellular/status` | Modem registration and network status |
| POST | `/api/cellular/pin` | Set SIM PIN |
| GET | `/api/cellular/info` | Modem hardware info (IMEI, model) |
| GET | `/api/cellular/sms` | SMS inbox |
| POST | `/api/cellular/sms/send` | Send an SMS |
| GET | `/api/cellular/broadcasts` | Cell broadcast messages |
| POST | `/api/cellular/broadcasts/{id}/ack` | Acknowledge a broadcast |
| POST | `/api/cellular/data/connect` | Initiate data connection |
| POST | `/api/cellular/data/disconnect` | Disconnect data |
| GET | `/api/cellular/data/status` | Data connection status |
| GET | `/api/cellular/dyndns/status` | Dynamic DNS status |
| POST | `/api/cellular/dyndns/update` | Force DynDNS update |

### SIM Cards

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/cellular/sim-cards` | List registered SIM cards |
| POST | `/api/cellular/sim-cards` | Register a SIM card |
| PUT | `/api/cellular/sim-cards/{id}` | Update SIM card details |
| DELETE | `/api/cellular/sim-cards/{id}` | Remove a SIM card |
| GET | `/api/cellular/sim-cards/current` | Currently active SIM |

---

## Contacts

Unified address book supporting multiple transport addresses per contact.

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/contacts` | List all contacts |
| POST | `/api/contacts` | Create a contact |
| GET | `/api/contacts/lookup` | Lookup by address (phone, node ID, email) |
| GET | `/api/contacts/{id}` | Get contact details |
| PUT | `/api/contacts/{id}` | Update a contact |
| DELETE | `/api/contacts/{id}` | Delete a contact |
| POST | `/api/contacts/{id}/addresses` | Add an address to a contact |
| PUT | `/api/contacts/{id}/addresses/{aid}` | Update an address |
| DELETE | `/api/contacts/{id}/addresses/{aid}` | Remove an address |

---

## Delivery Ledger

Full lifecycle tracking for every message across all channels.

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/deliveries` | List deliveries with filters |
| GET | `/api/deliveries/stats` | Delivery statistics (success rate, avg latency) |
| GET | `/api/deliveries/{id}` | Get delivery details |
| POST | `/api/deliveries/{id}/cancel` | Cancel a pending delivery |
| POST | `/api/deliveries/{id}/retry` | Retry a failed delivery |
| GET | `/api/deliveries/message/{ref}` | All deliveries for a message reference |
| GET | `/api/loop-metrics` | Loop prevention counters |

---

## Device Registry (Hub)

IMEI-keyed device registry for managing field bridges.

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/device-registry` | List all registered devices |
| POST | `/api/device-registry` | Register a new device |
| GET | `/api/device-registry/{id}` | Get device details |
| PUT | `/api/device-registry/{id}` | Update device metadata |
| DELETE | `/api/device-registry/{id}` | Remove a device |
| GET | `/api/device-registry/{id}/config` | Get device configuration |
| PUT | `/api/device-registry/{id}/config` | Push configuration to device |
| GET | `/api/device-registry/{id}/config/versions` | Configuration version history |
| GET | `/api/device-registry/{id}/config/versions/{version}` | Get specific config version |
| POST | `/api/device-registry/{id}/config/rollback/{version}` | Rollback to previous version |

---

## Satellite Rate Limiting

Per-device rate limiting for paid satellite channels.

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/satellite/rate-limits` | List all rate limits |
| GET | `/api/satellite/rate-limits/{device_id}` | Get rate limit for device |
| PUT | `/api/satellite/rate-limits/{device_id}` | Set rate limit |
| DELETE | `/api/satellite/rate-limits/{device_id}` | Remove rate limit |
| POST | `/api/satellite/rate-limits/{device_id}/override` | Temporary override |
| POST | `/api/satellite/rate-limits/{device_id}/reset` | Reset rate limit counters |
| GET | `/api/satellite/usage` | Satellite usage across all devices |

---

## Configuration

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/config/export` | Export running config (Cisco-style YAML) |
| POST | `/api/config/import` | Import configuration |
| POST | `/api/config/diff` | Preview diff between running and uploaded config |

---

## Field Intelligence

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/deadman` | Dead man's switch status |
| POST | `/api/deadman` | Configure dead man's switch |
| GET | `/api/geofences` | List geofence zones |
| POST | `/api/geofences` | Create a geofence zone |
| DELETE | `/api/geofences/{id}` | Delete a geofence zone |
| GET | `/api/burst/status` | Satellite burst queue depth |
| POST | `/api/burst/flush` | Flush burst queue |
| POST | `/api/sos/activate` | Activate SOS |
| POST | `/api/sos/cancel` | Cancel SOS |
| GET | `/api/sos/status` | SOS status |

---

## Routing (Reticulum-inspired)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/routing/identity` | Local routing identity (Ed25519 public key) |
| GET | `/api/routing/destinations` | Known remote destinations |
| POST | `/api/links` | Establish a link to a remote node |
| GET | `/api/links` | List active links |
| DELETE | `/api/links/{id}` | Tear down a link |

---

## Backup & Restore

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/backup/create` | Create a full backup (config + rules + devices) |
| GET | `/api/backup/list` | List available backups |
| GET | `/api/backup/download/{filename}` | Download a backup archive |
| POST | `/api/backup/preview` | Preview what a restore would change |
| POST | `/api/backup/restore` | Restore from backup |
| GET | `/api/backup/schedule` | Get auto-backup schedule |
| PUT | `/api/backup/schedule` | Set auto-backup schedule |

---

## Audit & Security

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/audit` | Audit log (Ed25519 signed, hash-chained) |
| GET | `/api/audit/verify` | Verify audit log integrity |
| GET | `/api/audit/signer` | Get signer public key |
| POST | `/api/crypto/generate-key` | Generate AES-256-GCM encryption key |
| POST | `/api/crypto/validate-transforms` | Validate a transform chain |

---

## Presets & Canned Messages

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/presets` | List preset messages |
| POST | `/api/presets` | Create a preset |
| PUT | `/api/presets/{id}` | Update a preset |
| DELETE | `/api/presets/{id}` | Delete a preset |
| POST | `/api/presets/{id}/send` | Send a preset message |
| GET | `/api/canned-messages` | List canned message codebook |
| POST | `/api/canned-messages` | Update canned messages |

---

## Webhooks

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/webhooks/inbound` | Receive inbound webhook |
| POST | `/api/webhook/rockblock` | RockBLOCK MO webhook receiver |
| GET | `/api/webhooks/log` | Webhook delivery log |

---

## SSE Events Stream

Connect to `GET /api/events` for real-time Server-Sent Events:

```bash
curl -N https://hub.example.com/api/events
```

Event types include: `message`, `position`, `telemetry`, `node_update`, `gateway_status`, `interface_state`, `delivery_update`, `sos`, `geofence_alert`, `deadman_alert`.

---

## Error Responses

All errors follow a consistent format:

```json
{
  "error": "human-readable error message"
}
```

Common HTTP status codes:

| Code | Meaning |
|------|---------|
| 200 | Success |
| 400 | Bad request (invalid parameters) |
| 404 | Resource not found |
| 409 | Conflict (duplicate, already exists) |
| 500 | Internal server error |
| 503 | Service unavailable (subsystem not initialized) |
