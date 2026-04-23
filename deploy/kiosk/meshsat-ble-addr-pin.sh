#!/usr/bin/env bash
# meshsat-ble-addr-pin.sh — pin the BT adapter to
# connectable + pairable + discoverable + LE + advertising on so the
# bridge's Reticulum GATT peripheral advertises with the **public**
# (stable) BD_ADDR rather than a rotating NRPA. [MESHSAT-678]
#
# Ordering: runs After=bluetooth.service. Earlier we tried pre-bluetoothd
# (Before=bluetooth.service) but `btmgmt power off` blocks indefinitely
# on Pi 5 brcmfmac during cold boot — the /sys/class/bluetooth/hci0
# node appears before the kernel mgmt socket is answering controller
# queries, and btmgmt has no internal timeout. Field-verified stuck on
# both kits 2026-04-24. Running After=bluetooth.service lets bluetoothd
# bring the controller up via its own initialisation path; we then
# flip the flags on a known-healthy adapter. BlueZ persists the flag
# state, so bluetoothd already re-flipped it during its init but this
# script ensures the full MeshSat-required set is on regardless of
# pre-existing state.
#
# Every btmgmt call is wrapped in `timeout 8` — if the mgmt socket
# ever stalls, the step fails soft and the unit finishes so
# docker.service can still start the bridge. Non-zero exits are
# logged but never fatal; pre-MESHSAT-678 behaviour (BlueZ default
# address selection) is the fallback floor.
#
# Why bluetoothctl is not used:
#   - bluetoothctl's `agent` / `default-agent` model requires keeping
#     a TTY-ish process alive; we want fire-and-forget.
#   - btmgmt talks directly to the kernel mgmt socket
#     (AF_BLUETOOTH HCI_CHANNEL_CONTROL) — no D-Bus dance.

set -uo pipefail  # NOT -e: every step is best-effort

ADAPTER="${ADAPTER:-hci0}"
SYS_PATH="/sys/class/bluetooth/${ADAPTER}"

log() { printf '[meshsat-ble-addr-pin] %s\n' "$*" >&2; }

# Belt-and-braces: kill any orphan btmgmt processes from a prior run.
# btmgmt has no internal timeout — if a previous invocation got stuck on
# the kernel mgmt socket, subsequent btmgmt calls in this script will
# also block (kernel serialises mgmt commands). Reaping them here is
# always safe; a properly-completed btmgmt run leaves no process behind.
# Without this, a single stuck cold-boot run wedges every subsequent
# unit start until manual cleanup. [field-verified 2026-04-24]
/usr/bin/pkill -9 -x btmgmt 2>/dev/null || true

# Wait up to 10 s for the adapter sysfs node to appear.
for _ in $(seq 1 20); do
  [ -e "$SYS_PATH" ] && break
  sleep 0.5
done
if [ ! -e "$SYS_PATH" ]; then
  log "$SYS_PATH not found after 10s — no BT hardware, skipping"
  exit 0
fi

# btmgmt requires a CONTROLLING TERMINAL — every subcommand (info,
# power, connectable, etc.) hangs forever when stdin is /dev/null
# (the systemd default). Field-verified 2026-04-24: under systemd
# `btmgmt power on < /dev/null` returns exit 124 (timeout); under a
# pty (`script -qec`) it returns instantly with the expected output.
# So we wrap every btmgmt invocation in `script` to allocate a pty.
# `script` is in `bsdutils`, preinstalled on Ubuntu base.
btmgmt_pty() {
  # `script -qec CMD /dev/null < /dev/null` runs CMD inside a fresh
  # pty; the outer `< /dev/null` keeps `script` itself happy when
  # we ourselves have stdin closed (systemd). The `timeout 8` cap
  # protects against a wedged mgmt socket.
  timeout 8 /usr/bin/script -qec "/usr/bin/btmgmt -i $ADAPTER $*" /dev/null < /dev/null
}

# Wait up to 15 s for the kernel mgmt socket to answer with a
# non-empty controller list. btmgmt info under a pty prints the
# `addr` line once the mgmt subsystem has finished init.
for i in $(seq 1 30); do
  info=$(btmgmt_pty info 2>/dev/null || true)
  if printf '%s' "$info" | grep -qE '^\s+addr '; then
    break
  fi
  sleep 0.5
done

# Fire each flag through the same pty wrapper. timeout 8 per step;
# a single stuck step doesn't cascade.
bt() {
  if ! btmgmt_pty "$@" >/dev/null 2>&1; then
    log "btmgmt $* failed or timed out (continuing)"
    return 1
  fi
  return 0
}

bt power on
bt le on
bt connectable on
bt pairable yes
bt bondable yes
bt discov yes 0    # 0 = no timeout
bt advertising on

# Final settings report for the journal.
if final=$(btmgmt_pty info 2>/dev/null); then
  settings=$(printf '%s' "$final" | grep 'current settings' | head -1 | tr -d '\r')
  addr=$(printf '%s' "$final" | grep -E '^\s+addr ' | head -1 | awk '{print $2}')
  log "pinned: addr=$addr $settings"
else
  log "pinned (unable to read final state)"
fi

exit 0
