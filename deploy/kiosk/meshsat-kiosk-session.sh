#!/usr/bin/env bash
# meshsat-kiosk-session.sh — launched from the kiosk user's
# .bash_profile on tty1 login. Starts swayidle for backlight-on-idle
# and exec's labwc, which autostarts Chromium via
# ~/.config/labwc/autostart. [MESHSAT-578 / MESHSAT-580]
#
# Output rotation + touch rotation are handled elsewhere:
#   • Output: wlr-randr invocation in ~/.config/labwc/autostart
#   • Touch:  /etc/udev/rules.d/99-touch-rotate.rules (libinput layer)
#
# We use labwc (0.7+) rather than cage (0.1.5 in Noble) because
# cage pre-dates the wlr_output_management_v1 protocol and can't
# rotate outputs via wlr-randr. Labwc implements the protocol +
# ships a config file so output + touch rotation are declarative.

set -u

# Only auto-start on tty1. SSH + tty2/3/4 get a normal shell so
# operators can debug or recover the kit in the field.
if [ "$(tty)" != "/dev/tty1" ]; then
  return 0 2>/dev/null || exit 0
fi

# Don't re-enter if we're already in a Wayland session (handles
# `exec bash` and similar edge cases).
if [ -n "${WAYLAND_DISPLAY:-}" ]; then
  return 0 2>/dev/null || exit 0
fi

# Backlight dim on idle via swayidle → /api/system/backlight
# (MESHSAT-556). Thresholds: 2 min → ~20%, 5 min → off, any input
# → full brightness. The sysfs writes live inside the meshsat
# container; we POST through the REST endpoint so no suid / sudoers
# bridge is needed on the kiosk user.
(swayidle -w \
  timeout 120 'curl -s -X POST http://localhost:6050/api/system/backlight -H "Content-Type: application/json" -d "{\"value\":50}"  >/dev/null 2>&1' \
  timeout 300 'curl -s -X POST http://localhost:6050/api/system/backlight -H "Content-Type: application/json" -d "{\"value\":0}"   >/dev/null 2>&1' \
  resume 'curl -s -X POST http://localhost:6050/api/system/backlight -H "Content-Type: application/json" -d "{\"value\":255}" >/dev/null 2>&1' \
  &) 2>/dev/null

# Cursor theme — `blank` is the fully-transparent theme installed
# by deploy/kiosk/install-blank-cursor.sh. Exported here (as well
# as in ~/.config/environment.d/50-cursor.conf) so wlroots picks it
# up during labwc init — environment.d is only read by systemd
# user sessions, and the kiosk's tty1 launch isn't one.
export XCURSOR_THEME=blank
export XCURSOR_SIZE=1

# labwc reads ~/.config/labwc/rc.xml + autostart; autostart launches
# wlr-randr and chromium. If labwc crashes the shell returns and
# agetty re-triggers autologin on tty1 (via `systemctl restart
# getty@tty1.service` from SSH if needed).
exec labwc
