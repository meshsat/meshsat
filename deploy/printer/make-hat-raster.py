#!/usr/bin/env python3
"""Draw the MeshSat hat as a 1-bit raster for the milestone slip. [MESHSAT-1201]

Run on a dev machine, NOT on a kit: it needs Pillow. The blob it writes is committed
next to the service and loaded at runtime with nothing but stdlib, exactly like
meshsat-logo.raster.

    ./make-hat-raster.py web/public/meshsat-lockup.png deploy/printer/meshsat-hat.raster

The hat is a real item, not an invention: `meshsat-website/brand/embroidery` specifies
the lockup embroidered on a BLACK hat, 80 x 40 mm, white and orange thread, no black
stitching because the hat is the background. A thermal printer has one ink, so the cap
prints as a solid black silhouette and the lockup is knocked out to bare paper - which
is what white thread on black fabric actually looks like.

Only the cap shape is drawn here. The lockup itself is the approved master, scaled and
used as a mask: the brand guide forbids redrawing the mark or re-typesetting the
wordmark, and that applies just as much at 300 dots wide.

Blob format matches make-logo-raster.py: magic "MSR1", uint16 width, uint16 height,
then ceil(width/8) bytes per row, MSB first, 1 = black dot.
"""

import argparse
import struct
import sys

from PIL import Image, ImageDraw

SS = 4                      # supersample, then downsample for clean curves


def draw_cap(width, height):
    """A cap in PROFILE: crown, headband, button, and the bill projecting to the
    right. Drawn from the side on purpose - seen head on, a cap and a bucket hat
    have the same silhouette, and the first attempt read as a bowler."""
    w, h = width * SS, height * SS
    im = Image.new("L", (w, h), 0)
    d = ImageDraw.Draw(im)
    u = w / 300.0               # one unit = 1/300 of the final width
    BAND = 128                  # the headband line, where the crown stops

    # Crown: the top of a tall ellipse, cut off at the band.
    d.ellipse((44 * u, 18 * u, 252 * u, 238 * u), fill=255)
    d.rectangle((0, BAND * u, w, h), fill=0)
    # Headband, a little wider than the crown so it reads as a separate piece.
    d.rectangle((42 * u, (BAND - 13) * u, 254 * u, BAND * u), fill=255)

    # Bill: an ellipse emerging from the band and curving down to the right.
    bill = Image.new("L", (w, h), 0)
    bd = ImageDraw.Draw(bill)
    bd.ellipse((168 * u, (BAND - 26) * u, 296 * u, (BAND + 14) * u), fill=255)
    bd.rectangle((0, 0, w, (BAND - 13) * u), fill=0)
    im.paste(255, (0, 0), bill)

    # Button on the crown. No panel seam: in profile it would run straight through
    # the wordmark, and a hairline is the first thing a thermal head loses anyway.
    d.ellipse((140 * u, 10 * u, 156 * u, 26 * u), fill=255)
    return im


def knockout_lockup(im, lockup_path, width, height):
    """Punch the approved lockup out of the side panel in bare paper - the white
    thread of the embroidery spec, as one ink can render it."""
    art = Image.open(lockup_path).convert("RGBA").split()[3]     # alpha is the artwork
    box = art.getbbox()
    if box:
        art = art.crop(box)
    u = im.width / 300.0
    target_w = int(154 * u)
    target_h = max(1, round(art.height * target_w / art.width))
    art = art.resize((target_w, target_h), Image.LANCZOS)
    im.paste(0, (int(148 * u) - target_w // 2, int(88 * u) - target_h // 2), art)
    return im


def build(lockup_path, width):
    width -= width % 8
    height = round(width * 168 / 300)
    im = draw_cap(width, height)
    im = knockout_lockup(im, lockup_path, width, height)
    im = im.resize((width, height), Image.LANCZOS)

    px = im.load()
    row_bytes = width // 8
    out = bytearray()
    for y in range(height):
        for xb in range(row_bytes):
            byte = 0
            for bit in range(8):
                if px[xb * 8 + bit, y] >= 128:
                    byte |= 0x80 >> bit
            out.append(byte)
    return width, height, bytes(out)


def preview(width, height, data, cols=96):
    row_bytes = width // 8
    step_x = max(1, width // cols)
    step_y = max(1, step_x * 2)
    for y in range(0, height, step_y):
        line = []
        for x in range(0, width, step_x):
            byte = data[y * row_bytes + (x >> 3)]
            line.append("#" if byte & (0x80 >> (x & 7)) else ".")
        print("".join(line))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("lockup", help="web/public/meshsat-lockup.png")
    ap.add_argument("output")
    ap.add_argument("--width", type=int, default=304, help="dots (58 mm head = 384 max)")
    ap.add_argument("--preview", action="store_true")
    args = ap.parse_args()

    w, h, data = build(args.lockup, args.width)
    with open(args.output, "wb") as fh:
        fh.write(b"MSR1" + struct.pack("<HH", w, h) + data)
    ink = sum(bin(b).count("1") for b in data)
    print(f"{args.output}: {w}x{h} dots, {len(data)} bytes, {ink * 100 // (w * h)}% ink")
    if args.preview:
        preview(w, h, data)
    return 0


if __name__ == "__main__":
    sys.exit(main())
