"""Tests for the MO timeout path of jspr_helper.py. [MESHSAT-1282]

Run: cd cmd/jspr-helper && python3 -m unittest test_jspr_helper -v
No serial port and no pyserial needed: the modem is a scripted fake.
"""
import json
import queue
import sys
import types
import unittest

sys.modules.setdefault("serial", types.ModuleType("serial"))  # not needed by the fake
import jspr_helper  # noqa: E402


class FakeHelper(jspr_helper.JSPRHelper):
    """A helper whose modem is a script: each write may release replies."""

    def __init__(self, on_write):
        self.running = True
        self._rx_buf = bytearray()
        self._cmd_queue = queue.Queue()
        self.ser = types.SimpleNamespace(reset_input_buffer=lambda: None)
        self.written = []   # (method, target, dict)
        self.emitted = []   # (type, code, target, dict)
        self._inbox = []
        self._on_write = on_write

    def _serial_write_raw(self, method, target, json_body):
        body = json.loads(json_body)
        self.written.append((method, target, body))
        self._inbox.extend(self._on_write(method, target, body) or [])

    def serial_read_lines(self):
        out, self._inbox = self._inbox, []
        return out

    def emit(self, msg_type, code, target, json_str):
        self.emitted.append((msg_type, code, target, json.loads(json_str)))

    def results(self):
        return [e for e in self.emitted if e[0] == "mo_result"]


def line(code, target, obj):
    return {"code": code, "target": target, "json_str": json.dumps(obj)}


def accepted_reply(message_id=23, request_reference=7, topic_id=244):
    """The modem's originate reply, as the real 9704 sends it: with the
    reference and topic of the command it answers."""
    return line(200, "messageOriginate", {"topic_id": topic_id, "request_reference": request_reference,
                                          "message_id": message_id, "message_response": "message_accepted"})


def modem(final_after_cancel):
    """Accepts the MO, never gets a satellite, answers a cancel with
    final_after_cancel (None = stays silent)."""
    def on_write(method, target, body):
        if target == "messageOriginate":
            return [accepted_reply()]
        if target == "messageOriginateStatus" and body.get("action") == "cancel":
            replies = [line(200, "messageOriginateStatus", {"message_id": 23})]
            if final_after_cancel:
                replies.append(line(299, "messageOriginateStatus",
                                    {"topic_id": 244, "message_id": 23, "final_mo_status": final_after_cancel}))
            return replies
        return []
    return on_write


class MOTimeoutTest(unittest.TestCase):
    def run_mo(self, h, **kw):
        h._do_send_mo_inner(244, "QUJD", 3, 7, lambda m: None, timeout_s=0.05, **kw)

    def test_timeout_cancels_and_reports_cancelled(self):
        h = FakeHelper(modem("message_cancelled_pre_transit"))
        self.run_mo(h)
        cancels = [w for w in h.written if w[1] == "messageOriginateStatus"]
        self.assertEqual(cancels, [("PUT", "messageOriginateStatus",
                                    {"topic_id": 244, "message_id": 23, "action": "cancel"})])
        res = h.results()
        self.assertEqual(len(res), 1)
        self.assertEqual(res[0][3]["final_mo_status"], "message_cancelled_pre_transit")
        self.assertEqual(res[0][3]["request_reference"], 7)

    def test_cancel_too_late_reports_the_ack(self):
        # The message went out while we were giving up: it IS delivered, and
        # reporting a failure here is what made the retries duplicate it.
        h = FakeHelper(modem("mo_ack_received"))
        self.run_mo(h)
        res = h.results()
        self.assertEqual(len(res), 1)
        self.assertEqual(res[0][3]["final_mo_status"], "mo_ack_received")

    def test_silent_modem_falls_back_to_helper_timeout(self):
        h = FakeHelper(modem(None))
        orig = h._cancel_and_settle
        h._cancel_and_settle = lambda *a, **k: orig(*a, settle_s=0.05, **k)
        self.run_mo(h)
        res = h.results()
        self.assertEqual(len(res), 1)
        self.assertEqual(res[0][3]["final_mo_status"], "helper_timeout")
        self.assertEqual(res[0][3]["request_reference"], 7)

    def test_the_cancel_ack_itself_is_never_a_result(self):
        # 200 messageOriginateStatus is the modem accepting the cancel. Go is
        # waiting on that very target, so forwarding it would settle the send.
        h = FakeHelper(modem("message_cancelled_pre_transit"))
        self.run_mo(h)
        leaked = [e for e in h.emitted if e[0] != "mo_result" and e[2] == "messageOriginateStatus"]
        self.assertEqual(leaked, [])

    def test_late_status_of_another_message_is_not_this_result(self):
        def on_write(method, target, body):
            if target == "messageOriginate":
                return [accepted_reply(),
                        line(299, "messageOriginateStatus", {"topic_id": 244, "message_id": 22, "final_mo_status": "mo_ack_received"}),
                        line(299, "messageOriginateStatus", {"topic_id": 244, "message_id": 23, "final_mo_status": "mo_ack_received"})]
            return []
        h = FakeHelper(on_write)
        h._do_send_mo_inner(244, "QUJD", 3, 7, lambda m: None, timeout_s=5)
        res = h.results()
        self.assertEqual(len(res), 1)
        self.assertEqual(res[0][3]["message_id"], 23)


