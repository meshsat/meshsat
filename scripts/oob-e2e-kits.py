#!/usr/bin/env python3
"""OOB management frames: kit-to-kit end-to-end acceptance driver (tesseract <-> parallax).

Run from a host that reaches both kits on the LAN:
    python3 scripts/oob-e2e-kits.py

It verifies the host agent on both kits, enables OOB with a test-sized reply budget,
pairs the kits (tesseract issues the mgmt key bundle, parallax imports it), then runs
PING / STATUS-NET / LOG / RESET / BEARER round trips over SMS and APRS while watching
both frame logs and the delivery ledger, and prints a PASS/FAIL table. Restores the
production reply budget at the end. Costs a handful of SMS. Kit names, numbers and
callsigns are the field kit values (MESHSAT-403). [MESHSAT-756]
"""
import http.client
import json
import sys
import time
import urllib.error
import urllib.request

T = "http://nllei01tesseract01:6050"
P = "http://nllei01parallax01:6050"
KITS = {"tesseract": T, "parallax": P}
ADDR = {
    "tesseract": {"cellular_0": "+31653618463", "aprs_0": "MSTSRT-10", "mesh_0": "!a1d763bc"},
    "parallax": {"cellular_0": "+31653207829", "aprs_0": "MSPRLX-10", "mesh_0": "!6bcc53b2"},
}
results = []
T0 = time.time()


def stamp():
    return "+%4.0fs" % (time.time() - T0)


class Unconfirmed(RuntimeError):
    """A mutating request reached the bridge but its response was lost.
    The caller must reconcile instead of resending. [MESHSAT-786]"""


