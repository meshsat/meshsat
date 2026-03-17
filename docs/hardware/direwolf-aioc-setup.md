# Direwolf + AIOC Setup for MeshSat APRS

## Hardware Chain

```
Baofeng UV-5R / Quansheng UV-K5
         │
    K1/K2 audio jack
         │
    AIOC (All-In-One Cable)
         │
    USB-C to Raspberry Pi
         │
    Direwolf (software TNC)
         │
    KISS TCP :8001
         │
    MeshSat APRS Gateway
```

## 1. AIOC Firmware

The AIOC (All-In-One Cable) enumerates as a USB soundcard + virtual serial port.

1. Download firmware from https://github.com/skuep/AIOC/releases
2. Flash with STM32 DFU: hold button → plug USB → `dfu-util -D aioc-firmware.bin`
3. After flashing, unplug and replug. Device should appear as:
   - ALSA soundcard: `hw:X,0` (check with `arecord -l`)
   - Serial port: `/dev/ttyACMX` (for PTT control)

## 2. Direwolf Installation

```bash
# Debian/Ubuntu/Raspberry Pi OS
sudo apt update && sudo apt install -y direwolf

# Verify
direwolf --version
```

## 3. Direwolf Configuration

Create `/etc/direwolf.conf`:

```conf
# AIOC audio device — adjust number to match your AIOC
# Find with: arecord -l | grep AIOC
ADEVICE plughw:1,0

# PTT via AIOC serial port — adjust device path
PTT /dev/ttyACM1 RTS

# Channel 0 configuration
CHANNEL 0
MYCALL PA3XYZ-10        # Your callsign, SSID 10 = IGate
MODEM 1200              # Standard APRS (1200 baud AFSK)

# KISS TCP interface for MeshSat
KISSPORT 8001

# Optional: APRS digipeater (uncomment to enable)
# DIGIPEAT 0 0 ^WIDE[3-7]-[1-7]$|^TEST$ ^WIDE[12]-[12]$ TRACE
```

**Important:**
- `MYCALL` must be your actual amateur radio callsign
- SSID 10 is conventional for IGates
- `plughw:1,0` — the `1` may vary; find your AIOC with `arecord -l`

## 4. Start Direwolf

```bash
# Foreground (for testing)
direwolf -t 0 -c /etc/direwolf.conf

# As a systemd service
sudo tee /etc/systemd/system/direwolf.service << 'EOF'
[Unit]
Description=Direwolf TNC
After=sound.target

[Service]
ExecStart=/usr/bin/direwolf -t 0 -c /etc/direwolf.conf
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl enable --now direwolf
```

## 5. Configure MeshSat APRS Gateway

In the MeshSat web dashboard (Bridge > Interfaces), create an APRS interface:

```json
{
  "callsign": "PA3XYZ",
  "ssid": 10,
  "kiss_host": "localhost",
  "kiss_port": 8001,
  "frequency_mhz": 144.800,
  "aprs_is_enabled": false
}
```

Or set environment variables:

```bash
# In docker-compose.yml or .env
MESHSAT_APRS_CALLSIGN=PA3XYZ
MESHSAT_APRS_SSID=10
MESHSAT_APRS_KISS_HOST=localhost
MESHSAT_APRS_KISS_PORT=8001
```

## 6. Verify

1. Direwolf should show `Ready to prior frames on prior 0` in its log
2. MeshSat dashboard should show APRS interface as "online"
3. Any local APRS station transmitting on 144.800 MHz should appear as an inbound message in MeshSat

## Dual AIOC Setup

MeshSat supports two AIOC devices simultaneously (e.g., one for APRS, one for a different frequency):

1. Each AIOC gets its own Direwolf instance on a different KISS port
2. Configure two APRS interfaces in MeshSat with different `kiss_port` values
3. Each interface has its own callsign + SSID

```bash
# Direwolf instance 1: APRS on 144.800
direwolf -t 0 -c /etc/direwolf-aprs.conf     # KISSPORT 8001

# Direwolf instance 2: alternate frequency
direwolf -t 0 -c /etc/direwolf-alt.conf       # KISSPORT 8002
```

## Troubleshooting

| Issue | Fix |
|-------|-----|
| No audio device | Check `arecord -l` for AIOC. May need `pulseaudio --kill` first |
| PTT not keying | Verify `/dev/ttyACMX` path matches AIOC serial port |
| No packets decoded | Verify frequency matches local APRS (EU: 144.800, US: 144.390) |
| KISS connection refused | Direwolf may not be running, or `KISSPORT` not set in config |
| MeshSat APRS "offline" | Check `ss -tlnp | grep 8001` to verify Direwolf is listening |

## EU vs US Frequencies

| Region | APRS Frequency |
|--------|---------------|
| Europe | 144.800 MHz |
| North America | 144.390 MHz |
| Australia | 145.175 MHz |
| Japan | 144.660 MHz |
