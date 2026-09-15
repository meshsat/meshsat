#!/usr/bin/env python3
"""Per-frame statistics for one aprs-linktest.sh block, from the two packet rings. [MESHSAT-1021]

    scripts/aprs-linkstats.py <outdir> <block> <sender> <receiver> [t_start] [t_end]

Every APRS frame the sender's ring recorded as tx is one trial. A trial succeeded when the
receiver's ring holds an rx record with the same raw AX.25 hex within the match window.
Acks are the receiver's tx records whose info field is ":<CALL>:ack<ID>"; they succeeded when
the sender recorded the same raw as rx. The receiver's clock offset is removed as the median
of (rx time - tx time) over the matched data frames.

For every lost frame the delta to the receiving kit's nearest own transmission is binned:
  overlap    |delta| <= 1.2 s        the receiver was keying at the same time        (H2)
  turnaround 1.2 s < delta <= 3 s    the receiver had just unkeyed                    (H3)
  unrelated  everything else         nothing of ours explains it                      (H1)
"""
import json
import re
import statistics
import sys
from datetime import datetime, timezone

MATCH_WINDOW = 6.0  # s, tx start to rx record on the far kit


def ts(s):
    s = s.replace("Z", "+00:00")
    if "." in s:  # trim nanoseconds to microseconds for fromisoformat
        head, tail = s.split(".", 1)
        frac = re.match(r"(\d+)", tail).group(1)[:6]
        rest = tail[len(re.match(r"(\d+)", tail).group(1)):]
        s = f"{head}.{frac}{rest}"
    return datetime.fromisoformat(s).astimezone(timezone.utc).timestamp()


def load_ring(path):
    d = json.load(open(path))
    l = d.get("packets", d) if isinstance(d, dict) else d
    recs = []
    for p in l:
        if p.get("bearer") != "aprs" or not p.get("raw"):
            continue
        raw = bytes.fromhex(p["raw"]) if re.fullmatch(r"[0-9a-fA-F]*", p["raw"]) else b""
        info = ax25_info(raw)
        recs.append({"t": ts(p["time"]), "dir": p["dir"], "from": p.get("from", ""), "raw": p["raw"],
                     "info": info, "ack": ack_of(info), "is_ack": info.startswith(b":") and b":ack" in info})
    return recs


def ax25_info(frame):
    # address field: 7-byte blocks until one has the extension bit set, then control + PID
    i = 0
    while i + 7 <= len(frame):
        i += 7
        if frame[i - 1] & 0x01:
            break
    return frame[i + 2:] if i + 2 <= len(frame) else b""


def ack_of(info):
    m = re.search(rb"\{([0-9A-Z]{5})$", info)
    return m.group(1).decode() if m else ""


def main():
    out, block, sender, receiver = sys.argv[1:5]
    t0 = ts(sys.argv[5]) if len(sys.argv) > 5 else 0
    t1 = ts(sys.argv[6]) if len(sys.argv) > 6 else 9e18
    S = [r for r in load_ring(f"{out}/{block}-packets-{sender}.json") if t0 - 5 <= r["t"] <= t1 + 5]
    R = [r for r in load_ring(f"{out}/{block}-packets-{receiver}.json") if t0 - 5 <= r["t"] <= t1 + 5]
    s_tx = [r for r in S if r["dir"] == "tx" and r["ack"] and not r["is_ack"]]
    r_rx = [r for r in R if r["dir"] == "rx"]
    r_tx = [r for r in R if r["dir"] == "tx"]
    s_rx = [r for r in S if r["dir"] == "rx"]

    # clock offset from matched data frames (receiver record time - sender tx time)
    def nearest(recs, raw, t, lo, hi):
        best = None
        for r in recs:
            if r["raw"] == raw and lo <= r["t"] - t <= hi:
                if best is None or abs(r["t"] - t) < abs(best["t"] - t):
                    best = r
        return best
    pairs = [(tx, nearest(r_rx, tx["raw"], tx["t"], -30, 30)) for tx in s_tx]
    diffs = [rx["t"] - tx["t"] for tx, rx in pairs if rx]
    skew = statistics.median(diffs) if diffs else 0.0

    lost, ok = [], []
    for tx in s_tx:
        rx = nearest(r_rx, tx["raw"], tx["t"] + skew, -MATCH_WINDOW, MATCH_WINDOW)
        (ok if rx else lost).append(tx)
    r_acks = [r for r in r_tx if r["is_ack"]]
    ack_lost = [a for a in r_acks if not nearest(s_rx, a["raw"], a["t"] - skew, -MATCH_WINDOW, MATCH_WINDOW)]

    # bins for lost data frames against the receiver's own transmissions (receiver clock)
    bins = {"overlap": 0, "turnaround": 0, "unrelated": 0}
    for tx in lost:
        t = tx["t"] + skew
        deltas = [t - r["t"] for r in r_tx]
        if not deltas:
            bins["unrelated"] += 1
            continue
        d = min(deltas, key=abs)
        if abs(d) <= 1.2:
            bins["overlap"] += 1
        elif 1.2 < d <= 3.0:
            bins["turnaround"] += 1
        else:
            bins["unrelated"] += 1

    by_id = {}
    for tx in s_tx:
        by_id.setdefault(tx["ack"], []).append(tx)
    acked_ids = {r["info"].split(b":ack", 1)[1][:5].decode(errors="ignore") for r in s_rx if r["is_ack"]}
    first_ok = sum(1 for i, v in by_id.items() if len(v) == 1 and i in acked_ids)
    n_msgs = len(by_id)

    def pct(a, b):
        return f"{100.0 * a / b:.1f} %" if b else "n/a"
    print(f"block {block} {sender} -> {receiver}: clock offset {skew:+.2f} s over {len(diffs)} matched frames")
    print(f"  messages {n_msgs}, data frames sent {len(s_tx)}, lost {len(lost)} = {pct(len(lost), len(s_tx))} per-frame loss")
    print(f"  acks sent by {receiver} {len(r_acks)}, lost on the way back {len(ack_lost)} = {pct(len(ack_lost), len(r_acks))}")
    print(f"  first-attempt success {first_ok}/{n_msgs} = {pct(first_ok, n_msgs)}; attempts per message {len(s_tx) / n_msgs:.2f}" if n_msgs else "  no messages")
    print(f"  lost frames vs {receiver}'s own tx: overlap {bins['overlap']} (H2), turnaround {bins['turnaround']} (H3), unrelated {bins['unrelated']} (H1)")
    print(f"  {sender} bytes per data frame {statistics.mean(len(r['raw']) // 2 for r in s_tx):.0f}" if s_tx else "")


if __name__ == "__main__":
    main()
