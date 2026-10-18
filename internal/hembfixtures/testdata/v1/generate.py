#!/usr/bin/env python3
"""Generate the HeMB-NG stage 2 fixture release v1 (MESHSAT-1584).

Every file in this folder except README.md and this script is written by it,
byte for byte the same on every run: the ES256 signatures use RFC 6979
deterministic signing. Each case's expected value is written by hand below and
checked against a small reference implementation of the contract before
anything is written; a mismatch stops the run.

    python3 generate.py            write the files and SHA256SUMS
    python3 generate.py --check    regenerate in memory and compare

Needs Python 3.10+ and the `cryptography` package (OpenSSL 3.2+ for the
deterministic signatures). The contract is "HeMB-NG stage 2 and stage 3: the
cross-repo contract", r3 (18 Oct 2026); the decision numbers below (C1d, C3,
C6, ...) are its.
"""

import base64
import hashlib
import itertools
import json
import os
import struct
import sys
import uuid

from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec

HERE = os.path.dirname(os.path.abspath(__file__))
RELEASE = "fixtures-v1.0"
NAMESPACE = uuid.uuid5(uuid.NAMESPACE_URL, "https://meshsat.net/ns/hemb-ng/v1")
SKEW_MS = 120_000
KEEP_AFTER_EXPIRY_MS = 24 * 3600_000
CAPACITY = 50_000


def dump(obj):
    return (json.dumps(obj, indent=2, sort_keys=True, ensure_ascii=True) + "\n").encode()


# --- C1e: ids ---------------------------------------------------------------

def uuid7(ts_ms, rand):
    """A UUIDv7 from a millisecond time and 10 fixed bytes (RFC 9562 5.7)."""
    b = bytearray(ts_ms.to_bytes(6, "big") + rand)
    b[6] = 0x70 | (b[6] & 0x0F)
    b[8] = 0x80 | (b[8] & 0x3F)
    return uuid.UUID(bytes=bytes(b))


def id_valid(text, field, context):
    """C1e: v7 everywhere; v5 only in the two documented cases."""
    try:
        u = uuid.UUID(text)
    except ValueError:
        return False
    if str(u) != text or (u.bytes[8] >> 6) != 0b10:
        return False
    if u.version == 7:
        return True
    if u.version != 5:
        return False
    if field == "mid" and context.get("sender") == "hub":
        return context.get("name") is not None and str(uuid.uuid5(NAMESPACE, context["name"])) == text
    if field == "sos_id" and "legacy_run_ms" in context:
        return str(uuid.uuid5(NAMESPACE, "%s:%d" % (context["bridge_id"], context["legacy_run_ms"]))) == text
    return False


T0 = 1_792_330_000_000  # 2026-10-17T14:46:40Z, the fixtures' clock
MID_A = str(uuid7(T0, bytes.fromhex("0a0b0c0d0e0f10111213")))
MID_B = str(uuid7(T0 + 1, bytes.fromhex("1a1b1c1d1e1f20212223")))
MID_C = str(uuid7(T0 + 2, bytes.fromhex("2a2b2c2d2e2f30313233")))
OP_1 = str(uuid7(T0 + 10, bytes.fromhex("31323334353637383940")))
OP_2 = str(uuid7(T0 + 20, bytes.fromhex("41424344454647484950")))
OP_3 = str(uuid7(T0 + 30, bytes.fromhex("51525354555657585960")))
OP_OTHER = str(uuid7(T0 + 40, bytes.fromhex("61626364656667686970")))
V4 = "0f8fad5b-d9cb-469f-a165-70867728950e"


