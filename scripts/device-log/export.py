#!/usr/bin/env python3
"""Export the device reliability log of both field kits (SIDN D4, MESHSAT-1842).

Reads each kit read-only: the device rows of its signed audit log (through
the Bridge API, filtered on the kit so no other row leaves it), the chain
check over the whole log, the watchdog's current state, the bridge's
powered-time rows, and the system journal's boot list over ssh (no root).
Then folds the rows into wedges, adds what history.toml records from the
project's notes, and writes the dated evidence folder.

    python3 scripts/device-log/export.py --out docs/evidence/device-log/2026-11-13
    python3 scripts/device-log/export.py --fetch-only --cache /tmp/devlog
    python3 scripts/device-log/export.py --cache /tmp/devlog --out /tmp/devlog/out

Standard library only (Python 3.11+ for tomllib).
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import tomllib
from datetime import datetime, timezone
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import fold  # noqa: E402

REPO = HERE.parent.parent
KITS = ("tesseract", "parallax")
SSH_KEY = os.path.expanduser(os.environ.get("MESHSAT_KIT_SSH_KEY", "~/.ssh/one_key"))
SSH_USER = os.environ.get("MESHSAT_KIT_SSH_USER", "kyriakosp")

# Runs on the kit. Prints one JSON document: the device rows of the audit
# log, the full-chain check (every hash recomputed and every link to the
# row before), the watchdog state and the powered-time rows. Message and
# OOB rows are read for the chain check only and never printed.
KIT_SIDE = r'''
import hashlib, json, urllib.request
BASE = "http://localhost:6050/api"
KEEP = %(keep)r
def get(path):
    return json.load(urllib.request.urlopen(BASE + path, timeout=30))
rows, before = [], None
while True:
    page = get("/audit?limit=1000" + ("&before=%%d" %% before if before else ""))
    if not page:
        break
    rows += page
    before = min(r["id"] for r in page)
    if len(page) < 1000:
        break
rows.sort(key=lambda r: r["id"])
bad_hash, bad_link, undecodable = [], [], []
for i, r in enumerate(rows):
    if "�" in r["detail"]:
        undecodable.append(r["id"])
    h = hashlib.sha256((r["prev_hash"] + r["timestamp"] + r["event_type"] + r["detail"]).encode()).hexdigest()
    if h != r["hash"] and "�" not in r["detail"]:
        bad_hash.append(r["id"])
    if i and r["prev_hash"] != rows[i - 1]["hash"]:
        bad_link.append(r["id"])
try:
    server = get("/audit/verify?limit=100000")
except Exception as e:
    server = {"error": str(e)}
try:
    power = get("/devices/power")
except Exception:
    power = {"boots": []}
out = {
    "rows": [r for r in rows if r["event_type"] in KEEP],
    "chain": {"rows": len(rows), "first_id": rows[0]["id"] if rows else None, "last_id": rows[-1]["id"] if rows else None,
              "first_ts": rows[0]["timestamp"] if rows else None, "last_ts": rows[-1]["timestamp"] if rows else None,
              "id_gaps": (rows[-1]["id"] - rows[0]["id"] + 1 - len(rows)) if rows else 0,
              "bad_hash": bad_hash, "bad_link": bad_link, "undecodable": undecodable, "server_verify": server},
    "health": get("/devices/health"),
    "power": power,
}
print(json.dumps(out))
'''

BOOTS = r'''
for b in $(journalctl --list-boots -o json --no-pager | python3 -c 'import json,sys; print(" ".join(x["boot_id"] for x in json.load(sys.stdin)))'); do
  journalctl -b "$b" -n 1 -o json --output-fields=__MONOTONIC_TIMESTAMP,__REALTIME_TIMESTAMP,_BOOT_ID --no-pager
done
'''


def kit_host(kit: str) -> str:
    out = subprocess.run([str(REPO / "scripts" / "kit-host.sh"), kit], capture_output=True, text=True, timeout=60)
    host = out.stdout.strip()
    if out.returncode != 0 or not host:
        raise SystemExit(f"{kit}: no address answers ({out.stderr.strip()})")
    return host


def ssh(host: str, cmd: list[str], stdin: str | None = None) -> str:
    out = subprocess.run(["ssh", "-o", "ConnectTimeout=15", "-o", "BatchMode=yes", "-i", SSH_KEY, f"{SSH_USER}@{host}", *cmd],
                         input=stdin, capture_output=True, text=True, timeout=600)
    if out.returncode != 0:
        raise SystemExit(f"{host}: {' '.join(cmd)} failed: {out.stderr.strip()[:300]}")
    return out.stdout


def fetch(kit: str) -> dict:
    host = kit_host(kit)
    data = json.loads(ssh(host, ["python3", "-"], KIT_SIDE % {"keep": list(fold.DEVICE_EVENTS)}))
    boots = []
    for line in ssh(host, ["bash", "-s"], BOOTS).splitlines():
        if line.strip():
            d = json.loads(line)
            boots.append({"boot_id": d["_BOOT_ID"], "uptime_us": int(d["__MONOTONIC_TIMESTAMP"]), "last_realtime_us": int(d["__REALTIME_TIMESTAMP"])})
    data["journal_boots"] = boots
    data["fetched_at"] = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    return data


def load_history(path: Path) -> dict:
    if not path.exists():
        return {}
    with path.open("rb") as f:
        return tomllib.load(f)


def history_inventory(hist: dict) -> list[fold.Inventory]:
    inv = []
    for i in hist.get("inventory", []):
        kits = list(KITS) if i["kit"] == "both" else [i["kit"]]
        for k in kits:
            inv.append(fold.Inventory(k, i["device"], i["change"], fold.parse_ts(str(i["at"])), i.get("note", ""), "notes"))
    return inv


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", help="evidence folder to write")
    ap.add_argument("--cache", help="folder for the raw per-kit data (read from it when present)")
    ap.add_argument("--fetch-only", action="store_true", help="fetch into --cache and stop")
    ap.add_argument("--refetch", action="store_true", help="fetch even when the cache holds data")
    ap.add_argument("--history", default=str(REPO / "docs" / "evidence" / "device-log" / "history.toml"))
    args = ap.parse_args()

    raw = {}
    for kit in KITS:
        cached = Path(args.cache) / f"{kit}.json" if args.cache else None
        if cached and cached.exists() and not args.refetch:
            raw[kit] = json.loads(cached.read_text())
            continue
        print(f"{kit}: reading the kit", file=sys.stderr)
        raw[kit] = fetch(kit)
        if cached:
            cached.parent.mkdir(parents=True, exist_ok=True)
            cached.write_text(json.dumps(raw[kit]))
    if args.fetch_only:
        return 0
    if not args.out:
        ap.error("--out is required unless --fetch-only")

    import report  # noqa: E402 (only needed to write)

    hist = load_history(Path(args.history))
    inv = history_inventory(hist)
    results = {}
    for kit in KITS:
        current = {t["name"]: t.get("state", "") for t in raw[kit]["health"].get("targets", [])}
        res = fold.fold(kit, raw[kit]["rows"], inv, current)
        answers = [a for a in hist.get("answer", []) if a["kit"] == kit]
        res_unmatched = fold.apply_answers(res.wedges, answers)
        bridge_rows = [{"boot_id": b["boot"], "first_seen": b["first_seen"], "last_seen": b["last_seen"]} for b in raw[kit]["power"].get("boots", [])]
        power = fold.powered_hours(raw[kit]["journal_boots"], bridge_rows)
        results[kit] = {"fold": res, "unmatched_answers": res_unmatched, "power": power, "raw": raw[kit]}
    report.write(Path(args.out), results, hist)
    print(f"written: {args.out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