def req(base, method, path, body=None, timeout=20, retry_for=240):
    """HTTP JSON call. Connection-phase failures (parallax's WiFi flaps:
    refused, no route, connect timeout) are retried for up to retry_for
    seconds. A failure in the response phase means the request was sent and
    may have been accepted: GETs are retried, anything mutating raises
    Unconfirmed so it is never sent twice (3 Sep 2026: a RESET went out
    twice this way). HTTP errors are not retried."""
    data = json.dumps(body).encode() if body is not None else None
    deadline = time.time() + retry_for
    attempt = 0
    while True:
        attempt += 1
        r = urllib.request.Request(base + path, data=data, method=method, headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(r, timeout=timeout) as resp:
                raw = resp.read()
                return json.loads(raw) if raw.strip() else {}
        except urllib.error.HTTPError as e:
            raw = e.read().decode(errors="replace")
            raise RuntimeError("%s %s -> %d %s" % (method, path, e.code, raw[:200]))
        except urllib.error.URLError as e:
            # urllib wraps connect and send errors in URLError: the request
            # never reached the bridge, so a retry cannot duplicate it.
            phase = "connect"
            err = e
        except (TimeoutError, http.client.RemoteDisconnected, ConnectionResetError, OSError) as e:
            # Raised while reading the response: the request was sent.
            if method != "GET":
                raise Unconfirmed("%s %s%s: response lost after the request was sent (attempt %d): %s"
                                  % (method, base, path, attempt, str(e)[:60]))
            phase = "read"
            err = e
        if time.time() > deadline:
            raise RuntimeError("%s %s%s unreachable after %d attempts (%s): %s" % (method, base, path, attempt, phase, err))
        if attempt == 1 or attempt % 10 == 0:
            print("%s   (%s%s not reachable, retrying (%s): %s)" % (stamp(), base, path, phase, str(err)[:60]), flush=True)
        time.sleep(3)


def latest_out_counter(base, peer_id, via):
    """Highest counter of an outgoing request to peer_id on via in the
    source's frame log, or -1. Used to reconcile a send whose HTTP response
    was lost."""
    best = -1
    try:
        for e in log(base):
            if e.get("direction") == "out" and e.get("kind") == "request" and e.get("peer_id") == peer_id and e.get("bearer") == via:
                best = max(best, int(e.get("counter", -1)))
    except Exception as e:  # noqa: BLE001
        print("%s   (log read for reconciliation failed: %s)" % (stamp(), str(e)[:60]), flush=True)
    return best


def reconcile_send(base, peer_id, via, before, timeout=60):
    """After an Unconfirmed POST /api/oob/send: find the request the bridge
    queued anyway (counter above `before`) and return it in the shape the
    send endpoint would have returned, or None."""
    def newest():
        found = None
        for e in log(base):
            if e.get("direction") == "out" and e.get("kind") == "request" and e.get("peer_id") == peer_id \
                    and e.get("bearer") == via and int(e.get("counter", -1)) > before:
                if found is None or int(e["counter"]) > int(found["counter"]):
                    found = e
        return found
    e = wait_value(newest, timeout)
    if not e:
        return None
    return {"counter": int(e["counter"]), "delivery_id": e.get("delivery_id"), "text": "", "reconciled": True}


def record(name, ok, note):
    results.append((name, ok, note))
    print("%s [%s] %s: %s" % (stamp(), "PASS" if ok else "FAIL", name, note), flush=True)


def wait_value(fn, timeout, interval=3):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            v = fn()
        except Exception as e:  # noqa: BLE001
            v = None
            print("   (poll error: %s)" % str(e)[:80], flush=True)
        if v:
            return v
        time.sleep(interval)
    return None


def log(base, limit=80):
    return req(base, "GET", "/api/oob/log?limit=%d" % limit)


def roundtrip(name, src, dst, peer_id, via, cmd, args=None, encrypt=None, timeout=150):
    t0 = time.time()
    body = {"peer_id": peer_id, "via": via, "cmd": cmd, "args": args or {}}
    if encrypt is not None:
        body["encrypt"] = encrypt
    before = latest_out_counter(src, peer_id, via)
    try:
        sent = req(src, "POST", "/api/oob/send", body)
    except Unconfirmed as e:
        # Never resend: the bridge may already have queued the frame. Read
        # what it queued from its own log and carry on with that counter.
        print("%s   %s" % (stamp(), e), flush=True)
        sent = reconcile_send(src, peer_id, via, before)
        if sent is None:
            record(name, False, "send response lost and no queued request found in the log; not resent")
            return None
        print("%s   reconciled from the log: ctr=%d delivery=#%s" % (stamp(), sent["counter"], sent["delivery_id"]), flush=True)
    except Exception as e:  # noqa: BLE001
        record(name, False, "send failed: %s" % e)
        return None
    ctr = sent["counter"]
    print("%s   sent %s via %s ctr=%d text=%d chars delivery=#%s" % (stamp(), cmd, via, ctr, len(sent["text"]), sent["delivery_id"]), flush=True)

    def executed():
        for e in log(dst):
            if e["direction"] == "in" and e["kind"] == "request" and e["counter"] == ctr and e.get("result"):
                return e
        return None

    ex = wait_value(executed, timeout)
    t1 = time.time()

    def reply():
        for e in log(src):
            if e["direction"] == "in" and e["kind"] == "reply":
                try:
                    d = json.loads(e.get("detail") or "{}")
                except ValueError:
                    d = {}
                if d.get("req_counter_lo") == (ctr & 0xFFFF):
                    return (e, d)
        return None

    rep = wait_value(reply, timeout)
    t2 = time.time()
    try:
        dl = req(src, "GET", "/api/deliveries/%d" % sent["delivery_id"])
        dstatus = dl.get("status")
    except Exception:  # noqa: BLE001
        dstatus = "?"
    note = "ctr=%d %dch delivery=%s executed=%s reply=%s" % (
        ctr, len(sent["text"]), dstatus,
        ("+%.0fs rc=%s" % (t1 - t0, ex["result"])) if ex else "NOT SEEN",
        ("+%.0fs rc=%s seq=%s/%s body=%r" % (t2 - t0, rep[0]["result"], rep[1].get("seq"), rep[1].get("total"), rep[1].get("body"))) if rep else "NOT SEEN",
    )
    record(name, bool(ex) and bool(rep) and rep[0]["result"] == "ok", note)
    return rep


def main():
    # 0. preflight: new build with the agent reachable on both kits
    for name, base in KITS.items():
        try:
            agent = req(base, "GET", "/api/oob/agent")
            record("%s agent" % name, agent.get("available") is True, json.dumps(agent))
        except Exception as e:  # noqa: BLE001
            record("%s agent" % name, False, str(e))
            print("preflight failed, aborting", flush=True)
            return 2

    # 1. enable with a test-sized reply budget
    for name, base in KITS.items():
        cfg = req(base, "PUT", "/api/oob/config", {"enabled": True, "reply_budget": 40, "host_socket": "/run/meshsat-oob/agent.sock"})
        record("%s enabled" % name, cfg.get("enabled") is True and cfg.get("reply_budget") == 40, json.dumps(cfg))

    # 2. pairing: tesseract issues, parallax imports
    peers = req(T, "GET", "/api/oob/peers")
    mine = [p for p in peers if p["alias"] == "parallax"]
    if mine:
        pid = mine[0]["peer_id"]
        req(T, "PUT", "/api/oob/peers/%d" % pid, {"role": "control", "enabled": True, "addresses": ADDR["parallax"]})
    else:
        pid = req(T, "POST", "/api/oob/peers", {"alias": "parallax", "role": "control", "addresses": ADDR["parallax"]})["peer_id"]
    bundle = req(T, "POST", "/api/oob/peers/%d/bundle" % pid, {"issuer_alias": "tesseract"})
    imp = req(P, "POST", "/api/keys/import", {"url": bundle["url"]})
    print("%s   import on parallax: %s" % (stamp(), json.dumps(imp)[:200]), flush=True)
    theirs = [p for p in req(P, "GET", "/api/oob/peers") if p["alias"] == "tesseract"]
    if not theirs:
        record("pairing", False, "parallax has no peer 'tesseract' after import")
        return 2
    ppid = theirs[0]["peer_id"]
    req(P, "PUT", "/api/oob/peers/%d" % ppid, {"role": "control", "enabled": True, "addresses": ADDR["tesseract"]})
    tp = [p for p in req(T, "GET", "/api/oob/peers") if p["peer_id"] == pid][0]
    pp = [p for p in req(P, "GET", "/api/oob/peers") if p["peer_id"] == ppid][0]
    record("pairing", pid == ppid and tp["local_role"] == "issuer" and pp["local_role"] == "importer",
           "peer id 0x%04x, tesseract=%s, parallax=%s" % (pid, tp["local_role"], pp["local_role"]))

    # 3. round trips
    roundtrip("SMS PING encrypted (T->P)", T, P, pid, "cellular_0", "PING")
    roundtrip("SMS PING clear (T->P)", T, P, pid, "cellular_0", "PING", encrypt=False)
    roundtrip("APRS PING encrypted (T->P)", T, P, pid, "aprs_0", "PING", timeout=120)
    roundtrip("APRS PING clear (T->P)", T, P, pid, "aprs_0", "PING", encrypt=False, timeout=120)
    roundtrip("APRS STATUS-NET (T->P, chunked)", T, P, pid, "aprs_0", "STATUS-NET", timeout=150)
    roundtrip("APRS LOG docker 3 lines (T->P, chunked)", T, P, pid, "aprs_0", "LOG", {"unit": "docker", "lines": 3}, timeout=150)
    roundtrip("SMS RESET aprs level 1 on parallax", T, P, pid, "cellular_0", "RESET", {"target": "aprs", "level": 1})
    time.sleep(8)
    try:
        gws = req(P, "GET", "/api/gateways")
        items = gws.get("gateways", gws) if isinstance(gws, dict) else gws
        aprs = [g for g in items if isinstance(g, dict) and g.get("type") == "aprs"]
        record("parallax aprs_0 back after soft reset", bool(aprs) and aprs[0].get("connected") is True, json.dumps({k: aprs[0].get(k) for k in ("enabled", "connected")}) if aprs else "no aprs gateway")
    except Exception as e:  # noqa: BLE001
        record("parallax aprs_0 back after soft reset", False, str(e))
    roundtrip("SMS BEARER aprs off on parallax", T, P, pid, "cellular_0", "BEARER", {"target": "aprs", "state": "off"})
    roundtrip("SMS BEARER aprs on on parallax", T, P, pid, "cellular_0", "BEARER", {"target": "aprs", "state": "on"})
    roundtrip("SMS PING reverse (P->T, importer sends)", P, T, ppid, "cellular_0", "PING")
    # Only with a per-port switchable hub on parallax (StarTech HB30A4AIB,
    # MESHSAT-786): cuts the XIAO's port power and lets the supervisor reclaim it.
    if "--power-cycle" in sys.argv:
        roundtrip("SMS RESET mesh level 3 (port power cycle) on parallax", T, P, pid, "cellular_0", "RESET", {"target": "mesh", "level": 3}, timeout=150)

    # 4. metrics and audit
    for name, base in KITS.items():
        try:
            r = urllib.request.urlopen(base + "/metrics", timeout=15).read().decode()
            lines = [l for l in r.splitlines() if l.startswith("meshsat_oob_")]
            record("%s metrics" % name, len(lines) > 0, " | ".join(lines)[:400])
        except Exception as e:  # noqa: BLE001
            record("%s metrics" % name, False, str(e))
    try:
        audit = req(P, "GET", "/api/audit?limit=100")
        entries = audit.get("entries", audit) if isinstance(audit, dict) else audit
        kinds = {}
        for e in entries:
            if isinstance(e, dict) and str(e.get("event_type", "")).startswith("oob_"):
                kinds[e["event_type"]] = kinds.get(e["event_type"], 0) + 1
        record("parallax audit has oob entries", kinds.get("oob_command", 0) > 0, json.dumps(kinds))
    except Exception as e:  # noqa: BLE001
        record("parallax audit has oob entries", False, str(e))

    # restore the production reply budget
    for base in KITS.values():
        try:
            req(base, "PUT", "/api/oob/config", {"enabled": True, "reply_budget": 12, "host_socket": "/run/meshsat-oob/agent.sock"})
        except Exception:  # noqa: BLE001
            pass

    print("\n=== SUMMARY (%d pass, %d fail) ===" % (sum(1 for r in results if r[1]), sum(1 for r in results if not r[1])), flush=True)
    for name, ok, note in results:
        print("%s  %s" % ("PASS" if ok else "FAIL", name), flush=True)
    return 0 if all(r[1] for r in results) else 1


if __name__ == "__main__":
    sys.exit(main())
