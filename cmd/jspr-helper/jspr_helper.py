#!/usr/bin/env python3
"""
jspr-helper.py: Drop-in replacement for the C jspr-helper binary.
Uses pyserial for reliable serial I/O on ARM64.

Two threads:
- stdin_thread: reads JSON commands from Go, sends JSPR on serial
- serial_thread: reads JSPR responses/unsolicited from serial, emits JSON on stdout

Same stdin/stdout JSON protocol as the C helper.
"""
import sys
import os
import json
import time
import signal
import threading

import serial


class JSPRHelper:
    def __init__(self, port, baud=230400):
        self.ser = serial.Serial(
            port, baud, timeout=0.5,
            bytesize=serial.EIGHTBITS,
            parity=serial.PARITY_NONE,
            stopbits=serial.STOPBITS_ONE,
            xonxoff=False, rtscts=False, dsrdtr=False,
        )
        self.ser.reset_input_buffer()
        self.ser.reset_output_buffer()
        self.running = True
        self._write_lock = threading.Lock()
        self._emit_lock = threading.Lock()

        # Set FTDI latency timer to 1ms if possible
        dev = os.path.basename(port)
        lat = f"/sys/bus/usb-serial/devices/{dev}/latency_timer"
        try:
            with open(lat, "w") as f:
                f.write("1")
        except Exception:
            pass

    def jspr_send(self, method, target, json_body):
        """Send a JSPR command: 'METHOD target {json}\r'"""
        line = f"{method} {target} {json_body}\r"
        with self._write_lock:
            self.ser.write(line.encode("ascii"))
            self.ser.flush()

    def jspr_receive(self):
        """Read one JSPR line: 'CODE target {json}\r'. Returns dict or None."""
        buf = bytearray()
        deadline = time.monotonic() + 2.0
        while self.running and time.monotonic() < deadline:
            ch = self.ser.read(1)
            if not ch:
                if len(buf) == 0:
                    return None
                continue
            if ch == b"\r":
                break
            buf.append(ch[0])

        if not buf:
            return None

        text = buf.decode("ascii", errors="ignore")

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

    def emit(self, msg_type, code, target, json_str):
        """Write JSON line to stdout for Go to read (thread-safe)."""
        try:
            json_obj = json.loads(json_str)
        except (json.JSONDecodeError, TypeError):
            json_obj = {}
        line = json.dumps(
            {"type": msg_type, "code": code, "target": target, "json": json_obj},
            separators=(",", ":"),
        )
        with self._emit_lock:
            sys.stdout.write(line + "\n")
            sys.stdout.flush()

    def stdin_thread(self):
        """Read JSON commands from Go's stdin pipe, send JSPR on serial."""
        stdin_fd = sys.stdin.fileno()
        buf = ""
        sys.stderr.write("jspr-helper.py: stdin_thread started\n")
        sys.stderr.flush()
        while self.running:
            try:
                data = os.read(stdin_fd, 4096)
            except OSError as e:
                sys.stderr.write(f"jspr-helper.py: stdin read error: {e}\n")
                sys.stderr.flush()
                break
            if not data:
                sys.stderr.write("jspr-helper.py: stdin EOF\n")
                sys.stderr.flush()
                break  # EOF

            sys.stderr.write(f"jspr-helper.py: stdin got {len(data)} bytes\n")
            sys.stderr.flush()
            buf += data.decode("utf-8", errors="replace")

            while "\n" in buf:
                line, buf = buf.split("\n", 1)
                line = line.strip()
                if not line:
                    continue
                try:
                    cmd = json.loads(line)
                    method = cmd.get("method", "")
                    target = cmd.get("target", "")
                    json_field = cmd.get("json", {})
                    if isinstance(json_field, dict):
                        json_body = json.dumps(json_field, separators=(", ", ": "))
                    else:
                        json_body = str(json_field)
                    if method and target:
                        sys.stderr.write(f"jspr-helper.py: TX {method} {target}\n")
                        sys.stderr.flush()
                        self.jspr_send(method, target, json_body)
                except (json.JSONDecodeError, KeyError) as e:
                    sys.stderr.write(f"jspr-helper.py: parse error: {e}\n")
                    sys.stderr.flush()

        self.running = False

    def serial_thread(self):
        """Read JSPR responses from serial, emit JSON on stdout."""
        while self.running:
            resp = self.jspr_receive()
            if resp:
                if resp["code"] == 299:
                    self.emit("unsolicited", resp["code"], resp["target"], resp["json_str"])
                else:
                    self.emit("response", resp["code"], resp["target"], resp["json_str"])

    def run(self):
        # Drain stale serial data
        time.sleep(0.1)
        if self.ser.in_waiting > 0:
            self.ser.read(self.ser.in_waiting)

        t_stdin = threading.Thread(target=self.stdin_thread, daemon=True)
        t_serial = threading.Thread(target=self.serial_thread, daemon=True)

        t_stdin.start()
        t_serial.start()

        # Wait until either thread signals shutdown
        while self.running:
            time.sleep(0.1)

        self.ser.close()


def main():
    if len(sys.argv) < 2:
        sys.stderr.write("Usage: jspr_helper.py /dev/ttyUSB0 [baud]\n")
        sys.exit(1)

    port = sys.argv[1]
    baud = int(sys.argv[2]) if len(sys.argv) > 2 else 230400

    def handle_signal(sig, frame):
        helper.running = False
    signal.signal(signal.SIGTERM, handle_signal)
    signal.signal(signal.SIGINT, handle_signal)

    sys.stdout = os.fdopen(sys.stdout.fileno(), "w", buffering=1)

    sys.stderr.write(f"jspr-helper.py: connected to {port} at {baud} baud\n")
    sys.stderr.flush()

    helper = JSPRHelper(port, baud)
    try:
        helper.run()
    except Exception as e:
        sys.stderr.write(f"jspr-helper.py: fatal: {e}\n")
        sys.stderr.flush()
        sys.exit(1)

    sys.stderr.write("jspr-helper.py: shutdown\n")
    sys.stderr.flush()


if __name__ == "__main__":
    main()
