#!/usr/bin/env bash
# install-blank-cursor.sh — install a fully-transparent xcursor theme
# named "blank". Every cursor variant symlinks to a single 1×1
# transparent xcursor file, so labwc / wlroots draws zero visible
# pixels regardless of pointer activity.
#
# The xcursor binary is emitted by a short Python snippet below so
# we have zero external tool dependencies (xcursorgen was flaky;
# the previous attempt produced a 0-byte file because the wrong
# apt package was pulled in).

set -euo pipefail

KIOSK_USER="${KIOSK_USER:-kiosk}"
THEME_DIR="/usr/share/icons/blank"

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
  echo "Run as root" >&2; exit 1
fi

echo "Building blank cursor theme at $THEME_DIR…"
mkdir -p "$THEME_DIR/cursors"

# Emit a 1×1 transparent xcursor file directly. The binary format:
#   MAGIC "Xcur" | header_sz=16 | version=0x10000 | ntoc=1
#   toc[0]:   type=0xfffd0002 (image) | subtype=32 | position=28
#   image:    header_sz=36 | type=0xfffd0002 | subtype=32 | version=1
#             width=1 | height=1 | xhot=0 | yhot=0 | delay=0
#             pixels (4 bytes ARGB, 0x00000000 = transparent)
# Total: 16 + 12 + 36 + 4 = 68 bytes.
python3 - "$THEME_DIR/cursors/default" <<'PY'
import struct, sys
out = open(sys.argv[1], "wb")
# Xcursor file header
out.write(b"Xcur")
out.write(struct.pack("<III", 16, 0x10000, 1))          # hdrsz, version, ntoc
# TOC entry
out.write(struct.pack("<III", 0xfffd0002, 32, 28))       # type, subtype, pos
# Image chunk (starts at byte 28)
out.write(struct.pack("<IIII", 36, 0xfffd0002, 32, 1))   # hdrsz, type, subtype, version
out.write(struct.pack("<IIIII", 1, 1, 0, 0, 0))          # w, h, xhot, yhot, delay
out.write(b"\x00\x00\x00\x00")                            # 1×1 ARGB transparent
out.close()
PY

# Verify the file is sane (68 bytes, not zero).
sz=$(stat --printf='%s' "$THEME_DIR/cursors/default")
if [ "$sz" -lt 60 ]; then
  echo "ERROR: blank cursor file is only $sz bytes — generation failed" >&2
  exit 1
fi
echo "  → /usr/share/icons/blank/cursors/default ($sz bytes)"

# Symlink every cursor name wlroots/Gtk/Qt might request to the
# single transparent cursor. Missing names fall back to "default"
# automatically, but naming them explicitly avoids wlroots logging
# 'failed to load cursor X' warnings which some compositors trigger
# a fallback theme scan on.
cd "$THEME_DIR/cursors"
for name in left_ptr arrow top_left_arrow pointer hand1 hand2 \
            question_arrow context-menu help cell crosshair \
            text xterm vertical-text ns-resize ew-resize \
            nesw-resize nwse-resize sw-resize se-resize \
            s-resize w-resize n-resize e-resize ne-resize \
            nw-resize row-resize col-resize all-scroll move \
            grab grabbing dnd-move dnd-copy dnd-link dnd-none \
            not-allowed no-drop alias copy link wait progress \
            watch pencil zoom-in zoom-out openhand closedhand \
            plus cross left_side right_side bottom_side top_side \
            top_left_corner top_right_corner bottom_left_corner \
            bottom_right_corner size_ver size_hor size_fdiag size_bdiag \
            split_v split_h fleur up-arrow down-arrow left-arrow right-arrow; do
  ln -sf default "$name"
done

# index.theme so labwc / wlroots can discover the theme name.
cat >"$THEME_DIR/index.theme" <<EOF
[Icon Theme]
Name=blank
Comment=Fully transparent cursor theme for kiosk use
Inherits=
EOF

echo "Blank cursor theme installed ($(ls "$THEME_DIR/cursors" | wc -l) cursor names)."
