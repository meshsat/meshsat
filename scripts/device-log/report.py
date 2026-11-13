"""Write the device reliability log's evidence folder (SIDN D4, MESHSAT-1842).

README.md (the result, the method and the limits), wedges.csv and
wedges.json (one row per wedge), events.jsonl (the device rows of both
kits' signed audit logs, redacted) and power.json (powered time per boot).
Everything is generated; the folder is never edited by hand.
"""

from __future__ import annotations

import csv
import json
from collections import Counter, defaultdict
from datetime import datetime, timezone
from pathlib import Path

import fold

KITS = ("tesseract", "parallax")
CLEARED = {
    "software": "the watchdog's own software step",
    "itself": "came back by itself",
    "by_hand": "cleared by hand",
    "came_back": "came back with no software step that acted (a hand, a restart or the device itself)",
    "restart": "ended with a restart of the bridge or the kit",
    "removed": "never recovered; the device was taken out of the kit",
    "not_recorded": "end not logged",
    "open": "still open when the log was read",
}


def _fmt_hours(h: float) -> str:
    return f"{h:,.0f}"


def _row(cells) -> str:
    return "| " + " | ".join(str(c) for c in cells) + " |"


def write(out: Path, results: dict, hist: dict) -> None:
    out.mkdir(parents=True, exist_ok=True)
    # The moment the kits were read, not the moment the files were written.
    generated = min(results[k]["raw"].get("fetched_at") or datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ") for k in KITS)

    wedges, events, power = [], [], {}
    for kit in KITS:
        res = results[kit]["fold"]
        wedges += [w.as_dict() for w in res.wedges]
        power[kit] = results[kit]["power"]
        keep = set(fold.DEVICE_EVENTS)
        for r in sorted(results[kit]["raw"]["rows"], key=lambda x: x["id"]):
            if r["event_type"] in keep:
                events.append({"kit": kit, "id": r["id"], "timestamp": r["timestamp"], "event_type": r["event_type"],
                               "interface_id": r.get("interface_id"), "detail": fold.redact(r["detail"]),
                               "prev_hash": r["prev_hash"], "hash": r["hash"]})
    for i in hist.get("incident", []):
        wedges.append({
            "kit": i["kit"], "device": i["device"], "device_name": i["device"], "started": str(i["date"]), "ended": None,
            "last_logged": None, "minutes": None, "first_symptom": i["symptom"], "steps_tried": [], "step_errors": [],
            "cleared_by": "notes", "cleared_detail": i["cleared_by"], "case_opened": i["case_opened"],
            "case_source": "notes", "notes": [f"source: {i.get('source', '')}"], "carried_across_restart": False,
            "inferred": "inferred" in i["case_opened"], "audit_rows": 0, "audit_ids": [], "source": "notes (" + i.get("kind", "wedge") + ")",
        })

    # Hashes are hex and may hold long digit runs; every other field is checked.
    text = [e["detail"] for e in events] + [json.dumps({k: v for k, v in w.items() if k != "audit_ids"}, ensure_ascii=False) for w in wedges]
    for t in text:
        if fold.unredacted(t):
            raise SystemExit(f"redaction check failed: {t[:120]!r}")

    (out / "wedges.json").write_text(json.dumps(wedges, indent=1, ensure_ascii=False) + "\n")
    with (out / "wedges.csv").open("w", newline="") as f:
        cols = ["kit", "device", "started", "ended", "last_logged", "minutes", "first_symptom", "steps_tried", "cleared_by",
                "cleared_detail", "case_opened", "case_source", "notes", "inferred", "audit_rows", "audit_ids", "source"]
        wr = csv.writer(f)
        wr.writerow(cols)
        for w in wedges:
            wr.writerow(["; ".join(map(str, w[c])) if isinstance(w[c], list) else w[c] for c in cols])
    with (out / "events.jsonl").open("w") as f:
        for e in events:
            f.write(json.dumps(e, ensure_ascii=False) + "\n")
    (out / "power.json").write_text(json.dumps(power, indent=1) + "\n")
    (out / "README.md").write_text(readme(results, hist, wedges, power, generated))


def _simultaneous_aprs(auto: list) -> int:
    """APRS receive silences (not a silent TNC) that began on both kits
    within two minutes of each other, counted once per pair."""
    def starts(kit):
        return [fold.parse_ts(w["started"]) for w in auto
                if w["kit"] == kit and w["device"] == "aprs" and "TNC silent" not in w["first_symptom"]]
    t, p = starts("tesseract"), starts("parallax")
    n = 0
    for a in t:
        if any(abs((a - b).total_seconds()) <= 120 for b in p):
            n += 1
    return n


def readme(results: dict, hist: dict, wedges: list, power: dict, generated: str) -> str:
    auto = [w for w in wedges if w["source"] == "automatic"]
    by_kit = {k: [w for w in auto if w["kit"] == k] for k in KITS}
    date = generated[:10]
    first = {k: fold.iso(results[k]["fold"].first_row) for k in KITS}
    resets = {k: len(results[k]["fold"].radio_resets) for k in KITS}
    L = []
    L.append(f"# Device reliability log, {datetime.fromisoformat(date).strftime('%-d %B %Y')}")
    L.append("")
    L.append("SIDN fonds project 261042, deliverable D4: every time a radio or modem on the two MeshSat field kits locked up, which device it was, when, what cleared it and whether the case had to be opened, published with the kits' powered hours. A wedge is a device that stops answering until something resets it.")
    L.append("")
    for c in hist.get("correction", []):
        L.append(f"**Correction, {datetime.fromisoformat(c['date']).strftime('%-d %B %Y')}:** {c['text']}")
        L.append("")
    L.append("## Result")
    L.append("")
    L.append(_row(["", "tesseract", "parallax"]))
    L.append(_row(["---", "---:", "---:"]))
    L.append(_row(["Wedges recorded automatically", len(by_kit["tesseract"]), len(by_kit["parallax"])]))
    for key in ("software", "itself", "by_hand", "came_back", "restart", "removed", "not_recorded", "open"):
        n = [sum(1 for w in by_kit[k] if w["cleared_by"] == key) for k in KITS]
        if sum(n):
            L.append(_row([f"... {CLEARED[key]}", n[0], n[1]]))
    for val, label in (("yes", "Case opened"), ("no", "Case not opened"), ("not recorded", "Case opened or not: not recorded")):
        n = [sum(1 for w in by_kit[k] if w["case_opened"] == val) for k in KITS]
        L.append(_row([label, n[0], n[1]]))
    L.append(_row(["Reboots the mesh radio reported (logged since 13 November 2026)", resets["tesseract"], resets["parallax"]]))
    L.append(_row(["Powered hours since 13 September 2026", _fmt_hours(power["tesseract"]["total_hours"]), _fmt_hours(power["parallax"]["total_hours"])]))
    L.append(_row(["Automatic log starts", first["tesseract"], first["parallax"]]))
    L.append("")
    L.append(f"Read from the kits on {generated}. Every wedge, with its steps and the audit rows that prove it, is in `wedges.csv` and `wedges.json`; the rows themselves are in `events.jsonl`.")
    L.append("")
    L.append("## By device, both kits")
    L.append("")
    L.append(_row(["Device", "Wedges", "Software step", "By itself", "By hand", "No step acted", "Restart", "Taken out", "End not logged", "Still open", "Longest (hours)"]))
    L.append(_row(["---"] + ["---:"] * 10))
    per = defaultdict(list)
    for w in auto:
        per[w["device_name"]].append(w)
    for name in sorted(per, key=lambda n: -len(per[n])):
        ws = per[name]
        c = Counter(w["cleared_by"] for w in ws)
        longest = max((w["minutes"] or 0) for w in ws) / 60
        L.append(_row([name, len(ws), c["software"], c["itself"], c["by_hand"], c["came_back"], c["restart"], c["removed"], c["not_recorded"], c["open"], f"{longest:.1f}"]))
    L.append("")
    both = _simultaneous_aprs(auto)
    if both:
        L.append(f"{both} of the APRS receive silences began on both kits within two minutes of each other. The APRS watchdog reads a silence as frames that stopped while the channel was busy; when both kits report one together, the cause is more likely on the air than in either radio, so those rows say more about the channel than about the hardware.")
        L.append("")
    L.append("## How the kits record it")
    L.append("")
    L.append("- **The device-health watchdog** in the Bridge probes every USB device at protocol level every 30 seconds. Three misses in a row (one, for a radio that went silent the moment its port opened) start a ladder of recovery steps: reconnect, the device's own reset, a cut of its USB hub port. One step runs per grace window, hard resets are budgeted per hour, and a ladder that runs out waits an hour before it starts again.")
    L.append("- **The APRS receive watchdog** does the same for the APRS radio: a receiver that stops decoding while the channel is busy gets its gateway restarted, its port reopened and, last, the bridge restarted. A PicoAPRS that is switched off cannot be reset by software; it is reported as silent.")
    L.append("- **Every row goes into the kit's audit log**, where each row carries the SHA-256 of the row before it, so a changed or removed row breaks the chain.")
    L.append("- **What software cannot see is added by hand on the kit**, in the Bridge web app (Interfaces, Devices tab): a fix done by hand and whether the case had to be opened, and a device taken out of the kit or put back. These go into the same signed log.")
    L.append("")
    L.append("## How the log is read")
    L.append("")
    L.append("`scripts/device-log/` in this repository reads each kit's device rows and folds them into one record per wedge. The rules, tested in `test_fold.py`:")
    L.append("")
    L.append("- A wedge starts when the watchdog calls a device unhealthy and ends when it answers again. The hourly restarts of a ladder that ran out, and a silent radio reported again after each restart, belong to the same wedge.")
    L.append("- **What cleared it.** A software step is credited only when the device answered within that step's grace window, about three minutes at most. A step whose own error says the thing it acts on was missing (no switchable hub port, no such device) is never credited.")
    L.append("- **By itself.** A device that answered again within 5 minutes with no step that acted, or an APRS receiver that decoded a frame again with no reset in between, is put down as having come back by itself, case not opened. This is an inference and is marked as one.")
    if hist.get("rules", {}).get("picoaprs_silence_cleared_by_ptt"):
        L.append("- **Case opened.** A software step never needs the case opened. A PicoAPRS that went silent switches on again only with its PTT button, inside the sealed case, so such a silence that ended counts as cleared by hand with the case opened. Anything else is \"not recorded\" until a note from the kit or an answer in `history.toml` says otherwise.")
    else:
        L.append("- **Case opened.** A software step never needs the case opened. A press of the PicoAPRS's PTT button does, since the radio sits inside the sealed case; it counts only where a note from the kit or an answer in `history.toml` records it. Anything else is \"not recorded\".")
    L.append("- **Taken out.** A device taken out of the kit is not wedged: its rows after the removal are left out, and a wedge open at the removal is closed by it.")
    L.append("- **Older rows.** Before 13 November 2026 the watchdog did not log a device that left the USB bus while wedged, the end of a PicoAPRS silence, or its own restarts. For those rows the reading is an inference, marked in the `inferred` column.")
    L.append("")
    inv = [i for i in hist.get("inventory", [])]
    if inv:
        L.append("## Devices taken out or swapped")
        L.append("")
        L.append(_row(["Date", "Kit", "Device", "Change", "Note", "Source"]))
        L.append(_row(["---"] * 6))
        for i in sorted(inv, key=lambda x: str(x["at"])):
            L.append(_row([str(i["at"])[:10], i["kit"], fold.DEVICE_NAMES.get(i["device"], i["device"]), i["change"], i.get("note", ""), i.get("source", "")]))
        L.append("")
    inc = hist.get("incident", [])
    if inc:
        L.append("## Before automatic logging, and devices the watchdog does not watch")
        L.append("")
        L.append("From the project's own notes, not recorded by the kits. \"Inferred\" means the notes do not say whether the case was opened and the answer follows from where the device sits.")
        L.append("")
        L.append(_row(["Date", "Kit", "Device", "What happened", "What cleared it", "Case opened", "Kind", "Source"]))
        L.append(_row(["---"] * 8))
        for i in sorted(inc, key=lambda x: str(x["date"])):
            L.append(_row([str(i["date"]), i["kit"], i["device"], i["symptom"], i["cleared_by"], i["case_opened"], i.get("kind", ""), i.get("source", "")]))
        L.append("")
    L.append("## Powered hours")
    L.append("")
    for k in KITS:
        p = power[k]
        L.append(f"- **{k}:** {_fmt_hours(p['total_hours'])} hours over {len(p['boots'])} boots.")
    L.append("")
    L.append("Each boot counts from the kit's own uptime counter at its last journal entry, so a wrong wall clock at boot (the kits had no clock cell until they were fitted this autumn) changes nothing. The system journal on the kits goes back to 13 September 2026; time powered before that was not recorded. From 13 November 2026 the Bridge also keeps its own count, every five minutes, for boots the journal no longer holds.")
    L.append("")
    L.append("## Chain check")
    L.append("")
    for k in KITS:
        c = results[k]["raw"]["chain"]
        und = c.get("undecodable") or []
        line = (f"- **{k}:** {c['rows']:,} rows, ids {c['first_id']} to {c['last_id']} with {c['id_gaps']} missing, "
                f"{len(c['bad_link'])} broken links, {len(c['bad_hash'])} hashes that do not recompute.")
        sv = c.get("server_verify") or {}
        # The check runs a moment after the rows are read; a few new rows may land in between.
        chain = sv.get("chain") or {}
        whole = (chain.get("broken_at") == -1 and chain.get("rows", 0) >= c["rows"] - 50) or \
            (sv.get("verified") is True and sv.get("checked", 0) >= c["rows"] - 50)
        if c.get("redacted"):
            line += (f" {c['redacted']:,} rows older than the retention period had their personal fields redacted; "
                     "their links are checked, their hashes no longer recompute, and a checkpoint row signed by the kit records that the chain verified before each redaction.")
        if und:
            line += (f" {len(und)} rows (ids {', '.join(map(str, und))}) hold bytes that are not text, from a relayed binary frame; "
                     "the API shows them with replacement characters, so their hashes can only be recomputed on the kit.")
        if whole:
            line += f" The kit's own check of the whole chain ({chain.get('rows', sv.get('checked', 0)):,} rows, hashes and links) passes."
        L.append(line)
    L.append("")
    L.append("The published `events.jsonl` holds the device rows only, with their hashes; the other rows of the audit log record messages and stay on the kits.")
    L.append("")
    L.append("## Limits")
    L.append("")
    L.append("- The touch display is not watched; its hangs are in the notes table only.")
    L.append("- A PicoAPRS that ran its cell empty still enumerates on USB and is caught only as a silent TNC once its link is open.")
    L.append("- Before 18 September 2026 the StarTech hubs' port \"cut\" was taken for a power cut; it is a data reconnect, so a step of that name did not remove power.")
    L.append("- The kits run as development and demonstration kits: wedges during bench work are in the log like any other.")
    L.append("- Durations run from the first to the last logged moment and include time the kit was switched off.")
    L.append("")
    L.append("## Files")
    L.append("")
    L.append("- `wedges.csv`, `wedges.json`: one row per wedge, automatic and from the notes.")
    L.append("- `events.jsonl`: every device row of both kits' audit logs, redacted (numbers, addresses and node ids masked).")
    L.append("- `power.json`: powered time per boot.")
    L.append("- `../history.toml`: what the kits could not record, from the project's notes.")
    L.append("")
    L.append("This export was generated by `scripts/device-log/export.py` and is not edited by hand.")
    L.append("")
    return "\n".join(L)
