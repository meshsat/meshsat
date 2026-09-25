#!/usr/bin/env python3
"""A software KISS TNC pair for tests: two pseudo-terminals that behave like
two TNCs on the same channel.

Each side swallows KISS command frames (TXDELAY, P, SLOTTIME, TXTAIL,
FULLDUPLEX, READY, SETHARDWARE, RETURN) the way a real TNC consumes them,
counts them, and relays every data frame (command 0x00) to the other side
byte for byte, so an rnsd KISSInterface on side A and the MeshSat bridge
on side B exchange packets as if over the air. Optionally answers each
data frame with a READY (0x0F) frame like a flow-controlled TNC.

Usage: fake_kiss_tnc.py [--ready]
Prints "A /dev/pts/N" and "B /dev/pts/M", then one line "PARAM <side> <cmd> <hex>"
per command frame consumed; runs until killed. [MESHSAT-1350]
"""
import argparse, os, pty, select, sys, threading, tty

FEND, FESC, TFEND, TFESC = 0xC0, 0xDB, 0xDC, 0xDD


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


class Side:
    def __init__(self, name, ready):
        self.name = name
        self.ready = ready
        self.master, slave = pty.openpty()
        tty.setraw(self.master)
        tty.setraw(slave)
        self.path = os.ttyname(slave)
        self.peer = None
        self.buf = bytearray()
        self.in_frame = False
        self.lock = threading.Lock()

    def write(self, data):
        with self.lock:
            os.write(self.master, data)

    def feed(self, data):
        for c in data:
            if c == FEND:
                if self.in_frame and self.buf:
                    self.frame(bytes(self.buf))
                self.buf = bytearray()
                self.in_frame = True
            elif self.in_frame:
                self.buf.append(c)

    def frame(self, raw):
        cmd = raw[0] & 0x0F
        payload = unescape(raw[1:])
        if cmd == 0x00:
            self.peer.write(bytes([FEND, 0x00]) + escape(payload) + bytes([FEND]))
            if self.ready:
                self.write(bytes([FEND, 0x0F, 0x01, FEND]))
        else:
            sys.stdout.write("PARAM %s %02x %s\n" % (self.name, raw[0], payload.hex()))
            sys.stdout.flush()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--ready", action="store_true")
    a = ap.parse_args()
    A, B = Side("A", a.ready), Side("B", a.ready)
    A.peer, B.peer = B, A
    print("A", A.path)
    print("B", B.path)
    sys.stdout.flush()
    fds = {A.master: A, B.master: B}
    while True:
        r, _, _ = select.select(list(fds), [], [])
        for fd in r:
            try:
                data = os.read(fd, 4096)
            except OSError:
                data = b""
            if not data:
                continue
            fds[fd].feed(data)


if __name__ == "__main__":
    main()
