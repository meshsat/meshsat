#!/usr/bin/env bash
# uninstall-kiosk.sh — undo scripts/install-kiosk.sh.
# Removes the autologin drop-in, the Chromium managed policy, and
# the kiosk user's .bash_profile hook. Leaves the packages installed
# (safer — removing cage/chromium might break unrelated setups).

set -euo pipefail

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
  echo "Run as root: sudo bash $0" >&2
  exit 1
fi

KIOSK_USER="${KIOSK_USER:-kiosk}"

echo "Removing autologin drop-in…"
rm -f /etc/systemd/system/getty@tty1.service.d/autologin.conf
rmdir /etc/systemd/system/getty@tty1.service.d 2>/dev/null || true

echo "Removing Chromium managed policy…"
rm -f /etc/chromium/policies/managed/meshsat-kiosk.json

echo "Removing touch-rotation udev rule…"
rm -f /etc/udev/rules.d/99-touch-rotate.rules
udevadm control --reload-rules 2>/dev/null || true

echo "Removing labwc kiosk config…"
if id "$KIOSK_USER" >/dev/null 2>&1; then
  rm -rf "/home/$KIOSK_USER/.config/labwc"
fi

if id "$KIOSK_USER" >/dev/null 2>&1; then
  echo "Stripping .bash_profile hook for $KIOSK_USER…"
  PROFILE="/home/$KIOSK_USER/.bash_profile"
  if [ -f "$PROFILE" ]; then
    sed -i '/# meshsat kiosk — auto-launch on tty1 only/,/^fi$/d' "$PROFILE"
  fi
  rm -f "/home/$KIOSK_USER/.local/bin/meshsat-kiosk-session.sh"
fi

systemctl daemon-reload

echo
echo "Kiosk deprovisioned. Reboot to return the Pi to a normal login."
echo "Packages (labwc / chromium-browser / wlr-randr / swayidle) were"
echo "NOT removed; uninstall manually if you're sure they're not needed:"
echo "    sudo apt-get remove labwc chromium-browser wlr-randr swayidle"