class DrainTest(unittest.TestCase):
    def test_drain_drops_polls_and_keeps_messages(self):
        h = FakeHelper(lambda *a: [])
        h._cmd_queue.put({"cmd": "send", "method": "GET", "target": "constellationState"})
        h._cmd_queue.put({"cmd": "send_mo", "request_reference": 8})
        h._cmd_queue.put({"cmd": "send", "method": "GET", "target": "constellationState"})
        h._drain_stale_commands()
        left = []
        while not h._cmd_queue.empty():
            left.append(h._cmd_queue.get_nowait())
        self.assertEqual(left, [{"cmd": "send_mo", "request_reference": 8}])


if __name__ == "__main__":
    unittest.main()


class ByteSerial:
    """A serial port as bytes: what the modem has sent and the host not read."""

    def __init__(self, pending=b""):
        self.pending = bytearray(pending)

    @property
    def in_waiting(self):
        return len(self.pending)

    def read(self, n):
        out = bytes(self.pending[:n])
        del self.pending[:n]
        return out

    def reset_input_buffer(self):
        self.pending.clear()


class ByteHelper(jspr_helper.JSPRHelper):
    """A helper on the real line reader, over a ByteSerial."""

    def __init__(self, ser, on_write):
        self.running = True
        self._rx_buf = bytearray()
        self._cmd_queue = queue.Queue()
        self.ser = ser
        self.emitted = []
        self._on_write = on_write

    def _serial_write_raw(self, method, target, json_body):
        self.ser.pending += self._on_write(method, target, json.loads(json_body))

    def emit(self, msg_type, code, target, json_str):
        self.emitted.append((msg_type, code, target, json.loads(json_str)))


class MTDuringMOTest(unittest.TestCase):
    def test_mt_arriving_as_a_send_begins_is_kept(self):
        """The 9704 keeps no copy of an MT: half a segment line on the wire
        when a send starts must reach Go once the rest arrives. [MESHSAT-1282]"""
        seg = ('299 messageTerminateSegment {"topic_id": 244, "message_id": 5, '
               '"segment_length": 3, "segment_start": 0, "data": "QUJD"}\r').encode()
        half = len(seg) // 2

        def on_write(method, target, body):
            if target == "messageOriginate":
                return (seg[half:] +
                        b'200 messageOriginate {"topic_id": 244, "request_reference": 7, "message_id": 23, '
                        b'"message_response": "message_accepted"}\r')
            if target == "messageOriginateStatus" and body.get("action") == "cancel":
                return (b'200 messageOriginateStatus {"message_id": 23}\r'
                        b'299 messageOriginateStatus {"topic_id": 244, "message_id": 23, '
                        b'"final_mo_status": "message_cancelled_pre_transit"}\r')
            return b""

        h = ByteHelper(ByteSerial(seg[:half]), on_write)
        h._do_send_mo_inner(244, "QUJD", 3, 7, lambda m: None, timeout_s=0.05)
        segs = [e for e in h.emitted if e[2] == "messageTerminateSegment"]
        self.assertEqual(len(segs), 1, h.emitted)
        self.assertEqual(segs[0][3]["message_id"], 5)
        self.assertEqual(segs[0][3]["data"], "QUJD")


