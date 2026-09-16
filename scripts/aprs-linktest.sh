#!/usr/bin/env bash
# Kit-to-kit APRS link test, one direction at a time. [MESHSAT-1021]
#
#   scripts/aprs-linktest.sh <sender: tesseract|parallax> <count> [gap_s] [block] [outdir]
#
# Injects <count> tagged texts on the sending kit with POST /api/messages/simulate-mesh-rx
# (the same path a handheld text takes: rules, delivery ledger, the aprs_0 gateway with
# per-message acks), <gap_s> apart so the single delivery worker never backs up. Before and
# after the block it snapshots both kits' /api/aprs/status, the sender's deliveries, the
# receiver's messages and both packet rings (GET /api/packets?bearer=aprs&limit=500, raw
# AX.25 hex identical on both sides) into <outdir>/<block>-*.json, and prints a quick
# summary. Per-frame statistics come from scripts/aprs-linkstats.py on those files.
#
# Rule from the field: never run both directions at once; simultaneous frames collide on
# 144.800 and look exactly like a dead receiver.
set -u
sender=${1:?sender}; count=${2:?count}; gap=${3:-20}; block=${4:-$(date +%H%M%S)}; out=${5:-.}
case "$sender" in tesseract) receiver=parallax;; parallax) receiver=tesseract;; *) echo "sender must be tesseract or parallax" >&2; exit 2;; esac
K="ssh -i $HOME/.ssh/one_key -o BatchMode=yes -o ConnectTimeout=10"
# A kit answers on its house name or, when it is behind the Mudi 5G router, on its
# -field name through the WireGuard tunnel. Resolve both once per run so a booth
# session needs no edit here. [MESHSAT-1182]
declare -A KIT_ADDR
for _k in tesseract parallax; do
  KIT_ADDR[$_k]=$("$(dirname "$0")/kit-host.sh" "$_k") || exit 1
done
echo "== kits: tesseract=${KIT_ADDR[tesseract]} parallax=${KIT_ADDR[parallax]}"
host() { echo "kyriakosp@${KIT_ADDR[$1]}"; }
api() { $K "$(host "$1")" "curl -s -m 20 ${*:2}"; }
snap() { # kit tag
  api "$1" localhost:6050/api/aprs/status > "$out/$block-status-$2-$1.json"
}
mkdir -p "$out"
echo "== block $block: $sender -> $receiver, $count messages, $gap s apart, $(date +%H:%M:%S)"
for k in tesseract parallax; do snap "$k" before; done
api "$sender" localhost:6050/api/gateways/aprs | python3 -c 'import sys,json; c=json.load(sys.stdin).get("config",{}); print("  sender config:", {k:c.get(k) for k in ("beacon_secs","tx_repeat","tx_repeat_gap_ms","ack_attempts","ack_timeout_ms")})'
for k in tesseract parallax; do
  api "$k" localhost:6050/api/spectrum/status | python3 -c '
import sys,json
for b in json.load(sys.stdin).get("bands",[]):
    if b["band"]=="aprs_144": print("  '$k' aprs_144 ambient %.1f dB (baseline %.1f)" % (b.get("power_db",0), b.get("baseline_mean",0)))'
done
t_start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
for n in $(seq 1 "$count"); do
  tag=$(printf "lt-%s-%02d" "$block" "$n")
  api "$sender" "-X POST localhost:6050/api/messages/simulate-mesh-rx -H 'Content-Type: application/json' -d '{\"text\":\"$tag\",\"from\":\"!00c0ffee\"}'" >/dev/null
  printf "."
  [ "$n" -lt "$count" ] && sleep "$gap"
done
echo
echo "  injected $count, settling 45 s for the last acks and retries"
sleep 45
t_end=$(date -u +%Y-%m-%dT%H:%M:%SZ)
for k in tesseract parallax; do snap "$k" after; done
api "$sender" "'localhost:6050/api/deliveries?limit=300'" > "$out/$block-deliveries-$sender.json"
api "$receiver" "'localhost:6050/api/messages?limit=300'" > "$out/$block-messages-$receiver.json"
for k in tesseract parallax; do api "$k" "'localhost:6050/api/packets?bearer=aprs&limit=500'" > "$out/$block-packets-$k.json"; done
echo "$block $sender $receiver $count $gap $t_start $t_end" >> "$out/blocks.txt"
python3 - "$out" "$block" "$sender" "$receiver" "$count" <<'EOF'
import sys,json
out,block,sender,receiver,count=sys.argv[1:6]; count=int(count)
def load(p):
    try: return json.load(open(p))
    except Exception: return {}
msgs=load(f"{out}/{block}-messages-{receiver}.json").get("messages",[])
tags={f"lt-{block}-{n:02d}" for n in range(1,count+1)}
arrived={m["decoded_text"] for m in msgs if m.get("decoded_text") in tags and m.get("transport")=="aprs"}
d=load(f"{out}/{block}-deliveries-{sender}.json"); dl=d.get("deliveries",d) if isinstance(d,dict) else d
st={}
for x in dl:
    t=x.get("text_preview") or ""
    if t in tags: st.setdefault(t,[]).append(f'{x.get("channel")}:{x.get("status")}')
sms=sum(1 for t,v in st.items() if any(s.startswith("cellular_0") for s in v))
def delta(kit,key):
    b=load(f"{out}/{block}-status-before-{kit}.json"); a=load(f"{out}/{block}-status-after-{kit}.json")
    return (a.get(key) or 0)-(b.get(key) or 0)
print(f"  arrivals on {receiver}: {len(arrived)}/{count}  missing: {sorted(tags-arrived) or 'none'}")
print(f"  moved to SMS: {sms}")
print(f"  {sender}: tx +{delta(sender,'tx')} rx +{delta(sender,'rx')} acks_received +{delta(sender,'acks_received')} ack_retries +{delta(sender,'ack_retries')} ack_failures +{delta(sender,'ack_failures')} cts_deferred +{delta(sender,'cts_deferred')}")
print(f"  {receiver}: rx +{delta(receiver,'rx')} tx +{delta(receiver,'tx')} acks_sent +{delta(receiver,'acks_sent')} repaired +{delta(receiver,'repaired_frames')} bad +{delta(receiver,'bad_frames')}")
EOF