def ids():
    c1c_name = "twilio:SM0123456789abcdef0123456789abcdef"
    c10 = {"bridge_id": "kit-a", "legacy_run_ms": 1_792_330_000_123}
    mid_v5 = str(uuid.uuid5(NAMESPACE, c1c_name))
    sos_v5 = str(uuid.uuid5(NAMESPACE, "kit-a:1792330000123"))
    bad_variant = MID_A[:19] + "c" + MID_A[20:]
    cases = [
        {"case": "a v7 mid", "field": "mid", "id": MID_A, "context": {}, "valid": True},
        {"case": "a v7 op_id", "field": "op_id", "id": OP_1, "context": {}, "valid": True},
        {"case": "a v4 id is refused", "field": "mid", "id": V4, "context": {}, "valid": False},
        {"case": "a variant other than 10 is refused", "field": "mid", "id": bad_variant, "context": {}, "valid": False},
        {"case": "upper case is not the canonical text", "field": "mid", "id": MID_A.upper(), "context": {}, "valid": False},
        {"case": "C1c: a Hub ingress mid", "field": "mid", "id": mid_v5, "context": {"sender": "hub", "name": c1c_name}, "valid": True},
        {"case": "C1c: the same v5 mid from a Bridge is refused", "field": "mid", "id": mid_v5, "context": {"sender": "bridge:kit-a"}, "valid": False},
        {"case": "C1c: a v5 mid whose name does not derive it", "field": "mid", "id": mid_v5, "context": {"sender": "hub", "name": "twilio:SMother"}, "valid": False},
        {"case": "C10: a legacy SOS run", "field": "sos_id", "id": sos_v5, "context": c10, "valid": True},
        {"case": "C10: a v5 sos_id without its legacy run", "field": "sos_id", "id": sos_v5, "context": {"bridge_id": "kit-a"}, "valid": False},
        {"case": "C10: a legacy run of another Bridge", "field": "sos_id", "id": sos_v5, "context": {"bridge_id": "kit-b", "legacy_run_ms": 1_792_330_000_123}, "valid": False},
        {"case": "a v5 op_id is refused", "field": "op_id", "id": sos_v5, "context": c10, "valid": False},
    ]
    for c in cases:
        assert id_valid(c["id"], c["field"], c["context"]) == c["valid"], c["case"]
    return {
        "namespace": str(NAMESPACE),
        "namespace_derivation": "UUIDv5(NAMESPACE_URL, \"https://meshsat.net/ns/hemb-ng/v1\")",
        "v5_names": {"C1c": "<provider>:<provider message id>", "C10": "<bridge id>:<run start unix ms>"},
        "cases": cases,
    }


# --- C1d: the content digest -------------------------------------------------

TAG = b"meshsat.content/1"
TEXT = "text/plain;charset=utf-8"
MID_EXPIRES = T0 + 3600_000


def content_preimage(mid, recipient, content_type, mid_expires_ms, body):
    fields = [TAG, uuid.UUID(mid).bytes, recipient.encode(), content_type.encode(),
              struct.pack(">Q", mid_expires_ms), body]
    return b"".join(struct.pack(">I", len(f)) + f for f in fields)


def content_sha256(mid, recipient, content_type, mid_expires_ms, body):
    return hashlib.sha256(content_preimage(mid, recipient, content_type, mid_expires_ms, body)).hexdigest()


def digests():
    north = b"Water at the north gate"
    vectors = [
        ("plain ASCII", MID_A, "bridge:kit-b", TEXT, MID_EXPIRES, north),
        ("UTF-8 in three scripts", MID_A, "bridge:kit-b", TEXT, MID_EXPIRES,
         "Wasser am Nordtor. \u039d\u03b5\u03c1\u03cc \u03c3\u03c4\u03b7\u03bd \u03c0\u03cd\u03bb\u03b7. \u0645\u0627\u0621".encode()),
        ("an empty body", MID_B, "device:phone-1", TEXT, MID_EXPIRES, b""),
        ("another recipient changes the digest", MID_A, "bridge:kit-c", TEXT, MID_EXPIRES, north),
        ("another deadline changes the digest", MID_A, "bridge:kit-b", TEXT, MID_EXPIRES + 1, north),
    ]
    out = []
    for name, mid, rcpt, ct, exp, body in vectors:
        out.append({
            "case": name, "mid": mid, "recipient": rcpt, "content_type": ct,
            "mid_expires_at_ms": exp, "body_hex": body.hex(),
            "preimage_hex": content_preimage(mid, rcpt, ct, exp, body).hex(),
            "content_sha256": content_sha256(mid, rcpt, ct, exp, body),
        })
    assert len({v["content_sha256"] for v in out}) == len(out)
    return {"tag": TAG.decode(),
            "layout": "each field: 4-byte big-endian length, then the bytes; in order: tag, mid (16 raw bytes), recipient (UTF-8), content_type (UTF-8), mid_expires_at (unix ms, 8 bytes big-endian), body",
            "vectors": out}


D_A = content_sha256(MID_A, "bridge:kit-b", TEXT, MID_EXPIRES, b"Water at the north gate")
D_A2 = content_sha256(MID_A, "bridge:kit-b", TEXT, MID_EXPIRES, b"Water at the south gate")
D_B = content_sha256(MID_B, "bridge:kit-b", TEXT, MID_EXPIRES, b"Ten minutes")
D_C = content_sha256(MID_C, "bridge:kit-b", TEXT, MID_EXPIRES, b"x")


# --- C3: the receiver inbox --------------------------------------------------

