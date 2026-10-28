#!/usr/bin/env bash
# Open the Community Post on this kit: the interface, the access point, DHCP
# with DNS, and the firewall table, in that order. [MESHSAT-1733]
#
# Refuses when the Bridge's portal listener is not answering on the host
# (MESHSAT_POST_LISTEN unset, or the port not published), because an open
# WiFi whose portal does not load is worse than no post.
set -euo pipefail
[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo)" >&2; exit 1; }
[ -r /etc/default/meshsat-post ] && . /etc/default/meshsat-post
BACKEND="${MESHSAT_POST_BACKEND:-127.0.0.1:8081}"

if ! ls /sys/class/net | grep -q '^wlx'; then
  echo "no USB WiFi dongle (wlx*) present; the post needs it" >&2
  exit 1
fi
if systemctl is-active --quiet meshsat-p2p-link.service; then
  echo "meshsat-p2p-link is active on the same dongle; stop it first (systemctl stop meshsat-p2p-link)" >&2
  exit 1
fi
if ! curl -fsS -m 3 -o /dev/null "http://${BACKEND}/.well-known/captive-portal"; then
  echo "the Bridge's portal listener does not answer on ${BACKEND}:" >&2
  echo "  set MESHSAT_POST_LISTEN=0.0.0.0:8081 in the kit's .env, publish 127.0.0.1:8081:8081 in the compose file, recreate the container" >&2
  exit 1
fi

systemctl start meshsat-post-net.service
systemctl start meshsat-post-fw.service
systemctl start meshsat-post-dns.service
systemctl start meshsat-post-ap.service
sleep 2
systemctl --no-pager --lines=3 status meshsat-post-net meshsat-post-fw meshsat-post-dns meshsat-post-ap || true
echo
echo "The post is open. Check from a phone: join the SSID, the portal should open by itself."
echo "From this kit: curl -s http://${BACKEND}/ | head -3 ; nft list table inet meshsat_post"
echo "Close it with: sudo $(dirname "$0")/post-down.sh"
