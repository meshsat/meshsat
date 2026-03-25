#!/usr/bin/env python3
"""
jspr-helper.py: JSPR serial helper for RockBLOCK 9704.
Uses pyserial for reliable serial I/O on ARM64.

Architecture: SINGLE-THREAD EVENT LOOP (MESHSAT-334)
- One thread owns the serial port — no concurrent read/write
- Bulk reads via ser.in_waiting + ser.read(n) — no byte-by-byte
- stdin is read via non-blocking os.read on a separate thread that
  queues commands for the event loop to process
- This matches the standalone test script pattern that works 100%

Commands from Go (stdin JSON):
- {"cmd":"send", "method":"GET", "target":"apiVersion", "json":{}}
    -> sends single JSPR line, response comes back via event loop
- {"cmd":"send_mo", "topic_id":244, "data":"BASE64", "length":42, "request_reference":1}
    -> handles entire MO flow inline (originate + segment + wait for status)
    -> returns {"type":"mo_result", "code":200, "target":"messageOriginateStatus", "json":{...}}
"""
import sys
import os
import json
import time
import signal
import threading
import queue

import serial

# CRC-16/CCITT (XModem) — same as Go's crc16CCITT and the working test script
CRC16_TABLE = [
    0x0000, 0x1021, 0x2042, 0x3063, 0x4084, 0x50A5, 0x60C6, 0x70E7,
    0x8108, 0x9129, 0xA14A, 0xB16B, 0xC18C, 0xD1AD, 0xE1CE, 0xF1EF,
    0x1231, 0x0210, 0x3273, 0x2252, 0x52B5, 0x4294, 0x72F7, 0x62D6,
    0x9339, 0x8318, 0xB37B, 0xA35A, 0xD3BD, 0xC39C, 0xF3FF, 0xE3DE,
    0x2462, 0x3443, 0x0420, 0x1401, 0x64E6, 0x74C7, 0x44A4, 0x5485,
    0xA56A, 0xB54B, 0x8528, 0x9509, 0xE5EE, 0xF5CF, 0xC5AC, 0xD58D,
    0x3653, 0x2672, 0x1611, 0x0630, 0x76D7, 0x66F6, 0x5695, 0x46B4,
    0xB75B, 0xA77A, 0x9719, 0x8738, 0xF7DF, 0xE7FE, 0xD79D, 0xC7BC,
    0x48C4, 0x58E5, 0x6886, 0x78A7, 0x0840, 0x1861, 0x2802, 0x3823,
    0xC9CC, 0xD9ED, 0xE98E, 0xF9AF, 0x8948, 0x9969, 0xA90A, 0xB92B,
    0x5AF5, 0x4AD4, 0x7AB7, 0x6A96, 0x1A71, 0x0A50, 0x3A33, 0x2A12,
    0xDBFD, 0xCBDC, 0xFBBF, 0xEB9E, 0x9B79, 0x8B58, 0xBB3B, 0xAB1A,
    0x6CA6, 0x7C87, 0x4CE4, 0x5CC5, 0x2C22, 0x3C03, 0x0C60, 0x1C41,
    0xEDAE, 0xFD8F, 0xCDEC, 0xDDCD, 0xAD2A, 0xBD0B, 0x8D68, 0x9D49,
    0x7E97, 0x6EB6, 0x5ED5, 0x4EF4, 0x3E13, 0x2E32, 0x1E51, 0x0E70,
    0xFF9F, 0xEFBE, 0xDFDD, 0xCFFC, 0xBF1B, 0xAF3A, 0x9F59, 0x8F78,
    0x9188, 0x81A9, 0xB1CA, 0xA1EB, 0xD10C, 0xC12D, 0xF14E, 0xE16F,
    0x1080, 0x00A1, 0x30C2, 0x20E3, 0x5004, 0x4025, 0x7046, 0x6067,
    0x83B9, 0x9398, 0xA3FB, 0xB3DA, 0xC33D, 0xD31C, 0xE37F, 0xF35E,
    0x02B1, 0x1290, 0x22F3, 0x32D2, 0x4235, 0x5214, 0x6277, 0x7256,
    0xB5EA, 0xA5CB, 0x95A8, 0x8589, 0xF56E, 0xE54F, 0xD52C, 0xC50D,
    0x34E2, 0x24C3, 0x14A0, 0x0481, 0x7466, 0x6447, 0x5424, 0x4405,
    0xA7DB, 0xB7FA, 0x8799, 0x97B8, 0xE75F, 0xF77E, 0xC71D, 0xD73C,
    0x26D3, 0x36F2, 0x0691, 0x16B0, 0x6657, 0x7676, 0x4615, 0x5634,
    0xD94C, 0xC96D, 0xF90E, 0xE92F, 0x99C8, 0x89E9, 0xB98A, 0xA9AB,
    0x5844, 0x4865, 0x7806, 0x6827, 0x18C0, 0x08E1, 0x3882, 0x28A3,
    0xCB7D, 0xDB5C, 0xEB3F, 0xFB1E, 0x8BF9, 0x9BD8, 0xABBB, 0xBB9A,
    0x4A75, 0x5A54, 0x6A37, 0x7A16, 0x0AF1, 0x1AD0, 0x2AB3, 0x3A92,
    0xFD2E, 0xED0F, 0xDD6C, 0xCD4D, 0xBDAA, 0xAD8B, 0x9DE8, 0x8DC9,
    0x7C26, 0x6C07, 0x5C64, 0x4C45, 0x3CA2, 0x2C83, 0x1CE0, 0x0CC1,
    0xEF1F, 0xFF3E, 0xCF5D, 0xDF7C, 0xAF9B, 0xBFBA, 0x8FD9, 0x9FF8,
    0x6E17, 0x7E36, 0x4E55, 0x5E74, 0x2E93, 0x3EB2, 0x0ED1, 0x1EF0,
]


