#!/usr/bin/env bash
# meshsat-kiosk-session.sh — launched from the kiosk user's
# .bash_profile on tty1 login. Wraps `cage` + `chromium-browser`
# so the kiosk comes up, plus `swayidle` to dim the backlight
# when the operator walks away. [MESHSAT-578 / MESHSAT-580]
#
# Variables the installer writes into this file at provision time:
#   BRIDGE_URL  — the URL to open (default: http://localhost:6050/?shell=kiosk)
# Everything else is hardcoded so an operator pulling up tty2 to
# troubleshoot isn't fighting env-var drift.

set -u

BRIDGE_URL="${BRIDGE_URL:-http://localhost:6050/?shell=kiosk}"

# Only auto-start on tty1 — any other tty (SSH, serial console) gets
# a normal shell so operators can still log in, debug, or recover.
if [ "$(tty)" != "/dev/tty1" ]; then
  return 0 2>/dev/null || exit 0
fi

# Don't re-enter if we're already inside a Wayland session (handles
# the case where the operator runs `exec bash` or similar).
if [ -n "${WAYLAND_DISPLAY:-}" ]; then
  return 0 2>/dev/null || exit 0
fi

# Backlight dim on idle. Uses meshsat's own REST endpoint
# (MESHSAT-556) so we don't need a suid on /sys/class/backlight.
# Thresholds: 2 min → dim to 50 (≈20%), 5 min → off, any input →
# full brightness.
dim_url() { curl -s -X POST "http://localhost:6050/api/system/backlight" \
  -H "Content-Type: application/json" -d "{\"value\":$1}" >/dev/null 2>&1; }

(swayidle -w \
  timeout 120 'curl -s -X POST http://localhost:6050/api/system/backlight -H "Content-Type: application/json" -d "{\"value\":50}" >/dev/null 2>&1' \
  timeout 300 'curl -s -X POST http://localhost:6050/api/system/backlight -H "Content-Type: application/json" -d "{\"value\":0}"  >/dev/null 2>&1' \
  resume 'curl -s -X POST http://localhost:6050/api/system/backlight -H "Content-Type: application/json" -d "{\"value\":255}" >/dev/null 2>&1' \
  &) 2>/dev/null

# Cage is a one-shot Wayland compositor: launches a single app
# fullscreen with no desktop / panel / taskbar. Chromium runs in
# --kiosk mode — no address bar, no nav chrome, F11-fullscreen
# forced. --app=<URL> keeps the window locked to that URL (no tab
# handling either).
#
# -s keeps cage running if Chromium crashes (auto-relaunch via
# systemd would be nicer but cage handles it inline for the MVP).
exec cage -s -- chromium-browser \
  --kiosk \
  --noerrdialogs \
  --disable-infobars \
  --disable-translate \
  --no-first-run \
  --no-default-browser-check \
  --start-fullscreen \
  --app="$BRIDGE_URL"
