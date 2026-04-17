# AIOC Setup for MeshSat APRS

As of **MESHSAT-514** the Bridge image bundles Direwolf and supervises it as
a subprocess. Field-kit setup on the host is now just the AIOC firmware flash.

## Hardware chain

```
Baofeng UV-5R / Quansheng UV-K5
        |
   K1/K2 audio jack
        |
   AIOC (All-In-One Cable)
        |
   USB-C to Raspberry Pi
        |
   MeshSat container (bundled Direwolf -> loopback KISS -> APRS gateway)
```

## 1. AIOC firmware (only host-side step)

The AIOC enumerates as a USB soundcard + virtual serial port.

1. Download firmware: https://github.com/skuep/AIOC/releases
2. Flash via STM32 DFU: hold button, plug USB, `dfu-util -D aioc-firmware.bin`
3. Unplug/replug. Verify on the host:
   - `arecord -l` lists a card with `AllInOneCable` in the name
   - `ls /dev/ttyACM*` shows the PTT serial port

No `apt install direwolf`, no `/etc/direwolf.conf`, no systemd unit, no
udev rule. MeshSat renders Direwolf's config from its own `APRSConfig` on
every start and writes it to a tmpfs inside the container.

## 2. Configure the APRS gateway

Dashboard: Bridge > Interfaces > APRS. Required fields:

| Field | Typical value |
|-------|---------------|
| Callsign | your licensed callsign |
| SSID | 10 (convention for IGate) |
| Audio card | `AllInOneCable` (default) |
| PTT device | `/dev/ttyACM1` (default) |
| PTT line | `RTS` (default) |
| Modem baud | `1200` (AFSK) |
| Frequency | 144.800 MHz (EU) / 144.390 MHz (US) |

Env-var equivalents: `MESHSAT_APRS_CALLSIGN`, `MESHSAT_APRS_SSID`,
`MESHSAT_APRS_AUDIO_CARD`, `MESHSAT_APRS_PTT_DEVICE`,
`MESHSAT_APRS_PTT_LINE`, `MESHSAT_APRS_MODEM_BAUD`.

## 3. Verify

| Check | Expected |
|-------|----------|
| `curl http://<kit>:6050/api/status \| jq .gateways.aprs` | `connected: true`, `direwolf_bundled: true`, `direwolf_running: true` |
| `docker exec meshsat pgrep -a direwolf` | one running process |
| Tune UV-K5 to APRS frequency, key a nearby station | inbound frame appears in MeshSat logs within a few seconds |

## External-Direwolf mode (legacy)

If for any reason you need the old host-side path (debugging, non-AIOC audio),
set `APRSConfig.ExternalDirewolf = true` (or `MESHSAT_APRS_EXTERNAL_DIREWOLF=1`).
MeshSat skips the supervisor and connects to whatever KISS server is
listening on `KISSHost:KISSPort`. You are then responsible for running
Direwolf yourself.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `direwolf_running: false`, `connected: false` | AIOC unplugged or wrong ALSA card | `arecord -l`; set correct `AudioCard` |
| `direwolf_running: true`, `connected: false` | AIOC serial PTT device wrong | check `ls /dev/ttyACM*`; set `PTTDevice` |
| Container log spammed with `direwolf-preflight: AIOC not found` | USB cable / power / firmware | re-seat AIOC, reflash firmware |
| No packets RX | wrong frequency / antenna / squelch | verify radio tuning and volume |

## Dual-AIOC (two radios)

Not yet supported with bundled Direwolf (single TNC per container). Open
an issue against MESHSAT if this matters — the fix is one Direwolf instance
per AIOC with distinct `KISSPORT`s and two `APRSGateway` instances.
