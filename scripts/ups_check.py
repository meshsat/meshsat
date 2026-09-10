#!/usr/bin/env python3
"""Read the Geekworm X1202 UPS (MAX17040 fuel gauge) on I2C bus 1, addr 0x36.

Prints the raw register and, when the x1202-monitor has learned this
pack's full point (MESHSAT-1018, /var/lib/x1202/full_soc.json), the
scaled percentage the bridge reports.  The raw register plateaus at
91 to 98 % on a full pack because the X1202 lets the cell settle
below the gauge model's 4.2 V after terminating at 4.23 V.

Installed by hand on both kits at /usr/local/bin/ups_check.py.
"""
import json
import subprocess
import sys

FULL_PATH = "/var/lib/x1202/full_soc.json"


def i2cget(bus, addr, reg):
    r = subprocess.run(["i2cget", "-y", str(bus), hex(addr), str(reg), "w"],
                       capture_output=True, text=True)
    if r.returncode != 0:
        return None
    return int(r.stdout.strip(), 16)


def learned_full():
    try:
        with open(FULL_PATH) as f:
            return float(json.load(f)["full_soc"])
    except Exception:
        return None


vcell_raw = i2cget(1, 0x36, 2)
soc_raw = i2cget(1, 0x36, 4)

if vcell_raw is None or soc_raw is None:
    print("No MAX17040 at 0x36 on I2C bus 1")
    sys.exit(1)

# i2cget -w returns little-endian on ARM: swap bytes
vcell_be = ((vcell_raw & 0xFF) << 8) | ((vcell_raw >> 8) & 0xFF)
soc_be = ((soc_raw & 0xFF) << 8) | ((soc_raw >> 8) & 0xFF)

vcell_mv = (vcell_be >> 4) * 1.25
soc_pct = soc_be / 256.0

print(f"Voltage: {vcell_mv:.0f} mV ({vcell_mv/1000:.2f} V)")
full = learned_full()
if full:
    shown = min(100.0, soc_pct / full * 100.0)
    print(f"SOC: {shown:.0f}% (raw register {soc_pct:.1f}%, learned full point {full:.1f}%)")
else:
    print(f"SOC: {soc_pct:.0f}% (raw register, no learned full point yet)")
