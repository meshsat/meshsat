#!/usr/bin/env bash
# install-kit-network.sh — make a field kit's WiFi stay connected.
#
# Why this exists: the kits sit where several access points broadcast one SSID
# at similar signal. The Pi's Broadcom firmware roams on its own heuristics,
# re-triggers constantly, and the kit leaves the network every minute or two.
# Measured on parallax 2026-09-01: 47 disconnects in 90 minutes before this,
# 1 after. Everything below was hand-applied to both kits until now and was
# not recoverable from git.
#
# What this does (stock Ubuntu 24.04 only — no third-party repos):
#   1. apt-installs `iw` (needed to read or set anything on a wireless
#      interface; it was missing entirely on tesseract).
#   2. /etc/modprobe.d/brcmfmac.conf — roamoff=1, hands roaming decisions to
#      wpa_supplicant instead of the firmware. NEEDS A REBOOT to take effect.
#   3. /etc/modprobe.d/mt76_usb.conf — disable_usb_sg=1 for the MT7612U
#      dongle, mandated by CLAUDE.md but never installed by any script.
#   4. meshsat-wifi-tune.service — power save off on every wireless interface
#      at each boot.
#   5. meshsat-mgmt-keepalive.timer — a 30s outbound ping to the management
#      host, so its neighbour entry for the kit never goes stale. Without it
#      the kit becomes unreachable *inbound* while perfectly online.
#   6. meshsat-p2p-link.service — the kit-to-kit P2P bearer on the MT7612U
#      dongle (IBSS, fixed BSSID). Only enabled when MESHSAT_P2P_ADDR is set,
#      since each kit needs its own overlay address.
#
# What this deliberately does NOT do: touch /etc/netplan. Kit credentials and
# addressing stay exactly as provisioned. Changing the netplan of a kit whose
# only path is that same WiFi is how you strand a sealed box in the field.
#
# Idempotent — safe to re-run.
#
# Usage:
#   sudo bash scripts/install-kit-network.sh
#   sudo MESHSAT_MGMT_HOSTS="192.168.181.111 192.168.181.232" \
#        bash scripts/install-kit-network.sh

set -euo pipefail

# ─── config ─────────────────────────────────────────────────────
MGMT_HOSTS="${MESHSAT_MGMT_HOSTS:-192.168.181.111}"
# Kit-to-kit P2P bearer. Each kit needs its own overlay address, so this is
# opt-in: unset means the bearer is installed but not enabled.
P2P_ADDR="${MESHSAT_P2P_ADDR:-}"
P2P_SSID="${MESHSAT_P2P_SSID:-meshsat-link}"
P2P_FREQ="${MESHSAT_P2P_FREQ:-2412}"
P2P_BSSID="${MESHSAT_P2P_BSSID:-02:12:34:56:78:9a}"
REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
DEPLOY_DIR="$REPO_DIR/deploy/network"

# ─── preflight ──────────────────────────────────────────────────
if [ "${EUID:-$(id -u)}" -ne 0 ]; then
  echo "This script must run as root: sudo bash $0" >&2
  exit 1
fi
for f in brcmfmac.conf mt76_usb.conf meshsat-wifi-tune meshsat-wifi-tune.service \
         meshsat-mgmt-keepalive meshsat-mgmt-keepalive.service meshsat-mgmt-keepalive.timer \
         meshsat-p2p-link meshsat-p2p-link.service; do
  if [ ! -f "$DEPLOY_DIR/$f" ]; then
    echo "Deploy file missing: $DEPLOY_DIR/$f" >&2
    exit 1
  fi
done

echo "══ MeshSat kit network provisioning ══"
echo "Management hosts : $MGMT_HOSTS"
echo

# ─── 1. packages ────────────────────────────────────────────────
echo "[1/6] Installing packages…"
export DEBIAN_FRONTEND=noninteractive
if ! command -v iw >/dev/null 2>&1; then
  apt-get update -q
  apt-get install -y -q --no-install-recommends iw
else
  echo "      iw already present ($(iw --version 2>/dev/null))"
fi

