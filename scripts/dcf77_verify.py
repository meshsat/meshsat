#!/usr/bin/env python3
"""
DCF77 frame sniffer for parallax (DCF-1060N-800 / SP6007 module).

Wiring on the 40-pin header:
    V  -> pin 1  (3.3V)
    G  -> pin 39 (GND)
    T  -> pin 35 (BCM 19)  data (time-code out)
    P1 -> pin 40 (BCM 21)  PON, active-low, held LOW while this script runs

Uses libgpiod v1 Python bindings (parallax ships 1.6.3).

What it does
------------
1. Drives BCM 21 LOW  -> receiver enabled.
2. Both-edge monitors BCM 19, measures pulse durations.
3. Auto-detects output polarity (SP6007 is usually inverted; not guaranteed).
4. Classifies each pulse: ~100 ms => bit 0, ~200 ms => bit 1.
5. Detects the ~1.9 s second-59 silence, closes out the 59-bit frame.
6. Validates start bit, S marker, and 3 BCD even-parity checks.
7. Prints each decoded frame + drift vs. the host clock.

The SP6007 AGC needs ~60-120 s after PON before frames come in cleanly.
Frames arrive once per minute. Ctrl-C to exit; PON is released on exit.

Run
---
    python3 dcf77_verify.py
"""

import signal
import sys
import time
from datetime import datetime, timedelta, timezone
from typing import List, Optional, Tuple

try:
    import gpiod
except ImportError:
    sys.exit("Missing python3-libgpiod.  sudo apt install python3-libgpiod")

CHIP    = "/dev/gpiochip4"   # Pi 5 RP1
PIN_TCO = 19                 # BCM 19 / header pin 35 -> T
PIN_PON = 21                 # BCM 21 / header pin 40 -> P1

# DCF77 pulse width classification (seconds).
BIT0_MIN, BIT0_MAX = 0.060, 0.140   # ~100 ms carrier reduction
BIT1_MIN, BIT1_MAX = 0.160, 0.260   # ~200 ms carrier reduction
MINUTE_MARK_MIN   = 1.500           # gap > 1.5 s = second-59 silence

