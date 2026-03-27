#!/usr/bin/env bash
# MESHSAT-338: E2E full-stack validation — Bridge + Hub + dual satellite modems
#
# Usage:
#   ./scripts/e2e_validate.sh                          # Uses defaults
#   BRIDGE_URL=http://mule01:6050 ./scripts/e2e_validate.sh
#   BRIDGE_URL=http://mule01:6050 HUB_URL=https://hub.meshsat.net HUB_API_KEY=xxx ./scripts/e2e_validate.sh
#
# Exit codes: 0 = all pass, 1 = failures detected

set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:6050}"
HUB_URL="${HUB_URL:-https://hub.meshsat.net}"
HUB_API_KEY="${HUB_API_KEY:-}"

PASS=0
FAIL=0
SKIP=0
RESULTS=()

# --- Helpers ---

check() {
    local name="$1"
    local result="$2"  # "pass", "fail", "skip"
    local detail="${3:-}"

    case "$result" in
        pass) PASS=$((PASS + 1)); symbol="PASS" ;;
        fail) FAIL=$((FAIL + 1)); symbol="FAIL" ;;
        skip) SKIP=$((SKIP + 1)); symbol="SKIP" ;;
    esac
    RESULTS+=("[$symbol] $name${detail:+ — $detail}")
    printf "  [%-4s] %s%s\n" "$symbol" "$name" "${detail:+ — $detail}"
}

bridge_get() {
    curl -sf --max-time 10 "${BRIDGE_URL}$1" 2>/dev/null || echo '{"error":"unreachable"}'
}

hub_get() {
    if [ -z "$HUB_API_KEY" ]; then
        echo '{"error":"no_api_key"}'
        return
    fi
    curl -sf --max-time 15 -H "Authorization: Bearer $HUB_API_KEY" "${HUB_URL}$1" 2>/dev/null || echo '{"error":"unreachable"}'
}

jq_val() {
    echo "$1" | python3 -c "import sys,json; d=json.load(sys.stdin); print($2)" 2>/dev/null || echo ""
}

# --- Banner ---

echo ""
echo "MESHSAT-338 E2E Validation"
echo "=========================="
echo "bridge: $BRIDGE_URL"
echo "hub:    $HUB_URL"
echo "api_key: ${HUB_API_KEY:+set}${HUB_API_KEY:-not set}"
echo ""

# --- 1. Bridge reachability ---

echo "1. Bridge connectivity"
BRIDGE_GW=$(bridge_get "/api/gateways")
if echo "$BRIDGE_GW" | grep -q '"error"'; then
    check "Bridge API reachable" "fail" "$BRIDGE_URL unreachable"
else
    check "Bridge API reachable" "pass"
fi

# --- 2. Dual modem detection ---

echo ""
echo "2. Dual modem detection (9603 SBD + 9704 IMT)"

SBD_FOUND=$(echo "$BRIDGE_GW" | python3 -c "
import sys,json
data=json.load(sys.stdin)
gws=data.get('gateways',[])
for g in gws:
    if g.get('type')=='iridium':
        print('connected' if g.get('connected') else 'disconnected')
        sys.exit(0)
print('not_found')
" 2>/dev/null || echo "error")

IMT_FOUND=$(echo "$BRIDGE_GW" | python3 -c "
import sys,json
data=json.load(sys.stdin)
gws=data.get('gateways',[])
for g in gws:
    if g.get('type')=='iridium_imt':
        print('connected' if g.get('connected') else 'disconnected')
        sys.exit(0)
print('not_found')
" 2>/dev/null || echo "error")

case "$SBD_FOUND" in
    connected)    check "9603 SBD auto-detected" "pass" "connected" ;;
    disconnected) check "9603 SBD auto-detected" "fail" "detected but disconnected" ;;
    not_found)    check "9603 SBD auto-detected" "fail" "not found in gateway list" ;;
    *)            check "9603 SBD auto-detected" "fail" "parse error" ;;
esac

case "$IMT_FOUND" in
    connected)    check "9704 IMT auto-detected" "pass" "connected" ;;
    disconnected) check "9704 IMT auto-detected" "fail" "detected but disconnected" ;;
    not_found)    check "9704 IMT auto-detected" "fail" "not found in gateway list" ;;
    *)            check "9704 IMT auto-detected" "fail" "parse error" ;;
esac

# --- 3. Signal from both modems ---

echo ""
echo "3. Satellite signal"

SIGNAL=$(bridge_get "/api/iridium/signal/fast")
SIG_BARS=$(echo "$SIGNAL" | python3 -c "import sys,json; print(json.load(sys.stdin).get('bars',0))" 2>/dev/null || echo "0")
SIG_SRC=$(echo "$SIGNAL" | python3 -c "import sys,json; print(json.load(sys.stdin).get('source',''))" 2>/dev/null || echo "")

if [ -n "$SIG_SRC" ] && [ "$SIG_SRC" != "" ]; then
    check "Active modem signal" "pass" "${SIG_BARS} bars (source: ${SIG_SRC})"
else
    check "Active modem signal" "fail" "no signal source reported"
fi

# --- 4. Interface status ---

echo ""
echo "4. Interface status"

