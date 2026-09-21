#!/usr/bin/env python3
"""Unit tests for scripts/x1202-monitor.py. Run from the repo root:

    python3 -m unittest scripts/test_x1202_monitor.py

They need no smbus, run no gpioget or shutdown and write nothing under
/run or /var/lib: every host side effect is replaced by a tripwire that
fails the test if it is reached. [MESHSAT-794]
"""

import contextlib
import importlib.machinery
import importlib.util
import io
import json
import os
import tempfile
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
TRACES = os.path.join(HERE, "testdata", "x1202-traces.json")


def load_monitor():
    loader = importlib.machinery.SourceFileLoader("x1202_monitor", os.path.join(HERE, "x1202-monitor.py"))
    spec = importlib.util.spec_from_loader("x1202_monitor", loader)
    mod = importlib.util.module_from_spec(spec)
    loader.exec_module(mod)
    return mod


mon = load_monitor()


class Tripwire(BaseException):
    """BaseException so the monitor's `except Exception` blocks cannot swallow it."""


def tripwire(name):
    def boom(*args, **kwargs):
        raise Tripwire("%s called with %r %r" % (name, args, kwargs))
    return boom


class Guarded(unittest.TestCase):
    def setUp(self):
        fake_subprocess = mock.Mock()
        fake_subprocess.run.side_effect = tripwire("subprocess.run")
        fake_subprocess.Popen.side_effect = tripwire("subprocess.Popen")
        fake_time = mock.Mock(wraps=mon.time)
        fake_time.sleep.side_effect = tripwire("time.sleep")
        self.scratch = tempfile.mkdtemp(prefix="x1202-test-")
        patches = [
            mock.patch.object(mon, "subprocess", fake_subprocess),
            mock.patch.object(mon, "time", fake_time),
            mock.patch.object(mon, "shutdown", tripwire("shutdown")),
            mock.patch.object(mon, "quickstart", tripwire("quickstart")),
            mock.patch.object(mon, "write_status", tripwire("write_status")),
            mock.patch.object(mon, "save_full", tripwire("save_full")),
            mock.patch.object(mon, "load_full", tripwire("load_full")),
            mock.patch.object(mon, "read_ac", tripwire("read_ac")),
            mock.patch.object(mon, "run", tripwire("run")),
            mock.patch.object(mon, "STATUS_PATH", os.path.join(self.scratch, "never", "x1202.json")),
            mock.patch.object(mon, "FULL_PATH", os.path.join(self.scratch, "never", "full_soc.json")),
        ]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)
        self.addCleanup(self._no_writes)

    def _no_writes(self):
        self.assertFalse(os.path.exists(os.path.join(self.scratch, "never")))
        for root, _dirs, files in os.walk(self.scratch, topdown=False):
            for f in files:
                os.remove(os.path.join(root, f))
            os.rmdir(root)


def trace(name):
    return mon.load_trace(TRACES, name)


def replay(samples, **kw):
    with contextlib.redirect_stdout(io.StringIO()):
        return list(mon.replay(samples, **kw))


def actions(steps):
    return [step.decision.action for _t, _v, _s, _ac, step in steps]


def first_halt(steps):
    acts = actions(steps)
    return acts.index("halt") if "halt" in acts else None


class ImportTests(Guarded):
    def test_loads_without_smbus(self):
        with mock.patch.object(mon, "smbus", None):
            self.assertEqual(mon.read_battery(), (None, None))

    def test_constants(self):
        self.assertEqual(mon.HARD_LOW_VOLTAGE, 3.30)
        self.assertEqual(mon.LOW_VOLTAGE, 3.45)
        self.assertEqual(mon.LOW_CAPACITY, 5)
        self.assertEqual(mon.TRUST_GAUGE_BELOW_V, 3.60)
        self.assertEqual(mon.CURVE_LOW_SOC, 15)
        self.assertEqual(mon.CHARGE_WINDOW_SEC, 1800)


class CurveTests(Guarded):
    def test_spot_values(self):
        self.assertAlmostEqual(mon.soc_from_voltage(3.45), 10.0, places=6)
        self.assertAlmostEqual(mon.soc_from_voltage(3.60), 27.5, places=6)
        self.assertAlmostEqual(mon.soc_from_voltage(3.50), 15.0, places=6)
        for v, soc in mon.SOC_CURVE:
            self.assertAlmostEqual(mon.soc_from_voltage(v), soc, places=6)

    def test_clamps(self):
        self.assertEqual(mon.soc_from_voltage(3.20), 0.0)
        self.assertEqual(mon.soc_from_voltage(3.05), 0.0)
        self.assertEqual(mon.soc_from_voltage(0.0), 0.0)
        self.assertEqual(mon.soc_from_voltage(4.20), 100.0)
        self.assertEqual(mon.soc_from_voltage(4.35), 100.0)

    def test_monotonic(self):
        prev = -1.0
        for mv in range(3000, 4400, 5):
            soc = mon.soc_from_voltage(mv / 1000.0)
            self.assertGreaterEqual(soc, prev)
            prev = soc