def crc16(data):
    crc = 0
    for b in data:
        crc = ((crc << 8) ^ CRC16_TABLE[((crc >> 8) ^ b) & 0xFF]) & 0xFFFF
    return crc


def jd(obj):
    """JSON with mandatory spaces (modem rejects compact JSON with 407)."""
    return json.dumps(obj, separators=(", ", ": "))


def log(msg):
    sys.stderr.write(f"jspr-helper.py: {msg}\n")
    sys.stderr.flush()


def parse_jspr_line(raw):
    """Parse a raw JSPR line into {code, target, json_str} or None."""
    text = raw.decode("ascii", errors="ignore")

    # Skip leading non-printable (DC1 on boot, \n leftover)
    i = 0
    while i < len(text) and not text[i].isdigit():
        i += 1
    text = text[i:]

    if len(text) < 3:
        return None

    try:
        code = int(text[:3])
    except ValueError:
        return None
    if code < 200 or code > 500:
        return None

    rest = text[4:]  # skip "CODE "

    space = rest.find(" ")
    if space >= 0:
        target = rest[:space]
        remainder = rest[space + 1:]
        brace = remainder.find("{")
        json_str = remainder[brace:] if brace >= 0 else "{}"
    else:
        target = rest
        json_str = "{}"

    return {"code": code, "target": target, "json_str": json_str}


