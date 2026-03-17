---
title: "Webhooks"
weight: 8
---

# Webhook HTTP

MeshSat can send outbound HTTP POST/PUT requests and receive inbound webhooks for integration with external services.

## Configuration

```json
{
  "outbound_url": "https://example.com/api/meshsat",
  "outbound_method": "POST",
  "outbound_headers": {
    "Authorization": "Bearer <token>"
  },
  "inbound_enabled": true,
  "inbound_secret": "your-webhook-secret",
  "retry_count": 5,
  "timeout_sec": 10
}
```

## Inbound Webhooks

POST to `http://<meshsat>:6050/api/webhook/inbound` with the shared secret to inject messages into the mesh.

## Outbound Payload

```json
{
  "from": "!aabbccdd",
  "to": "!ffffffff",
  "channel": 0,
  "portnum": 1,
  "text": "Hello from the mesh",
  "timestamp": "2026-03-17T12:00:00Z",
  "source": "meshsat"
}
```