class ShutdownTests(Guarded):
    def test_parallax_brownout_halts_by_third_reading(self):
        steps = replay(trace("parallax_2026-09-04_brownout"))
        self.assertEqual(first_halt(steps), 2)
        self.assertEqual(actions(steps)[:2], ["none", "none"])
        self.assertIn("halt vote", steps[0][4].decision.reason)

    def test_tesseract_3v05_halts_at_once(self):
        samples = trace("tesseract_2026-09-04_cutoff")
        steps = replay(samples)
        idx = first_halt(steps)
        self.assertIsNotNone(idx)
        self.assertEqual(samples[idx]["voltage"], 3.05)
        self.assertNotIn("halt", actions(steps)[:idx])
        self.assertIn("hard floor", steps[idx][4].decision.reason)
        # and with no history at all, when the gauge agrees the pack is low
        d = mon.ShutdownDecider().update(3.05, 1.0)
        self.assertEqual(d.action, "halt")
        self.assertIn("hard floor", d.reason)

    def test_hard_floor_needs_the_gauge_to_agree(self):
        # One glitched low voltage on a full pack is a vote, not a halt.
        dec = mon.ShutdownDecider()
        self.assertEqual(dec.update(3.10, 97.0).action, "none")
        self.assertEqual(dec.update(4.17, 97.0).action, "none")
        self.assertEqual(dec.update(4.17, 97.0).action, "none")
        # A sustained low voltage still halts on the vote.
        dec = mon.ShutdownDecider()
        self.assertEqual([dec.update(3.10, 50.0).action for _ in range(3)], ["none", "none", "halt"])

    def test_implausible_voltage_is_ignored(self):
        # An I2C read of zeros must not halt a kit on mains, nor count as a vote.
        dec = mon.ShutdownDecider()
        for _ in range(5):
            d = dec.update(0.0, 98.0)
            self.assertEqual(d.action, "none")
            self.assertIn("implausible", d.reason)
        self.assertEqual(len(dec.history), 0)

    def test_bounce_halts_on_three_of_four(self):
        samples = trace("bounce_3v44_3v47")
        steps = replay(samples)
        idx = first_halt(steps)
        self.assertIsNotNone(idx)
        verdicts = [mon.reading_verdict(s["voltage"], s["soc"])[0] for s in samples[: idx + 1]]
        self.assertEqual(verdicts[-4:].count("halt"), 3)
        run = best = 0
        for v in verdicts:
            run = run + 1 if v == "halt" else 0
            best = max(best, run)
        self.assertLess(best, 3, "the trace must halt by the 3-of-4 vote, not by 3 in a row")

    def test_single_dip_does_not_halt(self):
        steps = replay(trace("single_dip"))
        self.assertIsNone(first_halt(steps))

    def test_mains_never_halts_or_warns(self):
        steps = replay(trace("mains_healthy"))
        self.assertEqual(set(actions(steps)), {"none"})
        self.assertFalse(any(step.input_insufficient for *_rest, step in steps))

    def test_gauge_glitch_warns_only(self):
        steps = replay(trace("gauge_glitch"))
        self.assertEqual(set(actions(steps)), {"warn"})
        self.assertIn("looks wrong", steps[0][4].decision.reason)

    def test_failed_reads_neither_halt_early_nor_reset(self):
        samples = trace("failed_reads")
        steps = replay(samples)
        valid = [i for i, s in enumerate(samples) if s["voltage"] is not None]
        self.assertEqual(first_halt(steps), valid[2])
        for i in range(valid[2]):
            self.assertEqual(steps[i][4].decision.action, "none")
        # a long run of failed reads between the votes does not clear them either
        d = mon.ShutdownDecider()
        d.update(3.40, 3.0)
        d.update(3.40, 3.0)
        for _ in range(50):
            self.assertEqual(d.update(None, None).action, "none")
        self.assertEqual(d.update(3.40, 3.0).action, "halt")

    def test_soc_rule_uses_raw_register_and_voltage(self):
        # the parallax fix: empty gauge below TRUST_GAUGE_BELOW_V is believed
        self.assertEqual(mon.reading_verdict(3.55, 4.9)[0], "halt")
        self.assertEqual(mon.reading_verdict(3.60, 4.9)[0], "warn")
        self.assertEqual(mon.reading_verdict(3.60, 5.0)[0], "ok")
        self.assertEqual(mon.reading_verdict(3.44, 80.0)[0], "halt")
        self.assertEqual(mon.reading_verdict(3.70, None)[0], "ok")

    def test_boot_grace_skips_decisions(self):
        m = mon.Monitor(mon.FullLearner(persist=False))
        for t in range(0, 100, 10):
            self.assertEqual(m.step(t, 3.05, 0.1, "1", in_grace=True).decision.action, "none")


