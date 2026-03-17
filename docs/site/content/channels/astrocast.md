---
title: "Astrocast"
weight: 3
---

# Astrocast

Astrocast is a LEO satellite IoT network. MeshSat supports the Astronode S module via binary UART protocol (not AT commands).

## Status

Code complete. Awaiting physical Astronode S module for integration testing.

## Specifications

- **Uplink**: 160 bytes per message
- **Downlink**: 40 bytes per message
- **Protocol**: Binary UART at 9600 baud
- **Fragmentation**: Auto-fragment for payloads >160 bytes (1-byte header: MSG_ID:4bit + FRAG_NUM:2bit + FRAG_TOTAL:2bit)

## Configuration

```bash
MESHSAT_ASTROCAST_PORT=auto
```
