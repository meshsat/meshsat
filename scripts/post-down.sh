#!/usr/bin/env bash
# Close the Community Post on this kit: the four units, in reverse order.
# The dongle is left down, as it was. wlan0 is not touched. [MESHSAT-1733]
set -euo pipefail
[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo)" >&2; exit 1; }
systemctl stop meshsat-post-ap.service 2>/dev/null || true
systemctl stop meshsat-post-dns.service 2>/dev/null || true
systemctl stop meshsat-post-fw.service 2>/dev/null || true
systemctl stop meshsat-post-net.service 2>/dev/null || true
# A table left behind by a crash is removed too.
nft delete table inet meshsat_post 2>/dev/null || true
echo "The post is closed."