class HostCancelTest(unittest.TestCase):
    def test_host_cancel_cancels_the_send_in_the_modem(self):
        """Go asks to cancel the MT poll for a waiting message: the helper
        cancels it officially and reports the modem's verdict. [MESHSAT-1282]"""
        h = FakeHelper(modem("message_cancelled_pre_transit"))
        h._cancel_ops = {"op-a"}
        h._do_send_mo_inner(244, "QUJD", 3, 7, lambda m: None, timeout_s=5, op="op-a")
        cancels = [w for w in h.written if w[1] == "messageOriginateStatus"]
        self.assertEqual(len(cancels), 1)
        res = h.results()
        self.assertEqual(len(res), 1)
        self.assertEqual(res[0][3]["final_mo_status"], "message_cancelled_pre_transit")
        self.assertEqual(h._cancel_ops, set())

    def test_cancel_for_another_send_is_ignored(self):
        h = FakeHelper(modem("message_cancelled_pre_transit"))
        h._cancel_ops = {"op-b"}
        h._do_send_mo_inner(244, "QUJD", 3, 7, lambda m: None, timeout_s=0.05, op="op-a")
        res = h.results()
        # Only the timeout path cancelled it, not the request for op-b.
        self.assertEqual(len(res), 1)
        self.assertEqual(h._cancel_ops, {"op-b"})

    def test_a_cancel_names_the_op_not_the_reference(self):
        """Two sends with the same request_reference (it wraps at 100): a
        cancel for the first one's op never touches the second, which
        goes through; the same cancel for the second's own op cancels it.
        [MESHSAT-1584]"""
        def acking_modem(method, target, body):
            if target == "messageOriginate":
                return [accepted_reply(), line(299, "messageOriginateSegment",
                        {"topic_id": 244, "message_id": 23, "segment_length": 3, "segment_start": 0})]
            if target == "messageOriginateSegment":
                return [line(299, "messageOriginateStatus", {"topic_id": 244, "message_id": 23, "final_mo_status": "mo_ack_received"})]
            if target == "messageOriginateStatus" and body.get("action") == "cancel":
                return [line(200, "messageOriginateStatus", {"message_id": 23}),
                        line(299, "messageOriginateStatus", {"topic_id": 244, "message_id": 23,
                                                             "final_mo_status": "message_cancelled_pre_transit"})]
            return []
        for cancel_op, want_cancel in (("op-first", False), ("op-second", True)):
            h = FakeHelper(acking_modem)
            h._cancel_ops = {cancel_op}
            h._do_send_mo_inner(244, "QUJD", 3, 7, lambda m: None, timeout_s=5, op="op-second")
            cancels = [w for w in h.written if w[1] == "messageOriginateStatus" and w[2].get("action") == "cancel"]
            res = h.results()
            self.assertEqual(len(res), 1)
            self.assertEqual(bool(cancels), want_cancel, (cancel_op, h.written))
            if not want_cancel:
                self.assertEqual(res[0][3]["final_mo_status"], "mo_ack_received")
            # (With its own op the cancel is written; this modem had already
            # queued the ack, so the modem's word, too late, still wins.)

    def test_stdin_cancel_skips_the_queue(self):
        import os
        r, w = os.pipe()
        h = FakeHelper(modem(None))
        h._cancel_ops = set()
        os.write(w, b'{"cmd":"cancel_mo","op":"op-9"}\n{"cmd":"cancel_mo","request_reference":9}\n'
                 b'{"cmd":"send","method":"GET","target":"x","json":{}}\n')
        os.close(w)
        old = sys.stdin
        sys.stdin = os.fdopen(r)
        try:
            h.stdin_reader()
        finally:
            sys.stdin = old
        # A cancel without an op cannot name a send and is dropped.
        self.assertEqual(h._cancel_ops, {"op-9"})
        self.assertEqual(h._cmd_queue.qsize(), 1)


class FakeClock:
    """time.monotonic and time.sleep for the helper's 30 s first-answer
    deadline: every sleep moves the clock on by step seconds."""

    def __init__(self, step):
        self.now, self.step = 1000.0, step

    def monotonic(self):
        return self.now

    def sleep(self, _s):
        self.now += self.step


