#!/bin/sh
# meshsat-goodix-rebind — make sure the Touch Display 2 touch controller is
# bound with a valid config. Run once after boot by goodix-touch-rebind.service.
#
# The Goodix probe goes wrong in two ways on kernel 6.8.0-1051:
#   1. It races the panel's attiny reset line and gives up (-121 EREMOTEIO),
#      leaving 11-005d unbound (MESHSAT-808).
#   2. It binds, but the GT911 reports a zero size or zero contacts, so the
#      driver logs "Invalid config (x, y, n), using defaults" and registers
#      0..4095 on both axes. The panel still sends 0..719 / 0..1279, so every
#      touch lands in one corner (MESHSAT-1094, tesseract 13 Sep 2026).
#      Only an unbind + bind re-reads the config.
#
# GOODIX_DRV overrides the sysfs driver path for testing.

DRV="${GOODIX_DRV:-/sys/bus/i2c/drivers/Goodix-TS}"
DEV=11-005d
ATTEMPTS=3

bound() {
  [ -e "$DRV/$DEV" ]
}

# A probe prints "ID <n>, version" first and the warning after it, so the last
# probe was bad when the last of those lines is the warning.
last_probe_bad() {
  dmesg | grep -E "Goodix-TS $DEV: (ID |Invalid config)" | tail -n 1 | grep -q "Invalid config"
}

usable() {
  bound && ! last_probe_bad
}

attempt=1
while [ "$attempt" -le "$ATTEMPTS" ]; do
  if usable; then
    echo "$DEV bound with a valid config"
    exit 0
  fi
  if bound; then
    echo "attempt $attempt: last probe logged Invalid config, rebinding $DEV"
    echo "$DEV" >"$DRV/unbind"
  else
    echo "attempt $attempt: $DEV not bound, binding"
  fi
  echo "$DEV" >"$DRV/bind" || echo "attempt $attempt: bind failed"
  attempt=$((attempt + 1))
  sleep 2
done

if usable; then
  echo "$DEV bound with a valid config"
  exit 0
fi
echo "$DEV still unusable after $ATTEMPTS attempts, see: dmesg | grep Goodix" >&2
exit 1
