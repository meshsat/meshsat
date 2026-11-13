"""Fold a kit's signed audit rows into the device reliability log.

A wedge is a device that stops answering until something resets it (SIDN
fonds project 261042, deliverable D4). The Bridge's device-health watchdog
and the APRS receive watchdog write every step of one into the kit's
hash-chained audit log; this module reads those rows back into one record
per wedge: which device, when, what cleared it, and whether the case had to
be opened.

Standard library only. The rules are tested in test_fold.py against lines
taken from the kits' own logs.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone

# The audit event types the log reads. Everything else in the audit log
# (message deliveries, OOB commands) stays private and is never read here.
DEVICE_EVENTS = ("device_health", "aprs_rx_watchdog", "device_inventory", "device_note", "radio_reset")

# A wedge whose device has nothing logged for longer than this, and then
# opens a new wedge, is taken to have ended unrecorded in between (a bridge
# restart drops the watchdog's memory). A failed ladder re-arms every hour,
# so a gap shorter than this is the same wedge carried on.
CARRY_OVER = timedelta(minutes=70)

# A hands-on note or answer attaches to the device's wedge that was open at
# that moment or ended at most this long before it.
NOTE_WINDOW = timedelta(hours=6)

# An answer from the notes names the moment a wedge was cleared by hand,
# which can be the next day (a dongle re-seated in the morning).
ANSWER_WINDOW = timedelta(hours=36)

# A silent PicoAPRS TNC is reported once per link opening and its end was
# not logged before MESHSAT-1842, so each bridge restart repeats the row.
# Rows this close together with nothing else in between are one silence.
TNC_CARRY = timedelta(hours=6)

# A device that answers again this soon after its wedge opened, with no step
# that acted, came back by itself: nobody opens a sealed case and re-seats a
# device that fast. Marked as inferred; an answer in history.toml wins.
QUICK_RETURN = timedelta(minutes=5)

# Before the watchdog wrote its own verdict (MESHSAT-1842), a recovery was
# always worded "recovered after step N (rung)". A recovery this long after
# the rung ran is not credited to it: the longest grace on the kits is
# 3 minutes, plus two 30-second ticks.
LEGACY_CREDIT_LIMIT = timedelta(minutes=10)

DEVICE_NAMES = {
    "mesh": "mesh radio",
    "cellular": "cellular modem",
    "zigbee": "ZigBee coordinator",
    "rtl_sdr": "RTL-SDR receiver",
    "gps": "GPS receiver",
    "imt": "Iridium 9704 (IMT)",
    "iridium": "Iridium 9602/9603 (SBD)",
    "aprs": "APRS radio",
    "display": "touch display",
    "kit": "the whole kit",
}


def parse_ts(s: str) -> datetime:
    s = s.strip()
    if s.endswith("Z"):
        s = s[:-1] + "+00:00"
    if len(s) == 10:
        s += "T00:00:00+00:00"
    d = datetime.fromisoformat(s)
    if d.tzinfo is None:
        d = d.replace(tzinfo=timezone.utc)
    return d.astimezone(timezone.utc)


def iso(d: datetime | None) -> str | None:
    return d.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ") if d else None


# ---------------------------------------------------------------- redaction

_REDACT = [
    (re.compile(r"(?<![0-9])[0-9]{15,20}(?![0-9])"), "[number]"),  # IMEI, ICCID
    (re.compile(r"\+[0-9][0-9 ]{7,16}[0-9]"), "[phone]"),
    (re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}"), "[email]"),
    (re.compile(r"![0-9a-fA-F]{8}\b"), "![node]"),
]


def redact(text: str) -> str:
    for rx, sub in _REDACT:
        text = rx.sub(sub, text)
    return text


def unredacted(text: str) -> bool:
    """True when text still holds something redact() would mask."""
    return any(rx.search(text) for rx, _ in _REDACT)


# ------------------------------------------------------------------ parsing

@dataclass
class Row:
    """One audit row the log reads, already classified."""

    id: int
    ts: datetime
    event_type: str
    device: str
    kind: str  # open | continue | step | step_error | away | recovered | came_back | failed | start | removed | fitted | note | radio_reset | other
    text: str  # the row's own words, without the device prefix
    step_name: str = ""
    step_no: int = 0
    origin: str = ""  # auto | manual
    boot: str = ""
    case_opened: str = ""  # yes | no, on a note
    action: str = ""  # on a note
    step_error: bool = False  # on a recovery: "though the step reported an error"
    silence: timedelta | None = None  # APRS: how long no frame was decoded, as the row says


_STEP = re.compile(r"^(?P<dev>[a-z_]+): step (?P<n>\d+), level (?P<lvl>\d+), (?P<rung>.+) \((?P<origin>auto|manual)\)$")
_STEP_ERR = re.compile(r"^step (?P<n>\d+) (?P<rest>.+)$")
_RECOVERED = re.compile(r"^(?P<dev>[a-z_]+) recovered after step (?P<n>\d+)(?: \((?P<rung>[^)]*)\))?(?P<err>, though the step reported an error)?$")
_CAME_BACK = re.compile(r"^(?P<dev>[a-z_]+) came back without a software step")
_LEFT = re.compile(r"^(?P<dev>[a-z_]+) left the bus while unhealthy: (?P<why>.*)$")
_OPEN = re.compile(r"^(?P<dev>[a-z_]+) unhealthy: (?P<why>.*)$")
_BACK_STILL = re.compile(r"^(?P<dev>[a-z_]+) back but still unhealthy: (?P<why>.*)$")
_COOLDOWN = re.compile(r"^(?P<dev>[a-z_]+) still unhealthy after cooldown: (?P<why>.*)$")
_FAILED = re.compile(r"^(?P<dev>[a-z_]+): (ladder exhausted after|hard-reset budget of|level \d+ rung )")
_START = re.compile(r"^watchdog started \(boot (?P<boot>[^)]*)\)$")
_NOTE = re.compile(r"^(?P<dev>[a-z_]+) hands-on fix: (?P<action>[a-z-]+); case opened: (?P<case>yes|no)(?:; (?P<note>.*))?$")
_INV = re.compile(r"^(?P<dev>[a-z_]+) (?P<what>removed from the kit|fitted again)(?:: (?P<note>.*))?$")
_APRS_STEP = re.compile(r"step (?P<n>\d+), (?P<rung>.+)$")
_APRS_MANUAL = re.compile(r" \(manual, [^)]*\)$")
_APRS_FOR = re.compile(r" for (?P<d>(?:\d+h)?(?:\d+m)?(?:\d+(?:\.\d+)?s)?)\b")

# A failed rung whose error says the thing it acts on was not there: such a
# rung could not have cleared anything.
_COULD_NOT_ACT = re.compile(r"no switchable|not on the bus|not on a switchable|no such device|no such file|invalid serial port|port closed|no rtl-sdr|no action", re.I)


def parse_go_duration(s: str) -> timedelta | None:
    m = re.fullmatch(r"(?:(\d+)h)?(?:(\d+)m)?(?:(\d+(?:\.\d+)?)s)?", s or "")
    if not m or not s:
        return None
    h, mi, se = m.groups()
    return timedelta(hours=int(h or 0), minutes=int(mi or 0), seconds=float(se or 0))


def classify(raw: dict) -> Row | None:
    """Turn one audit row into a Row, or None when the log has no use for it."""
    et = raw.get("event_type", "")
    if et not in DEVICE_EVENTS:
        return None
    detail = raw.get("detail") or ""
    rid, ts = int(raw["id"]), parse_ts(raw["timestamp"])

    if et == "aprs_rx_watchdog":
        return _classify_aprs(rid, ts, detail)
    if et == "radio_reset":
        return Row(rid, ts, et, "mesh", "radio_reset", detail.split(": ", 1)[-1])

    target, _, rest = detail.partition(": ")
    if not rest:
        return Row(rid, ts, et, target or "unknown", "other", detail)

    if et == "device_inventory":
        m = _INV.match(rest)
        if not m:
            return Row(rid, ts, et, target, "other", rest)
        kind = "removed" if m["what"] == "removed from the kit" else "fitted"
        return Row(rid, ts, et, target, kind, rest)
    if et == "device_note":
        m = _NOTE.match(rest)
        if not m:
            return Row(rid, ts, et, target, "other", rest)
        return Row(rid, ts, et, target, "note", m["note"] or "", action=m["action"], case_opened=m["case"])

    # device_health
    if target == "watchdog":
        m = _START.match(rest)
        return Row(rid, ts, et, "watchdog", "start" if m else "other", rest, boot=m["boot"] if m else "")
    for rx, kind in ((_OPEN, "open"), (_BACK_STILL, "continue"), (_COOLDOWN, "continue")):
        m = rx.match(rest)
        if m:
            return Row(rid, ts, et, target, kind, m["why"])
    m = _STEP.match(rest)
    if m:
        return Row(rid, ts, et, target, "step", rest, step_name=m["rung"], step_no=int(m["n"]), origin=m["origin"])
    m = _RECOVERED.match(rest)
    if m:
        return Row(rid, ts, et, target, "recovered", rest, step_name=m["rung"] or "", step_no=int(m["n"]), step_error=bool(m["err"]))
    if _CAME_BACK.match(rest):
        return Row(rid, ts, et, target, "came_back", rest)
    m = _LEFT.match(rest)
    if m:
        return Row(rid, ts, et, target, "away", m["why"])
    if _FAILED.match(rest):
        return Row(rid, ts, et, target, "failed", rest.split(": ", 1)[-1])
    m = _STEP_ERR.match(rest)
    if m:
        return Row(rid, ts, et, target, "step_error", rest, step_no=int(m["n"]))
    return Row(rid, ts, et, target, "other", rest)


def _classify_aprs(rid: int, ts: datetime, detail: str) -> Row:
    d = detail
    et = "aprs_rx_watchdog"
    if "TNC talking again" in d:
        return Row(rid, ts, et, "aprs", "came_back", d)
    if d.startswith("step ") and " failed:" in d:
        m = re.match(r"step (\d+)", d)
        return Row(rid, ts, et, "aprs", "step_error", d, step_no=int(m.group(1)))
    if " recovered" in d:
        m = re.search(r"after step (\d+)", d)
        return Row(rid, ts, et, "aprs", "recovered", d, step_no=int(m.group(1)) if m else 0)
    if d.endswith(": restarting the bridge"):
        return Row(rid, ts, et, "aprs", "step", d, step_name="restart of the bridge", origin="auto")
    m = _APRS_STEP.search(d)
    if m:
        rung, origin = m["rung"], "auto"
        # A TNC reset asked for by API or OOB during a silence is that
        # silence's next step, worded "(manual, API)". [MESHSAT-1896]
        mm = _APRS_MANUAL.search(rung)
        if mm:
            rung, origin = rung[: mm.start()], "manual"
        return Row(rid, ts, et, "aprs", "step", d, step_name=rung, step_no=int(m["n"]), origin=origin)
    if "silent" in d or "deaf" in d:
        m = _APRS_FOR.search(d)
        return Row(rid, ts, et, "aprs", "open", d, silence=parse_go_duration(m["d"]) if m else None)
    return Row(rid, ts, et, "aprs", "other", d)


# ------------------------------------------------------------------ folding

@dataclass
class Wedge:
    kit: str
    device: str
    started: datetime
    symptom: str
    first_id: int
    last_id: int = 0
    last_seen: datetime | None = None
    ended: datetime | None = None
    steps: list[str] = field(default_factory=list)
    step_errors: list[str] = field(default_factory=list)
    cleared_by: str = ""  # software | came_back | restart | power_cycle | removed | not_recorded | open
    cleared_detail: str = ""
    case_opened: str = ""  # yes | no | not recorded | n/a
    case_source: str = ""  # watchdog | note | answer | inferred
    notes: list[str] = field(default_factory=list)
    carried_restart: bool = False
    inferred: bool = False
    rows: int = 0
    last_step_at: datetime | None = None
    last_step_name: str = ""
    last_step_failed: bool = False
    last_step_error: str = ""
    away: bool = False
    restart_at: datetime | None = None
    restart_kind: str = ""  # bridge restart | kit power cycle

    def as_dict(self) -> dict:
        dur = None
        if self.ended:
            dur = round((self.ended - self.started).total_seconds() / 60)
        return {
            "kit": self.kit,
            "device": self.device,
            "device_name": DEVICE_NAMES.get(self.device, self.device),
            "started": iso(self.started),
            "ended": iso(self.ended),
            "last_logged": iso(self.last_seen),
            "minutes": dur,
            "first_symptom": redact(self.symptom),
            "steps_tried": [redact(s) for s in self.steps],
            "step_errors": [redact(s) for s in self.step_errors],
            "cleared_by": self.cleared_by,
            "cleared_detail": redact(self.cleared_detail),
            "case_opened": self.case_opened,
            "case_source": self.case_source,
            "notes": [redact(n) for n in self.notes],
            "carried_across_restart": self.carried_restart,
            "inferred": self.inferred,
            "audit_rows": self.rows,
            "audit_ids": [self.first_id, self.last_id or self.first_id],
            "source": "automatic",
        }


@dataclass
class Inventory:
    kit: str
    device: str
    change: str  # removed | fitted
    at: datetime
    note: str = ""
    source: str = "kit"  # kit (signed row) | notes


@dataclass
class FoldResult:
    wedges: list[Wedge]
    radio_resets: list[Row]
    loose_notes: list[Row]
    inventory: list[Inventory]
    restarts: list[Row]
    first_row: datetime | None
    last_row: datetime | None
    rows_read: int


def _close(w: Wedge, at: datetime, how: str, detail: str, case: str, source: str) -> None:
    w.ended, w.cleared_by, w.cleared_detail = at, how, detail
    if not w.case_opened:
        w.case_opened, w.case_source = case, source


def fold(kit: str, raw_rows: list[dict], inventory: list[Inventory] | None = None,
         current: dict[str, str] | None = None, read_at: datetime | None = None) -> FoldResult:
    """Fold one kit's audit rows (any order, any event types) into wedges.

    inventory holds removals known from the project's notes, for the time
    before the kit could record them itself; the kit's own device_inventory
    rows are added to it. current maps a device to the watchdog's state
    when the log was read: a wedge still open in the rows of a device that
    is fine now ended unseen.
    """
    rows = [r for r in (classify(x) for x in raw_rows) if r is not None]
    rows.sort(key=lambda r: r.id)
    inv = sorted([i for i in (inventory or []) if i.kit in (kit, "both")], key=lambda i: i.at)
    for r in rows:
        if r.kind in ("removed", "fitted"):
            m = _INV.match(r.text)
            inv.append(Inventory(kit, r.device, r.kind, r.ts, (m["note"] or "") if m else "", "kit"))
    inv.sort(key=lambda i: i.at)

    def removed_at(device: str, at: datetime) -> Inventory | None:
        """The removal in force for a device at a moment, if any."""
        state = None
        for i in inv:
            if i.device != device or i.at > at:
                continue
            state = i if i.change == "removed" else None
        return state

    open_w: dict[str, Wedge] = {}
    done: list[Wedge] = []
    resets: list[Row] = []
    notes: list[Row] = []
    starts: list[Row] = []
    last_boot = None

    def finish(dev: str) -> None:
        w = open_w.pop(dev, None)
        if w:
            done.append(w)

    def close_open_before_removal(dev: str, at: datetime) -> None:
        rem = removed_at(dev, at)
        w = open_w.get(dev)
        if not (w and rem and rem.at >= w.started):
            return
        if w.last_seen and rem.at - w.last_seen > CARRY_OVER:
            # Nothing logged for a long while before the removal: the wedge
            # ended unseen before the device was taken out.
            end_unseen(w)
            return
        note = f"never recovered; taken out of the kit {iso(rem.at)[:10]}" + (f" ({rem.note})" if rem.note else "")
        _close(w, rem.at, "removed", note, "yes", "inferred")
        w.inferred = True
        finish(dev)

    def end_unseen(w: Wedge) -> None:
        """The wedge's end was never logged: a restart took the watchdog's
        memory, or the device came back while nothing watched."""
        if w.restart_at:
            case = "no" if w.restart_kind == "bridge restart" else "not recorded"
            _close(w, w.restart_at, "restart", f"ended with a {w.restart_kind}", case, "watchdog")
        else:
            w.cleared_by, w.cleared_detail = "not_recorded", "the end was not logged (a restart of the bridge, or the device came back while nothing watched)"
            w.case_opened, w.case_source = "not recorded", "watchdog"
        w.inferred = True
        finish(w.device)

    for r in rows:
        if r.kind == "radio_reset":
            resets.append(r)
            continue
        if r.kind == "note":
            notes.append(r)
            continue
        if r.kind == "start":
            starts.append(r)
            kind = "kit power cycle" if last_boot is not None and r.boot not in (last_boot, "unknown") else "bridge restart"
            last_boot = r.boot
            for dev in list(open_w):
                close_open_before_removal(dev, r.ts)
            for w in list(open_w.values()):
                window = TNC_CARRY if "TNC silent" in w.symptom else CARRY_OVER
                if w.last_seen and r.ts - w.last_seen > window:
                    end_unseen(w)  # long quiet before the restart: it ended unseen earlier
                else:
                    w.restart_at, w.restart_kind = r.ts, kind
            continue
        if r.kind in ("removed", "fitted", "other"):
            if r.kind == "removed":
                close_open_before_removal(r.device, r.ts)
            continue
        dev = r.device
        close_open_before_removal(dev, r.ts)
        if removed_at(dev, r.ts):
            continue  # a device out of the kit is not wedged

        w = open_w.get(dev)
        if r.kind == "open" and w and dev == "aprs" and ("TNC silent" in r.text or "TNC silent" in w.symptom):
            if "TNC silent" in r.text and "TNC silent" in w.symptom and w.last_seen and r.ts - w.last_seen <= TNC_CARRY:
                w.rows += 1
                w.last_id, w.last_seen = r.id, r.ts
                if w.restart_at:
                    w.carried_restart, w.restart_at = True, None
                continue
            end_unseen(w)
            w = None
        if r.kind == "open" and w:
            if dev == "aprs" and r.silence is not None:
                # The row says when the last frame was decoded: before the
                # wedge's last row means the same silence carried on.
                last_frame = r.ts - r.silence
                if last_frame <= (w.last_seen or w.started) + timedelta(minutes=2):
                    w.rows += 1
                    w.last_id, w.last_seen = r.id, r.ts
                    if w.restart_at:
                        w.carried_restart, w.restart_at = True, None
                    continue
                if w.last_step_name == "restart of the bridge":
                    _close(w, last_frame, "software", "restart of the bridge by the APRS watchdog (a frame was decoded again after it)", "no", "watchdog")
                else:
                    _close(w, last_frame, "came_back", "receive came back (a frame was decoded again); the log does not say what cleared it", "not recorded", "watchdog")
                w.inferred = True
                finish(dev)
                w = None
            elif w.last_seen and r.ts - w.last_seen <= CARRY_OVER:
                w.rows += 1
                w.last_id, w.last_seen = r.id, r.ts
                if w.restart_at:
                    w.carried_restart, w.restart_at = True, None
                continue
            else:
                end_unseen(w)
                w = None
        if r.kind == "open":
            w = Wedge(kit, dev, r.ts, r.text, r.id, r.id, r.ts, rows=1)
            open_w[dev] = w
            continue
        if not w:
            if r.kind in ("continue", "step", "failed", "away", "step_error"):
                # A ladder already running when the log starts: open here.
                w = Wedge(kit, dev, r.ts, r.text if r.kind == "continue" else "(the log starts while the device is already unhealthy)", r.id, r.id, r.ts)
                w.inferred = True
                open_w[dev] = w
            else:
                continue
        w.rows += 1
        w.last_id, w.last_seen = r.id, r.ts
        if w.restart_at:
            w.carried_restart, w.restart_at = True, None
        if r.kind == "step":
            label = r.step_name + (" (run by hand from the web app or API)" if r.origin == "manual" else "")
            w.steps.append(label)
            w.last_step_at, w.last_step_name, w.last_step_failed, w.last_step_error = r.ts, r.step_name, False, ""
        elif r.kind == "step_error":
            w.step_errors.append(r.text)
            w.last_step_failed, w.last_step_error = True, r.text
        elif r.kind == "away":
            w.away = True
        elif r.kind == "recovered":
            if dev == "aprs":
                if r.step_no == 0:
                    _close(w, r.ts, "came_back", "receive came back by itself", "not recorded", "watchdog")
                else:
                    _close(w, r.ts, "software", w.last_step_name or "restart of the APRS gateway", "no", "watchdog")
            else:
                late = w.last_step_at is not None and r.ts - w.last_step_at > LEGACY_CREDIT_LIMIT
                failed = w.last_step_failed or r.step_error
                could_not = failed and bool(_COULD_NOT_ACT.search(w.last_step_error))
                rung = r.step_name or w.last_step_name
                if w.away or late or could_not:
                    why = ("it had left the bus" if w.away else "the last step could not act" if could_not
                           else "it answered long after the last step ran")
                    _close(w, r.ts, "came_back", f"came back without a software step that worked ({why})", "not recorded", "watchdog")
                    w.inferred = True
                elif failed:
                    _close(w, r.ts, "software", f"{rung} (the step reported an error, the device answered within its grace)", "no", "watchdog")
                else:
                    _close(w, r.ts, "software", rung, "no", "watchdog")
            finish(dev)
        elif r.kind == "came_back":
            if dev == "aprs":
                _close(w, r.ts, "came_back", "the radio was switched on again (a PicoAPRS needs a 3 s PTT press)", "not recorded", "watchdog")
            else:
                _close(w, r.ts, "came_back", "came back without a software step (by hand, by itself or a restart)", "not recorded", "watchdog")
            finish(dev)

    first = rows[0].ts if rows else None
    last = rows[-1].ts if rows else None
    for dev, w in list(open_w.items()):
        rem = removed_at(dev, last or w.started)
        if rem and rem.at >= w.started:
            close_open_before_removal(dev, last or w.started)
        elif w.restart_at and read_at is not None and read_at - w.restart_at < (TNC_CARRY if "TNC silent" in w.symptom else CARRY_OVER):
            # The kit restarted moments before the read: the watchdog has
            # not had the time to report the device again.
            w.cleared_by, w.cleared_detail, w.case_opened = "open", "still open when the log was read (the kit had just restarted)", "n/a"
            finish(dev)
        elif w.restart_at or (current is not None and current.get(dev, "") not in ("degraded", "healing", "failed", "silent", "deaf")):
            end_unseen(w)
        else:
            w.cleared_by, w.cleared_detail, w.case_opened = "open", "still open when the log was read", "n/a"
            finish(dev)

    for w in done:
        _infer_by_itself(w)
    done.sort(key=lambda w: w.started)
    loose = attach_notes(done, notes)
    return FoldResult(done, resets, loose, inv, starts, first, last, len(rows))


def _infer_by_itself(w: Wedge) -> None:
    """A wedge that came back without a step that acted is put down as
    "by itself" (case not opened, inferred) when that is the only reading:
    it answered within QUICK_RETURN, or it is an APRS receive silence that
    ended with a decoded frame (the radio was never reset; someone may
    simply have transmitted again). A silent TNC is left open: that one
    needs a button on the unit."""
    if w.cleared_by != "came_back" or w.case_source in ("note", "answer"):
        return
    quick = w.ended is not None and w.ended - w.started <= QUICK_RETURN
    aprs_decode = w.device == "aprs" and "TNC silent" not in w.symptom and "frame was decoded again" in w.cleared_detail
    if quick or aprs_decode:
        w.cleared_by = "itself"
        reason = f"within {int(QUICK_RETURN.total_seconds() // 60)} minutes" if quick else "a frame was decoded again with no reset in between"
        w.cleared_detail = f"came back by itself ({reason})"
        w.case_opened, w.case_source, w.inferred = "no", "inferred", True


def attach_notes(wedges: list[Wedge], notes: list[Row]) -> list[Row]:
    """Attach each hands-on note to its device's wedge; return the rest."""
    loose = []
    for n in notes:
        best = None
        for w in wedges:
            if w.device != n.device or w.started > n.ts:
                continue
            if w.cleared_by == "open" or n.ts <= (w.ended or w.last_seen or w.started) + NOTE_WINDOW:
                best = w  # the latest such wedge wins
        if best is None:
            loose.append(n)
            continue
        best.notes.append(f"{n.action}: {n.text}" if n.text else n.action)
        best.case_opened, best.case_source = n.case_opened, "note"
        if best.cleared_by in ("came_back", "not_recorded", "open", "itself"):
            best.cleared_detail = f"by hand: {n.action}" + (f" ({n.text})" if n.text else "")
            best.cleared_by = "by_hand"
            if best.ended is None:
                best.ended = n.ts
    return loose