class JSPRHelper:
    def __init__(self, port, baud=230400):
        self.ser = serial.Serial(
            port, baud, timeout=0.05,  # short timeout for non-blocking-style reads
            bytesize=serial.EIGHTBITS,
            parity=serial.PARITY_NONE,
            stopbits=serial.STOPBITS_ONE,
            xonxoff=False, rtscts=False, dsrdtr=False,
        )
        self.ser.reset_input_buffer()
        self.ser.reset_output_buffer()
        self.running = True
        self._rx_buf = bytearray()
        self._cmd_queue = queue.Queue()

        # Set FTDI latency timer to 1ms if possible
        dev = os.path.basename(port)
        lat = f"/sys/bus/usb-serial/devices/{dev}/latency_timer"
        try:
            with open(lat, "w") as f:
                f.write("1")
        except Exception:
            pass

    # ── Serial I/O (ONLY called from event loop thread) ──────────────

    def serial_write(self, method, target, json_body):
        """Send a JSPR command with pacing.
        Only called from the event loop thread.
        Does NOT reset input buffer — that would discard modem responses
        that are in flight (e.g. stale MO status arriving between commands)."""
        time.sleep(0.005)  # 5ms pacing — prevent command coalescence in USB frame
        line = f"{method} {target} {json_body}\r"
        self.ser.write(line.encode("ascii"))

    def serial_read_lines(self):
        """Bulk-read all available data and return complete JSPR lines.
        Matches the standalone test script pattern: ser.in_waiting + ser.read(n).
        Only called from the event loop thread."""
        lines = []
        n = self.ser.in_waiting
        if n > 0:
            data = self.ser.read(n)
            self._rx_buf += data

        while b"\r" in self._rx_buf:
            line, self._rx_buf = self._rx_buf.split(b"\r", 1)
            line = line.strip()
            if line:
                parsed = parse_jspr_line(line)
                if parsed:
                    lines.append(parsed)
        return lines

    # ── Stdout emission ──────────────────────────────────────────────

    def emit(self, msg_type, code, target, json_str):
        """Write JSON line to stdout for Go to read."""
        try:
            json_obj = json.loads(json_str)
        except (json.JSONDecodeError, TypeError):
            json_obj = {}
        line = json.dumps(
            {"type": msg_type, "code": code, "target": target, "json": json_obj},
            separators=(",", ":"),
        )
        data = (line + "\n").encode("utf-8")
        os.write(1, data)

    # ── MO flow (blocking inline state machine) ─────────────────────

    def _serial_write_raw(self, method, target, json_body):
        """Write a JSPR command directly — no buffer reset.
        Used mid-MO where we must not discard pending serial data."""
        time.sleep(0.005)  # 5ms pacing
        line = f"{method} {target} {json_body}\r"
        self.ser.write(line.encode("ascii"))
        self.ser.flush()

    def do_send_mo(self, topic_id, payload_b64, length, request_reference):
        """Handle entire MO flow inline — single thread, no async.
        Matches the standalone test script exactly."""
        mlog = lambda msg: log(f"MO: {msg}")

        try:
            self._do_send_mo_inner(topic_id, payload_b64, length, request_reference, mlog)
        finally:
            self._drain_stale_commands()

    def _do_send_mo_inner(self, topic_id, payload_b64, length, request_reference, mlog):
        # Drain pending data — emit complete lines, then clear partial data
        for resp in self.serial_read_lines():
            if resp["code"] == 299:
                self.emit("unsolicited", resp["code"], resp["target"], resp["json_str"])
            else:
                self.emit("response", resp["code"], resp["target"], resp["json_str"])
        self.ser.reset_input_buffer()
        self._rx_buf = bytearray()
        time.sleep(0.1)  # 100ms settle — match standalone test script

        # Drain any stale messageOriginateStatus from previous MO
        for resp in self.serial_read_lines():
            if resp["code"] == 299 and resp["target"] == "messageOriginateStatus":
                mlog(f"drained stale status: {resp['json_str'][:80]}")
            elif resp["code"] == 299:
                self.emit("unsolicited", resp["code"], resp["target"], resp["json_str"])

        # 1. PUT messageOriginate
        cmd = jd({"topic_id": topic_id, "message_length": length, "request_reference": request_reference})
        mlog(f"TX PUT messageOriginate {cmd}")
        self._serial_write_raw("PUT", "messageOriginate", cmd)

        # 2. Poll for responses — handle entire flow inline
        msg_id = None
        segment_sent = False
        # Two-phase timeout: 30s for initial 200 response, then 4 min for satellite
        initial_deadline = time.monotonic() + 30
        full_deadline = time.monotonic() + 240
        got_200 = False

        while self.running:
            # Check appropriate deadline
            if not got_200 and time.monotonic() > initial_deadline:
                mlog("no 200 response to messageOriginate within 30s — modem not responding")
                self.emit("mo_result", 200, "messageOriginateStatus",
                          jd({"final_mo_status": "no_response", "message_id": 0, "topic_id": topic_id}))
                return
            if time.monotonic() > full_deadline:
                break

            lines = self.serial_read_lines()
            if not lines:
                time.sleep(0.010)
                continue

            for resp in lines:
                code = resp["code"]
                target = resp["target"]
                json_str = resp["json_str"]
                mlog(f"RX {code} {target}")

                # Forward unsolicited non-MO messages to Go
                if code == 299 and target not in ("messageOriginateSegment", "messageOriginateStatus"):
                    self.emit("unsolicited", code, target, json_str)
                    continue

                # 200 messageOriginate — extract message_id
                if code == 200 and target == "messageOriginate":
                    try:
                        d = json.loads(json_str)
                        msg_id = d.get("message_id")
                        resp_str = d.get("message_response", "")
                        got_200 = True
                        mlog(f"message_id={msg_id} response={resp_str}")
                        self.emit("response", code, target, json_str)
                        if resp_str != "message_accepted":
                            self.emit("mo_result", 200, "messageOriginateStatus",
                                      jd({"final_mo_status": resp_str, "message_id": msg_id or 0, "topic_id": topic_id}))
                            return
                    except (json.JSONDecodeError, KeyError):
                        pass
                    continue

                # 299 messageOriginateSegment — respond IMMEDIATELY (raw write)
                if code == 299 and target == "messageOriginateSegment" and not segment_sent:
                    try:
                        d = json.loads(json_str)
                        seg_cmd = jd({
                            "topic_id": d.get("topic_id", topic_id),
                            "message_id": d.get("message_id", msg_id),
                            "segment_length": d.get("segment_length", length),
                            "segment_start": d.get("segment_start", 0),
                            "data": payload_b64,
                        })
                        mlog(f"TX PUT messageOriginateSegment (msg_id={d.get('message_id', msg_id)})")
                        self._serial_write_raw("PUT", "messageOriginateSegment", seg_cmd)
                        segment_sent = True
                    except (json.JSONDecodeError, KeyError) as e:
                        mlog(f"segment parse error: {e}")
                    continue

                # 200 messageOriginateSegment — segment accepted
                if code == 200 and target == "messageOriginateSegment":
                    mlog("segment accepted")
                    self.emit("response", code, target, json_str)
                    continue

                # 299 messageOriginateStatus — final result
                if code == 299 and target == "messageOriginateStatus":
                    try:
                        d = json.loads(json_str)
                        status_msg_id = d.get("message_id", -1)
                        final = d.get("final_mo_status", "unknown")
                        mlog(f"status msg_id={status_msg_id} final={final}")
                        if msg_id is not None and status_msg_id != msg_id:
                            mlog(f"STALE status (expected msg_id={msg_id}), ignoring")
                            continue
                        self.emit("mo_result", 200, "messageOriginateStatus", json_str)
                        return
                    except (json.JSONDecodeError, KeyError):
                        pass
                    continue

                # Non-200 error for messageOriginate
                if code >= 400 and target == "messageOriginate":
                    mlog(f"messageOriginate error: {code}")
                    self.emit("mo_result", code, target, json_str)
                    return

                # Forward anything else to Go
                if code == 299:
                    self.emit("unsolicited", code, target, json_str)
                else:
                    self.emit("response", code, target, json_str)

        # Timeout
        mlog("TIMEOUT waiting for MO completion")
        self.emit("mo_result", 200, "messageOriginateStatus",
                  jd({"final_mo_status": "helper_timeout", "message_id": msg_id or 0, "topic_id": topic_id}))

    def _drain_stale_commands(self):
        """Discard queued commands accumulated during MO flow.
        Signal poll commands (GET constellationState) pile up while
        MO blocks the event loop — executing them all at once would
        cause a rapid burst of writes. Discard them; the next poll
        cycle will issue a fresh one."""
        drained = 0
        while True:
            try:
                self._cmd_queue.get_nowait()
                drained += 1
            except queue.Empty:
                break
        if drained:
            log(f"MO: drained {drained} stale queued commands")

    # ── Stdin reader thread ──────────────────────────────────────────
    # This is the ONLY other thread. It reads from Go's stdin pipe and
    # queues commands for the event loop. It NEVER touches the serial port.

    def stdin_reader(self):
        """Read JSON commands from Go and queue them. Never touches serial."""
        stdin_fd = sys.stdin.fileno()
        buf = ""
        while self.running:
            try:
                data = os.read(stdin_fd, 4096)
            except OSError:
                break
            if not data:
                break  # EOF — Go process died

            buf += data.decode("utf-8", errors="replace")

            while "\n" in buf:
                line, buf = buf.split("\n", 1)
                line = line.strip()
                if not line:
                    continue
                try:
                    cmd = json.loads(line)
                    self._cmd_queue.put(cmd)
                except json.JSONDecodeError as e:
                    log(f"stdin parse error: {e}")

        self.running = False

    # ── Main event loop (single thread owns serial) ──────────────────

    def run(self):
        # Drain stale serial data
        time.sleep(0.1)
        if self.ser.in_waiting > 0:
            self.ser.read(self.ser.in_waiting)

        # Start stdin reader (only thread besides event loop, never touches serial)
        t_stdin = threading.Thread(target=self.stdin_reader, daemon=True)
        t_stdin.start()

        # Event loop — single thread owns the serial port
        while self.running:
            # 1. Read serial (bulk)
            lines = self.serial_read_lines()
            for resp in lines:
                code = resp["code"]
                target = resp["target"]
                json_str = resp["json_str"]

                if code == 299 and target != "constellationState":
                    log(f"RX unsolicited {code} {target} {json_str[:100]}")

                if code == 299:
                    self.emit("unsolicited", code, target, json_str)
                else:
                    self.emit("response", code, target, json_str)

            # 2. Process queued commands from Go
            try:
                cmd = self._cmd_queue.get_nowait()
            except queue.Empty:
                cmd = None

            if cmd:
                cmd_type = cmd.get("cmd", "send")

                if cmd_type == "send_mo":
                    # MO — blocking inline state machine, no other serial
                    # activity until complete
                    self.do_send_mo(
                        topic_id=cmd.get("topic_id", 244),
                        payload_b64=cmd.get("data", ""),
                        length=cmd.get("length", 0),
                        request_reference=cmd.get("request_reference", 1),
                    )
                elif cmd_type == "send":
                    method = cmd.get("method", "")
                    target = cmd.get("target", "")
                    json_field = cmd.get("json", {})
                    if isinstance(json_field, dict):
                        json_body = json.dumps(json_field, separators=(", ", ": "))
                    else:
                        json_body = str(json_field)
                    if method and target:
                        self.serial_write(method, target, json_body)

            # 3. Brief sleep to avoid busy-spinning (matches C library's 10ms)
            if not lines and cmd is None:
                time.sleep(0.01)

        self.ser.close()


def main():
    if len(sys.argv) < 2:
        log("Usage: jspr_helper.py /dev/ttyUSB0 [baud]")
        sys.exit(1)

    port = sys.argv[1]
    baud = int(sys.argv[2]) if len(sys.argv) > 2 else 230400

    helper = JSPRHelper(port, baud)

    def handle_signal(sig, frame):
        helper.running = False
    signal.signal(signal.SIGTERM, handle_signal)
    signal.signal(signal.SIGINT, handle_signal)

    log(f"connected to {port} at {baud} baud")

    try:
        helper.run()
    except Exception as e:
        log(f"fatal: {e}")
        sys.exit(1)

    log("shutdown")


if __name__ == "__main__":
    main()
