"""Tests for the device reliability log's fold (SIDN D4, MESHSAT-1842).

The row texts are the kits' own wordings, taken from their audit logs.

    python3 -m unittest scripts/device-log/test_fold.py
"""

from __future__ import annotations

import sys
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import fold  # noqa: E402

T0 = datetime(2026, 9, 10, 10, 0, 0, tzinfo=timezone.utc)


class Tape:
    """Builds audit rows with rising ids and times."""

    def __init__(self):
        self.rows, self.id, self.t = [], 100, T0

    def at(self, minutes: float) -> "Tape":
        self.t = T0 + timedelta(minutes=minutes)
        return self

    def add(self, detail: str, event_type: str = "device_health") -> "Tape":
        self.id += 1
        self.rows.append({"id": self.id, "timestamp": fold.iso(self.t), "event_type": event_type, "detail": detail,
                          "interface_id": None, "prev_hash": "", "hash": ""})
        return self

    def aprs(self, detail: str) -> "Tape":
        return self.add(detail, "aprs_rx_watchdog")


def one(res: fold.FoldResult, device: str) -> fold.Wedge:
    ws = [w for w in res.wedges if w.device == device]
    assert len(ws) == 1, [(fold.iso(w.started), w.cleared_by) for w in ws]
    return ws[0]


class Classify(unittest.TestCase):
    def test_every_wording_seen_on_the_kits(self):
        cases = {
            "mesh: mesh unhealthy: connected but the radio never sent its node number (3 consecutive misses)": "open",
            "mesh: mesh: step 1, level 1, serial reconnect (auto)": "step",
            "mesh: step 1 serial reconnect: meshtastic: radio silent during config handshake": "step_error",
            "mesh: mesh recovered after step 2 (DTR/RTS reboot)": "recovered",
            "rtl_sdr: rtl_sdr: ladder exhausted after 2 rungs": "failed",
            "rtl_sdr: rtl_sdr still unhealthy after cooldown: dongle not on the USB bus, last seen at 4-2": "continue",
            "rtl_sdr: rtl_sdr: hard-reset budget of 3 per hour spent": "failed",
            'gps: gps: level 3 rung "USB reset" needs operator confirm (POST /api/devices/health/gps/heal {"level":3,"confirm":true})': "failed",
            "zigbee: step 2 SYS_RESET_REQ: sys reset send: zigbee port closed": "step_error",
            "cellular: cellular: step 3, level 3, hub port power cycle with quiet window (manual)": "step",
            "rtl_sdr: rtl_sdr left the bus while unhealthy: dongle not on the USB bus": "away",
            "rtl_sdr: rtl_sdr came back without a software step (by hand, by itself or a restart)": "came_back",
            "zigbee: zigbee back but still unhealthy: silent (3 consecutive misses)": "continue",
            "watchdog: watchdog started (boot 1a2b3c4d)": "start",
        }
        for detail, kind in cases.items():
            self.assertEqual(fold.classify({"id": 1, "timestamp": "2026-09-10T10:00:00Z", "event_type": "device_health", "detail": detail}).kind, kind, detail)
        aprs = {
            "APRS receive silent: no frame decoded for 5m26s while the channel was alive 5m26s ago": "open",
            "APRS receive silent: step 1, restart APRS gateway (Direwolf respawn)": "step",
            "APRS receive still silent after the gateway restart and step 2: restarting the bridge": "step",
            "APRS receive recovered after step 1": "recovered",
            "step 2 failed: AIOC not on a switchable hub port": "step_error",
            "APRS TNC silent: no bytes from the TNC for 30m2s since the link opened; a PicoAPRS that is off needs a 3 s PTT press": "open",
            "APRS TNC talking again after its silence (no step ran: the radio was switched on)": "came_back",
        }
        for detail, kind in aprs.items():
            self.assertEqual(fold.classify({"id": 1, "timestamp": "2026-09-10T10:00:00Z", "event_type": "aprs_rx_watchdog", "detail": detail}).kind, kind, detail)
        r = fold.classify({"id": 1, "timestamp": "2026-09-10T10:00:00Z", "event_type": "aprs_rx_watchdog",
                           "detail": "APRS receive silent: no frame decoded for 1h17m13s while the channel was alive 1h17m13s ago"})
        self.assertEqual(r.silence, timedelta(hours=1, minutes=17, seconds=13))
        self.assertIsNone(fold.classify({"id": 1, "timestamp": "2026-09-10T10:00:00Z", "event_type": "deliver", "detail": "x"}))


