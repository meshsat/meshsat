#!/usr/bin/env python3
"""
Standalone 9704 JSPR test — no Go, no bridge, just pyserial.
Tests whether the modem stays alive for 3 minutes on this USB controller.

Usage: python3 test_9704_standalone.py /dev/ttyUSB0
"""
import sys
import time
import json
import serial

def jd(obj):
    return json.dumps(obj, separators=(", ", ": "))

def log(msg):
    ts = time.strftime("%H:%M:%S", time.localtime())
    print(f"[{ts}] {msg}", flush=True)

def send(ser, method, target, payload):
    line = f"{method} {target} {jd(payload)}\r"
    ser.write(line.encode("ascii"))
    log(f"TX: {method} {target} {jd(payload)}")
    time.sleep(0.1)

def read_lines(ser, rx_buf, timeout=2.0):
    """Bulk read with timeout, return (lines, updated_rx_buf)."""
    deadline = time.monotonic() + timeout
    lines = []
    while time.monotonic() < deadline:
        n = ser.in_waiting
        if n > 0:
            rx_buf += ser.read(n)
        while b"\r" in rx_buf:
            line, rx_buf = rx_buf.split(b"\r", 1)
            text = line.decode("ascii", errors="ignore").strip()
            if text:
                lines.append(text)
        if lines:
            break
        time.sleep(0.01)
    return lines, rx_buf

def wait_response(ser, rx_buf, target, timeout=5.0):
    """Wait for a specific target response. Empty target matches any non-299."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        lines, rx_buf = read_lines(ser, rx_buf, timeout=0.5)
        for line in lines:
            log(f"RX: {line[:120]}")
            if not target:  # match any non-unsolicited
                if not line.startswith("299"):
                    return line, rx_buf
            elif target in line:
                return line, rx_buf
    return None, rx_buf

def main():
    port = sys.argv[1] if len(sys.argv) > 1 else "/dev/ttyUSB0"
    baud = 230400

    log(f"Opening {port} at {baud} baud")
    ser = serial.Serial(
        port, baud, timeout=0.05,
        bytesize=serial.EIGHTBITS, parity=serial.PARITY_NONE,
        stopbits=serial.STOPBITS_ONE,
        xonxoff=False, rtscts=False, dsrdtr=False, exclusive=True,
    )
    ser.reset_input_buffer()
    ser.reset_output_buffer()

    # Set FTDI latency timer
    import os
    dev = os.path.basename(port)
    try:
        with open(f"/sys/bus/usb-serial/devices/{dev}/latency_timer", "w") as f:
            f.write("1")
        log("FTDI latency_timer set to 1ms")
    except Exception:
        pass

    time.sleep(0.3)
    rx_buf = bytearray()

    # === HANDSHAKE ===
    log("=== HANDSHAKE ===")

    # Step 1: GET apiVersion (first command always returns 405 MALFORMED on cold start)
    send(ser, "GET", "apiVersion", {})
    # Accept ANY response — 405 MALFORMED won't contain "apiVersion" as target
    resp, rx_buf = wait_response(ser, rx_buf, "", timeout=3)
    if resp and "405" in resp:
        log("Got 405 (cold buffer, expected) — retrying")
    time.sleep(0.3)
    send(ser, "GET", "apiVersion", {})
    resp, rx_buf = wait_response(ser, rx_buf, "apiVersion", timeout=5)
    if not resp or "200" not in resp:
        log(f"FAIL: apiVersion response: {resp}")
        return

    # Step 2: PUT apiVersion
    send(ser, "PUT", "apiVersion", {"active_version": {"major": 1, "minor": 7, "patch": 0}})
    resp, rx_buf = wait_response(ser, rx_buf, "apiVersion", timeout=5)

    # Step 3: PUT simConfig
    send(ser, "PUT", "simConfig", {"interface": "internal"})
    resp, rx_buf = wait_response(ser, rx_buf, "simConfig", timeout=5)

    # Step 4: PUT operationalState
    send(ser, "PUT", "operationalState", {"state": "active"})
    resp, rx_buf = wait_response(ser, rx_buf, "operationalState", timeout=5)

    log("=== HANDSHAKE COMPLETE ===")
    log("")
    log("=== MONITORING (3 minutes) — watching for unsolicited messages ===")
    log("If modem goes silent, we'll see a gap in timestamps.")
    log("")

    # === MONITOR ===
    start = time.monotonic()
    last_rx = time.monotonic()
    msg_count = 0
    poll_count = 0
    MONITOR_SECS = 180  # 3 minutes

    while time.monotonic() - start < MONITOR_SECS:
        elapsed = time.monotonic() - start

        # Bulk read
        lines, rx_buf = read_lines(ser, rx_buf, timeout=1.0)
        for line in lines:
            msg_count += 1
            gap = time.monotonic() - last_rx
            last_rx = time.monotonic()
            # Only log first 100 chars to avoid noise
            log(f"RX [{msg_count}] (gap={gap:.1f}s, elapsed={elapsed:.0f}s): {line[:100]}")

        # Every 30s, send active GET constellationState (mimics signal poller)
        if int(elapsed) > 0 and int(elapsed) % 30 == 0 and poll_count < int(elapsed) // 30:
            poll_count = int(elapsed) // 30
            send(ser, "GET", "constellationState", {})

        # Detect silence
        silence = time.monotonic() - last_rx
        if silence > 15:
            log(f"WARNING: {silence:.0f}s of silence — modem may be hung!")

    log("")
    log(f"=== DONE — {msg_count} messages in {MONITOR_SECS}s ===")
    gap = time.monotonic() - last_rx
    if gap > 10:
        log(f"VERDICT: Modem went SILENT (last RX was {gap:.0f}s ago)")
        log("This confirms USB controller / driver issue — NOT software")
    else:
        log("VERDICT: Modem stayed ALIVE for full duration")
        log("Issue is in Go interaction layer, not USB")

    ser.close()

if __name__ == "__main__":
    main()