def charge_run(points, full=None, cw=None):
    cw = cw or mon.ChargeWatch()
    out = []
    for t, soc, ac in points:
        events = cw.update(t, soc, ac, full)
        out.append((t, cw.charging, cw.input_insufficient, events))
    return cw, out


class ChargeWatchTests(Guarded):
    def test_input_draining_trace(self):
        steps = replay(trace("input_draining"))
        by_t = {t: step for t, _v, _s, _ac, step in steps}
        for t, step in by_t.items():
            if t < 1800:
                self.assertFalse(step.input_insufficient, "t=%s: window not full yet" % t)
                self.assertIsNone(step.charging, "t=%s" % t)
        for t in range(1800, 2401, 60):
            self.assertTrue(by_t[t].input_insufficient, "t=%s" % t)
            self.assertIs(by_t[t].charging, False)
        self.assertFalse(by_t[2460].input_insufficient, "clears when SOC rises")
        events = [e for step in by_t.values() for e in step.events]
        self.assertEqual(len([e for e in events if e.startswith("input insufficient:")]), 1)
        self.assertEqual(len([e for e in events if e.startswith("input recovered")]), 1)
        self.assertEqual(set(actions(steps)), {"none"})

    def test_full_pack_resting_on_mains_is_not_draining(self):
        # parallax 21 Sep 2026: charger terminated at full, raw SOC drifted
        # 101.9 -> 98.9 over an hour while the pack sat at 4.17 V on a good
        # 12 V supply; the old rule flagged "input insufficient".
        cw = mon.ChargeWatch()
        for k in range(0, 61):
            soc = 101.9 - 0.07 * min(k, 30) - 0.03 * max(k - 30, 0)  # 99.8 at 30 min, 98.9 at 60
            cw.update(k * 60, round(soc, 2), "1", full=98.1, voltage=4.17)
            self.assertFalse(cw.input_insufficient, "t=%s" % (k * 60))
        self.assertIs(cw.charging, True)
        # the same drift with the pack sagging below the recharge threshold is real
        cw = mon.ChargeWatch()
        for k in range(0, 31):
            cw.update(k * 60, round(101.9 - 0.1 * k, 2), "1", full=98.1, voltage=4.05)
        self.assertTrue(cw.input_insufficient)

    def test_rising_soc_on_mains_is_charging(self):
        pts = [(k * 60, 50.0 + 0.2 * k, "1") for k in range(0, 36)]
        _cw, out = charge_run(pts)
        for t, charging, insufficient, _events in out:
            self.assertFalse(insufficient)
            if t < 1800:
                self.assertIsNone(charging, "t=%s" % t)
            else:
                self.assertIs(charging, True, "t=%s" % t)

    def test_ac_states(self):
        cw = mon.ChargeWatch()
        cw.update(0, 60.0, "0")
        self.assertIs(cw.charging, False)
        cw.update(10, 60.0, "unknown")
        self.assertIsNone(cw.charging)
        cw.update(20, 99.0, "1", full=98.5)
        self.assertIs(cw.charging, True, "at the learned full point")

    def test_flapping_ac_keeps_the_window(self):
        # parallax on 4 Sep: the AC flag flipped every minute or so on a weak supply
        pts = []
        for k in range(0, 31):
            pts.append((k * 60, round(70.0 - 0.1 * k, 1), "1"))
            pts.append((k * 60 + 20, None, "0"))
        cw, out = charge_run(pts)
        self.assertTrue(cw.input_insufficient)
        self.assertEqual(sum(1 for *_x, ev in out for e in ev if e.startswith("input insufficient:")), 1)

    def test_mains_gone_clears_and_resets(self):
        pts = [(k * 60, round(70.0 - 0.1 * k, 1), "1") for k in range(0, 31)]
        cw, _ = charge_run(pts)
        self.assertTrue(cw.input_insufficient)
        events = []
        for t in range(1860, 1860 + mon.CHARGE_AC_GONE_SEC + 1, 10):
            events += cw.update(t, 60.0, "0")
        self.assertFalse(cw.input_insufficient)
        self.assertEqual(events, ["input insufficient cleared: mains gone"])
        self.assertEqual(len(cw.samples), 0)
        # back on mains after an hour on battery: no instant false episode
        cw.update(5000, 50.0, "1")
        self.assertFalse(cw.input_insufficient)

    def test_recovery_needs_a_fresh_window_before_next_episode(self):
        pts = [(k * 60, round(70.0 - 0.1 * k, 1), "1") for k in range(0, 31)]
        pts.append((31 * 60, 68.0, "1"))  # +1 point: recovered
        pts += [(k * 60, round(68.0 - 0.1 * (k - 31), 1), "1") for k in range(32, 40)]
        _cw, out = charge_run(pts)
        flags = {t: ins for t, _c, ins, _e in out}
        self.assertTrue(flags[1800])
        self.assertFalse(flags[1860])
        for t in range(1920, 40 * 60, 60):
            self.assertFalse(flags[t], "t=%s: old samples must not restart the episode" % t)