class Fold(unittest.TestCase):
    def test_a_recovery_inside_the_grace_is_the_software_step(self):
        t = Tape()
        t.at(0).add("mesh: mesh unhealthy: local probe: no reply from the radio within 10s (3 consecutive misses)")
        t.at(0).add("mesh: mesh: step 1, level 1, serial reconnect (auto)")
        t.at(1).add("mesh: mesh recovered after step 1 (serial reconnect)")
        w = one(fold.fold("parallax", t.rows), "mesh")
        self.assertEqual((w.cleared_by, w.cleared_detail, w.case_opened), ("software", "serial reconnect", "no"))
        self.assertEqual(w.ended - w.started, timedelta(minutes=1))

    def test_a_step_that_had_nothing_to_act_on_is_never_credited(self):
        t = Tape()
        t.at(0).add("rtl_sdr: rtl_sdr unhealthy: dongle not on the USB bus, last seen at 4-2 (3 consecutive misses)")
        t.at(0).add("rtl_sdr: rtl_sdr: step 2, level 3, hub-port power cycle (auto)")
        t.at(0).add("rtl_sdr: step 2 hub-port power cycle: rtl-sdr: no switchable hub port to power-cycle; a USB reset is not used, it wedges the Blog V4")
        t.at(40).add("rtl_sdr: rtl_sdr recovered after step 2 (hub-port power cycle)")
        w = one(fold.fold("tesseract", t.rows), "rtl_sdr")
        self.assertEqual(w.cleared_by, "came_back")
        self.assertIn("could not act", w.cleared_detail)
        self.assertEqual(w.case_opened, "not recorded")

    def test_a_step_that_reported_a_timeout_but_worked_is_credited(self):
        t = Tape()
        t.at(0).add("zigbee: zigbee unhealthy: coordinator not ready (hold) (3 consecutive misses)")
        t.at(0).add("zigbee: zigbee: step 1, level 1, start gateway or reopen and re-init (auto)")
        t.at(1.5).add("zigbee: step 1 start gateway or reopen and re-init: context deadline exceeded")
        t.at(2).add("zigbee: zigbee recovered after step 1 (start gateway or reopen and re-init)")
        w = one(fold.fold("parallax", t.rows), "zigbee")
        self.assertEqual(w.cleared_by, "software")
        self.assertIn("reported an error", w.cleared_detail)

    def test_the_hourly_rearm_of_a_ladder_that_ran_out_is_one_wedge(self):
        t = Tape()
        t.at(0).add("rtl_sdr: rtl_sdr unhealthy: dongle not on the USB bus, last seen at 4-2 (3 consecutive misses)")
        t.at(3).add("rtl_sdr: rtl_sdr: ladder exhausted after 2 rungs")
        for h in range(1, 6):
            t.at(60 * h).add("rtl_sdr: rtl_sdr still unhealthy after cooldown: dongle not on the USB bus, last seen at 4-2")
            t.at(60 * h).add("rtl_sdr: rtl_sdr: step 2, level 3, hub-port power cycle (auto)")
            t.at(60 * h + 3).add("rtl_sdr: rtl_sdr: ladder exhausted after 2 rungs")
        res = fold.fold("tesseract", t.rows, current={"rtl_sdr": "failed"})
        w = one(res, "rtl_sdr")
        self.assertEqual(w.cleared_by, "open")
        self.assertEqual(w.rows, 2 + 5 * 3)

    def test_a_new_wedge_long_after_the_last_row_closes_the_old_one_unseen(self):
        t = Tape()
        t.at(0).add("mesh: mesh unhealthy: radio silent (3 consecutive misses)")
        t.at(0).add("mesh: mesh: step 1, level 1, serial reconnect (auto)")
        t.at(300).add("mesh: mesh unhealthy: radio silent (3 consecutive misses)")
        t.at(301).add("mesh: mesh recovered after step 1 (serial reconnect)")
        ws = fold.fold("parallax", t.rows).wedges
        self.assertEqual([w.cleared_by for w in ws], ["not_recorded", "software"])
        self.assertIsNone(ws[0].ended)
        self.assertTrue(ws[0].inferred)

    def test_a_device_back_within_five_minutes_with_nothing_acting_came_back_by_itself(self):
        t = Tape()
        t.at(0).add("zigbee: zigbee unhealthy: coordinator not ready (hold) (3 consecutive misses)")
        t.at(0).add("zigbee: zigbee: step 1, level 1, start gateway or reopen and re-init (auto)")
        t.at(1.5).add("zigbee: step 1 start gateway or reopen and re-init: reopen: reopen /dev/ttyUSB0: no such device")
        t.at(2).add("zigbee: zigbee recovered after step 1 (start gateway or reopen and re-init)")
        w = one(fold.fold("parallax", t.rows), "zigbee")
        self.assertEqual((w.cleared_by, w.case_opened, w.case_source), ("itself", "no", "inferred"))

    def test_an_aprs_silence_reported_again_after_a_restart_is_the_same_wedge(self):
        t = Tape()
        t.at(0).aprs("APRS receive silent: no frame decoded for 5m15s while the channel was alive 5m15s ago")
        t.at(0).aprs("APRS receive silent: step 1, restart APRS gateway (Direwolf respawn)")
        t.at(11).aprs("APRS receive still silent after gateway restart and AIOC power cycle: restarting the bridge")
        t.at(12).aprs("APRS receive silent: no frame decoded for 17m18s while the channel was alive 17m18s ago")
        res = fold.fold("parallax", t.rows)
        self.assertEqual(len(res.wedges), 1)
        # A silence that started after the last row: a frame came in between.
        t.at(200).aprs("APRS receive silent: no frame decoded for 5m4s while the channel was alive 5m4s ago")
        ws = fold.fold("parallax", t.rows).wedges
        self.assertEqual(len(ws), 2)
        self.assertEqual(ws[0].cleared_by, "software")
        self.assertIn("restart of the bridge", ws[0].cleared_detail)
        self.assertEqual(ws[0].ended, T0 + timedelta(minutes=200) - timedelta(minutes=5, seconds=4))

    def test_a_silent_tnc_repeated_after_each_restart_is_one_silence_until_it_talks(self):
        t = Tape()
        for m in (0, 46, 100, 151, 205):
            t.at(m).aprs("APRS TNC silent: no bytes from the TNC for 30m2s since the link opened; a PicoAPRS that is off needs a 3 s PTT press")
        t.at(240).aprs("APRS TNC talking again after its silence (no step ran: the radio was switched on)")
        w = one(fold.fold("parallax", t.rows), "aprs")
        self.assertEqual((w.rows, w.cleared_by), (6, "came_back"))
        self.assertIn("PTT", w.cleared_detail)
        self.assertEqual(w.case_opened, "not recorded")

    def test_a_device_taken_out_is_not_wedged(self):
        t = Tape()
        t.at(0).add("rtl_sdr: rtl_sdr unhealthy: dongle not on the USB bus, last seen at 4-2 (3 consecutive misses)")
        for h in range(1, 4):
            t.at(60 * h).add("rtl_sdr: rtl_sdr still unhealthy after cooldown: dongle not on the USB bus, last seen at 4-2")
        removal = fold.Inventory("tesseract", "rtl_sdr", "removed", T0 + timedelta(minutes=200), "out for good", "notes")
        for h in range(4, 9):
            t.at(60 * h).add("rtl_sdr: rtl_sdr still unhealthy after cooldown: dongle not on the USB bus, last seen at 4-2")
        res = fold.fold("tesseract", t.rows, [removal], current={"rtl_sdr": "failed"})
        w = one(res, "rtl_sdr")
        self.assertEqual((w.cleared_by, w.case_opened), ("removed", "yes"))
        self.assertEqual(w.ended, removal.at)
        self.assertEqual(w.rows, 4)

    def test_the_kit_records_its_own_removal(self):
        t = Tape()
        t.at(0).add("rtl_sdr: rtl_sdr unhealthy: dongle not on the USB bus, last seen at 4-2 (3 consecutive misses)")
        t.at(10).add("rtl_sdr: rtl_sdr removed from the kit: out for good", "device_inventory")
        t.at(70).add("rtl_sdr: rtl_sdr unhealthy: dongle not on the USB bus, last seen at 4-2 (3 consecutive misses)")
        res = fold.fold("tesseract", t.rows)
        self.assertEqual([w.cleared_by for w in res.wedges], ["removed"])
        self.assertEqual(res.inventory[-1].source, "kit")

    def test_a_restart_ends_a_wedge_that_is_not_reported_again(self):
        t = Tape()
        t.at(0).add("watchdog: watchdog started (boot aaaaaaaa)")
        t.at(5).add("cellular: cellular unhealthy: AT: AT command \"AT\" timed out after 5s (3 consecutive misses)")
        t.at(6).add("watchdog: watchdog started (boot aaaaaaaa)")
        t.at(200).add("mesh: mesh unhealthy: radio silent (3 consecutive misses)")
        w = one(fold.fold("parallax", t.rows), "cellular")
        self.assertEqual((w.cleared_by, w.cleared_detail, w.case_opened), ("restart", "ended with a bridge restart", "no"))
        t2 = Tape()
        t2.at(0).add("watchdog: watchdog started (boot aaaaaaaa)")
        t2.at(5).add("cellular: cellular unhealthy: modem silent (3 consecutive misses)")
        t2.at(6).add("watchdog: watchdog started (boot bbbbbbbb)")
        w = one(fold.fold("parallax", t2.rows), "cellular")
        self.assertEqual((w.cleared_detail, w.case_opened), ("ended with a kit power cycle", "not recorded"))

    def test_a_restart_does_not_claim_a_wedge_that_went_quiet_long_before(self):
        t = Tape()
        t.at(0).add("gps: gps unhealthy: port open, no NMEA sentence yet (3 consecutive misses)")
        t.at(1).add('gps: gps: level 3 rung "USB reset" needs operator confirm (POST /api/devices/health/gps/heal {"level":3,"confirm":true})')
        t.at(5000).add("watchdog: watchdog started (boot aaaaaaaa)")
        w = one(fold.fold("parallax", t.rows), "gps")
        self.assertEqual(w.cleared_by, "not_recorded")
        self.assertIsNone(w.ended)

    def test_a_wedge_reported_again_right_after_a_restart_carries_on(self):
        t = Tape()
        t.at(0).add("mesh: mesh unhealthy: radio silent (3 consecutive misses)")
        t.at(2).add("watchdog: watchdog started (boot aaaaaaaa)")
        t.at(4).add("mesh: mesh unhealthy: radio silent (3 consecutive misses)")
        t.at(5).add("mesh: mesh: step 1, level 1, serial reconnect (auto)")
        t.at(6).add("mesh: mesh recovered after step 1 (serial reconnect)")
        w = one(fold.fold("parallax", t.rows), "mesh")
        self.assertTrue(w.carried_restart)
        self.assertEqual(w.cleared_by, "software")

    def test_a_hands_on_note_answers_the_case_question(self):
        t = Tape()
        t.at(0).add("rtl_sdr: rtl_sdr unhealthy: dongle not on the USB bus, last seen at 4-2 (3 consecutive misses)")
        t.at(2).add("rtl_sdr: rtl_sdr left the bus while unhealthy: dongle not on the USB bus")
        t.at(90).add("rtl_sdr: rtl_sdr came back without a software step (by hand, by itself or a restart)")
        t.at(95).add("rtl_sdr: rtl_sdr hands-on fix: re-seat; case opened: yes; loose plug", "device_note")
        t.at(99).add("display: display hands-on fix: power-cycle; case opened: no", "device_note")
        res = fold.fold("tesseract", t.rows)
        w = one(res, "rtl_sdr")
        self.assertEqual((w.cleared_by, w.case_opened, w.case_source), ("by_hand", "yes", "note"))
        self.assertIn("re-seat", w.cleared_detail)
        self.assertEqual([n.device for n in res.loose_notes], ["display"])

    def test_an_answer_from_the_notes_names_the_latest_wedge_before_it(self):
        t = Tape()
        t.at(0).add("rtl_sdr: rtl_sdr unhealthy: dongle not on the USB bus, last seen at 4-2 (3 consecutive misses)")
        t.at(1440).add("rtl_sdr: rtl_sdr unhealthy: dongle not on the USB bus, last seen at 4-2 (3 consecutive misses)")
        t.at(1446).add("rtl_sdr: rtl_sdr: ladder exhausted after 2 rungs")
        res = fold.fold("parallax", t.rows, current={"rtl_sdr": "ok"})
        left = fold.apply_answers(res.wedges, [{"kit": "parallax", "device": "rtl_sdr", "at": fold.iso(T0 + timedelta(minutes=1600)),
                                                "cleared_by": "re-seated by hand", "case_opened": "yes"}])
        self.assertEqual(left, [])
        self.assertEqual([w.case_opened for w in res.wedges], ["not recorded", "yes"])
        self.assertEqual(res.wedges[1].cleared_by, "by_hand")


    def test_the_ptt_rule_answers_every_ended_picoaprs_silence(self):
        t = Tape()
        t.at(0).aprs("APRS TNC silent: no bytes from the TNC for 30m2s since the link opened; a PicoAPRS that is off needs a 3 s PTT press")
        t.at(60).aprs("APRS TNC talking again after its silence (no step ran: the radio was switched on)")
        t.at(2000).aprs("APRS TNC silent: no bytes from the TNC for 30m1s since the link opened; a PicoAPRS that is off needs a 3 s PTT press")
        res = fold.fold("tesseract", t.rows, current={"aprs": "degraded"})
        fold.apply_rules(res.wedges, {"picoaprs_silence_cleared_by_ptt": True})
        self.assertEqual([(w.cleared_by, w.case_opened) for w in res.wedges], [("by_hand", "yes"), ("open", "n/a")])

    def test_with_the_ptt_rule_off_an_unlogged_end_stays_not_recorded(self):
        # The owner pressed the PTT button 2 or 3 times from September to
        # 13 November 2026, so the rule is off: a silence whose end the kit
        # did not log is not a press. [MESHSAT-1842]
        t = Tape()
        t.at(0).aprs("APRS TNC silent: no bytes from the TNC for 30m2s since the link opened; a PicoAPRS that is off needs a 3 s PTT press")
        t.at(8 * 60).aprs("APRS TNC silent: no bytes from the TNC for 30m1s since the link opened; a PicoAPRS that is off needs a 3 s PTT press")
        res = fold.fold("tesseract", t.rows, current={"aprs": "degraded"})
        fold.apply_rules(res.wedges, {"picoaprs_silence_cleared_by_ptt": False})
        self.assertEqual([(w.cleared_by, w.case_opened) for w in res.wedges], [("not_recorded", "not recorded"), ("open", "n/a")])

    def test_the_history_file_keeps_the_ptt_rule_off(self):
        import tomllib
        hist = tomllib.loads((Path(__file__).resolve().parents[2] / "docs/evidence/device-log/history.toml").read_text())
        self.assertFalse(hist["rules"]["picoaprs_silence_cleared_by_ptt"])
        self.assertTrue(any("2 or 3 times" in c["text"] for c in hist.get("correction", [])))


