#!/usr/bin/env python3
"""A software RNode for tests: two pseudo-terminals that behave like two
RNode radios on the same channel.

Each side answers the host protocol of RNode firmware 1.75 (detect, firmware
version, platform, MCU, configuration echo, READY flow control) and every
data frame written to one side is delivered as a data frame on the other,
so an rnsd RNodeInterface on side A and the MeshSat bridge on side B talk
as if over LoRa, with no radio and no CPU to speak of.

Usage: fake_rnode.py [--no-ready] [--drop N]
Prints two lines "A /dev/pts/N" and "B /dev/pts/M" and runs until killed.
[MESHSAT-1349]
"""
import argparse, os, pty, select, sys, threading, tty

FEND, FESC, TFEND, TFESC = 0xC0, 0xDB, 0xDC, 0xDD
CMD_DATA, CMD_FREQ, CMD_BW, CMD_TXP, CMD_SF, CMD_CR, CMD_STATE, CMD_DETECT = 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x08
CMD_LEAVE, CMD_ST_ALOCK, CMD_LT_ALOCK, CMD_READY, CMD_PLATFORM, CMD_MCU, CMD_FW = 0x0A, 0x0B, 0x0C, 0x0F, 0x48, 0x49, 0x50
DETECT_REQ, DETECT_RESP, PLATFORM_ESP32 = 0x73, 0x46, 0x80


def escape(b):
    return b.replace(bytes([FESC]), bytes([FESC, TFESC])).replace(bytes([FEND]), bytes([FESC, TFEND]))


def unescape(b):
    out, esc = bytearray(), False
    for c in b:
        if esc:
            out.append(FEND if c == TFEND else FESC if c == TFESC else c); esc = False
        elif c == FESC:
            esc = True
        else:
            out.append(c)
    return bytes(out)


def frame(cmd, payload=b""):
    return bytes([FEND, cmd]) + escape(payload) + bytes([FEND])


class Side:
    def __init__(self, name, ready=True):
        self.name = name
        self.master, slave = pty.openpty()
        tty.setraw(self.master)
        tty.setraw(slave)
        self.path = os.ttyname(slave)
        self.peer = None
        self.ready = ready
        self.buf = bytearray()
        self.in_frame = False
        self.lock = threading.Lock()

    def write(self, b):
        with self.lock:
            os.write(self.master, b)

    def handle(self, cmd, payload):
        if cmd == CMD_DETECT and payload[:1] == bytes([DETECT_REQ]):
            self.write(frame(CMD_DETECT, bytes([DETECT_RESP])))
        elif cmd == CMD_FW:
            self.write(frame(CMD_FW, bytes([1, 75])))
        elif cmd == CMD_PLATFORM:
            self.write(frame(CMD_PLATFORM, bytes([PLATFORM_ESP32])))
        elif cmd == CMD_MCU:
            self.write(frame(CMD_MCU, bytes([0x81])))
        elif cmd in (CMD_FREQ, CMD_BW, CMD_TXP, CMD_SF, CMD_CR, CMD_STATE, CMD_ST_ALOCK, CMD_LT_ALOCK):
            self.write(frame(cmd, payload))
        elif cmd == CMD_DATA:
            if self.peer is not None:
                self.peer.write(frame(CMD_DATA, payload))
            if self.ready:
                self.write(frame(CMD_READY, b"\x01"))

    def feed(self, data):
        for c in data:
            if c == FEND:
                if self.in_frame and len(self.buf) > 0:
                    self.handle(self.buf[0], unescape(bytes(self.buf[1:])))
                self.buf = bytearray(); self.in_frame = True
            elif self.in_frame:
                self.buf.append(c)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--no-ready", action="store_true", help="never send READY (no flow control)")
    args = ap.parse_args()
    a, b = Side("A", ready=not args.no_ready), Side("B", ready=not args.no_ready)
    a.peer, b.peer = b, a
    print(f"A {a.path}", flush=True)
    print(f"B {b.path}", flush=True)
    fds = {a.master: a, b.master: b}
    while True:
        r, _, _ = select.select(list(fds), [], [], 1.0)
        for fd in r:
            try:
                data = os.read(fd, 4096)
            except OSError:
                data = b""
            if data:
                fds[fd].feed(data)


if __name__ == "__main__":
    main()
