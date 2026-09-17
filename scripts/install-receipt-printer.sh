#!/usr/bin/env bash
# =============================================================================
# MeshSat booth receipt printer — kit-side installer [MESHSAT-1154]
# =============================================================================
# Installs the host service that prints one slip for every message arriving on
# THIS kit: it finds the printer, connects over Bluetooth, prints, disconnects.
# The link is never held, so both kits share one printer by taking turns.
#
# Run as root ON A KIT, from a checkout or an unpacked copy of this repo:
#
#   sudo MESHSAT_PRINTER_MAC=AA:BB:CC:DD:EE:FF \
#        MESHSAT_PRINTER_CALLSIGN=MSTSRT-10 \
#        scripts/install-receipt-printer.sh
#
# Idempotent. Touches nothing the bridge owns: no container, no netplan, no
# compose file, no /srv/meshsat. Stdlib Python only — no pip, no PyBluez.
# =============================================================================
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: run as root (sudo)" >&2
  exit 1
fi

HERE="$(cd "$(dirname "$0")/.." && pwd)"
SRC="${HERE}/deploy/printer"
BIN=/usr/local/bin/meshsat-receipt-printer
UNIT=/etc/systemd/system/meshsat-receipt-printer.service
DEFAULTS=/etc/default/meshsat-receipt-printer

PRINTER_MAC="${MESHSAT_PRINTER_MAC:-}"
PRINTER_NAME="${MESHSAT_PRINTER_NAME:-BlueTooth Printer}"
PRINTER_CALLSIGN="${MESHSAT_PRINTER_CALLSIGN:-$(hostname)}"
PRINTER_QR="${MESHSAT_PRINTER_QR:-https://meshsat.net}"

echo "== MeshSat receipt printer installer =="
echo "   host:     $(hostname)"
echo "   printer:  ${PRINTER_MAC:-<discover by name: ${PRINTER_NAME}>}"
echo "   callsign: ${PRINTER_CALLSIGN}"

# --- 1. Bluetooth stack ------------------------------------------------------
# bluez is already on the kits (bluetoothctl, rfcomm, sdptool, hcitool present
# 2026-09-16). Never apt-install here: a kit's Chromium snap and kernel are
# pinned and a casual apt run is how a field kit loses its display.
for tool in hcitool bluetoothctl; do
  command -v "$tool" >/dev/null || { echo "ERROR: $tool missing — install bluez by hand" >&2; exit 1; }
done
systemctl is-active --quiet bluetooth || systemctl start bluetooth
hciconfig hci0 up 2>/dev/null || true

python3 - <<'PY' || { echo "ERROR: this Python has no RFCOMM socket support" >&2; exit 1; }
import socket, sys
sys.exit(0 if hasattr(socket, "AF_BLUETOOTH") and hasattr(socket, "BTPROTO_RFCOMM") else 1)
PY

# --- 2. Service and logo -----------------------------------------------------
install -m 0755 "${SRC}/meshsat-receipt-printer" "$BIN"
install -m 0644 "${SRC}/meshsat-receipt-printer.service" "$UNIT"
# The logo is a pre-rendered 1-bit raster (deploy/printer/make-logo-raster.py on a dev
# machine): the kits need no image library to print it, and the slip falls back to the
# name in text if this file is ever missing.
if [ -f "${SRC}/meshsat-logo.raster" ]; then
  install -d -m 0755 /usr/local/share/meshsat
  install -m 0644 "${SRC}/meshsat-logo.raster" /usr/local/share/meshsat/meshsat-logo.raster
  echo "   installed logo raster"
fi
# The hat, printed on the milestone slip only (make-hat-raster.py on a dev box).
if [ -f "${SRC}/meshsat-hat.raster" ]; then
  install -d -m 0755 /usr/local/share/meshsat
  install -m 0644 "${SRC}/meshsat-hat.raster" /usr/local/share/meshsat/meshsat-hat.raster
  echo "   installed hat raster"
fi
# Where the slip counter lives. Created here so the first slip does not have to.
install -d -m 0755 /var/lib/meshsat

# --- 3. Per-kit configuration ------------------------------------------------
# Written once, then left alone: the MAC and callsign differ per kit and an
# upgrade must not clobber a hand-tuned file.
if [ ! -f "$DEFAULTS" ]; then
  cat > "$DEFAULTS" <<EOF
# MeshSat booth receipt printer [MESHSAT-1154]
# Empty MAC = discover by name on every failure.
MESHSAT_PRINTER_MAC=${PRINTER_MAC}
MESHSAT_PRINTER_NAME=${PRINTER_NAME}
MESHSAT_PRINTER_CHANNEL=0
MESHSAT_PRINTER_CALLSIGN=${PRINTER_CALLSIGN}
MESHSAT_PRINTER_QR=${PRINTER_QR}
MESHSAT_BRIDGE_URL=http://127.0.0.1:6050
MESHSAT_PRINTER_WIDTH=32
# Contention with the other kit is normal: it holds the printer's single RFCOMM
# slot while it prints. These control how long we keep trying.
MESHSAT_PRINTER_ATTEMPTS=6
MESHSAT_PRINTER_BACKOFF=2
MESHSAT_PRINTER_BACKOFF_MAX=20
EOF
  chmod 0644 "$DEFAULTS"
  echo "   wrote ${DEFAULTS}"
else
  echo "   kept existing ${DEFAULTS} (edit by hand to change the MAC or callsign)"
fi

# --- 4. Enable ---------------------------------------------------------------
systemctl daemon-reload
systemctl enable --now meshsat-receipt-printer.service
sleep 2
systemctl --no-pager --lines=5 status meshsat-receipt-printer.service || true

cat <<EOF

Installed. Next:
  Print a test slip:   sudo systemctl stop meshsat-receipt-printer \\
                       && sudo -E ${BIN} --self-test ; sudo systemctl start meshsat-receipt-printer
  Watch it work:       journalctl -fu meshsat-receipt-printer
  Config:              ${DEFAULTS}

The printer must be powered on. It is shared: each kit connects only for the
duration of one slip, so a busy printer shows up as a retry in the log, not a
loss.
EOF