def apply_answers(wedges: list[Wedge], answers: list[dict]) -> list[dict]:
    """Apply the owner's answers from history.toml (kit, device, at,
    cleared_by, case_opened, note) to the wedges they name; return the
    answers that matched nothing."""
    unmatched = []
    for a in answers:
        at = parse_ts(str(a["at"]))
        hit = None
        for w in wedges:
            if w.kit != a["kit"] or w.device != a["device"] or w.started > at + timedelta(minutes=30):
                continue
            if w.cleared_by == "open" or at <= (w.ended or w.last_seen or w.started) + ANSWER_WINDOW:
                hit = w  # the latest wedge that started before the answer's moment
        if hit is None:
            unmatched.append(a)
            continue
        hit.case_opened, hit.case_source = a["case_opened"], "answer"
        if a.get("cleared_by"):
            hit.cleared_detail = a["cleared_by"]
            if hit.cleared_by in ("came_back", "not_recorded", "itself", "open"):
                hit.cleared_by = "by_hand"
                if hit.ended is None:
                    hit.ended = at
        if a.get("note"):
            hit.notes.append(a["note"])
    return unmatched


def apply_rules(wedges: list[Wedge], rules: dict) -> None:
    """Standing answers from history.toml's [rules] table.

    picoaprs_silence_cleared_by_ptt: a PicoAPRS sits inside the sealed case
    and switches on again only with its PTT button, so a silent TNC that
    ended was cleared by hand with the case opened."""
    if rules.get("picoaprs_silence_cleared_by_ptt"):
        for w in wedges:
            if w.device == "aprs" and "TNC silent" in w.symptom and w.cleared_by in ("came_back", "not_recorded") \
                    and w.case_source not in ("note", "answer"):
                w.cleared_by = "by_hand"
                w.cleared_detail = "PTT held on the PicoAPRS, the only way to switch it on" + ("" if w.ended else " (when, the log does not say)")
                w.case_opened, w.case_source = "yes", "rule"