class Inbox:
    """Reference for C3: commit key (origin, op_id), index (origin, mid)."""

    def __init__(self, capacity=CAPACITY):
        self.ops = {}
        self.mids = {}
        self.capacity = capacity

    def prune(self, now):
        for key in [k for k, v in self.ops.items() if now > v["expires_at"] + SKEW_MS + KEEP_AFTER_EXPIRY_MS]:
            self.ops.pop(key)

    def commit(self, s, now, clock_established=True):
        origin, op = s["origin"], s["op_id"]
        if not (id_valid(op, "op_id", {}) and id_valid(s["mid"], "mid", {})):
            return {"result": "refused", "reason": "bad_id"}
        if s["content_sha256"] != s["recomputed_sha256"]:
            return {"result": "refused", "reason": "digest_mismatch"}
        if s["expires_at"] > s["mid_expires_at"]:
            return {"result": "refused", "reason": "bad_expiry"}
        held = self.ops.get((origin, op))
        if held is not None:
            if held["mid"] == s["mid"] and held["digest"] == s["content_sha256"]:
                return {"result": "stored", "reason": "", "stored_now": False}
            return {"result": "refused", "reason": "op_conflict"}
        known = self.mids.get((origin, s["mid"]))
        if known is not None and known != s["content_sha256"]:
            return {"result": "refused", "reason": "mid_conflict"}
        unjudged = False
        if now > s["expires_at"] + SKEW_MS:
            if clock_established:
                return {"result": "refused", "reason": "expired_at_recipient"}
            unjudged = True
        self.prune(now)
        if len(self.ops) >= self.capacity:
            return {"result": "refused", "reason": "receiver_busy"}
        self.ops[(origin, op)] = {"mid": s["mid"], "digest": s["content_sha256"], "expires_at": s["expires_at"]}
        stored_now = known is None
        self.mids[(origin, s["mid"])] = s["content_sha256"]
        r = {"result": "stored", "reason": "", "stored_now": stored_now}
        if unjudged:
            r["expiry_unjudged"] = True
        return r


def sub(op, mid=MID_A, digest=D_A, origin="bridge:kit-a", expires=T0 + 600_000, recomputed=None, mid_expires=MID_EXPIRES):
    return {"origin": origin, "op_id": op, "mid": mid, "content_sha256": digest,
            "recomputed_sha256": recomputed or digest, "expires_at": expires, "mid_expires_at": mid_expires}


def step(s, now, expect, clock_established=True):
    return {"submission": s, "now": now, "clock_established": clock_established, "expect": expect}


OK_NEW = {"result": "stored", "reason": "", "stored_now": True}
OK_AGAIN = {"result": "stored", "reason": "", "stored_now": False}


def refused(reason):
    return {"result": "refused", "reason": reason}


def inbox_cases():
    inside = T0 + 700_000   # the op expires at T0 + 600 s; with the 120 s skew it is still in time
    after = T0 + 720_001    # 1 ms past expires_at + skew
    cases = [
        ("a first commit", [step(sub(OP_1), T0, OK_NEW)]),
        ("a repeat with the same digest is acknowledged again, not stored again",
         [step(sub(OP_1), T0, OK_NEW), step(sub(OP_1), T0 + 5000, OK_AGAIN)]),
        ("a repeat of the op with another digest is refused",
         [step(sub(OP_1), T0, OK_NEW), step(sub(OP_1, digest=D_A2), T0 + 5000, refused("op_conflict"))]),
        ("a repeat of the op with another mid is refused",
         [step(sub(OP_1), T0, OK_NEW), step(sub(OP_1, mid=MID_B, digest=D_B), T0 + 5000, refused("op_conflict"))]),
        ("a second op of the same message is stored once and acknowledged",
         [step(sub(OP_1), T0, OK_NEW), step(sub(OP_2), T0 + 5000, OK_AGAIN)]),
        ("a second op of the same mid with changed content is refused, never acknowledged as the old message",
         [step(sub(OP_1), T0, OK_NEW), step(sub(OP_2, digest=D_A2), T0 + 5000, refused("mid_conflict"))]),
        ("the same op id from another origin is another operation",
         [step(sub(OP_1), T0, OK_NEW), step(sub(OP_1, origin="bridge:kit-c"), T0 + 5000, OK_NEW)]),
        ("a carried digest that the recomputation does not give is refused",
         [step(sub(OP_1, recomputed=D_A2), T0, refused("digest_mismatch"))]),
        ("an op deadline past the message deadline is refused",
         [step(sub(OP_1, expires=MID_EXPIRES + 1), T0, refused("bad_expiry"))]),
        ("an op id that is not a v7 is refused",
         [step(sub(V4), T0, refused("bad_id"))]),
        ("a first commit inside the skew allowance is stored",
         [step(sub(OP_1), inside, OK_NEW)]),
        ("a first commit after expiry plus skew is refused",
         [step(sub(OP_1), after, refused("expired_at_recipient"))]),
        ("with the clock not established it is stored and marked unjudged",
         [step(sub(OP_1), after, dict(OK_NEW, expiry_unjudged=True), clock_established=False)]),
        ("a committed op repeated after its expiry is acknowledged again",
         [step(sub(OP_1), T0, OK_NEW), step(sub(OP_1), after + 3600_000, OK_AGAIN)]),
        ("a queued publication arriving after expiry is refused at commit; a new op cannot extend the message deadline",
         [step(sub(OP_1), after, refused("expired_at_recipient")),
          step(sub(OP_2, expires=MID_EXPIRES + 60_000), after, refused("bad_expiry"))]),
    ]
    out = []
    for name, steps in cases:
        box = Inbox()
        for st in steps:
            got = box.commit(st["submission"], st["now"], st["clock_established"])
            assert got == st["expect"], (name, got, st["expect"])
        out.append({"case": name, "capacity": CAPACITY, "steps": steps})
    # capacity: a receiver of two holds two live entries and refuses a third;
    # once the first is past its retention it is pruned and the third goes in
    retained_until = T0 + 1000 + SKEW_MS + KEEP_AFTER_EXPIRY_MS
    cap = [step(sub(OP_1, expires=T0 + 1000), T0, OK_NEW),
           step(sub(OP_2, mid=MID_B, digest=D_B), T0, OK_NEW),
           step(sub(OP_3, mid=MID_C, digest=D_C), T0 + 1, refused("receiver_busy")),
           step(sub(OP_3, mid=MID_C, digest=D_C, expires=retained_until + 600_000, mid_expires=retained_until + 3600_000), retained_until + 1, OK_NEW)]
    box = Inbox(capacity=2)
    for st in cap:
        got = box.commit(st["submission"], st["now"], st["clock_established"])
        assert got == st["expect"], ("capacity", got, st["expect"])
    out.append({"case": "at capacity a new op is refused receiver_busy; an entry past its retention goes first", "capacity": 2, "steps": cap})
    return {"skew_ms": SKEW_MS, "retention_after_expiry_ms": KEEP_AFTER_EXPIRY_MS, "capacity_target": CAPACITY,
            "check_order": ["ids (C1e)", "digest recomputed (C1d)", "expires_at <= mid_expires_at", "known (origin, op_id)",
                            "known (origin, mid)", "expiry at commit", "capacity"],
            "note": "recomputed_sha256 stands for the receiver's own recomputation of the digest from the delivered fields",
            "cases": out}


