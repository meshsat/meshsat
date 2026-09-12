#!/usr/bin/env bash
# install-kit-time.sh — make a field kit's clock right before the bridge starts.
#
# Why this exists: a Pi 5 with no RTC backup cell comes up at the epoch, and
# `fixrtc` on the kernel command line then restores the root filesystem's last
# mount time. Each boot re-writes that field with the value it just restored, so
# the clock is frozen at the last mount that happened to be correct — both kits
# came up at 12 Sep 2026 01:10 twice while the real time was 08:42 and 09:19.
# Correcting it is left entirely to chrony, and chrony is a coin flip: one boot
# stepped 17 s in, the next never stepped in 36 minutes and the bridge ran its
# whole life 7h32m in the past. Nothing ordered docker after time sync, and the
# bridge had no clock check of its own. [MESHSAT-1056]
#
# What this installs:
#   1. /usr/local/bin/meshsat-clock-guard — floor check, then chrony, then
#      cellular AT+CCLK?, then GPS RMC. Bounded, always exits 0.
#   2. meshsat-clock-guard.service — Before=docker.service, so the correction
#      lands before the bridge starts instead of racing it.
#   3. /etc/meshsat/clock-guard.env — the plausibility floor (this install date)
#      and the rung switches.
#   4. util-linux-extra, for hwclock (absent on both kits).
#
# What it deliberately does NOT do:
#   - It does not enable chrony-wait.service. That blocks until a source
#     answers, and at a booth with no internet it would hold the bridge down.
#     The guard is bounded and fails open instead.
#   - It does not touch the EEPROM. The RTC cell's trickle charge is a
#     config.txt dtparam and is applied separately with the cell itself.
#   - It does not open any serial port by raw device name. The cellular and GPS
#     rungs match /dev/serial/by-id patterns, so they can never land on the
#     XIAO mesh radio or the PicoAPRS TNC.
#
# Idempotent — safe to re-run.
#
# Usage:
#   sudo bash scripts/install-kit-time.sh
#   sudo CLOCK_GUARD_CELL=0 bash scripts/install-kit-time.sh   # skip the modem rung

set -euo pipefail

# ─── config ─────────────────────────────────────────────────────
CELL="${CLOCK_GUARD_CELL:-1}"
GPS="${CLOCK_GUARD_GPS:-1}"
CHRONY_WAIT="${CLOCK_GUARD_CHRONY_WAIT:-30}"
DEADLINE="${CLOCK_GUARD_DEADLINE:-90}"
DATA_STATE="${CLOCK_GUARD_DATA_STATE:-/srv/meshsat/data/clock-state}"
REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
DEPLOY_DIR="$REPO_DIR/deploy/time"

# ─── preflight ──────────────────────────────────────────────────
if [ "${EUID:-$(id -u)}" -ne 0 ]; then
  echo "This script must run as root: sudo bash $0" >&2
  exit 1
fi
for f in meshsat-clock-guard meshsat-clock-guard.service; do
  if [ ! -f "$DEPLOY_DIR/$f" ]; then
    echo "Deploy file missing: $DEPLOY_DIR/$f" >&2
    exit 1
  fi
done

# The floor is the moment of install. A clock earlier than this is impossible
# on a kit that was provisioned today, so it is a safe "the clock is wrong"
# test that needs no network. Re-running the installer moves it forward.
FLOOR="$(date +%s)"
NOW_HUMAN="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "══ MeshSat kit clock provisioning ══"
echo "Plausibility floor : $FLOOR ($NOW_HUMAN)"
echo "Cellular rung      : $CELL"
echo "GPS rung           : $GPS"
echo

# ─── 1. hwclock ─────────────────────────────────────────────────
echo "[1/5] Ensuring hwclock is present…"
if ! command -v hwclock >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -q
  apt-get install -y -q --no-install-recommends util-linux-extra
else
  echo "      hwclock already present"
fi

# ─── 2. guard script ────────────────────────────────────────────
echo "[2/5] Installing the clock guard…"
install -m 0755 "$DEPLOY_DIR/meshsat-clock-guard" /usr/local/bin/meshsat-clock-guard

# ─── 3. configuration ───────────────────────────────────────────
echo "[3/5] Writing /etc/meshsat/clock-guard.env…"
install -d -m 0755 /etc/meshsat
{
  printf '# Written by scripts/install-kit-time.sh on %s [MESHSAT-1056]\n' "$NOW_HUMAN"
  printf 'CLOCK_GUARD_FLOOR=%s\n'        "$FLOOR"
  printf 'CLOCK_GUARD_CHRONY_WAIT=%s\n'  "$CHRONY_WAIT"
  printf 'CLOCK_GUARD_DEADLINE=%s\n'     "$DEADLINE"
  printf 'CLOCK_GUARD_CELL=%s\n'         "$CELL"
  printf 'CLOCK_GUARD_GPS=%s\n'          "$GPS"
  printf 'CLOCK_GUARD_DATA_STATE=%s\n'   "$DATA_STATE"
} > /etc/meshsat/clock-guard.env
chmod 0644 /etc/meshsat/clock-guard.env

# ─── 4. unit ────────────────────────────────────────────────────
echo "[4/5] Installing and enabling the unit…"
install -m 0644 "$DEPLOY_DIR/meshsat-clock-guard.service" \
  /etc/systemd/system/meshsat-clock-guard.service
systemctl daemon-reload
systemctl enable meshsat-clock-guard.service >/dev/null

# ─── 5. first run ───────────────────────────────────────────────
# Run it now so the state file exists for the bridge that is already up, and so
# the operator sees which rung answers on this kit before trusting it at a boot.
echo "[5/5] Running it once…"
systemctl start meshsat-clock-guard.service || true
echo
echo "── result ──"
cat /run/meshsat/clock-state 2>/dev/null || echo "(no state file written)"
echo
journalctl -u meshsat-clock-guard.service -n 10 --no-pager 2>/dev/null || true

cat <<'NOTE'

Done. What to check before trusting this at the booth:

  systemd-analyze critical-chain docker.service | grep -A2 clock-guard
  systemctl status meshsat-clock-guard --no-pager

The real test is a cold one. Stop docker, power the kit off, wait, press the
button, and confirm `date` is right before the bridge container starts:

  systemctl stop docker.service docker.socket && systemctl poweroff
  # press the button, then:
  journalctl -u meshsat-clock-guard -b ; date ; docker ps

With the RTC cell fitted (Part A of MESHSAT-1056) the floor check should answer
on its own and no rung should be needed at all.
NOTE