# ─── 2. firmware roaming offload off ────────────────────────────
echo "[2/6] Disabling firmware roaming offload (brcmfmac)…"
install -m 0644 "$DEPLOY_DIR/brcmfmac.conf" /etc/modprobe.d/brcmfmac.conf

# ─── 3. USB dongle prophylactic ─────────────────────────────────
echo "[3/6] Installing mt76_usb options…"
install -m 0644 "$DEPLOY_DIR/mt76_usb.conf" /etc/modprobe.d/mt76_usb.conf

# ─── 4. power save off at every boot ────────────────────────────
echo "[4/6] Installing WiFi tuning unit…"
install -m 0755 "$DEPLOY_DIR/meshsat-wifi-tune" /usr/local/bin/meshsat-wifi-tune
install -m 0644 "$DEPLOY_DIR/meshsat-wifi-tune.service" \
  /etc/systemd/system/meshsat-wifi-tune.service

# supersede the earlier ad-hoc unit if a kit still carries it
if [ -f /etc/systemd/system/wlan0-nopowersave.service ]; then
  systemctl disable --now wlan0-nopowersave >/dev/null 2>&1 || true
  rm -f /etc/systemd/system/wlan0-nopowersave.service
fi

# ─── 5. management keepalive ────────────────────────────────────
echo "[5/6] Installing management keepalive…"
install -m 0755 "$DEPLOY_DIR/meshsat-mgmt-keepalive" /usr/local/bin/meshsat-mgmt-keepalive
install -m 0644 "$DEPLOY_DIR/meshsat-mgmt-keepalive.service" \
  /etc/systemd/system/meshsat-mgmt-keepalive.service
install -m 0644 "$DEPLOY_DIR/meshsat-mgmt-keepalive.timer" \
  /etc/systemd/system/meshsat-mgmt-keepalive.timer
{
  printf 'MESHSAT_MGMT_HOSTS="%s"\n' "$MGMT_HOSTS"
  printf 'MESHSAT_P2P_SSID="%s"\n'   "$P2P_SSID"
  printf 'MESHSAT_P2P_FREQ="%s"\n'   "$P2P_FREQ"
  printf 'MESHSAT_P2P_BSSID="%s"\n'  "$P2P_BSSID"
  printf 'MESHSAT_P2P_ADDR="%s"\n'   "$P2P_ADDR"
} > /etc/default/meshsat-network
chmod 0644 /etc/default/meshsat-network

# ─── 6. kit-to-kit P2P bearer ───────────────────────────────────
echo "[6/6] Installing P2P bearer…"
install -m 0755 "$DEPLOY_DIR/meshsat-p2p-link" /usr/local/bin/meshsat-p2p-link
install -m 0644 "$DEPLOY_DIR/meshsat-p2p-link.service" \
  /etc/systemd/system/meshsat-p2p-link.service

systemctl daemon-reload
systemctl enable --now meshsat-wifi-tune >/dev/null 2>&1
systemctl enable --now meshsat-mgmt-keepalive.timer >/dev/null 2>&1
if [ -n "$P2P_ADDR" ]; then
  systemctl enable --now meshsat-p2p-link >/dev/null 2>&1
else
  echo "      P2P bearer installed but not enabled (set MESHSAT_P2P_ADDR to enable)"
fi

echo
echo "══ Done ══"
echo "  wifi-tune  : $(systemctl is-active meshsat-wifi-tune)"
echo "  keepalive  : $(systemctl is-active meshsat-mgmt-keepalive.timer)"
echo "  p2p bearer : $(systemctl is-active meshsat-p2p-link 2>&1)${P2P_ADDR:+ at $P2P_ADDR}"
echo "  roamoff now: $(cat /sys/module/brcmfmac/parameters/roamoff 2>/dev/null || echo '?')"
echo
echo "  REBOOT REQUIRED for roamoff to take effect."
echo "  After reboot verify:  cat /sys/module/brcmfmac/parameters/roamoff   -> 1"
echo "  And measure:  journalctl -b | grep -c CTRL-EVENT-DISCONNECTED       -> near 0"
