#!/usr/bin/env python3
"""X1202 UPS battery monitor with safe shutdown.

Fixes:
- 120s boot grace period (no shutdown during early boot)
- MAX17040 quickstart on startup (recalibrates SOC)
- SOC validation (cross-check voltage vs SOC)
- I2C error tolerance (log, don't shutdown)
- Learned full point (MESHSAT-1018): the MAX17040's generic cell
  model puts 100 % near 4.2 V, but the X1202 terminates at 4.23 V
  and lets the cell node settle to 4.12 to 4.17 V, so the raw SOC
  register plateaus at 91 to 98 % on a full pack.  When mains is
  present, the cell is above the charger's 4.1 V recharge
  threshold and the raw SOC has not moved for FULL_STABLE_SEC, the
  charger has terminated: that raw value is stored as this pack's
  100 % and every reading is scaled by it.  Shutdown decisions keep
  using the raw register.
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
FULL_PATH = "/var/lib/x1202/full_soc.json"
FULL_MIN_VOLTAGE = 4.10   # X1202 recharge threshold: below it the charger is charging again
FULL_STABLE_SEC = 1200    # raw SOC unchanged for this long on mains = charge terminated
FULL_SOC_FLOOR = 85.0     # never accept a plateau below this as "full"
FULL_JITTER = 0.25        # register dither below this does not break a plateau

def log(msg):
    print("x1202: %s" % msg, flush=True)


def load_full():
    """Learned full point (raw SOC percent) or None."""
    try:
        with open(FULL_PATH) as f:
            v = float(json.load(f)["full_soc"])
        if FULL_SOC_FLOOR <= v <= 100.0:
            return v
    except Exception:
        pass
    return None


def save_full(full, voltage):
    try:
        os.makedirs(os.path.dirname(FULL_PATH), exist_ok=True)
        tmp = FULL_PATH + ".tmp"
        with open(tmp, "w") as f:
            json.dump({"full_soc": round(full, 2), "voltage": round(voltage, 3),
                       "learned_at": time.time()}, f)
        os.replace(tmp, FULL_PATH)
    except Exception as e:
        log("full-point save failed: %s" % e)


def scale_soc(soc, full):
    """Raw register percent -> displayed percent, clamped to 100."""
    if soc is None:
        return None
    if not full:
        return soc
    return min(100.0, soc / full * 100.0)


class FullLearner:
    """Detects the charge-terminated plateau and remembers it."""

    def __init__(self):
        self.full = load_full()
        self.last_soc = None
        self.stable_since = None
        if self.full is not None:
            log("full point loaded: raw %.1f%% = 100%%" % self.full)

    def update(self, voltage, soc, ac, now):
        if voltage is None or soc is None or ac != "1" or voltage < FULL_MIN_VOLTAGE:
            self.last_soc, self.stable_since = None, None
            return self.full
        if self.last_soc is None or abs(soc - self.last_soc) >= FULL_JITTER:
            self.last_soc, self.stable_since = soc, now
            return self.full
        if now - self.stable_since < FULL_STABLE_SEC:
            return self.full
        if soc < FULL_SOC_FLOOR:
            return self.full
        if self.full is None or abs(soc - self.full) >= 0.5:
            self.full = soc
            save_full(soc, voltage)
            log("full point learned: raw %.1f%% at %.2fV = 100%%" % (soc, voltage))
        return self.full


def write_status(voltage, soc, ac, soc_raw=None, full=None):
    """Write current state to /run/x1202.json for the bridge API
    to read.  MUST write in-place (open+truncate) rather than
    atomic-rename: the bridge container bind-mounts this file as
    a single-file mount (`/run/x1202.json:/run/x1202.json:ro`),
    and a single-file mount pins the inode at container-start
    time.  os.replace() swaps to a fresh inode, leaving the
    container staring at the original stale one forever.

    Payload is ~90 bytes so a partial-read race is vanishingly
    unlikely; the bridge handler tolerates JSON-parse errors
    anyway."""
    try:
        payload = {
            "voltage": round(voltage, 3) if voltage is not None else None,
            "soc_percent": round(soc, 1) if soc is not None else None,
            "soc_raw": round(soc_raw, 1) if soc_raw is not None else None,
            "full_soc": round(full, 1) if full else None,
            "ac_present": (ac == "1") if ac in ("0", "1") else None,
            "last_update": time.time(),
        }
        with open(STATUS_PATH, "w") as f:
            json.dump(payload, f)
            f.flush()
            os.fsync(f.fileno())
        try:
            os.chmod(STATUS_PATH, 0o644)
        except OSError:
            pass
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
    learner = FullLearner()

    while True:
        voltage, soc = read_battery()
        ac = read_ac()
        now = time.time()
        full = learner.update(voltage, soc, ac, now)
        shown = scale_soc(soc, full)
        write_status(voltage, shown, ac, soc_raw=soc, full=full)
        uptime = now - start_time
        in_grace = uptime < BOOT_GRACE_SEC

        # Periodic status log
        if now - last_log >= LOG_INTERVAL:
            v_str = "%.2fV" % voltage if voltage else "ERR"
            s_str = "%.1f%%" % shown if shown is not None else "ERR"
            if soc is not None and full:
                s_str += " (raw %.1f%%, full %.1f%%)" % (soc, full)
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