# --- C4: Hub ingress -----------------------------------------------------------

class HubOps:
    """Reference for C4: key (tenant, sender, op_id), index (tenant, sender, mid)."""

    def __init__(self):
        self.ops = {}
        self.mids = {}

    def submit(self, s):
        key = (s["tenant"], s["sender"], s["op_id"])
        if not (id_valid(s["op_id"], "op_id", {}) and id_valid(s["mid"], "mid", {})):
            return {"result": "refused", "reason": "bad_id"}
        if s["content_sha256"] != s["recomputed_sha256"]:
            return {"result": "refused", "reason": "digest_mismatch"}
        if s["expires_at"] > s["mid_expires_at"]:
            return {"result": "refused", "reason": "bad_expiry"}
        imm = (s["to"], s["mid"], s["content_sha256"], s["expires_at"])
        if key in self.ops:
            if self.ops[key] == imm:
                return {"result": "replay_all_events", "reason": ""}
            return {"result": "refused", "reason": "op_conflict"}
        mk = (s["tenant"], s["sender"], s["mid"])
        if mk in self.mids and self.mids[mk] != s["content_sha256"]:
            return {"result": "refused", "reason": "mid_conflict"}
        self.ops[key] = imm
        self.mids[mk] = s["content_sha256"]
        return {"result": "admitted", "reason": ""}


def hsub(op, tenant="t-1", sender="bridge:kit-a", to="bridge:kit-b", mid=MID_A, digest=D_A, expires=T0 + 600_000, recomputed=None):
    return {"tenant": tenant, "sender": sender, "op_id": op, "to": to, "mid": mid, "content_sha256": digest,
            "recomputed_sha256": recomputed or digest, "expires_at": expires, "mid_expires_at": MID_EXPIRES}


def hub_cases():
    A = {"result": "admitted", "reason": ""}
    R = {"result": "replay_all_events", "reason": ""}
    cases = [
        ("a new op is admitted", [(hsub(OP_1), A)]),
        ("a repeat with the same immutable fields replays every event", [(hsub(OP_1), A), (hsub(OP_1), R)]),
        ("a repeat with another recipient is refused", [(hsub(OP_1), A), (hsub(OP_1, to="bridge:kit-c"), refused("op_conflict"))]),
        ("a repeat with another deadline is refused", [(hsub(OP_1), A), (hsub(OP_1, expires=T0 + 600_001), refused("op_conflict"))]),
        ("the same op id from another tenant is another op", [(hsub(OP_1), A), (hsub(OP_1, tenant="t-2"), A)]),
        ("the same op id from another sender is another op", [(hsub(OP_1), A), (hsub(OP_1, sender="device:phone-1"), A)]),
        ("a new op of a known mid with the same content is admitted", [(hsub(OP_1), A), (hsub(OP_2), A)]),
        ("a new op of a known mid with changed content is refused", [(hsub(OP_1), A), (hsub(OP_2, digest=D_A2), refused("mid_conflict"))]),
        ("a digest the Hub does not recompute is refused", [(hsub(OP_1, recomputed=D_A2), refused("digest_mismatch"))]),
        ("an op deadline past the message deadline is refused", [(hsub(OP_1, expires=MID_EXPIRES + 1), refused("bad_expiry"))]),
        ("an op id that is not a v7 is refused", [(hsub(V4), refused("bad_id"))]),
    ]
    out = []
    for name, steps in cases:
        hub = HubOps()
        for s, want in steps:
            got = hub.submit(s)
            assert got == want, (name, got, want)
        out.append({"case": name, "steps": [{"submission": s, "expect": w} for s, w in steps]})
    return {"key": "(tenant, sender principal, op_id)", "mid_index": "(tenant, sender principal, mid)", "cases": out}


