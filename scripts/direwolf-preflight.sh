#!/bin/bash
# =============================================================================
# Direwolf Pre-Flight Check — runs before Direwolf starts (ExecStartPre)
# =============================================================================
# 1. Kills any stale process holding the AIOC ALSA device
# 2. Tests ALSA hw params — if it fails, resets the AIOC USB device
# 3. Retries ALSA test after reset
# 4. Exits non-zero only if AIOC is completely missing (no USB device)
# =============================================================================
set -euo pipefail

AIOC_VID="1209"
AIOC_PID="7388"
AIOC_CARD="AllInOneCable"
MAX_RETRIES=3

log() { echo "direwolf-preflight: $*"; }

# --- Find AIOC USB bus address for usbreset ---
find_aioc_busdev() {
    lsusb | grep -i "${AIOC_VID}:${AIOC_PID}" | head -1 | awk '{print "/dev/bus/usb/" $2 "/" $4}' | tr -d ':'
}

# --- Kill any stale process holding the ALSA device ---
kill_stale_holders() {
    for dev in /dev/snd/pcmC*D*{c,p}; do
        [ -e "$dev" ] || continue
        # Check if this belongs to AIOC card
        case "$dev" in
            *C2D*) ;;  # card 2 = AIOC typically
            *) continue ;;
        esac
        pids=$(fuser "$dev" 2>/dev/null | tr -s ' ') || true
        if [ -n "$pids" ]; then
            log "killing stale ALSA holders on $dev: $pids"
            fuser -k "$dev" 2>/dev/null || true
            sleep 1
        fi
    done
}

# --- Test ALSA hw params ---
test_alsa() {
    # Quick 0.1s recording test — validates hw params can be set
    timeout 3 arecord -D "hw:${AIOC_CARD},0" -r 48000 -f S16_LE -c 1 -d 1 /dev/null 2>&1
}

# --- USB reset AIOC ---
reset_aioc() {
    local busdev
    busdev=$(find_aioc_busdev)
    if [ -z "$busdev" ]; then
        log "AIOC USB device not found (${AIOC_VID}:${AIOC_PID})"
        return 1
    fi
    log "resetting AIOC USB device at $busdev"
    usbreset "${AIOC_VID}:${AIOC_PID}" 2>/dev/null || true
    sleep 3  # wait for USB re-enumeration + ALSA driver rebind
}

# --- Main ---

# Check AIOC is present
if ! lsusb | grep -qi "${AIOC_VID}:${AIOC_PID}"; then
    log "AIOC not found on USB bus — Direwolf will fail"
    exit 1
fi

# Kill stale holders
kill_stale_holders

# Test + retry loop
for attempt in $(seq 1 $MAX_RETRIES); do
    if test_alsa >/dev/null 2>&1; then
        log "ALSA test passed (attempt $attempt)"
        exit 0
    fi

    log "ALSA test failed (attempt $attempt/$MAX_RETRIES) — resetting USB"
    reset_aioc
    kill_stale_holders
done

# Final attempt after all resets
if test_alsa >/dev/null 2>&1; then
    log "ALSA test passed after USB reset"
    exit 0
fi

log "ALSA test still failing after $MAX_RETRIES resets — Direwolf will retry via systemd"
# Exit 0 anyway — let Direwolf try; systemd Restart=always will catch it
exit 0