DOW_NAMES = ["?", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"]


def _bcd(bits: List[int], weights: List[int]) -> int:
    return sum(b * w for b, w in zip(bits, weights))


def _even_parity_ok(bits: List[int]) -> bool:
    p = 0
    for b in bits:
        p ^= b
    return p == 0


def decode_frame(bits: List[int]) -> Tuple[bool, str, Optional[datetime]]:
    """(ok, message, decoded-utc-datetime-or-None)."""
    if len(bits) != 59:
        return False, f"length={len(bits)} (want 59)", None
    if bits[0] != 0:
        return False, f"start bit M={bits[0]} (want 0)", None
    if bits[20] != 1:
        return False, f"time marker S={bits[20]} (want 1)", None

    minutes = _bcd(bits[21:28], [1, 2, 4, 8, 10, 20, 40])
    if not _even_parity_ok(bits[21:29]):
        return False, f"P1 minute parity fail (mm={minutes:02d})", None

    hours = _bcd(bits[29:35], [1, 2, 4, 8, 10, 20])
    if not _even_parity_ok(bits[29:36]):
        return False, f"P2 hour parity fail (hh={hours:02d})", None

    day   = _bcd(bits[36:42], [1, 2, 4, 8, 10, 20])
    dow   = _bcd(bits[42:45], [1, 2, 4])
    month = _bcd(bits[45:50], [1, 2, 4, 8, 10])
    year  = _bcd(bits[50:58], [1, 2, 4, 8, 10, 20, 40, 80])
    if not _even_parity_ok(bits[36:59]):
        return False, f"P3 date parity fail (20{year:02d}-{month:02d}-{day:02d})", None

    if not (1 <= month <= 12 and 1 <= day <= 31 and 1 <= dow <= 7
            and 0 <= hours <= 23 and 0 <= minutes <= 59):
        return False, (f"range fail 20{year:02d}-{month:02d}-{day:02d} "
                       f"{hours:02d}:{minutes:02d}"), None

    dow_s = DOW_NAMES[dow]
    tz = "CEST" if bits[17] else ("CET" if bits[18] else "?TZ")
    flags = []
    if bits[16]: flags.append("TZ-CHG")
    if bits[19]: flags.append("LEAP")
    fstr = (" " + " ".join(flags)) if flags else ""

    # DCF77 broadcasts the time that is valid AT the minute mark we just saw,
    # so the decoded UTC should match `now` within a second or two.
    try:
        tz_offset_h = 2 if bits[17] else 1
        dt_utc = (datetime(2000 + year, month, day, hours, minutes, 0,
                           tzinfo=timezone.utc)
                  - timedelta(hours=tz_offset_h))
    except ValueError:
        dt_utc = None

    return (True,
            f"20{year:02d}-{month:02d}-{day:02d} {dow_s} "
            f"{hours:02d}:{minutes:02d} {tz}{fstr}",
            dt_utc)


def main() -> int:
    stop = {"v": False}

    def _sig(*_):
        stop["v"] = True

    signal.signal(signal.SIGINT, _sig)
    signal.signal(signal.SIGTERM, _sig)

    chip = gpiod.Chip(CHIP)

    pon_line = chip.get_line(PIN_PON)
    pon_line.request(
        consumer="dcf77-pon",
        type=gpiod.LINE_REQ_DIR_OUT,
        default_vals=[0],    # initial (and only) value: LOW -> receiver enabled
    )

    tco_line = chip.get_line(PIN_TCO)
    tco_line.request(
        consumer="dcf77-tco",
        type=gpiod.LINE_REQ_EV_BOTH_EDGES,
    )

    print(f"[{time.strftime('%H:%M:%S')}] PON=BCM{PIN_PON} driven LOW "
          f"(receiver ON)", flush=True)
    print(f"[{time.strftime('%H:%M:%S')}] listening on BCM{PIN_TCO}; "
          f"first good frame ~60-120 s (AGC warmup)", flush=True)
    print("---- raw pulses (warmup, sanity-check) ----", flush=True)

    last_t: Optional[float] = None
    pulse_state: Optional[int] = None     # level that represents AM-reduction pulse
    warmup: List[Tuple[float, int]] = []  # [(dur, state_that_just_ended)]
    bits: List[int] = []
    edges = 0
    frames_ok = 0
    frames_bad = 0
    last_edge_mono = time.monotonic()

    try:
        while not stop["v"]:
            # v1 API: event_wait(sec=2) returns True if events are pending.
            if not tco_line.event_wait(sec=2):
                now = time.monotonic()
                if now - last_edge_mono > 10:
                    print(f"[{time.strftime('%H:%M:%S')}] no edges in "
                          f"{int(now - last_edge_mono)}s - rotate antenna "
                          f"(broadside N-S from Frankfurt), move away from "
                          f"USB3 / SMPS / LED drivers / Ethernet switches",
                          flush=True)
                    last_edge_mono = now    # throttle the warning
                continue

            event = tco_line.event_read()
            edges += 1
            # v1 API: event.sec (int) + event.nsec (int, 0..999_999_999)
            t = event.sec + event.nsec / 1e9
            new_val = 1 if event.type == gpiod.LineEvent.RISING_EDGE else 0
            last_edge_mono = time.monotonic()

            if last_t is None:
                last_t = t
                continue

            dt = t - last_t
            prev_state = 1 - new_val     # level the line just left

            # ---------- polarity auto-detect ----------
            if pulse_state is None:
                warmup.append((dt, prev_state))
                if len(warmup) <= 20:
                    print(f"  edge#{edges:3d}  state={prev_state}  "
                          f"dur={dt * 1000:7.1f} ms", flush=True)
                if len(warmup) >= 20:
                    t0 = sum(d for d, s in warmup if s == 0)
                    t1 = sum(d for d, s in warmup if s == 1)
                    tot = t0 + t1
                    if tot >= 5.0 and max(t0, t1) > 0:
                        ratio = min(t0, t1) / max(t0, t1)
                        if ratio < 0.5:   # clear duty-cycle imbalance
                            pulse_state = 0 if t0 < t1 else 1
                            pct = ((t0 if pulse_state == 0 else t1)
                                   / tot * 100)
                            pol = ("inverted (LOW = AM reduction)"
                                   if pulse_state == 0
                                   else "normal (HIGH = AM reduction)")
                            print(f"[{time.strftime('%H:%M:%S')}] "
                                  f"polarity locked: {pol}  "
                                  f"(pulse duty {pct:.0f}% of {tot:.1f} s)",
                                  flush=True)
                            print("---- decoding frames ----", flush=True)
                            bits = []
                        elif len(warmup) > 200:
                            warmup = warmup[-120:]   # keep window bounded
                last_t = t
                continue

            # ---------- main decode (polarity locked) ----------
            if prev_state == pulse_state:
                # A carrier-reduction pulse just ended.
                if BIT0_MIN <= dt <= BIT0_MAX:
                    bits.append(0)
                elif BIT1_MIN <= dt <= BIT1_MAX:
                    bits.append(1)
                # else: glitch / wrong length, drop silently
            else:
                # A gap just ended -> long gap == minute mark.
                if dt >= MINUTE_MARK_MIN:
                    stamp = time.strftime("%H:%M:%S")
                    if len(bits) == 59:
                        ok, text, dt_utc = decode_frame(bits)
                        if ok:
                            frames_ok += 1
                            drift = ""
                            if dt_utc is not None:
                                d = (datetime.now(timezone.utc)
                                     - dt_utc).total_seconds()
                                drift = f"  drift={d:+.1f}s"
                            print(f"[{stamp}] OK  frame#"
                                  f"{frames_ok + frames_bad:3d}  "
                                  f"{text}{drift}    "
                                  f"[ok={frames_ok} bad={frames_bad} "
                                  f"edges={edges}]", flush=True)
                        else:
                            frames_bad += 1
                            print(f"[{stamp}] BAD frame#"
                                  f"{frames_ok + frames_bad:3d}  {text}    "
                                  f"[ok={frames_ok} bad={frames_bad}]",
                                  flush=True)
                    elif bits:
                        print(f"[{stamp}] partial: {len(bits)}/59 bits "
                              f"- signal jitter or glitch", flush=True)
                    bits = []

            last_t = t
    finally:
        print(f"\n[{time.strftime('%H:%M:%S')}] exiting - "
              f"edges={edges}, ok={frames_ok}, bad={frames_bad}",
              flush=True)
        try:
            tco_line.release()
        except Exception:
            pass
        try:
            pon_line.release()
        except Exception:
            pass

    return 0


if __name__ == "__main__":
    sys.exit(main())
