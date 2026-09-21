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