class SimulateCliTests(Guarded):
    def run_main(self, argv):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            try:
                code = mon.main(argv)
            except SystemExit as e:
                code = e.code
        return code, out.getvalue(), err.getvalue()

    def test_named_trace(self):
        code, out, _err = self.run_main(["--simulate", TRACES, "--trace", "parallax_2026-09-04_brownout"])
        self.assertEqual(code, 0)
        lines = out.strip().splitlines()
        samples = trace("parallax_2026-09-04_brownout")
        self.assertEqual(len([ln for ln in lines if ln.startswith("t=")]), len(samples))
        self.assertIn("-> would shut down:", lines[2])
        self.assertTrue(lines[0].startswith("t=0 V=3.50 soc_raw=2.4 ac=1 -> none"))
        self.assertIn("charging=", lines[0])
        self.assertTrue(lines[-1].startswith("result: the live loop would shut down at sample 2"))

    def test_every_named_trace_replays(self):
        with open(TRACES) as f:
            names = [k for k in json.load(f) if not k.startswith("_")]
        for name in names:
            code, out, _err = self.run_main(["--simulate", TRACES, "--trace", name])
            self.assertEqual(code, 0, name)
            self.assertIn("result:", out, name)

    def test_plain_list(self):
        path = os.path.join(self.scratch, "list.json")
        with open(path, "w") as f:
            json.dump([{"t": 0, "voltage": 4.10, "soc": 2.0, "ac": "1"},
                       {"t": 10, "voltage": None, "soc": None, "ac": "unknown"}], f)
        code, out, _err = self.run_main(["--simulate", path])
        self.assertEqual(code, 0)
        self.assertIn("-> warn:", out)
        self.assertIn("V=null soc_raw=null ac=unknown -> none", out)
        self.assertIn("result: no shutdown in 2 samples", out)

    def test_named_file_without_trace_is_an_error(self):
        code, _out, err = self.run_main(["--simulate", TRACES])
        self.assertEqual(code, 2)
        self.assertIn("--trace", err)


class WriteStatusTests(unittest.TestCase):
    """Runs the real write_status against a temp file, never /run."""

    def test_in_place_with_charge_fields(self):
        with tempfile.TemporaryDirectory(prefix="x1202-status-") as d:
            path = os.path.join(d, "x1202.json")
            with open(path, "w") as f:
                f.write("{}")
            inode = os.stat(path).st_ino
            with mock.patch.object(mon, "STATUS_PATH", path), contextlib.redirect_stdout(io.StringIO()):
                mon.write_status(3.912, 80.04, "1", soc_raw=76.0, full=95.0, charging=None, input_insufficient=True)
            self.assertEqual(os.stat(path).st_ino, inode, "must write in place: the bind mount pins the inode")
            with open(path) as f:
                payload = json.load(f)
            self.assertIsNone(payload["charging"])
            self.assertIs(payload["input_insufficient"], True)
            for key in ("voltage", "soc_percent", "soc_raw", "full_soc", "ac_present", "last_update"):
                self.assertIn(key, payload)
            self.assertEqual(payload["soc_percent"], 80.0)
            with mock.patch.object(mon, "STATUS_PATH", path), contextlib.redirect_stdout(io.StringIO()):
                mon.write_status(4.17, 100.0, "1", soc_raw=98.1, full=98.1, charging=True)
            with open(path) as f:
                payload = json.load(f)
            self.assertIs(payload["charging"], True)
            self.assertIs(payload["input_insufficient"], False)
            self.assertEqual(os.stat(path).st_ino, inode)


if __name__ == "__main__":
    unittest.main()