class PayloadAccountTest(unittest.TestCase):
    """What the helper says about a send it ends itself: whether the modem
    got the payload, and whether the modem refused the message outright.
    Go reads it to tell a send that cannot have left from one that may
    have. [MESHSAT-1584]"""

    def run_with_clock(self, h, step, timeout_s):
        from unittest import mock
        clock = FakeClock(step)
        with mock.patch.object(jspr_helper.time, "monotonic", clock.monotonic), \
                mock.patch.object(jspr_helper.time, "sleep", clock.sleep):
            h._do_send_mo_inner(244, "QUJD", 3, 7, lambda m: None, timeout_s=timeout_s)

    def test_no_answer_at_all_says_the_payload_never_went(self):
        h = FakeHelper(lambda *a: [])
        self.run_with_clock(h, 5, 240)
        res = h.results()
        self.assertEqual(len(res), 1)
        self.assertEqual(res[0][3]["final_mo_status"], "no_response")
        self.assertIs(res[0][3]["segment_supplied"], False)

    def test_a_lost_originate_reply_supplies_nothing(self):
        """The modem's originate reply never came, then a prompt: the send
        has no identity of its own, so the prompt is not answered (a prompt
        cannot prove it is this send's), and the send ends no_response with
        nothing supplied. The modem cannot transmit a message it has no data
        for. [MESHSAT-1584, review IMT-R1]"""
        def on_write(method, target, body):
            if target == "messageOriginate":
                return [line(299, "messageOriginateSegment",
                             {"topic_id": 244, "message_id": 23, "segment_length": 3, "segment_start": 0})]
            return []

        h = FakeHelper(on_write)
        self.run_with_clock(h, 1, 240)
        segs = [w for w in h.written if w[1] == "messageOriginateSegment"]
        self.assertEqual(segs, [])
        res = h.results()
        self.assertEqual(len(res), 1, res)
        self.assertEqual(res[0][3]["final_mo_status"], "no_response")
        self.assertIs(res[0][3]["segment_supplied"], False)

    def test_a_refusal_at_originate_is_marked(self):
        def on_write(method, target, body):
            if target == "messageOriginate":
                return [line(200, "messageOriginate", {"topic_id": 244, "request_reference": 7,
                                                        "message_response": "subscription_invalid"})]
            return []
        h = FakeHelper(on_write)
        h._do_send_mo_inner(244, "QUJD", 3, 7, lambda m: None, timeout_s=5)
        res = h.results()
        self.assertEqual(len(res), 1)
        self.assertEqual(res[0][3]["final_mo_status"], "subscription_invalid")
        self.assertEqual(res[0][3]["refused_at"], "originate")
        self.assertIs(res[0][3]["segment_supplied"], False)

    def test_an_error_code_at_originate_is_marked(self):
        def on_write(method, target, body):
            if target == "messageOriginate":
                return [line(418, "messageOriginate", {"error": "wrong state"})]
            return []
        h = FakeHelper(on_write)
        h._do_send_mo_inner(244, "QUJD", 3, 7, lambda m: None, timeout_s=5)
        res = h.results()
        self.assertEqual(len(res), 1)
        self.assertEqual(res[0][1], 418)
        self.assertEqual(res[0][3]["refused_at"], "originate")
        self.assertEqual(res[0][3]["request_reference"], 7)

    def test_helper_timeout_says_whether_the_segment_went(self):
        for asked, want in ((False, False), (True, True)):
            def on_write(method, target, body, asked=asked):
                if target == "messageOriginate":
                    out = [accepted_reply()]
                    if asked:
                        out.append(line(299, "messageOriginateSegment",
                                        {"topic_id": 244, "message_id": 23, "segment_length": 3, "segment_start": 0}))
                    return out
                return []  # silent to the cancel too
            h = FakeHelper(on_write)
            orig = h._cancel_and_settle
            h._cancel_and_settle = lambda *a, **k: orig(*a, settle_s=0.05, **k)
            h._do_send_mo_inner(244, "QUJD", 3, 7, lambda m: None, timeout_s=0.05)
            res = h.results()
            self.assertEqual(len(res), 1)
            self.assertEqual(res[0][3]["final_mo_status"], "helper_timeout")
            self.assertIs(res[0][3]["segment_supplied"], want)
