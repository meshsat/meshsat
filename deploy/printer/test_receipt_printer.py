#!/usr/bin/env python3
"""The receipt printer service's own tests, stdlib only: the Community Post's
slips (MESHSAT-1733) against a fake printer, and the event handling that queues
them. Run: cd deploy/printer && python3 -m unittest test_receipt_printer -v"""

import importlib.machinery
import importlib.util
import os
import tempfile
import unittest
from collections import deque

HERE = os.path.dirname(os.path.abspath(__file__))


def load_service():
    loader = importlib.machinery.SourceFileLoader("rp", os.path.join(HERE, "meshsat-receipt-printer"))
    spec = importlib.util.spec_from_loader("rp", loader)
    mod = importlib.util.module_from_spec(spec)
    loader.exec_module(mod)
    return mod


rp = load_service()


class FakePrinter:
    """Records every byte stream it is handed; fails the first `fail` times."""

    def __init__(self, fail=0):
        self.payloads, self.fail = [], fail

    def print_slip(self, payload):
        if self.fail:
            self.fail -= 1
            return False
        self.payloads.append(payload)
        return True


def config(tmp):
    class Args:
        mock_file = None
    cfg = rp.load_config(Args())
    cfg["logo"] = cfg["hat"] = None
    cfg["count_path"] = os.path.join(tmp, "count")
    cfg["peers"] = []
    cfg["backoff"] = cfg["backoff_max"] = 0.0
    cfg["attempts"] = 2
    cfg["bridge"] = "http://127.0.0.1:1"   # nothing listens: the report must stay quiet
    return cfg


POST = {"kind": "post", "code": "K7QW2X", "to": "+3161****78", "at": "2026-10-28T12:00:00Z",
        "url": "http://10.44.0.1/", "pickup": "http://10.44.0.1/pickup?code=K7QW2X", "operator": "Gemeente Leiden"}
REPLY = dict(POST, kind="reply", text="on my way, twenty minutes, keep the north gate open for the truck " * 2)


class PostSlip(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.cfg = config(self.tmp)
        self.log = []

    def test_post_slip_has_the_code_large_the_masked_recipient_the_time_and_the_portal(self):
        slip = rp.build_post_slip(POST, self.cfg)
        self.assertIn(rp.BIG_ON + b"K7QW2X\n" + rp.BIG_OFF, slip)
        self.assertIn(b"YOUR PICKUP CODE\n", slip)
        self.assertIn(b"to      +3161****78\n", slip)
        self.assertIn(b"time    2026-10-28 12:00 UTC\n", slip)
        self.assertIn(b"Community Post\n", slip)
        self.assertIn(b"Gemeente Leiden\n", slip)
        self.assertIn(b"http://10.44.0.1/\n", slip)
        self.assertIn(b"1P0http://10.44.0.1/pickup?code=K7QW2X", slip)   # the QR carries the pickup page
        self.assertNotIn(b"slip    #", slip)                            # no booth number
        self.assertNotIn(b"SLIP ", slip)                                # no hat
        self.assertNotIn(b"TTC26", slip)                                # no booth strapline
        self.assertIn(b"+3161****78", slip)
        self.assertNotIn(b"+31612345678", slip)

    def test_reply_slip_carries_the_text_wrapped_and_says_so(self):
        slip = rp.build_post_slip(REPLY, self.cfg)
        self.assertIn(b"A REPLY CAME BACK\n", slip)
        self.assertIn(rp.BIG_ON + b"K7QW2X\n", slip)
        self.assertIn(b"on my way, twenty minutes, keep\n", slip)
        for line in slip.split(b"\n"):
            if line and line[:1] not in (b"\x1b", b"\x1d") and b"\x1d" not in line:
                self.assertLessEqual(len(line), self.cfg["width"], line)

    def test_an_emergency_is_marked_and_non_ascii_does_not_break_the_paper(self):
        slip = rp.build_post_slip(dict(POST, emergency=True, to="m***@example.org", operator="Δήμος"), self.cfg)
        self.assertIn(b"SOS - YOUR PICKUP CODE\n", slip)
        self.assertIn(b"to      m***@example.org\n", slip)
        self.assertNotIn("Δήμος".encode(), slip)

    def test_the_queue_prints_a_post_slip_without_touching_the_booth_sequence(self):
        printer = FakePrinter(fail=1)
        q = rp.SlipQueue(printer, self.cfg, self.log.append)
        q._print_with_retry(dict(POST, id="post:K7QW2X"))
        self.assertEqual(len(printer.payloads), 1)
        self.assertIn(b"K7QW2X", printer.payloads[0])
        self.assertEqual(rp.read_count(self.cfg["count_path"]), 0)
        self.assertTrue(any("post slip printed" in m for m in self.log), self.log)
        # A booth slip still takes a number.
        q._print_with_retry({"id": "m1", "text": "hello", "from": "!de11f199", "bearer": "APRS", "crossing": "aprs_0"})
        self.assertEqual(rp.read_count(self.cfg["count_path"]), 1)
        self.assertIn(b"slip    #0001\n", printer.payloads[1])

    def test_the_report_is_quiet_when_the_bridge_takes_none(self):
        r = rp.Report()
        r.send(self.cfg, self.log.append)
        r.send(self.cfg, self.log.append)
        self.assertEqual(len([m for m in self.log if "printer report" in m]), 1, self.log)
        body = r.body(self.cfg)
        self.assertEqual(body["service"], "meshsat-receipt-printer")
        self.assertEqual(body["count"], 0)


class Submitted:
    def __init__(self):
        self.rows = []

    def submit(self, msg):
        self.rows.append(msg)


class Events(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.cfg = config(self.tmp)
        self.q, self.seen, self.log = Submitted(), deque(maxlen=16), []

    def handle(self, ev):
        rp.handle_event(ev, self.q, self.cfg, self.log.append, self.seen)

    def test_a_post_slip_event_queues_one_slip_and_a_repeat_none(self):
        self.handle({"type": "post_slip", "data": POST})
        self.handle({"type": "post_slip", "data": POST})
        self.assertEqual(len(self.q.rows), 1)
        self.assertEqual(self.q.rows[0]["kind"], "post")
        self.assertEqual(self.q.rows[0]["code"], "K7QW2X")
        self.handle({"type": "post_slip", "data": REPLY})
        self.assertEqual(len(self.q.rows), 2)
        self.assertEqual(self.q.rows[1]["kind"], "reply")
        self.handle({"type": "post_slip", "data": {"kind": "post"}})   # no code: nothing
        self.assertEqual(len(self.q.rows), 2)

    def test_a_posts_own_delivery_is_not_a_booth_slip(self):
        # The post's text leaves as a plain-class delivery; the booth trigger prints messages only.
        self.handle({"type": "delivery_sent", "data": {"status": "sent", "class": "plain", "channel": "iridium_0",
                                                        "msg_ref": "r1", "text_preview": "@+316 Maria: water [#K7QW2X]"}})
        self.assertEqual(self.q.rows, [])
        # A mesh-only hop is no crossing either; an OOB frame neither.
        self.handle({"type": "delivery_sent", "data": {"status": "sent", "channel": "mesh_0", "msg_ref": "r2", "text_preview": "hi"}})
        self.handle({"type": "delivery_sent", "data": {"status": "sent", "class": "oob", "channel": "aprs_0", "msg_ref": "r3", "text_preview": "x"}})
        self.assertEqual(self.q.rows, [])


if __name__ == "__main__":
    unittest.main()
