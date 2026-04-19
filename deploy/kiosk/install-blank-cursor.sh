#!/usr/bin/env bash
# install-blank-cursor.sh — build + install a fully-transparent
# X cursor theme named "blank". Called from install-kiosk.sh. Every
# cursor variant the compositor might ask for (default / left_ptr /
# arrow / pointer / xterm / hand2 / grab / move / resize / …) is a
# symlink to the same 1×1 transparent pixel, so the Wayland cursor
# literally renders nothing regardless of pointer activity.
#
# Why this is needed: CSS `cursor: none` only hides Chromium's own
# cursor inside the browser window. labwc still draws a compositor
# cursor on top whenever a pointer device is present (the AIOC HID
# on the kits registers as one). `cursorSize=1` is a 1-pixel
# fallback that's still faintly visible at Touch Display 2 DPI.
# A blank theme draws 0 visible pixels, period.

set -euo pipefail

KIOSK_USER="${KIOSK_USER:-kiosk}"
THEME_DIR="/usr/share/icons/blank"

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
  echo "Run as root" >&2; exit 1
fi

# Need xcursorgen to compile the transparent cursor from a PNG.
if ! command -v xcursorgen >/dev/null 2>&1; then
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq x11-apps >/dev/null
fi

echo "Building blank cursor theme at $THEME_DIR…"
mkdir -p "$THEME_DIR/cursors"

# 1×1 transparent PNG as base64 (smallest valid PNG of a single
# fully-transparent pixel — 67 bytes).
tmp=$(mktemp -d)
base64 -d >"$tmp/t.png" <<'EOF'
iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNgYGBgAAAABQABh6FO1AAAAABJRU5ErkJggg==
EOF

# xcursorgen config — one frame of the transparent PNG at nominal
# size 32 with hotspot at 0,0.
cat >"$tmp/c.in" <<EOF
32 0 0 $tmp/t.png
EOF

xcursorgen "$tmp/c.in" "$THEME_DIR/cursors/default" 2>/dev/null

# Symlink every cursor name the compositor might request to the
# single transparent cursor file. List adapted from xcursor-themes
# (Adwaita + common apps).
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

rm -rf "$tmp"
echo "Blank cursor theme installed."
