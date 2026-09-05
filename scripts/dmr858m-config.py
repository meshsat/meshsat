#!/usr/bin/env python3
"""Bench configurator for the NiceRF DMR858M walkie-talkie module over its 57600 8N1 UART.

Protocol (NiceRF DMR858M datasheet V1.2, and the open-source epiHATR/DMR858M-cpp library):
  frame = 0x68, CMD, R/W (0 read, 1 write, 2 report), S/R (1 request; reply 0 ok, 1 busy,
  2 channel error, 7 killed, 9 checksum error), CKSUM(2), LEN(2, big-endian), DATA, 0x10.
  CKSUM = ones' complement of the folded 16-bit big-endian word sum over the WHOLE frame
  with the two checksum bytes zeroed. 0x0000 is accepted by the module as "no checksum".
  Frequency (0x0D) payload = RX Hz uint32 + TX Hz uint32, little-endian in the C++ library;
  the datasheet's note 5 says big-endian, so --endian be exists and the readback tells.

Typical bench use, module on the kit's USB-C or on a 3.3 V USB-UART dongle (TXD 18, RXD 19):
  dmr858m-config.py --port /dev/ttyUSB0 firmware
  dmr858m-config.py --port /dev/ttyUSB0 aprs            # channel 8 to 144.800 MHz, analogue, CTCSS off, low power
  dmr858m-config.py --port /dev/ttyUSB0 freq            # read back
  dmr858m-config.py --port /dev/ttyUSB0 rssi
Not yet run against hardware (written 5 Sep 2026, modules arrive 7 Sep). Bench tool, not part of the bridge.
"""
import argparse, os, struct, sys, time

HEAD, TAIL = 0x68, 0x10
CMD = dict(channel=0x01, volume=0x02, status=0x04, rssi=0x05, mic_gain=0x0B, power_save=0x0C,
           freq=0x0D, squelch=0x12, ctcss_mode=0x13, ctcss=0x14, monitor=0x15, power=0x17,
           firmware=0x25, local_id=0x24, channel_config=0x1D)
SR_TEXT = {0: 'ok', 1: 'busy or failed', 2: 'no such channel or channel error', 7: 'module killed', 9: 'checksum error'}

def checksum(buf: bytes) -> int:
    total = 0
    for i in range(0, len(buf) - 1, 2):
        total += (buf[i] << 8) | buf[i + 1]
    if len(buf) % 2:
        total += buf[-1] << 8
    while total >> 16:
        total = (total & 0xFFFF) + (total >> 16)
    return (~total) & 0xFFFF

def build(cmd: int, rw: int, payload: bytes = b'', sr: int = 1) -> bytes:
    frame = bytearray([HEAD, cmd, rw, sr, 0, 0, len(payload) >> 8, len(payload) & 0xFF]) + payload + bytes([TAIL])
    ck = checksum(bytes(frame)); frame[4], frame[5] = ck >> 8, ck & 0xFF
    return bytes(frame)

def parse(raw: bytes):
    if len(raw) < 9 or raw[0] != HEAD: return None
    n = (raw[6] << 8) | raw[7]
    if len(raw) < 9 + n or raw[8 + n] != TAIL: return None
    copy = bytearray(raw[:9 + n]); given = (copy[4] << 8) | copy[5]; copy[4] = copy[5] = 0
    if given not in (0, checksum(bytes(copy))): return None
    return dict(cmd=raw[1], rw=raw[2], sr=raw[3], data=bytes(raw[8:8 + n]))

class Port:
    def __init__(self, dev, baud=57600):
        try:
            import serial
            self.s = serial.Serial(dev, baud, timeout=0.05)
            self.read = lambda n: self.s.read(n); self.write = self.s.write
        except ImportError:
            import termios, tty
            fd = os.open(dev, os.O_RDWR | os.O_NOCTTY | os.O_NONBLOCK)
            tty.setraw(fd); attr = termios.tcgetattr(fd); attr[4] = attr[5] = termios.B57600
            attr[2] = (attr[2] & ~termios.PARENB & ~termios.CSTOPB & ~termios.CSIZE) | termios.CS8 | termios.CLOCAL | termios.CREAD
            termios.tcsetattr(fd, termios.TCSANOW, attr); self.fd = fd
            self.read = lambda n: (lambda b: b if b else b'')(_nb_read(fd, n)); self.write = lambda b: os.write(fd, b)

def _nb_read(fd, n):
    try: return os.read(fd, n)
    except BlockingIOError: return b''

def transact(port, cmd, rw, payload=b'', timeout=1.0, verbose=False):
    tx = build(cmd, rw, payload)
    if verbose: print('>>', tx.hex(' '))
    port.write(tx)
    buf = b''; t0 = time.time()
    while time.time() - t0 < timeout:
        buf += port.read(64)
        i = buf.find(bytes([HEAD]))
        if i >= 0 and len(buf) >= i + 9:
            n = (buf[i + 6] << 8) | buf[i + 7]
            if len(buf) >= i + 9 + n:
                fr = parse(buf[i:i + 9 + n])
                if verbose: print('<<', buf[i:i + 9 + n].hex(' '))
                return fr
        time.sleep(0.01)
    return None

