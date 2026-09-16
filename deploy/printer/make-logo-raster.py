#!/usr/bin/env python3
"""Turn the MeshSat logo into a 1-bit raster the receipt printer can print. [MESHSAT-1154]

Run this on a dev machine, NOT on a kit: it needs Pillow, and the kits deliberately have
no pip packages for this service. The output blob is committed next to the service and
loaded at runtime with nothing but stdlib.

    ./make-logo-raster.py <source.png> deploy/printer/meshsat-logo.raster [--width 384]

Source: the brand lockup (dish + wordmark) lives in the meshsat-website repo as a PNG
embedded in `docs/public/logo.svg` — extract the base64 `href` to a .png and feed it in.
It ships as light-coloured artwork on transparency (for dark backgrounds), so its ALPHA
channel is the shape. `--from alpha` (the default) prints that
silhouette, which is what a black-on-white thermal printer wants. `--from luma` is there
for an ordinary dark-on-light image.

Blob format (little-endian): magic "MSR1", uint16 width_px, uint16 height_px, then
ceil(width/8) bytes per row, MSB first, 1 = black dot.
"""

import argparse
import struct
import sys

from PIL import Image


def build(path, width, source, threshold, invert):
    im = Image.open(path).convert("RGBA")
    if source == "alpha":
        mask = im.split()[3]                      # opaque = ink
    else:
        bg = Image.new("RGBA", im.size, (255, 255, 255, 255))
        mask = Image.alpha_composite(bg, im).convert("L").point(lambda p: 255 - p)
    box = mask.getbbox()
    if box:
        mask = mask.crop(box)
    if invert:
        mask = mask.point(lambda p: 255 - p)

    width = min(width, 576)                       # 80 mm heads top out here; 58 mm = 384
    width -= width % 8                            # whole bytes per row
    height = max(1, round(mask.height * width / mask.width))
    mask = mask.resize((width, height), Image.LANCZOS)

    px = mask.load()
    row_bytes = width // 8
    out = bytearray()
    for y in range(height):
        for xb in range(row_bytes):
            byte = 0
            for bit in range(8):
                if px[xb * 8 + bit, y] >= threshold:
                    byte |= 0x80 >> bit
            out.append(byte)
    return width, height, bytes(out)


def preview(width, height, data, cols=100):
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
    ap.add_argument("source")
    ap.add_argument("output")
    ap.add_argument("--width", type=int, default=384, help="dots (58 mm head = 384)")
    ap.add_argument("--from", dest="source_channel", choices=("alpha", "luma"),
                    default="alpha")
    ap.add_argument("--threshold", type=int, default=128)
    ap.add_argument("--invert", action="store_true")
    ap.add_argument("--preview", action="store_true")
    args = ap.parse_args()

    w, h, data = build(args.source, args.width, args.source_channel,
                       args.threshold, args.invert)
    with open(args.output, "wb") as fh:
        fh.write(b"MSR1" + struct.pack("<HH", w, h) + data)
    ink = sum(bin(b).count("1") for b in data)
    print(f"{args.output}: {w}x{h} dots, {len(data)} bytes, {ink * 100 // (w * h)}% ink")
    if args.preview:
        preview(w, h, data)
    return 0


if __name__ == "__main__":
    sys.exit(main())
