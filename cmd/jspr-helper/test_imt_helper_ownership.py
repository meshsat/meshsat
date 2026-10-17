"""Frames that are not this send's must never be acted on: an earlier send's
late ack, a prompt for another message or topic, an originate reply for
another reference. From the IMT route review of 17 Oct 2026 (IMT-R1), its
four cases as supplied, plus the other cases it lists. [MESHSAT-1584]

Run: cd cmd/jspr-helper && python3 -m unittest test_imt_helper_ownership -v
"""
import json
import unittest

from test_jspr_helper import FakeHelper, line


class IMTHelperOwnershipReview(unittest.TestCase):
    def run_helper(self, replies, final_replies=None):
        def on_write(method, target, body):
            if target == "messageOriginate":
                return replies
            if target == "messageOriginateSegment":
                matched = body["message_id"] == 23 and body["topic_id"] == 244
                final = "mo_ack_received" if matched else "segment_not_supplied"
                return (final_replies or []) + [line(299, "messageOriginateStatus", {
                    "topic_id": 244, "message_id": 23, "final_mo_status": final,
                })]
            if target == "messageOriginateStatus" and body.get("action") == "cancel":
                return [line(200, "messageOriginateStatus", {"message_id": body["message_id"]}),
                        line(299, "messageOriginateStatus", {"topic_id": 244, "message_id": body["message_id"],
                                                             "final_mo_status": "message_cancelled_pre_transit"})]
            return []

        h = FakeHelper(on_write)
        h._do_send_mo_inner(244, "QUJD", 3, 7, lambda m: None, timeout_s=1)
        segments = [body for method, target, body in h.written if target == "messageOriginateSegment"]
        results = [e[3] for e in h.results()]
        print(json.dumps({"test": self._testMethodName, "segments": segments, "results": results}), flush=True)
        return segments, results

    def accepted(self, request_reference=7, topic_id=244, message_id=23):
        return line(200, "messageOriginate", {
            "topic_id": topic_id, "message_id": message_id, "request_reference": request_reference,
            "message_response": "message_accepted",
        })

    def segment(self, message_id=23, topic_id=244, start=0, length=3):
        return line(299, "messageOriginateSegment", {
            "topic_id": topic_id, "message_id": message_id,
            "segment_length": length, "segment_start": start,
        })

    def assert_current_only(self, segments, results):
        self.assertEqual(len(results), 1)
        self.assertEqual(results[0]["message_id"], 23)
        self.assertEqual(results[0]["final_mo_status"], "mo_ack_received")
        self.assertEqual(results[0]["request_reference"], 7)
        self.assertIs(results[0]["segment_supplied"], True)
        self.assertEqual([(s["message_id"], s["topic_id"]) for s in segments], [(23, 244)])

    # --- the review's cases, as supplied ---

    def test_matching_identity_control(self):
        self.assert_current_only(*self.run_helper([self.accepted(), self.segment()]))

    def test_late_ack_before_current_identity_is_known(self):
        old = line(299, "messageOriginateStatus", {
            "topic_id": 244, "message_id": 22, "final_mo_status": "mo_ack_received",
        })
        self.assert_current_only(*self.run_helper([old, self.accepted(), self.segment()]))

    def test_foreign_segment_message_id_is_ignored(self):
        self.assert_current_only(*self.run_helper([
            self.accepted(), self.segment(message_id=22), self.segment(),
        ]))

    def test_foreign_segment_topic_is_ignored(self):
        self.assert_current_only(*self.run_helper([
            self.accepted(), self.segment(topic_id=245), self.segment(),
        ]))

    # --- the other cases the review lists ---

    def test_late_ack_after_identity_is_known(self):
        old = line(299, "messageOriginateStatus", {"topic_id": 244, "message_id": 22, "final_mo_status": "mo_ack_received"})
        self.assert_current_only(*self.run_helper([self.accepted(), old, self.segment()]))

    def test_ack_for_this_message_id_on_another_topic_is_ignored(self):
        foreign = line(299, "messageOriginateStatus", {"topic_id": 245, "message_id": 23, "final_mo_status": "mo_ack_received"})
        self.assert_current_only(*self.run_helper([self.accepted(), self.segment()], final_replies=[foreign]))

    def test_old_originate_reply_does_not_become_the_identity(self):
        # An earlier send's reply (reference 6) arrives first; only the
        # reply naming reference 7 is this send's.
        self.assert_current_only(*self.run_helper([
            self.accepted(request_reference=6, message_id=22), self.segment(message_id=22),
            self.accepted(), self.segment(),
        ]))

    def test_originate_reply_for_another_topic_is_not_the_identity(self):
        self.assert_current_only(*self.run_helper([
            self.accepted(topic_id=245, message_id=22), self.accepted(), self.segment(),
        ]))

    def test_a_prompt_for_more_than_one_segment_is_not_answered(self):
        segments, results = self.run_helper([self.accepted(), self.segment(start=0, length=2)])
        self.assertEqual(segments, [])
        self.assertEqual(len(results), 1)
        self.assertIs(results[0]["segment_supplied"], False)
        self.assertNotEqual(results[0]["final_mo_status"], "mo_ack_received")

    def test_a_late_ack_across_a_timeout_is_not_this_result(self):
        # The send times out and cancels; an earlier message's ack arrives
        # during the settle: only this message's own verdict ends it.
        old = line(299, "messageOriginateStatus", {"topic_id": 244, "message_id": 22, "final_mo_status": "mo_ack_received"})
        other_topic = line(299, "messageOriginateStatus", {"topic_id": 245, "message_id": 23, "final_mo_status": "mo_ack_received"})

        def on_write(method, target, body):
            if target == "messageOriginate":
                return [self.accepted()]
            if target == "messageOriginateStatus" and body.get("action") == "cancel":
                return [line(200, "messageOriginateStatus", {"message_id": 23}), old, other_topic,
                        line(299, "messageOriginateStatus", {"topic_id": 244, "message_id": 23,
                                                             "final_mo_status": "message_cancelled_pre_transit"})]
            return []

        h = FakeHelper(on_write)
        h._do_send_mo_inner(244, "QUJD", 3, 7, lambda m: None, timeout_s=0.05)
        res = [e[3] for e in h.results()]
        self.assertEqual(len(res), 1)
        self.assertEqual((res[0]["message_id"], res[0]["final_mo_status"]), (23, "message_cancelled_pre_transit"))
        self.assertIs(res[0]["segment_supplied"], False)

    def test_a_segment_is_written_only_in_answer_to_this_messages_prompt(self):
        """A modem that hands over one frame at a time and takes a segment
        only as the answer to its latest prompt: the helper must not answer
        a prompt for another message (even with its own message id), only
        the one for this message."""
        class OneFrameAtATime(FakeHelper):
            def serial_read_lines(self):
                if not self._inbox:
                    return []
                frame = self._inbox.pop(0)
                if frame["target"] == "messageOriginateSegment" and frame["code"] == 299:
                    d = json.loads(frame["json_str"])
                    self.last_prompt = (d["message_id"], d["topic_id"])
                return [frame]

        unprompted = []

        def on_write(method, target, body):
            if target == "messageOriginate":
                return [self.accepted(), self.segment(message_id=22), self.segment()]
            if target == "messageOriginateSegment":
                if getattr(h, "last_prompt", None) != (body["message_id"], body["topic_id"]):
                    unprompted.append((getattr(h, "last_prompt", None), body["message_id"]))
                    return [line(299, "messageOriginateStatus", {"topic_id": 244, "message_id": 23,
                                                                 "final_mo_status": "segment_not_supplied"})]
                return [line(299, "messageOriginateStatus", {"topic_id": 244, "message_id": 23,
                                                             "final_mo_status": "mo_ack_received"})]
            return []

        h = OneFrameAtATime(on_write)
        h._do_send_mo_inner(244, "QUJD", 3, 7, lambda m: None, timeout_s=1)
        self.assertEqual(unprompted, [])
        res = [e[3] for e in h.results()]
        self.assertEqual(len(res), 1)
        self.assertEqual(res[0]["final_mo_status"], "mo_ack_received")

    def test_a_stale_cancel_is_dropped_when_a_new_send_starts(self):
        # Go wrote a cancel for an earlier send's op after that send ended;
        # it is dropped when the next send starts. One for the starting
        # send itself is taken by it.
        h = FakeHelper(lambda *a: [])
        h._cancel_ops = {"op-old", "op-now"}
        h.do_send_mo(244, "QUJD", 3, 7, timeout_s=0.05, op="op-now")
        self.assertEqual(h._cancel_ops, set())


if __name__ == "__main__":
    unittest.main(verbosity=2)
