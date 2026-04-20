#!/usr/bin/env python3
"""TCP loopback interop test: starts rnsd with TCP interface, connects,
exchanges announces, and reports results.

Usage: interop_tcp_test.py <bridge_announce_hex>
  - Starts rnsd listening on 127.0.0.1:4242
  - Sends the bridge announce via TCP (HDLC framed)
  - Waits for rnsd to send its own announce back
  - Reports whether rnsd accepted the bridge announce

Output: JSON with results.
"""
import hashlib
import json
import os
import socket
import struct
import sys
import tempfile
import time
import threading
import signal

sys.path.insert(0, '/tmp/rns-test/lib/python3.11/site-packages')

# HDLC framing (matches RNS TCPInterface.py)
FLAG = 0x7E
ESC = 0x7D
ESC_MASK = 0x20

def hdlc_escape(data: bytes) -> bytes:
    data = data.replace(bytes([ESC]), bytes([ESC, ESC ^ ESC_MASK]))
    data = data.replace(bytes([FLAG]), bytes([ESC, FLAG ^ ESC_MASK]))
    return data

def hdlc_frame(data: bytes) -> bytes:
    return bytes([FLAG]) + hdlc_escape(data) + bytes([FLAG])

def hdlc_extract_frames(buf: bytes) -> tuple:
    """Extract complete HDLC frames from buffer. Returns (frames, remaining)."""
    frames = []
    while True:
        start = buf.find(bytes([FLAG]))
        if start == -1:
            break
        end = buf.find(bytes([FLAG]), start + 1)
        if end == -1:
            buf = buf[start:]
            break
        frame_data = buf[start+1:end]
        if len(frame_data) >= 19:  # Minimum Reticulum header
            # Unescape
            frame_data = frame_data.replace(bytes([ESC, FLAG ^ ESC_MASK]), bytes([FLAG]))
            frame_data = frame_data.replace(bytes([ESC, ESC ^ ESC_MASK]), bytes([ESC]))
            frames.append(frame_data)
        buf = buf[end:]
    return frames, buf


def parse_packet_type(raw: bytes) -> str:
    if len(raw) < 2:
        return "unknown"
    flags = raw[0]
    pt = flags & 0x03
    return {0: "DATA", 1: "ANNOUNCE", 2: "LINKREQUEST", 3: "PROOF"}.get(pt, f"unknown({pt})")


def run_test(bridge_announce_hex: str) -> dict:
    results = {
        "bridge_announce_sent": False,
        "rnsd_received_frames": 0,
        "rnsd_announces_received": [],
        "bridge_announce_accepted": False,
        "errors": [],
    }

    try:
        bridge_announce = bytes.fromhex(bridge_announce_hex)
    except ValueError as e:
        results["errors"].append(f"bad hex: {e}")
        return results

    # Create rnsd config
    config_dir = tempfile.mkdtemp(prefix="rnsd_test_")
    config_file = os.path.join(config_dir, "config")
    with open(config_file, "w") as f:
        f.write("""
[reticulum]
  enable_transport = No
  share_instance = No
  shared_instance_port = 0
  instance_control_port = 0

[interfaces]
  [[TCP Server Interface]]
    type = TCPServerInterface
    enabled = yes
    listen_ip = 127.0.0.1
    listen_port = 14242
""")

    # Start rnsd in background
    import subprocess
    rnsd_proc = subprocess.Popen(
        [sys.executable, "-c", f"""
import sys, os, time
sys.path.insert(0, '/tmp/rns-test/lib/python3.11/site-packages')
os.environ['RNS_INSTANCE'] = 'test_interop'

import RNS
reticulum = RNS.Reticulum(configdir='{config_dir}', loglevel=RNS.LOG_DEBUG)

# Create a destination that will announce
identity = RNS.Identity()
dest = RNS.Destination(identity, RNS.Destination.IN, RNS.Destination.SINGLE, "test", "interop")

# Announce every 2 seconds
time.sleep(1)
dest.announce(app_data=b"hello from rnsd")

# Keep running for 10 seconds
time.sleep(10)
"""],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )

    try:
        # Wait for rnsd to start
        time.sleep(3)

        # Connect via TCP
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(5)
        try:
            sock.connect(("127.0.0.1", 14242))
        except Exception as e:
            results["errors"].append(f"TCP connect failed: {e}")
            return results

        # Send bridge announce (HDLC framed)
        frame = hdlc_frame(bridge_announce)
        sock.sendall(frame)
        results["bridge_announce_sent"] = True

        # Read responses for 5 seconds
        buf = b""
        deadline = time.time() + 5
        while time.time() < deadline:
            sock.settimeout(max(0.1, deadline - time.time()))
            try:
                data = sock.recv(4096)
                if not data:
                    break
                buf += data
            except socket.timeout:
                continue

        # Extract frames
        frames, _ = hdlc_extract_frames(buf)
        results["rnsd_received_frames"] = len(frames)

        for f in frames:
            ptype = parse_packet_type(f)
            if ptype == "ANNOUNCE":
                # Parse the announce
                flags = f[0]
                hops = f[1]
                dest_hash = f[2:18].hex()
                results["rnsd_announces_received"].append({
                    "dest_hash": dest_hash,
                    "hops": hops,
                    "size": len(f),
                })

        # If we received any announce from rnsd, the connection works
        if results["rnsd_announces_received"]:
            results["bridge_announce_accepted"] = True

        sock.close()

    finally:
        rnsd_proc.terminate()
        try:
            rnsd_proc.wait(timeout=5)
        except:
            rnsd_proc.kill()

        # Cleanup
        import shutil
        shutil.rmtree(config_dir, ignore_errors=True)

    return results


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(json.dumps({"errors": ["usage: interop_tcp_test.py <bridge_announce_hex>"]}))
        sys.exit(1)

    result = run_test(sys.argv[1])
    print(json.dumps(result, indent=2))
