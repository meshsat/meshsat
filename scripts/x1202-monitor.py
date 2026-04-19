#!/usr/bin/env python3
"""X1202 UPS battery monitor with safe shutdown.

Fixes:
- 120s boot grace period (no shutdown during early boot)
- MAX17040 quickstart on startup (recalibrates SOC)
- SOC validation (cross-check voltage vs SOC)
- I2C error tolerance (log, don't shutdown)
"""
import smbus
import subprocess
import time
import sys
import os
import json
import tempfile

I2C_BUS = 1
ADDR = 0x36
BOOT_GRACE_SEC = 120
LOW_VOLTAGE = 3.20
LOW_CAPACITY = 5
AC_LOSS_GRACE_SEC = 86400  # disabled until GPIO6 wired
POLL_SEC = 10
LOG_INTERVAL = 300
STATUS_PATH = "/run/x1202.json"

def log(msg):
    print("x1202: %s" % msg, flush=True)


def write_status(voltage, soc, ac):
    """Atomic-write current state to /run/x1202.json for the bridge
    API to read. Tolerate write errors silently — monitoring job
    must not crash on disk issue."""
    try:
        payload = {
            "voltage": round(voltage, 3) if voltage is not None else None,
            "soc_percent": round(soc, 1) if soc is not None else None,
            "ac_present": (ac == "1") if ac in ("0", "1") else None,
            "last_update": time.time(),
        }
        fd, tmp = tempfile.mkstemp(dir="/run", prefix=".x1202.")
        with os.fdopen(fd, "w") as f:
            json.dump(payload, f)
        os.chmod(tmp, 0o644)
        os.replace(tmp, STATUS_PATH)
    except Exception as e:
        log("status-write failed: %s" % e)

def quickstart():
    """Send quickstart command to recalibrate SOC."""
    try:
        bus = smbus.SMBus(I2C_BUS)
        bus.write_word_data(ADDR, 0x06, 0x4000)
        bus.close()
        log("quickstart sent — SOC will recalibrate")
    except Exception as e:
        log("quickstart failed: %s" % e)

def read_battery():
    """Read voltage and SOC from MAX17040."""
    try:
        bus = smbus.SMBus(I2C_BUS)
        vdata = bus.read_i2c_block_data(ADDR, 0x02, 2)
        sdata = bus.read_i2c_block_data(ADDR, 0x04, 2)
        bus.close()
        voltage = ((vdata[0] << 8 | vdata[1]) >> 4) * 0.00125
        soc = sdata[0] + sdata[1] / 256.0
        return voltage, soc
    except Exception as e:
        log("I2C read error: %s" % e)
        return None, None

def voltage_to_soc_estimate(v):
    """Rough voltage-based SOC for validation."""
    if v >= 4.15: return 100
    if v <= 3.20: return 0
    return int((v - 3.20) / (4.15 - 3.20) * 100)

def read_ac():
    """Read AC power status via GPIO 6."""
    try:
        r = subprocess.run(
            ["gpioget", "--bias=pull-up", "gpiochip4", "6"],
            capture_output=True, text=True, timeout=5
        )
        return r.stdout.strip()
    except Exception:
        return "unknown"

def shutdown(reason):
    log("SHUTDOWN: %s" % reason)
    subprocess.run(["sudo", "shutdown", "-h", "now"])
    sys.exit(0)

def main():
    log("started (grace period %ds)" % BOOT_GRACE_SEC)
    start_time = time.time()

    # Quickstart to recalibrate SOC after power loss
    quickstart()
    time.sleep(5)

    ac_lost_since = None
    last_log = 0

    while True:
        voltage, soc = read_battery()
        ac = read_ac()
        write_status(voltage, soc, ac)
        now = time.time()
        uptime = now - start_time
        in_grace = uptime < BOOT_GRACE_SEC

        # Periodic status log
        if now - last_log >= LOG_INTERVAL:
            v_str = "%.2fV" % voltage if voltage else "ERR"
            s_str = "%.1f%%" % soc if soc is not None else "ERR"
            grace_str = " [GRACE]" if in_grace else ""
            log("status: %s, %s, AC=%s%s" % (v_str, s_str, ac, grace_str))
            last_log = now

        # Skip shutdown decisions during grace period
        if in_grace:
            time.sleep(POLL_SEC)
            continue

        # Skip shutdown on I2C read failure
        if voltage is None:
            time.sleep(POLL_SEC)
            continue

        # Voltage-based shutdown (always trusted)
        if voltage < LOW_VOLTAGE:
            shutdown("low voltage %.2fV" % voltage)

        # SOC-based shutdown (validated against voltage)
        if soc is not None and soc < LOW_CAPACITY:
            v_estimate = voltage_to_soc_estimate(voltage)
            if v_estimate < LOW_CAPACITY + 10:
                shutdown("low SOC %.1f%% (voltage %.2fV confirms)" % (soc, voltage))
            else:
                log("SOC %.1f%% looks wrong (voltage %.2fV suggests ~%d%%) — ignoring" % (soc, voltage, v_estimate))

        # AC loss tracking (log only, never shutdown on AC loss)
        if ac == "0":
            if ac_lost_since is None:
                ac_lost_since = now
                log("AC power lost (logging only, no shutdown)")
        else:
            if ac_lost_since is not None:
                log("AC power restored")
                ac_lost_since = None

        time.sleep(POLL_SEC)

if __name__ == "__main__":
    main()