# --- C6 and C6a: the reducer -------------------------------------------------

KINDS = [
    {"kind": "hub_broker_puback", "class": "accepted", "authority": "local", "outcome": "accepted, not confirmed"},
    {"kind": "hub_forwarded", "class": "forwarded", "authority": "hub", "outcome": "evidence only"},
    {"kind": "recipient_app_stored", "class": "completed", "authority": "recipient", "outcome": "delivered_app"},
    {"kind": "recipient_refused", "class": "failed", "authority": "recipient", "outcome": "none"},
    {"kind": "recipient_refused", "class": "expired", "authority": "recipient", "outcome": "none"},
    {"kind": "hub_refused", "class": "failed", "authority": "hub", "outcome": "none"},
    {"kind": "hub_expired", "class": "expired", "authority": "hub", "outcome": "none"},
    {"kind": "hub_unknown", "class": "unknown", "authority": "hub", "outcome": "none"},
]
ALLOWED = {(k["kind"], k["class"], k["authority"]) for k in KINDS}
ORDER = ["completed", "failed", "expired", "forwarded", "unknown", "accepted", "pending"]
OPERATION = {"op_id": OP_1, "mid": MID_A, "recipient": "bridge:kit-b"}


def valid_event(op, e):
    return (e["op_id"] == op["op_id"] and e["mid"] == op["mid"] and e["recipient"] == op["recipient"]
            and (e["kind"], e["class"], e["authority"]) in ALLOWED and id_valid(e["event_id"], "event_id", {}))


def reduce_events(op, events):
    held = {}
    rejected = set()
    for e in events:
        if valid_event(op, e):
            held.setdefault(e["event_id"], e)
        else:
            rejected.add(e["event_id"])
    classes = {e["class"] for e in held.values()}
    state = next((c for c in ORDER if c in classes), "pending")
    return {
        "state": state,
        "conflict": "completed" in classes and "failed" in classes,
        "failed_reasons": sorted({e["reason"] for e in held.values() if e["class"] in ("failed", "expired") and e["reason"]}),
        "evidence": sorted(held),
        "rejected": sorted(rejected - set(held)),
    }


EV_SEQ = itertools.count(1)