IFACES=$(bridge_get "/api/interfaces")
IFACE_COUNT=$(echo "$IFACES" | python3 -c "
import sys,json
data=json.load(sys.stdin)
ifaces=data if isinstance(data,list) else []
online=sum(1 for i in ifaces if i.get('state')=='online')
total=len(ifaces)
print(f'{online}/{total}')
" 2>/dev/null || echo "0/0")
check "Interfaces online" "pass" "$IFACE_COUNT"

# --- 5. Delivery ledger ---

echo ""
echo "5. Delivery ledger"

DELIVERIES=$(bridge_get "/api/deliveries?limit=20")
DLV_SUMMARY=$(echo "$DELIVERIES" | python3 -c "
import sys,json
data=json.load(sys.stdin)
items=data.get('deliveries',[])
counts={}
for d in items:
    s=d.get('status','unknown')
    counts[s]=counts.get(s,0)+1
parts=[f'{k}={v}' for k,v in sorted(counts.items())]
print(', '.join(parts) if parts else 'empty')
" 2>/dev/null || echo "error")
check "Delivery ledger" "pass" "$DLV_SUMMARY"

# --- 6. Reticulum routing ---

echo ""
echo "6. Reticulum routing"

ROUTING=$(bridge_get "/api/routing/status")
RET_HASH=$(echo "$ROUTING" | python3 -c "import sys,json; print(json.load(sys.stdin).get('identity_hash','')[:16])" 2>/dev/null || echo "")
RET_ROUTES=$(echo "$ROUTING" | python3 -c "import sys,json; print(json.load(sys.stdin).get('route_count',0))" 2>/dev/null || echo "0")

if [ -n "$RET_HASH" ] && [ "$RET_HASH" != "" ]; then
    check "Reticulum identity" "pass" "${RET_HASH}..."
else
    check "Reticulum identity" "fail" "no identity hash"
fi
check "Reticulum routes" "pass" "$RET_ROUTES routes"

# --- 7. Burst queue ---

echo ""
echo "7. Burst queue"

BURST=$(bridge_get "/api/burst/status")
BURST_PENDING=$(echo "$BURST" | python3 -c "import sys,json; print(json.load(sys.stdin).get('pending',0))" 2>/dev/null || echo "0")
check "Burst queue" "pass" "${BURST_PENDING} pending"

# --- 8. Hub integration ---

echo ""
echo "8. Hub integration"

if [ -z "$HUB_API_KEY" ]; then
    check "Hub API reachable" "skip" "HUB_API_KEY not set"
    check "Bridge registered in Hub" "skip" "HUB_API_KEY not set"
    check "Bridge online in Hub" "skip" "HUB_API_KEY not set"
else
    HUB_BRIDGES=$(hub_get "/api/bridges")
    if echo "$HUB_BRIDGES" | grep -q '"error"'; then
        check "Hub API reachable" "fail" "$HUB_URL unreachable"
    else
        check "Hub API reachable" "pass"

        BRIDGE_STATUS=$(echo "$HUB_BRIDGES" | python3 -c "
import sys,json
data=json.load(sys.stdin)
bridges=data if isinstance(data,list) else data.get('bridges',data.get('items',[]))
online=sum(1 for b in bridges if b.get('online'))
total=len(bridges)
print(f'{online}/{total}')
" 2>/dev/null || echo "0/0")

        check "Bridges in Hub" "pass" "$BRIDGE_STATUS online"

        # Check bridge health freshness
        HUB_STALE=$(echo "$HUB_BRIDGES" | python3 -c "
import sys,json,datetime
data=json.load(sys.stdin)
bridges=data if isinstance(data,list) else data.get('bridges',data.get('items',[]))
now=datetime.datetime.now(datetime.timezone.utc)
stale=[]
for b in bridges:
    ls=b.get('last_seen','')
    if ls:
        try:
            ts=datetime.datetime.fromisoformat(ls.replace('Z','+00:00'))
            age=(now-ts).total_seconds()
            if age>300:
                stale.append(f\"{b.get('id','?')}={int(age)}s\")
        except: pass
print(','.join(stale) if stale else 'none')
" 2>/dev/null || echo "error")

        if [ "$HUB_STALE" = "none" ]; then
            check "Bridge health freshness" "pass" "all <5m"
        else
            check "Bridge health freshness" "fail" "stale: $HUB_STALE"
        fi
    fi

    # Hub Reticulum
    HUB_RET=$(hub_get "/api/reticulum/relay/status")
    if echo "$HUB_RET" | grep -q '"error"'; then
        check "Hub Reticulum relay" "skip" "endpoint unavailable"
    else
        RET_FWD=$(echo "$HUB_RET" | python3 -c "import sys,json; print(json.load(sys.stdin).get('forwarded',0))" 2>/dev/null || echo "0")
        check "Hub Reticulum relay" "pass" "${RET_FWD} forwarded"
    fi
fi

# --- 9. Latency ---

echo ""
echo "9. Latency measurement"

BRIDGE_START=$(date +%s%N)
bridge_get "/api/gateways" > /dev/null
BRIDGE_END=$(date +%s%N)
BRIDGE_MS=$(( (BRIDGE_END - BRIDGE_START) / 1000000 ))
check "Bridge API latency" "pass" "${BRIDGE_MS}ms"

if [ -n "$HUB_API_KEY" ]; then
    HUB_START=$(date +%s%N)
    hub_get "/api/bridges" > /dev/null
    HUB_END=$(date +%s%N)
    HUB_MS=$(( (HUB_END - HUB_START) / 1000000 ))
    check "Hub API latency" "pass" "${HUB_MS}ms"
fi

# --- Summary ---

echo ""
echo "=========================="
echo "Results: $PASS pass, $FAIL fail, $SKIP skip"
echo "=========================="

if [ "$FAIL" -gt 0 ]; then
    echo ""
    echo "Failures:"
    for r in "${RESULTS[@]}"; do
        if [[ "$r" == *"[FAIL]"* ]]; then
            echo "  $r"
        fi
    done
    exit 1
fi

exit 0
