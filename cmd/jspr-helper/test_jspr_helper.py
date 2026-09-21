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


def modem(final_after_cancel):
    """Accepts the MO, never gets a satellite, answers a cancel with
    final_after_cancel (None = stays silent)."""
    def on_write(method, target, body):
        if target == "messageOriginate":
            return [line(200, "messageOriginate", {"message_id": 23, "message_response": "message_accepted"})]
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
                return [line(200, "messageOriginate", {"message_id": 23, "message_response": "message_accepted"}),
                        line(299, "messageOriginateStatus", {"message_id": 22, "final_mo_status": "mo_ack_received"}),
                        line(299, "messageOriginateStatus", {"message_id": 23, "final_mo_status": "mo_ack_received"})]
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
                        b'200 messageOriginate {"message_id": 23, "message_response": "message_accepted"}\r')
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
        h._cancel_refs = {7}
        h._do_send_mo_inner(244, "QUJD", 3, 7, lambda m: None, timeout_s=5)
        cancels = [w for w in h.written if w[1] == "messageOriginateStatus"]
        self.assertEqual(len(cancels), 1)
        res = h.results()
        self.assertEqual(len(res), 1)
        self.assertEqual(res[0][3]["final_mo_status"], "message_cancelled_pre_transit")
        self.assertEqual(h._cancel_refs, set())

    def test_cancel_for_another_send_is_ignored(self):
        h = FakeHelper(modem("message_cancelled_pre_transit"))
        h._cancel_refs = {6}
        h._do_send_mo_inner(244, "QUJD", 3, 7, lambda m: None, timeout_s=0.05)
        res = h.results()
        # Only the timeout path cancelled it, not the request for ref 6.
        self.assertEqual(len(res), 1)
        self.assertEqual(h._cancel_refs, {6})

    def test_stdin_cancel_skips_the_queue(self):
        import os
        r, w = os.pipe()
        h = FakeHelper(modem(None))
        h._cancel_refs = set()
        os.write(w, b'{"cmd":"cancel_mo","request_reference":9}\n{"cmd":"send","method":"GET","target":"x","json":{}}\n')
        os.close(w)
        old = sys.stdin
        sys.stdin = os.fdopen(r)
        try:
            h.stdin_reader()
        finally:
            sys.stdin = old
        self.assertEqual(h._cancel_refs, {9})
        self.assertEqual(h._cmd_queue.qsize(), 1)
