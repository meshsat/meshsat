#!/usr/bin/env bash
# meshsat-ble-addr-pin.sh — pin the BLE advertising identity to the
# dual-mode controller's **public** BD_ADDR (stable across reboots)
# rather than letting BlueZ pick a rotating Non-Resolvable Private
# Address (NRPA). Runs once at boot, BEFORE bluetooth.service, via
# meshsat-ble-addr-pin.service. [MESHSAT-678]
#
# Why this runs on the host, not in the meshsat container:
#   - btmgmt's HCI_CHANNEL_CONTROL socket returns an empty controller
#     list when opened from inside the `network_mode: host` +
#     `privileged: true` meshsat container on Pi 5 brcmfmac (field-
#     verified 2026-04-23 on both tesseract + parallax). The in-
#     container `ensureStableAdvAddress` helper emitted the
#     `btmgmt info hci0: no addr line in output` warning and then
#     no-op'd. MESHSAT-677's approach was wrong — hence this rewrite.
#   - Even if the mgmt socket worked from the container, the Pi 5
#     brcmfmac is dual-mode with a valid public address: per
#     mgmt-api.txt, "if the controller is dual-mode and has a public
#     address, using a static address is not required ... the Static
#     Address command will be persisted but will not replace the
#     public address for advertising." So the fix is not to set a
#     static-random MAC at all — it is to force BlueZ to USE the
#     public (identity) address for advertising, which is what
#     Connectable+Discoverable together accomplish.
#
# Why bluetoothctl is not used:
#   - bluetoothctl talks to bluetoothd over D-Bus. We run Before=
#     bluetooth.service so bluetoothd is not yet up. btmgmt talks
#     directly to the kernel mgmt socket (AF_BLUETOOTH,
#     HCI_CHANNEL_CONTROL) and works pre-bluetoothd. BlueZ 5.55+
#     preserves mgmt state across (re)starts of bluetoothd, so
#     setting connectable+discov here is durable.

set -euo pipefail

ADAPTER="${ADAPTER:-hci0}"
SYS_PATH="/sys/class/bluetooth/${ADAPTER}"

# Wait up to 10 s for the adapter to appear. On cold boot the brcmfmac
# + BT firmware loader can take 3-6 s before hci0 is registered; on
# warm reboot it is already present.
for _ in $(seq 1 20); do
  if [ -e "$SYS_PATH" ]; then
    break
  fi
  sleep 0.5
done

if [ ! -e "$SYS_PATH" ]; then
  echo "meshsat-ble-addr-pin: $SYS_PATH not found after 10s — skipping" >&2
  # Exit 0: missing BT hardware is not an error for kits without a
  # BLE radio (e.g. mule01 post-decomm). The unit is RemainAfterExit=
  # yes so bluetooth.service still proceeds.
  exit 0
fi

# Clean power-cycle via mgmt socket. `power off` releases any stale
# advertising slot; `power on` re-binds cleanly before bluetoothd
# starts claiming things.
/usr/bin/btmgmt -i "$ADAPTER" power off || true
sleep 0.2
/usr/bin/btmgmt -i "$ADAPTER" power on

# Force the adapter to use its identity (public) address for
# advertising. On dual-mode Pi 5 brcmfmac this selects the BD_ADDR
# burned into the controller (e.g. tesseract D8:3A:DD:DF:D3:E9 /
# parallax 88:A2:9E:64:99:4D). Without this, BlueZ picks an NRPA
# that rotates on every bluetoothd restart and orphans peer bonds.
/usr/bin/btmgmt -i "$ADAPTER" connectable on
/usr/bin/btmgmt -i "$ADAPTER" discov on

echo "meshsat-ble-addr-pin: $ADAPTER pinned (connectable+discoverable on)"