def show(fr):
    if fr is None: print('no reply (check port, baud 57600, TXD 18 to RXD of the adapter, module powered, DIP on NORMAL)'); return
    print(f"cmd 0x{fr['cmd']:02X} rw {fr['rw']} status {fr['sr']} ({SR_TEXT.get(fr['sr'], '?')}) data {fr['data'].hex(' ') or '-'}")

def u32(v, endian): return struct.pack('<I' if endian == 'le' else '>I', v)
def r32(b, endian): return struct.unpack('<I' if endian == 'le' else '>I', b)[0]

def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--port', default='/dev/ttyUSB0'); ap.add_argument('--endian', choices=['le', 'be'], default='le')
    ap.add_argument('-v', '--verbose', action='store_true'); ap.add_argument('--dry-run', action='store_true')
    sub = ap.add_subparsers(dest='op', required=True)
    sub.add_parser('firmware'); sub.add_parser('status'); sub.add_parser('rssi'); sub.add_parser('freq'); sub.add_parser('selftest')
    p = sub.add_parser('channel'); p.add_argument('n', type=int)
    p = sub.add_parser('setfreq'); p.add_argument('rx_hz', type=int); p.add_argument('tx_hz', type=int, nargs='?')
    p = sub.add_parser('power'); p.add_argument('level', choices=['low', 'high'])
    p = sub.add_parser('squelch'); p.add_argument('level', type=int)
    p = sub.add_parser('ctcss'); p.add_argument('code', type=int, help='0 = off')
    p = sub.add_parser('raw'); p.add_argument('cmd', type=lambda x: int(x, 0)); p.add_argument('rw', type=int); p.add_argument('hexdata', nargs='?', default='')
    p = sub.add_parser('aprs'); p.add_argument('--channel', type=int, default=8); p.add_argument('--hz', type=int, default=144800000)
    a = ap.parse_args()

    if a.op == 'selftest':
        f = build(CMD['freq'], 1, u32(144800000, 'le') * 2); assert parse(f)['data'] == u32(144800000, 'le') * 2
        g = build(CMD['firmware'], 0); assert parse(g)['cmd'] == CMD['firmware']; print('frame build/parse ok:', f.hex(' ')); return
    if a.dry_run:
        class Dummy:
            def write(self, b): print('>>', b.hex(' '))
            def read(self, n): return b''
        port = Dummy()
    else:
        port = Port(a.port)
    T = lambda cmd, rw, payload=b'': transact(port, cmd, rw, payload, verbose=a.verbose)

    if a.op == 'firmware': fr = T(CMD['firmware'], 0); show(fr); fr and print('version:', fr['data'].decode('ascii', 'replace'))
    elif a.op == 'status': show(T(CMD['status'], 0))
    elif a.op == 'rssi': show(T(CMD['rssi'], 0))
    elif a.op == 'freq':
        fr = T(CMD['freq'], 0); show(fr)
        if fr and len(fr['data']) >= 8:
            for e in ('le', 'be'): print(f"as {e}: rx {r32(fr['data'][:4], e)} Hz, tx {r32(fr['data'][4:8], e)} Hz")
    elif a.op == 'channel': show(T(CMD['channel'], 1, bytes([a.n])))
    elif a.op == 'setfreq':
        tx = a.tx_hz if a.tx_hz is not None else a.rx_hz
        show(T(CMD['freq'], 1, u32(a.rx_hz, a.endian) + u32(tx, a.endian)))
    elif a.op == 'power': show(T(CMD['power'], 1, bytes([1 if a.level == 'high' else 0])))
    elif a.op == 'squelch': show(T(CMD['squelch'], 1, bytes([a.level])))
    elif a.op == 'ctcss':
        show(T(CMD['ctcss_mode'], 1, bytes([0 if a.code == 0 else 1]))); show(T(CMD['ctcss'], 1, struct.pack('<H' if a.endian == 'le' else '>H', a.code)))
    elif a.op == 'raw': show(T(a.cmd, a.rw, bytes.fromhex(a.hexdata)))
    elif a.op == 'aprs':
        print(f'channel {a.channel} (analogue channels are 8 to 15), {a.hz} Hz simplex, CTCSS off, squelch 1, low power')
        show(T(CMD['channel'], 1, bytes([a.channel])))
        show(T(CMD['freq'], 1, u32(a.hz, a.endian) * 2))
        show(T(CMD['ctcss_mode'], 1, bytes([0]))); show(T(CMD['ctcss'], 1, bytes([0, 0])))
        show(T(CMD['squelch'], 1, bytes([1]))); show(T(CMD['power'], 1, bytes([0])))
        fr = T(CMD['freq'], 0); show(fr)
        if fr and len(fr['data']) >= 8:
            got = r32(fr['data'][:4], a.endian)
            print('readback', got, 'Hz:', 'MATCH' if got == a.hz else 'MISMATCH, try --endian ' + ('be' if a.endian == 'le' else 'le'))

if __name__ == '__main__':
    main()