class Helpers(unittest.TestCase):
    def test_redaction(self):
        s = "imei 300234067500220 to +31 6 1234 5678, mail a.b@example.org, node !4370c1d8, port /dev/ttyUSB0 at 4-2"
        out = fold.redact(s)
        self.assertNotIn("300234067500220", out)
        self.assertNotIn("1234 5678", out)
        self.assertNotIn("example.org", out)
        self.assertNotIn("4370c1d8", out)
        self.assertIn("/dev/ttyUSB0 at 4-2", out)
        self.assertFalse(fold.unredacted(out))

    def test_powered_hours_count_uptime_and_skip_boots_the_journal_has(self):
        boots = [{"boot_id": "aaaaaaaa11112222", "uptime_us": int(10 * 3.6e9), "last_realtime_us": 1_789_300_000_000_000},
                 {"boot_id": "bbbbbbbb33334444", "uptime_us": int(2.5 * 3.6e9), "last_realtime_us": 1_789_400_000_000_000}]
        bridge = [{"boot_id": "bbbbbbbb", "first_seen": "2026-10-01T00:00:00Z", "last_seen": "2026-10-01T02:00:00Z"},
                  {"boot_id": "cccccccc", "first_seen": "2026-11-20T00:00:00Z", "last_seen": "2026-11-20T04:00:00Z"}]
        p = fold.powered_hours(boots, bridge)
        self.assertEqual(p["total_hours"], 16.5)
        self.assertEqual([b["source"] for b in p["boots"]], ["journal", "journal", "bridge"])


if __name__ == "__main__":
    unittest.main()
