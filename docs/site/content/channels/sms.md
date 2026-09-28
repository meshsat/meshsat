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

### A modem that belongs to ModemManager (Linux phones)

On a Linux phone such as the PinePhone Pro under Mobian, and on any host where
ModemManager and NetworkManager own the modem, the serial port cannot be shared.
Point the bridge at ModemManager instead:

```bash
MESHSAT_CELLULAR_PORT=modemmanager
```

The bridge then takes SMS (sent and received), signal, SIM state, registration,
operator, IMEI, own number and cell info from ModemManager over the system bus;
mobile data stays NetworkManager's. Sending and control are polkit-protected, so
the user the bridge runs as needs a rule such as
`/etc/polkit-1/rules.d/50-meshsat.rules`:

```javascript
polkit.addRule(function(action, subject) {
    if (action.id.indexOf("org.freedesktop.ModemManager1.") === 0 && subject.user === "meshsat") {
        return polkit.Result.YES;
    }
});
```

`/api/cellular/status` reports `sim_state: NOT_INSERTED` while the phone has no SIM,
`PIN_REQUIRED` while it is locked (`MESHSAT_SIM_PIN` unlocks it), and a send is
refused with the reason until the modem is registered. AT passthrough
(`/api/cellular/at`) works only when ModemManager runs with `--debug`.

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
