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
- Halt before the brownout (MESHSAT-794): on 4 Sep 2026 both kits
  browned out instead of halting.  parallax sat at 2.0 to 2.4 % raw
  SOC and 3.50 V while a linear 3.20-4.15 V map called that "31 %"
  and vetoed the SOC rule; the 3.20 V backstop sat below the X1202
  output cut-off (tesseract's last reading was 3.05 V).  The voltage
  map is now a discharge curve under kit load, every reading casts a
  vote, 3 of the last 4 votes halt, and one reading under
  HARD_LOW_VOLTAGE halts at once.
- Input present but insufficient (MESHSAT-794): ChargeWatch flags a
  pack that keeps draining over 30 min while mains is reported, and
  /run/x1202.json carries "charging" and "input_insufficient".

Dry run on a kit (no gauge write, no status file, no shutdown):
    x1202-monitor.py --simulate scripts/testdata/x1202-traces.json \\
        --trace parallax_2026-09-04_brownout
"""
import argparse
import collections
import json
import os
import subprocess
import sys
import time

try:
    import smbus
except ImportError:  # dev machines and the unit tests have no I2C
    smbus = None

I2C_BUS = 1
ADDR = 0x36
BOOT_GRACE_SEC = 120
AC_LOSS_GRACE_SEC = 86400  # disabled until GPIO6 wired
POLL_SEC = 10
LOG_INTERVAL = 300
STATUS_PATH = "/run/x1202.json"
FULL_PATH = "/var/lib/x1202/full_soc.json"
FULL_MIN_VOLTAGE = 4.10   # X1202 recharge threshold: below it the charger is charging again
FULL_STABLE_SEC = 1200    # raw SOC unchanged for this long on mains = charge terminated
FULL_SOC_FLOOR = 85.0     # never accept a plateau below this as "full"
FULL_JITTER = 0.25        # register dither below this does not break a plateau

# Shutdown decision (MESHSAT-794).  Every SOC threshold is compared with
# the RAW gauge register, never the scaled soc_percent.
HARD_LOW_VOLTAGE = 3.30     # one valid reading below this halts at once, when the gauge agrees
HARD_FLOOR_MAX_SOC = 20.0   # ... i.e. raw SOC below this; otherwise it is one halt vote
MIN_VALID_VOLTAGE = 2.50    # a 1S cell powering the Pi never reads below this: a bus glitch
LOW_VOLTAGE = 3.45          # a reading below this is a halt vote
LOW_CAPACITY = 5            # raw SOC below this is a halt vote when the voltage agrees
TRUST_GAUGE_BELOW_V = 3.60  # below this voltage an empty gauge is believed
CURVE_LOW_SOC = 15          # ... and above it only when the curve also says < this
HALT_WINDOW = 4             # valid readings kept for the vote
HALT_VOTES = 3              # halt votes within HALT_WINDOW that halt the kit

# Cell voltage under kit load -> SOC percent, piecewise linear.
SOC_CURVE = (
    (3.20, 0.0),
    (3.45, 10.0),
    (3.55, 20.0),
    (3.65, 35.0),
    (3.75, 50.0),
    (3.90, 70.0),
    (4.05, 90.0),
    (4.20, 100.0),
)

# Charge watch (MESHSAT-794).
CHARGE_WINDOW_SEC = 1800    # raw SOC history kept while mains is present
CHARGE_DROP_POINTS = 2.0    # fall from the window's max that means the input cannot keep up
CHARGE_RISE_POINTS = 0.5    # rise above the episode's low that ends an episode
CHARGE_AC_GONE_SEC = 60     # mains absent this long resets the window (shorter gaps are
                            # the AC-detect flag flapping on a weak charger)

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
    """Detects the charge-terminated plateau and remembers it.

    persist=False (the simulator) never reads or writes FULL_PATH and
    starts from the given full point instead."""

    def __init__(self, persist=True, full=None):
        self.persist = persist
        self.full = load_full() if persist else full
        self.last_soc = None
        self.stable_since = None
        if self.persist and self.full is not None:
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
            if self.persist:
                save_full(soc, voltage)
            log("full point learned: raw %.1f%% at %.2fV = 100%%" % (soc, voltage))
        return self.full


def soc_from_voltage(v):
    """Cell voltage -> SOC percent along SOC_CURVE, clamped to 0..100."""
    if v <= SOC_CURVE[0][0]:
        return SOC_CURVE[0][1]
    if v >= SOC_CURVE[-1][0]:
        return SOC_CURVE[-1][1]
    for (v0, s0), (v1, s1) in zip(SOC_CURVE, SOC_CURVE[1:]):
        if v <= v1:
            return max(0.0, min(100.0, s0 + (v - v0) / (v1 - v0) * (s1 - s0)))
    return SOC_CURVE[-1][1]


def reading_verdict(voltage, soc):
    """One valid reading -> ("halt" | "warn" | "ok", why).

    soc is the RAW register percent (or None)."""
    if voltage < LOW_VOLTAGE:
        return "halt", "voltage %.2fV below %.2fV" % (voltage, LOW_VOLTAGE)
    if soc is not None and soc < LOW_CAPACITY:
        curve = soc_from_voltage(voltage)
        if voltage < TRUST_GAUGE_BELOW_V:
            return "halt", "raw SOC %.1f%% at %.2fV (gauge trusted below %.2fV)" % (
                soc, voltage, TRUST_GAUGE_BELOW_V)
        # With the curve above, voltage >= TRUST_GAUGE_BELOW_V already puts
        # the curve over CURVE_LOW_SOC; the check stays in case either moves.
        if curve < CURVE_LOW_SOC:
            return "halt", "raw SOC %.1f%% at %.2fV (curve ~%.0f%%)" % (soc, voltage, curve)
        return "warn", "SOC %.1f%% looks wrong (voltage %.2fV suggests ~%.0f%%), not halting on it" % (
            soc, voltage, curve)
    return "ok", ""


Decision = collections.namedtuple("Decision", "action reason")  # action: none | warn | halt


class ShutdownDecider:
    """Votes over the last HALT_WINDOW valid readings.  All state is in
    self.history; a failed read (voltage None) neither adds a vote nor
    clears the history."""

    def __init__(self, window=HALT_WINDOW, votes=HALT_VOTES):
        self.history = collections.deque(maxlen=window)
        self.votes = votes
        self.last_halt_why = ""

    def update(self, voltage, soc):
        if voltage is None:
            return Decision("none", "no reading")
        if voltage < MIN_VALID_VOLTAGE:
            # An I2C read that returns zeros must not halt a healthy kit on
            # mains: treat it like a failed read.
            return Decision("none", "implausible reading %.2fV ignored" % voltage)
        verdict, why = reading_verdict(voltage, soc)
        self.history.append(verdict)
        if verdict == "halt":
            self.last_halt_why = why
        # The hard floor skips the vote only when the gauge agrees the pack is
        # low, so one glitched voltage cannot halt a full pack. A real
        # discharge crosses 3.45 V first and halts on the vote well before.
        if voltage < HARD_LOW_VOLTAGE and (soc is None or soc < HARD_FLOOR_MAX_SOC):
            return Decision("halt", "voltage %.2fV below the hard floor %.2fV" % (voltage, HARD_LOW_VOLTAGE))
        n = sum(1 for v in self.history if v == "halt")
        if n >= self.votes:
            return Decision("halt", "%d of the last %d readings voted halt, last: %s" % (
                n, len(self.history), self.last_halt_why))
        if verdict == "warn":
            return Decision("warn", why)
        if verdict == "halt":
            return Decision("none", "halt vote %d of %d needed: %s" % (n, self.votes, why))
        return Decision("none", "")


class ChargeWatch:
    """Raw SOC over the last CHARGE_WINDOW_SEC with mains present.

    input_insufficient: mains present, a full window, raw SOC down
    CHARGE_DROP_POINTS from the window's max to the latest sample, and the
    pack below FULL_MIN_VOLTAGE when the voltage is known. A full pack
    rests after the charger terminates and the gauge's raw reading drifts
    down (parallax 21 Sep 2026: 101.9 -> 98.9 % over an hour at a flat
    4.17 V, flagged "draining on mains" with a healthy 12 V supply); a pack
    the input cannot carry sags below the recharge threshold instead.  It
    ends when SOC rises CHARGE_RISE_POINTS above the episode's low
    (a new episode then needs a fresh full window) or when mains has
    been gone for CHARGE_AC_GONE_SEC.

    charging: False with ac "0" or while input_insufficient; True with
    ac "1" when SOC is at or above the learned full point, or the window
    is full and SOC is at the window's max (within FULL_JITTER); None
    when AC is unknown, the window is not full yet, or SOC is falling
    too little to call."""

    def __init__(self, window=CHARGE_WINDOW_SEC):
        self.window = window
        self.samples = collections.deque()  # (t, raw soc) taken with ac "1"
        self.charging = None
        self.input_insufficient = False
        self.low = None           # lowest raw SOC in the current episode
        self.since = None         # samples before this t never start a new episode
        self.ac_gone_since = None

    def _reset(self):
        self.samples.clear()
        self.input_insufficient = False
        self.low = None
        self.since = None

    def update(self, t, soc, ac, full=None, voltage=None):
        """Feed one poll; returns log lines for episode changes."""
        events = []
        if ac == "0":
            self.charging = False
            if self.ac_gone_since is None:
                self.ac_gone_since = t
            if t - self.ac_gone_since >= CHARGE_AC_GONE_SEC:
                if self.input_insufficient:
                    events.append("input insufficient cleared: mains gone")
                self._reset()
            return events
        if ac != "1":
            self.charging = None
            return events
        self.ac_gone_since = None
        if soc is None:
            return events  # failed read: keep everything as it was

        self.samples.append((t, soc))
        # Keep one sample at or before the window's left edge so a full
        # window spans at least CHARGE_WINDOW_SEC.
        while len(self.samples) >= 2 and self.samples[1][0] <= t - self.window:
            self.samples.popleft()

        if self.input_insufficient:
            if soc >= self.low + CHARGE_RISE_POINTS:
                events.append("input recovered: raw SOC up %.1f points from %.1f%%" % (soc - self.low, self.low))
                self.input_insufficient = False
                self.low = None
                self.since = t
            else:
                self.low = min(self.low, soc)
        else:
            elig = [s for s in self.samples if self.since is None or s[0] >= self.since]
            if len(elig) >= 2 and elig[-1][0] - elig[0][0] >= self.window:
                peak = max(s for _, s in elig)
                resting_full = voltage is not None and voltage >= FULL_MIN_VOLTAGE
                if peak - soc >= CHARGE_DROP_POINTS and not resting_full:
                    self.input_insufficient = True
                    self.low = soc
                    events.append(
                        "input insufficient: raw SOC fell %.1f points (%.1f%% -> %.1f%%) in %d min "
                        "with mains present, pack draining" % (
                            peak - soc, peak, soc, round((elig[-1][0] - elig[0][0]) / 60)))

        self.charging = self._charging(soc, full)
        return events

    def _charging(self, soc, full):
        if self.input_insufficient:
            return False
        if full and soc >= full - FULL_JITTER:
            return True
        if len(self.samples) < 2 or self.samples[-1][0] - self.samples[0][0] < self.window:
            return None
        if soc >= max(s for _, s in self.samples) - FULL_JITTER:
            return True
        return None


Step = collections.namedtuple("Step", "decision full charging input_insufficient events")


class Monitor:
    """The per-poll decision code shared by the live loop and --simulate."""

    def __init__(self, learner):
        self.learner = learner
        self.decider = ShutdownDecider()
        self.charge = ChargeWatch()

    def step(self, t, voltage, soc, ac, in_grace=False):
        """t is a monotonic time in seconds; soc is the RAW register."""
        full = self.learner.update(voltage, soc, ac, t)
        events = self.charge.update(t, soc, ac, full, voltage)
        if in_grace:
            decision = Decision("none", "boot grace")
        else:
            decision = self.decider.update(voltage, soc)
        return Step(decision, full, self.charge.charging, self.charge.input_insufficient, events)


def write_status(voltage, soc, ac, soc_raw=None, full=None, charging=None, input_insufficient=False):
    """Write current state to /run/x1202.json for the bridge API
    to read.  MUST write in-place (open+truncate) rather than
    atomic-rename: the bridge container bind-mounts this file as
    a single-file mount (`/run/x1202.json:/run/x1202.json:ro`),
    and a single-file mount pins the inode at container-start
    time.  os.replace() swaps to a fresh inode, leaving the
    container staring at the original stale one forever.

    Payload is ~200 bytes so a partial-read race is vanishingly
    unlikely; the bridge handler tolerates JSON-parse errors
    anyway."""
    try:
        payload = {
            "voltage": round(voltage, 3) if voltage is not None else None,
            "soc_percent": round(soc, 1) if soc is not None else None,
            "soc_raw": round(soc_raw, 1) if soc_raw is not None else None,
            "full_soc": round(full, 1) if full else None,
            "ac_present": (ac == "1") if ac in ("0", "1") else None,
            "charging": None if charging is None else bool(charging),
            "input_insufficient": bool(input_insufficient),
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
    if smbus is None:
        log("quickstart skipped: python smbus module not installed")
        return
    try:
        bus = smbus.SMBus(I2C_BUS)
        bus.write_word_data(ADDR, 0x06, 0x4000)
        bus.close()
        log("quickstart sent — SOC will recalibrate")
    except Exception as e:
        log("quickstart failed: %s" % e)

def read_battery():
    """Read voltage and SOC from MAX17040."""
    if smbus is None:
        return None, None
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


# ─── --simulate ──────────────────────────────────────────────────────

def load_trace(path, name=None):
    """A trace is a JSON list of {"t", "voltage", "soc", "ac"} samples.
    A file of named traces maps name -> list, or name -> {"samples": list}."""
    with open(path) as f:
        data = json.load(f)
    if isinstance(data, dict):
        names = sorted(k for k in data if not k.startswith("_"))
        if not name:
            raise ValueError("%s holds named traces, pick one with --trace: %s" % (path, ", ".join(names)))
        if name not in names:
            raise ValueError("no trace %r in %s (have: %s)" % (name, path, ", ".join(names)))
        data = data[name]
        if isinstance(data, dict):
            data = data.get("samples")
    elif name:
        raise ValueError("%s is a plain list of samples, --trace does not apply" % path)
    if not isinstance(data, list):
        raise ValueError("a trace must be a JSON list of samples")
    return data


def normalize_ac(ac):
    if isinstance(ac, bool):
        return "1" if ac else "0"
    if ac in (0, 1):
        return str(ac)
    if ac in ("0", "1"):
        return ac
    return "unknown"


def replay(samples, full=None):
    """Feed samples through the same Monitor the live loop uses.  No boot
    grace (a trace starts after it), no gauge, no status file, no shutdown.
    Yields (t, voltage, soc, ac, Step)."""
    mon = Monitor(FullLearner(persist=False, full=full))
    for i, s in enumerate(samples):
        t = float(s.get("t", i * POLL_SEC))
        voltage = s.get("voltage")
        voltage = float(voltage) if voltage is not None else None
        soc = s.get("soc")
        # read_battery() fails as a pair; mirror that.
        soc = float(soc) if (soc is not None and voltage is not None) else None
        ac = normalize_ac(s.get("ac", "unknown"))
        yield t, voltage, soc, ac, mon.step(t, voltage, soc, ac)


def _num(x, spec):
    return "null" if x is None else spec % x


def _flag(x):
    return "null" if x is None else ("true" if x else "false")


def simulate(samples, full=None, out=None):
    out = out or sys.stdout
    first_halt = None
    flags = None
    n = 0
    for t, voltage, soc, ac, step in replay(samples, full=full):
        d = step.decision
        if d.action == "halt":
            verdict = "would shut down: %s" % d.reason
            if first_halt is None:
                first_halt = (n, t, d.reason)
        elif d.action == "warn":
            verdict = "warn: %s" % d.reason
        else:
            verdict = "none" + (" (%s)" % d.reason if d.reason else "")
        line = "t=%s V=%s soc_raw=%s ac=%s -> %s" % (
            _num(t, "%g"), _num(voltage, "%.2f"), _num(soc, "%.1f"), ac, verdict)
        now_flags = (step.charging, step.input_insufficient)
        if now_flags != flags:
            line += " | charging=%s input_insufficient=%s" % (_flag(step.charging), _flag(step.input_insufficient))
            flags = now_flags
        for e in step.events:
            line += " | %s" % e
        print(line, file=out)
        n += 1
    if first_halt is None:
        print("result: no shutdown in %d samples" % n, file=out)
    else:
        print("result: the live loop would shut down at sample %d (t=%g): %s" % first_halt, file=out)


# ─── live loop ───────────────────────────────────────────────────────

def run():
    log("started (grace period %ds)" % BOOT_GRACE_SEC)
    if smbus is None:
        log("python smbus module not installed: no gauge readings, no battery shutdown")
    start = time.monotonic()

    # Quickstart to recalibrate SOC after power loss
    quickstart()
    time.sleep(5)

    ac_lost_since = None
    last_log = None
    last_warn = None
    monitor = Monitor(FullLearner())

    while True:
        voltage, soc = read_battery()
        ac = read_ac()
        now = time.time()
        mono = time.monotonic()  # the kit clock gets stepped at boot (MESHSAT-1056)
        in_grace = mono - start < BOOT_GRACE_SEC
        step = monitor.step(mono, voltage, soc, ac, in_grace=in_grace)
        full = step.full
        shown = scale_soc(soc, full)
        write_status(voltage, shown, ac, soc_raw=soc, full=full,
                     charging=step.charging, input_insufficient=step.input_insufficient)
        for line in step.events:
            log(line)

        # Periodic status log
        if last_log is None or mono - last_log >= LOG_INTERVAL:
            v_str = "%.2fV" % voltage if voltage else "ERR"
            s_str = "%.1f%%" % shown if shown is not None else "ERR"
            if soc is not None and full:
                s_str += " (raw %.1f%%, full %.1f%%)" % (soc, full)
            grace_str = " [GRACE]" if in_grace else ""
            input_str = ", INPUT INSUFFICIENT" if step.input_insufficient else ""
            log("status: %s, %s, AC=%s, charging=%s%s%s" % (
                v_str, s_str, ac, _flag(step.charging), input_str, grace_str))
            last_log = mono

        # Skip shutdown decisions during grace period and on I2C read failure
        if in_grace or voltage is None:
            time.sleep(POLL_SEC)
            continue

        decision = step.decision
        if decision.action == "halt":
            shutdown(decision.reason)
        elif decision.action == "warn":
            if last_warn is None or mono - last_warn >= LOG_INTERVAL:
                log(decision.reason)
                last_warn = mono
        elif decision.reason:
            log(decision.reason)  # a halt vote short of the count

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


def main(argv=None):
    ap = argparse.ArgumentParser(description="X1202 UPS monitor. Without options: the live loop.")
    ap.add_argument("--simulate", metavar="TRACE.json",
                    help="replay a trace through the decision code; never touches the gauge, "
                         "/run/x1202.json or shutdown")
    ap.add_argument("--trace", metavar="NAME", help="trace name when TRACE.json holds named traces")
    ap.add_argument("--full", metavar="RAW_SOC", type=float,
                    help="learned full point to simulate with (default: none)")
    args = ap.parse_args(argv)
    if args.simulate:
        try:
            samples = load_trace(args.simulate, args.trace)
        except (OSError, ValueError) as e:
            ap.error(str(e))
        simulate(samples, full=args.full)
        return 0
    if args.trace or args.full is not None:
        ap.error("--trace and --full only apply with --simulate")
    run()
    return 0


if __name__ == "__main__":
    sys.exit(main())
