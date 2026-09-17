#!/usr/bin/env python3
"""Render what a slip will look like on paper, without using paper. [MESHSAT-1201]

Run on a dev machine, NOT on a kit: it needs Pillow and qrcode. It parses the real
ESC/POS byte stream the service produces, so it shows the actual slip rather than a
drawing of one - if the bytes are wrong, the preview is wrong in the same way.

    ./preview-slip.py out.png                     # an ordinary slip
    ./preview-slip.py out.png --serial 666        # the milestone slip
    ./preview-slip.py out.png --text "Hallo uit Amsterdam"

Understands the subset the service emits: ESC @, ESC d, ESC a, ESC E, GS !, GS v 0
(raster) and the GS ( k QR sequence. Anything else is skipped, which is the same thing
a cheap printer does.

The paper is 384 dots wide (58 mm at 203 dpi) and the preview is drawn at that width,
then scaled up, so what looks thin here prints thin.
"""

import argparse
import importlib.machinery
import importlib.util
import os
import sys

import qrcode
from PIL import Image, ImageDraw, ImageFont

PAPER = 384
FONT_DIR = "/usr/share/fonts/truetype/dejavu"
# 12.0 px per character at size 20 is exactly 32 columns across 384 dots, which is the
# service's MESHSAT_PRINTER_WIDTH. Keep them in step or the preview lies about wrapping.
FONT_SIZE = 20
CELL_W = 12.0
LINE_H = 23


def load_service(path):
    loader = importlib.machinery.SourceFileLoader("rp", path)
    spec = importlib.util.spec_from_loader("rp", loader)
    mod = importlib.util.module_from_spec(spec)
    loader.exec_module(mod)
    return mod


def qr_image(payload, module):
    q = qrcode.QRCode(version=None, error_correction=qrcode.constants.ERROR_CORRECT_M,
                      box_size=1, border=0)
    q.add_data(payload)
    q.make(fit=True)
    img = q.make_image(fill_color="black", back_color="white").convert("L")
    return img.resize((img.width * module, img.height * module), Image.NEAREST)


def render(payload, height=4000):
    im = Image.new("L", (PAPER, height), 255)
    d = ImageDraw.Draw(im)
    fonts = {
        (0, 1): ImageFont.truetype(f"{FONT_DIR}/DejaVuSansMono.ttf", FONT_SIZE),
        (1, 1): ImageFont.truetype(f"{FONT_DIR}/DejaVuSansMono-Bold.ttf", FONT_SIZE),
        (0, 2): ImageFont.truetype(f"{FONT_DIR}/DejaVuSansMono.ttf", FONT_SIZE * 2),
        (1, 2): ImageFont.truetype(f"{FONT_DIR}/DejaVuSansMono-Bold.ttf", FONT_SIZE * 2),
    }
    align, bold, scale, y = 0, 0, 1, 0
    qr_payload, qr_module = "", 6
    line = bytearray()

    def flush():
        nonlocal y, line
        text = line.decode("ascii", "replace")
        line = bytearray()
        f = fonts[(1 if bold else 0, scale)]
        w = len(text) * CELL_W * scale
        x = {0: 0, 1: (PAPER - w) / 2, 2: PAPER - w}.get(align, 0)
        if text.strip():
            d.text((x, y), text, font=f, fill=0)
        y += LINE_H * scale

    i, n = 0, len(payload)
    while i < n:
        c = payload[i]
        if c == 0x1B:                                   # ESC
            k = payload[i + 1]
            if k == 0x40:                               # @  init
                align, bold, scale, i = 0, 0, 1, i + 2
            elif k == 0x64:                             # d  feed n lines
                y += LINE_H * payload[i + 2]
                i += 3
            elif k == 0x61:                             # a  align
                align, i = payload[i + 2], i + 3
            elif k == 0x45:                             # E  bold
                bold, i = payload[i + 2], i + 3
            else:
                i += 2
        elif c == 0x1D:                                 # GS
            k = payload[i + 1]
            if k == 0x21:                               # !  character size
                scale, i = 2 if payload[i + 2] else 1, i + 3
            elif k == 0x76 and payload[i + 2] == 0x30:  # v 0  raster
                xl, xh = payload[i + 4], payload[i + 5]
                yl, yh = payload[i + 6], payload[i + 7]
                row_bytes, rows = xl + (xh << 8), yl + (yh << 8)
                blob = payload[i + 8:i + 8 + row_bytes * rows]
                # A set bit is a black dot, and ImageDraw.bitmap paints where the
                # mask is non-zero, so the blob IS the mask: inverting it here
                # would print the negative of the artwork.
                bmp = Image.frombytes("1", (row_bytes * 8, rows), bytes(blob))
                d.bitmap(((PAPER - bmp.width) // 2 if align == 1 else 0, y), bmp, fill=0)
                y += rows
                i += 8 + row_bytes * rows
            elif k == 0x28 and payload[i + 2] == 0x6B:  # ( k  QR
                size = payload[i + 3] + (payload[i + 4] << 8)
                body = payload[i + 5:i + 5 + size]
                fn = body[1] if len(body) > 1 else 0
                if fn == 0x43:                          # C  module size
                    qr_module = body[2]
                elif fn == 0x50:                        # P  store payload
                    qr_payload = bytes(body[3:]).decode("ascii", "replace")
                elif fn == 0x51 and qr_payload:         # Q  print
                    img = qr_image(qr_payload, qr_module)
                    im.paste(img, ((PAPER - img.width) // 2, y))
                    y += img.height
                i += 5 + size
            else:
                i += 3
        elif c == 0x0A:
            flush()
            i += 1
        else:
            line.append(c)
            i += 1
    if line:
        flush()
    return im.crop((0, 0, PAPER, min(height, int(y) + 8)))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("output")
    ap.add_argument("--serial", type=int, default=0)
    ap.add_argument("--text", default="Hallo uit Amsterdam! Groeten van stand S27.")
    ap.add_argument("--callsign", default="MSTSRT-10")
    ap.add_argument("--bearer", default="Meshtastic")
    ap.add_argument("--sender", default="!de11f199")
    ap.add_argument("--when", default="2026-09-22 10:14:07")
    ap.add_argument("--scale", type=int, default=2, help="upscale the preview")
    args = ap.parse_args()

    here = os.path.dirname(os.path.abspath(__file__))
    rp = load_service(os.path.join(here, "meshsat-receipt-printer"))

    class Args:
        mock_file = None
    cfg = rp.load_config(Args())
    cfg["callsign"] = args.callsign
    # Preview the blobs that are committed here, not whatever a kit happens to hold.
    cfg["logo"] = rp.load_logo(os.path.join(here, "meshsat-logo.raster"))
    cfg["hat"] = rp.load_logo(os.path.join(here, "meshsat-hat.raster"))
    msg = {"text": args.text, "from": args.sender, "bearer": args.bearer, "when": args.when}

    im = render(rp.build_slip(msg, cfg, args.serial))
    if args.scale > 1:
        im = im.resize((im.width * args.scale, im.height * args.scale), Image.LANCZOS)
    # A grey surround so the edge of the paper is visible in the preview.
    out = Image.new("RGB", (im.width + 48, im.height + 48), (32, 32, 36))
    out.paste(im.convert("RGB"), (24, 24))
    out.save(args.output)
    print(f"{args.output}: {out.width}x{out.height}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
