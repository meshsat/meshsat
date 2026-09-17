#!/usr/bin/env bash
# =============================================================================
# MeshSat booth receipt printer — the shared slip counter [MESHSAT-1201]
# =============================================================================
# Standard procedure (owner, 17 Sep 2026): ZERO THE COUNTERS AFTER EVERY PRINTER
# TEST, so the number on a visitor's slip is a real count and the hat at 666 is
# owed to a real person.
#
#   scripts/slip-counter.sh            # show where both kits are
#   scripts/slip-counter.sh zero       # back to 0 on both, after a test
#   scripts/slip-counter.sh set 250    # continue from a known number
#
# BOTH kits or neither. The two share one sequence: before printing, a kit takes
# max(its own, the peer's) + 1, so zeroing one kit alone does nothing at all -
# the other kit's number simply wins on the next slip. That is why this script
# refuses to do one of them, and why it reads both back afterwards.
#
# Run from the repo on the runner. Addresses come from kit-host.sh, so it works
# whether the kits are on the house LAN or behind the Mudi.
# =============================================================================
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
KITS=(tesseract parallax)
SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=10 -i "$HOME/.ssh/one_key")
COUNT=/var/lib/meshsat/receipt-count

action="${1:-show}"
value="${2:-0}"

case "$action" in
  show|zero|set) ;;
  *) echo "usage: $0 [show|zero|set <n>]" >&2; exit 2 ;;
esac
if [ "$action" = "set" ] && ! [[ "$value" =~ ^[0-9]+$ ]]; then
  echo "ERROR: set needs a whole number" >&2; exit 2
fi
[ "$action" = "zero" ] && value=0

declare -A HOST
for kit in "${KITS[@]}"; do
  if ! HOST[$kit]="$("$HERE/kit-host.sh" "$kit" 2>/dev/null)" || [ -z "${HOST[$kit]}" ]; then
    echo "ERROR: cannot resolve $kit." >&2
    # Writing one kit and not the other leaves the sequence wherever the
    # unreachable kit left it, which is worse than not writing at all.
    [ "$action" = "show" ] || { echo "Refusing to touch only one kit." >&2; exit 1; }
  fi
done

read_one() {
  ssh "${SSH_OPTS[@]}" "kyriakosp@$1" "sudo cat $COUNT 2>/dev/null || echo 0"
}

if [ "$action" = "show" ]; then
  for kit in "${KITS[@]}"; do
    [ -n "${HOST[$kit]:-}" ] && printf '%-10s %s\n' "$kit" "$(read_one "${HOST[$kit]}")" \
      || printf '%-10s unreachable\n' "$kit"
  done
  exit 0
fi

echo "Setting the slip counter to $value on both kits."
for kit in "${KITS[@]}"; do
  # The service reads the file per slip, so there is no need to stop it; a print
  # landing in this window simply takes the new number + 1.
  ssh "${SSH_OPTS[@]}" "kyriakosp@${HOST[$kit]}" \
    "sudo install -d -m 0755 $(dirname $COUNT) && echo $value | sudo tee $COUNT >/dev/null"
done

echo "Reading both back:"
ok=1
for kit in "${KITS[@]}"; do
  got="$(read_one "${HOST[$kit]}")"
  printf '%-10s %s\n' "$kit" "$got"
  [ "$got" = "$value" ] || ok=0
done
[ "$ok" = 1 ] || { echo "ERROR: a kit did not take the new value." >&2; exit 1; }
echo "Both agree. Next slip will be #$((value + 1))."
