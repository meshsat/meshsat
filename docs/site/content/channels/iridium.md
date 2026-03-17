---
title: "Iridium SBD"
weight: 2
---

# Iridium SBD (RockBLOCK 9603N)

Iridium Short Burst Data provides global satellite coverage for messages up to 340 bytes. MeshSat drives the RockBLOCK 9603N modem via AT commands over serial.

## Configuration

```bash
MESHSAT_IRIDIUM_PORT=auto  # or /dev/ttyUSB0
```

Gateway config (via Bridge > Interfaces in the dashboard):

```json
{
  "poll_interval_sec": 60,
  "include_gps": false,
  "pass_scheduling_enabled": true
}
```

## Key Constraints

- **340-byte MO buffer**: Messages larger than 340 bytes must be compressed or split
- **Serial mutex**: The 9603N only handles one AT command at a time. SBDIX takes 11-62 seconds
- **3-minute backoff**: After a failed send (mo_status=32/36), wait at least 3 minutes before retrying. Aggressive retries cause registration death spirals
- **Credit-based billing**: Each MO/MT message costs credits via Ground Control

## Pass Scheduling

MeshSat includes SGP4 TLE-based satellite pass prediction. When enabled, the gateway:
1. Pre-wakes the modem before a pass
2. Sends queued messages during the active pass window
3. Sleeps the modem after the pass to save power

## DLQ (Dead Letter Queue)

Failed sends enter the DLQ with ISU-aware exponential backoff:
- `mo_status=32/36` (no network): 3-minute initial backoff
- `mo_status=35` (low signal): 30-second initial backoff
- Maximum backoff: 30 minutes
- Maximum retries: configurable (default 10)