def ev(cls, kind, authority, rev, reason="", op=None, mid=None, recipient=None):
    n = next(EV_SEQ)
    return {"v": 1, "op_id": op or OP_1, "mid": mid or MID_A, "recipient": recipient or "bridge:kit-b",
            "event_id": str(uuid7(T0 + 1000 * n, n.to_bytes(10, "big"))), "rev": rev,
            "class": cls, "kind": kind, "reason": reason, "authority": authority,
            "scope": "the recipient's MeshSat app" if cls == "completed" else "the Hub",
            "detail": "", "at": "2026-10-17T14:%02d:%02dZ" % (46 + n // 60, n % 60)}


def reducer_cases():
    acc = ev("accepted", "hub_broker_puback", "local", 0)
    fwd = ev("forwarded", "hub_forwarded", "hub", 2)
    done = ev("completed", "recipient_app_stored", "recipient", 3)
    fail = ev("failed", "hub_refused", "hub", 4, "refused_not_admitted")
    exp = ev("expired", "hub_expired", "hub", 5, "expired")
    unk = ev("unknown", "hub_unknown", "hub", 6, "publish_timeout")
    fwd_hi = ev("forwarded", "hub_forwarded", "hub", 7)
    other_op = ev("completed", "recipient_app_stored", "recipient", 1, op=OP_OTHER)
    other_rcpt = ev("completed", "recipient_app_stored", "recipient", 1, recipient="bridge:kit-c")
    other_mid = ev("completed", "recipient_app_stored", "recipient", 1, mid=MID_B)
    wrong_auth = ev("completed", "recipient_app_stored", "hub", 8)
    sets = [
        ("the review's counterexample: completed rev 3 and failed rev 4", [done, fail], "completed", True),
        ("completion after a higher-revision weaker event", [fwd_hi, done], "completed", False),
        ("completion after expiry", [exp, done], "completed", False),
        ("completion after a timeout reported unknown", [unk, done], "completed", False),
        ("failed, and nothing stronger", [acc, fwd, fail], "failed", False),
        ("expired after forwarding", [acc, fwd, exp], "expired", False),
        ("forwarded outranks unknown", [unk, fwd], "forwarded", False),
        ("unknown alone", [acc, unk], "unknown", False),
        ("accepted alone", [acc], "accepted", False),
        ("a duplicate event counts once", [fwd, fwd, done, done], "completed", False),
        ("evidence for another op, recipient or mid stays out", [fwd, other_op, other_rcpt, other_mid], "forwarded", False),
        ("a completion the Hub claims for the recipient is not completion", [fwd, wrong_auth], "forwarded", False),
        ("no valid evidence", [other_op], "pending", False),
    ]
    out = []
    for name, events, state, conflict in sets:
        want = reduce_events(OPERATION, events)
        assert want["state"] == state and want["conflict"] == conflict, (name, want)
        for order in itertools.permutations(events):
            assert reduce_events(OPERATION, list(order)) == want, (name, order)
        out.append({"case": name, "events": events, "expect": want})
    escalation = [
        {"case": "completed while the attempt is current", "class": "completed", "intent_open": True, "attempt_current": True, "intent_expired": False, "action": "raise_outcome"},
        {"case": "completed after the intent moved to another route", "class": "completed", "intent_open": True, "attempt_current": False, "intent_expired": False, "action": "raise_outcome"},
        {"case": "completed after the intent closed", "class": "completed", "intent_open": False, "attempt_current": False, "intent_expired": True, "action": "raise_outcome"},
        {"case": "failed while the attempt is current", "class": "failed", "intent_open": True, "attempt_current": True, "intent_expired": False, "action": "may_plan_next_route"},
        {"case": "expired while the attempt is current", "class": "expired", "intent_open": True, "attempt_current": True, "intent_expired": False, "action": "may_plan_next_route"},
        {"case": "failed after the intent moved on", "class": "failed", "intent_open": True, "attempt_current": False, "intent_expired": False, "action": "record_only"},
        {"case": "failed after the intent expired", "class": "failed", "intent_open": True, "attempt_current": True, "intent_expired": True, "action": "record_only"},
        {"case": "forwarded changes no transition", "class": "forwarded", "intent_open": True, "attempt_current": True, "intent_expired": False, "action": "record_only"},
        {"case": "unknown changes no transition", "class": "unknown", "intent_open": True, "attempt_current": True, "intent_expired": False, "action": "record_only"},
    ]
    for c in escalation:
        if c["class"] == "completed":
            action = "raise_outcome"
        elif c["class"] in ("failed", "expired") and c["intent_open"] and c["attempt_current"] and not c["intent_expired"]:
            action = "may_plan_next_route"
        else:
            action = "record_only"
        assert action == c["action"], c["case"]
    return {"operation": OPERATION, "projection_order": ORDER,
            "rule": "state = the strongest class in the set of validated events (union by event_id); rev decides nothing; completed and failed together set conflict",
            "consumers": "reduce every permutation and every duplicate of each set; each must give expect",
            "sets": out, "escalation": escalation}


# --- C7: the v2 directory envelope ------------------------------------------

def test_key(label):
    seed = hashlib.sha256(b"meshsat fixtures v1 test key: " + label).digest()
    return ec.derive_private_key(int.from_bytes(seed, "big") % (2**255) + 1, ec.SECP256R1())


def pkix(key):
    return key.public_key().public_bytes(serialization.Encoding.DER, serialization.PublicFormat.SubjectPublicKeyInfo)


def envelope(key, payload):
    sig = key.sign(payload, ec.ECDSA(hashes.SHA256(), deterministic_signing=True))
    return {"v": 2, "key_id": hashlib.sha256(pkix(key)).hexdigest(), "alg": "ES256",
            "payload": base64.b64encode(payload).decode(), "sig": base64.b64encode(sig).decode()}


def verify(pub_der, env):
    pub = serialization.load_der_public_key(pub_der)
    try:
        pub.verify(base64.b64decode(env["sig"]), base64.b64decode(env["payload"]), ec.ECDSA(hashes.SHA256()))
        return True
    except Exception:
        return False


def directory():
    old, new = test_key(b"old"), test_key(b"new")
    snapshot = json.dumps({
        "tenant_id": "t-1", "version": 42, "seeded": False,
        "contacts": [{"id": "c-1", "name": "Kit B", "origin": "hub:t-1",
                      "addresses": [{"kind": "MQTT", "value": "bridge:kit-b", "origin": "hub:t-1"},
                                    {"kind": "TETRA", "value": "1001", "origin": "hub:t-1"}]}],
        "groups": [{"id": "g-1", "name": "North team", "kind": "team", "members": ["c-1"]}],
        "x_future_field": {"note": "a field this version does not know"},
    }, separators=(",", ":"), sort_keys=True).encode()
    env = envelope(old, snapshot)
    tampered = dict(env, payload=base64.b64encode(snapshot.replace(b'"version":42', b'"version":43')).decode())
    rotation_payload = json.dumps({"type": "rotation", "tenant_id": "t-1", "version": 43,
                                   "new_key_id": hashlib.sha256(pkix(new)).hexdigest(),
                                   "new_key_pkix": base64.b64encode(pkix(new)).decode()},
                                  separators=(",", ":"), sort_keys=True).encode()
    rotation = envelope(old, rotation_payload)
    after = envelope(new, snapshot.replace(b'"version":42', b'"version":44'))
    self_rotate = envelope(new, rotation_payload)
    assert verify(pkix(old), env) and not verify(pkix(old), tampered)
    assert verify(pkix(old), rotation) and verify(pkix(new), after) and not verify(pkix(old), after)
    assert not verify(pkix(old), self_rotate)
    return {
        "keys": {"old": {"key_id": hashlib.sha256(pkix(old)).hexdigest(), "pkix_b64": base64.b64encode(pkix(old)).decode()},
                 "new": {"key_id": hashlib.sha256(pkix(new)).hexdigest(), "pkix_b64": base64.b64encode(pkix(new)).decode()}},
        "note": "test keys only; the signatures are RFC 6979 deterministic so the file is reproducible; verify sig over the decoded payload bytes before parsing them",
        "cases": [
            {"case": "a v2 snapshot with a field the verifier does not know", "trusted_key": "old", "envelope": env, "verify": True, "apply_version": 42},
            {"case": "a payload changed after signing", "trusted_key": "old", "envelope": tampered, "verify": False},
            {"case": "a rotation record signed by the old key names the new key", "trusted_key": "old", "envelope": rotation, "verify": True, "rotates_to": "new"},
            {"case": "after the rotation the new key signs the snapshot", "trusted_key": "new", "envelope": after, "verify": True, "apply_version": 44},
            {"case": "the same snapshot is not trusted under the old key", "trusted_key": "old", "envelope": after, "verify": False},
            {"case": "a rotation signed only by the new key is refused", "trusted_key": "old", "envelope": self_rotate, "verify": False},
        ],
    }


# --- C10: SOS frames -----------------------------------------------------------

def lp(s):
    b = s.encode()
    return bytes([len(b)]) + b


def sos():
    hdr = lambda t: bytes([0x4D, 0x53, 0x01, t])
    sos_id = uuid7(T0, bytes.fromhex("7a7b7c7d7e7f80818283"))
    body = lp("kit-a") + lp("") + struct.pack(">ff", 52.16, 4.49) + lp("Injured, need help") + struct.pack(">I", 1_792_330_000)
    f02 = hdr(0x02) + body
    f07 = hdr(0x07) + body + sos_id.bytes
    f08 = hdr(0x08) + lp("kit-a") + sos_id.bytes + struct.pack(">I", 1_792_330_600) + bytes([0x00])
    assert len(f07) == len(f02) + 16
    second = str(uuid7(T0 + 9, bytes(10)))
    states = [
        {"case": "a cancel before the start is terminal; the start that follows does not page",
         "steps": [{"event": "cancel", "sos_id": str(sos_id)}, {"event": "start", "sos_id": str(sos_id)}],
         "expect": {"state": "cancelled", "pages": 0}},
        {"case": "a start then its cancel",
         "steps": [{"event": "start", "sos_id": str(sos_id)}, {"event": "cancel", "sos_id": str(sos_id)}],
         "expect": {"state": "cancelled", "pages": 1}},
        {"case": "the same start by three bearers pages once",
         "steps": [{"event": "start", "sos_id": str(sos_id), "bearer": b} for b in ("imt", "sms", "mqtt")],
         "expect": {"state": "active", "pages": 1}},
        {"case": "a new run with a new sos_id after a cancel alerts as usual",
         "steps": [{"event": "start", "sos_id": str(sos_id)}, {"event": "cancel", "sos_id": str(sos_id)},
                   {"event": "start", "sos_id": second}],
         "expect": {"state": "active", "pages": 2}},
    ]
    return {
        "frames": [
            {"type": "0x02", "what": "SOS today: header, bridge id, device id, lat, lon (float32 BE), text, unix s", "hex": f02.hex()},
            {"type": "0x07", "what": "SOS start: 0x02's body unchanged, then the 16-byte sos_id", "hex": f07.hex(), "sos_id": str(sos_id)},
            {"type": "0x08", "what": "SOS cancel: header, bridge id, sos_id, unix s (4 bytes BE), reason (00 operator, 01 test ended)", "hex": f08.hex(), "sos_id": str(sos_id)},
        ],
        "legacy_sos_id": {"bridge_id": "kit-a", "run_ms": 1_792_330_000_123, "sos_id": str(uuid.uuid5(NAMESPACE, "kit-a:1792330000123"))},
        "state_key": "(tenant, origin principal, sos_id)",
        "states": states,
    }


# --- C2: the LXMF mid field ---------------------------------------------------

def lxmf():
    mid = uuid.UUID(MID_A).bytes
    name = b"meshsat.mid/1"
    fields = bytes([0x82, 0xCC, 0xFB, 0xA0 | len(name)]) + name + bytes([0xCC, 0xFC, 0xC4, 0x10]) + mid
    return {"fields_msgpack_hex": fields.hex(), "mid": MID_A,
            "what": "the LXMF fields map {0xFB: \"meshsat.mid/1\", 0xFC: bin(16) mid} in msgpack; FIELD_CUSTOM_TYPE = 0xFB, FIELD_CUSTOM_DATA = 0xFC (LXMF 1.2.0)",
            "rule": "a message that already carries another custom type is sent without the mid"}


# --- SMS reports (C6a sms_status_received, FE-R1) -------------------------------

def sms_tsv():
    rows = ["# format\tstatus_hex\treport    TP-Status: 3GPP TS 23.040 9.2.3.15; CDMA: error class bits 25-24, status code bits 23-16"]
    for fmt, status, want in [
        ("3gpp", 0x00, "Delivered"), ("3gpp", 0x01, "CompletedUnconfirmed"), ("3gpp", 0x02, "CompletedUnconfirmed"),
        ("3gpp", 0x1F, "CompletedUnconfirmed"), ("3gpp", 0x20, "Pending"), ("3gpp", 0x3F, "Pending"),
        ("3gpp", 0x40, "Failed"), ("3gpp", 0x65, "Failed"),
        ("3gpp2", 2 << 16, "Delivered"), ("3gpp2", 0, "Pending"), ("3gpp2", 3 << 16, "Pending"),
        ("3gpp2", (2 << 24) | (2 << 16), "Pending"), ("3gpp2", (3 << 24) | (7 << 16), "Failed"),
        ("ims", 0x00, "unreadable"),
    ]:
        rows.append("%s\t%08x\t%s" % (fmt, status, want))
    parts = ["# parts\treports (part:report, in arrival order)\tcompletion    one send; the message is delivered when every part is"]
    for n, reports, want in [
        (2, "1:Delivered", "Open"),
        (2, "1:Delivered,0:Delivered", "Delivered"),
        (2, "0:Delivered,1:Delivered", "Delivered"),
        (3, "1:Delivered,1:Delivered,0:Delivered", "Open"),
        (2, "0:Delivered,1:CompletedUnconfirmed", "Open"),
        (2, "0:Failed,1:Delivered", "Failed"),
        (2, "0:Delivered,1:Failed,1:Delivered", "Delivered"),
        (2, "0:Delivered,0:Pending,1:Delivered", "Delivered"),
        (1, "0:Delivered", "Delivered"),
        (2, "2:Delivered", "Open"),
    ]:
        parts.append("%d\t%s\t%s" % (n, reports, want))
    return ("\n".join(rows) + "\n").encode(), ("\n".join(parts) + "\n").encode()


def build():
    reports, parts = sms_tsv()
    files = {
        "ids.json": dump(ids()),
        "content_digest.json": dump(digests()),
        "inbox_cases.json": dump(inbox_cases()),
        "hub_ingress_cases.json": dump(hub_cases()),
        "c6_reducer.json": dump(reducer_cases()),
        "c6a_receipt_kinds.json": dump({"kinds": KINDS}),
        "directory_v2.json": dump(directory()),
        "sos.json": dump(sos()),
        "lxmf_mid_field.json": dump(lxmf()),
        "sms_reports.tsv": reports,
        "sms_parts.tsv": parts,
        "RELEASE": (RELEASE + "\n").encode(),
    }
    for name in ("README.md", "generate.py"):
        with open(os.path.join(HERE, name), "rb") as f:
            files[name] = f.read()
    sums = "".join("%s  %s\n" % (hashlib.sha256(files[n]).hexdigest(), n) for n in sorted(files))
    files["SHA256SUMS"] = sums.encode()
    return files


def main():
    files = build()
    if "--check" in sys.argv:
        bad = [n for n, data in files.items()
               if not os.path.exists(os.path.join(HERE, n)) or open(os.path.join(HERE, n), "rb").read() != data]
        if bad:
            print("differs: " + ", ".join(sorted(bad)))
            sys.exit(1)
        print("ok: %d files, SHA256SUMS %s" % (len(files), hashlib.sha256(files["SHA256SUMS"]).hexdigest()))
        return
    for name, data in files.items():
        if name not in ("README.md", "generate.py"):
            with open(os.path.join(HERE, name), "wb") as f:
                f.write(data)
    print("wrote %d files, SHA256SUMS %s" % (len(files), hashlib.sha256(files["SHA256SUMS"]).hexdigest()))


if __name__ == "__main__":
    main()