# ------------------------------------------------------------ powered time

def powered_hours(boots: list[dict], kit_power: list[dict]) -> dict:
    """Powered time from the system journal (each boot's uptime at its last
    entry: the wall clock at boot was wrong before the RTC cells were
    fitted, the uptime counter never is) and from the bridge's own
    heartbeat for boots the journal no longer holds."""
    seen = set()
    rows = []
    for b in boots:
        bid = b["boot_id"].replace("-", "")
        seen.add(bid[:8])
        hours = b["uptime_us"] / 3.6e9
        end = datetime.fromtimestamp(b["last_realtime_us"] / 1e6, tz=timezone.utc)
        rows.append({"boot": bid[:8], "ended": iso(end), "hours": round(hours, 2), "source": "journal"})
    for k in kit_power:
        bid = k["boot_id"].replace("-", "")
        if bid[:8] in seen:
            continue
        a, b2 = parse_ts(k["first_seen"]), parse_ts(k["last_seen"])
        rows.append({"boot": bid[:8], "ended": iso(b2), "hours": round(max(0.0, (b2 - a).total_seconds() / 3600), 2), "source": "bridge"})
    rows.sort(key=lambda r: r["ended"])
    return {"total_hours": round(sum(r["hours"] for r in rows), 1), "boots": rows}
