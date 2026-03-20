---
title: "Cellular SMS"
weight: 6
---

# Cellular SMS

MeshSat sends and receives SMS messages via a USB cellular modem (e.g., Huawei E220) using AT commands.

## Configuration

```bash
MESHSAT_CELLULAR_PORT=auto  # or /dev/ttyUSB1
```

## Supported Modems

Any USB modem that supports AT+CMGS (send SMS) and AT+CMGR (read SMS):
- Huawei E220 (tested)
- Huawei E3372
- Quectel EC25/EG25
- SIMCom SIM7600
- LILYGO T-Call A7670E (tested, requires ATdebug firmware)

## Features

- Inbound SMS listener with configurable polling
- Outbound SMS with retry
- Contact management (name → phone number mapping)
- DynDNS updater for publishing gateway address
