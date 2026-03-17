---
title: "AIOC (All-In-One Cable)"
weight: 3
---

# AIOC — All-In-One Cable

The AIOC connects Baofeng, Quansheng, and other handheld radios to a computer via USB-C. It enumerates as both a soundcard (for audio/modem) and a serial port (for PTT control).

## How It Works

```
Radio audio jack (K1/K2) ←→ AIOC ←→ USB-C ←→ Raspberry Pi
```

The AIOC provides:
- **Soundcard**: For Direwolf to encode/decode 1200 baud AFSK APRS packets
- **Serial port**: For PTT (push-to-talk) control via RTS/DTR

## Firmware

Flash the AIOC firmware from [github.com/skuep/AIOC](https://github.com/skuep/AIOC).

## Integration with MeshSat

See the [APRS channel page](/channels/aprs/) for the full Direwolf + MeshSat setup.
