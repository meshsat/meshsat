#!/usr/bin/env bash
# =============================================================================
# MeshSat Community Post — kit-side installer [MESHSAT-1733]
# =============================================================================
# Places the host side of the Community Post: the open access point on the
# USB WiFi dongle, DHCP with a catch-all DNS, the post's own nftables table,
# and the systemd units that run them. It installs everything DISABLED and
# starts NOTHING: a kit that gets this is exactly as it was until an operator
# runs scripts/post-up.sh. There is no [Install] section in the units, so
# they cannot be enabled at boot by accident.
#
# What it never touches: /etc/netplan, wlan0 (the kit's only management
# path), the Bridge container, its compose file, /srv/meshsat.
#
# Run as root ON A KIT, from a checkout or an unpacked copy of this repo:
#
#   sudo MESHSAT_POST_COUNTRY=NL MESHSAT_POST_SSID="MeshSat Post" \
#        scripts/install-post.sh
#
# Then, deliberately, when the post is to open:
#   1. MESHSAT_POST_LISTEN=0.0.0.0:8081 (and the other MESHSAT_POST_* values)
#      in the kit's .env, the port published as 127.0.0.1:8081:8081 in BOTH
#      kits' compose files byte for byte, container recreated.
#   2. sudo scripts/post-up.sh
#
# Idempotent. Stock Ubuntu 24.04 packages only (hostapd, dnsmasq, nftables,
# iw); never upgrades anything already there.
# =============================================================================
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: run as root (sudo)" >&2
  exit 1
fi

HERE="$(cd "$(dirname "$0")/.." && pwd)"
SRC="${HERE}/deploy/post"
SHARE=/usr/local/share/meshsat-post
BIN=/usr/local/bin/meshsat-post-net
DEFAULTS=/etc/default/meshsat-post

for f in hostapd.conf dnsmasq.conf nftables.conf meshsat-post-net \
         meshsat-post-net.service meshsat-post-ap.service meshsat-post-dns.service meshsat-post-fw.service; do
  [ -f "${SRC}/${f}" ] || { echo "ERROR: ${SRC}/${f} missing" >&2; exit 1; }
done

echo "== MeshSat Community Post installer =="
echo "   host: $(hostname)"

# --- 1. packages (install only what is missing; never upgrade) ---------------
missing=()
command -v hostapd >/dev/null || missing+=(hostapd)
command -v dnsmasq >/dev/null || missing+=(dnsmasq)
command -v nft     >/dev/null || missing+=(nftables)
command -v iw      >/dev/null || missing+=(iw)
if [ "${#missing[@]}" -gt 0 ]; then
  echo "[1/4] Installing ${missing[*]}"
  export DEBIAN_FRONTEND=noninteractive
  apt-get install -y -q --no-install-recommends "${missing[@]}"
else
  echo "[1/4] hostapd, dnsmasq, nftables, iw already present"
fi
# Ubuntu's packages start hostapd and dnsmasq system-wide on install; the
# post runs its own instances from /run/meshsat-post, so the system ones
# stay off (they would also bind port 53 on every interface).
systemctl disable --now hostapd.service 2>/dev/null || true
systemctl disable --now dnsmasq.service 2>/dev/null || true
systemctl mask hostapd.service 2>/dev/null || true

# --- 2. templates and the interface script ------------------------------------
echo "[2/4] Placing templates in ${SHARE} and ${BIN}"
install -d -m 0755 "${SHARE}"
install -m 0644 "${SRC}/hostapd.conf"  "${SHARE}/hostapd.conf"
install -m 0644 "${SRC}/dnsmasq.conf"  "${SHARE}/dnsmasq.conf"
install -m 0644 "${SRC}/nftables.conf" "${SHARE}/nftables.conf"
install -m 0755 "${SRC}/meshsat-post-net" "${BIN}"
install -d -m 0755 /usr/local/share/doc/meshsat
[ -f "${HERE}/docs/community-post.md" ] && install -m 0644 "${HERE}/docs/community-post.md" /usr/local/share/doc/meshsat/community-post.md

# --- 3. defaults (written once; edit by hand afterwards) ----------------------
if [ ! -f "${DEFAULTS}" ]; then
  echo "[3/4] Writing ${DEFAULTS}"
  cat > "${DEFAULTS}" <<DEF
# MeshSat Community Post, host side. Read by meshsat-post-net. [MESHSAT-1733]
# The USB WiFi dongle; empty = the first wlx* interface. NEVER wlan0.
MESHSAT_POST_IFACE=${MESHSAT_POST_IFACE:-}
MESHSAT_POST_ADDR=${MESHSAT_POST_ADDR:-10.44.0.1/24}
MESHSAT_POST_SSID="${MESHSAT_POST_SSID:-MeshSat Post}"
MESHSAT_POST_CHANNEL=${MESHSAT_POST_CHANNEL:-6}
MESHSAT_POST_COUNTRY=${MESHSAT_POST_COUNTRY:-NL}
# dBm at the radio; the dongle's antennas add about 5 dBi (NL: 20 dBm EIRP).
MESHSAT_POST_TXPOWER=${MESHSAT_POST_TXPOWER:-15}
# Where the Bridge's portal listener is published on the host (compose:
# 127.0.0.1:8081:8081, MESHSAT_POST_LISTEN=0.0.0.0:8081 in the container).
MESHSAT_POST_BACKEND=${MESHSAT_POST_BACKEND:-127.0.0.1:8081}
MESHSAT_POST_DHCP_FROM=${MESHSAT_POST_DHCP_FROM:-10.44.0.10}
MESHSAT_POST_DHCP_TO=${MESHSAT_POST_DHCP_TO:-10.44.0.200}
DEF
  chmod 0644 "${DEFAULTS}"
else
  echo "[3/4] ${DEFAULTS} exists, left as it is"
fi

# --- 4. units, disabled -------------------------------------------------------
echo "[4/4] Placing units (not enabled, not started)"
for u in meshsat-post-net meshsat-post-ap meshsat-post-dns meshsat-post-fw; do
  install -m 0644 "${SRC}/${u}.service" "/etc/systemd/system/${u}.service"
done
systemctl daemon-reload
for u in meshsat-post-net meshsat-post-ap meshsat-post-dns meshsat-post-fw; do
  systemctl disable "${u}.service" 2>/dev/null || true
done

echo
echo "Done. Nothing is running. The post opens only with: sudo ${HERE}/scripts/post-up.sh"
echo "wlan0 and /etc/netplan were not touched."
