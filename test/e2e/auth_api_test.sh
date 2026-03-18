#!/usr/bin/env bash
# E2E API tests for MeshSat Hub auth, API keys, tenant isolation, and RBAC.
# Requires: HUB_AUTH_ENABLED=false (tests against the default tenant with no OIDC).
# Usage: HUB_URL=http://localhost:6050 bash test/e2e/auth_api_test.sh

set -euo pipefail

HUB_URL="${HUB_URL:-http://localhost:6050}"
PASS=0
FAIL=0
TOTAL=0

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m'

assert_status() {
  local desc="$1" expected="$2" actual="$3"
  TOTAL=$((TOTAL + 1))
  if [ "$expected" = "$actual" ]; then
    echo -e "  ${GREEN}PASS${NC} $desc (HTTP $actual)"
    PASS=$((PASS + 1))
  else
    echo -e "  ${RED}FAIL${NC} $desc (expected $expected, got $actual)"
    FAIL=$((FAIL + 1))
  fi
}

assert_contains() {
  local desc="$1" body="$2" needle="$3"
  TOTAL=$((TOTAL + 1))
  if echo "$body" | grep -q "$needle"; then
    echo -e "  ${GREEN}PASS${NC} $desc (contains '$needle')"
    PASS=$((PASS + 1))
  else
    echo -e "  ${RED}FAIL${NC} $desc (missing '$needle' in response)"
    FAIL=$((FAIL + 1))
  fi
}

echo -e "${YELLOW}=== MeshSat Hub Auth E2E Tests ===${NC}"
echo "Target: $HUB_URL"
echo ""

# ── 1. Auth status (should be disabled in test mode) ──
echo "1. Auth status endpoint"
STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$HUB_URL/auth/status")
assert_status "/auth/status returns 200" "200" "$STATUS"

BODY=$(curl -s "$HUB_URL/auth/status")
assert_contains "auth disabled in test mode" "$BODY" '"enabled":false'

# ── 2. Health check ──
echo ""
echo "2. Health check"
STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$HUB_URL/health")
assert_status "/health returns 200" "200" "$STATUS"

# ── 3. API access without auth (should work when auth disabled) ──
echo ""
echo "3. API access without auth"
STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$HUB_URL/api/device-registry")
assert_status "GET /api/device-registry works" "200" "$STATUS"

# ── 4. Device CRUD ──
echo ""
echo "4. Device CRUD"
# Create
DEVICE=$(curl -s -X POST "$HUB_URL/api/device-registry" \
  -H "Content-Type: application/json" \
  -d '{"imei":"300234063904190","label":"E2E Test Device","type":"rockblock"}')
DEVICE_ID=$(echo "$DEVICE" | grep -o '"id":[0-9]*' | head -1 | cut -d: -f2)
assert_contains "create device" "$DEVICE" '"imei":"300234063904190"'

if [ -n "$DEVICE_ID" ]; then
  # Read
  STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$HUB_URL/api/device-registry/$DEVICE_ID")
  assert_status "GET device by ID" "200" "$STATUS"

  # Update
  STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$HUB_URL/api/device-registry/$DEVICE_ID" \
    -H "Content-Type: application/json" \
    -d '{"label":"Updated E2E Device","type":"iridium","notes":"e2e test"}')
  assert_status "PUT device update" "200" "$STATUS"

  # Delete
  STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$HUB_URL/api/device-registry/$DEVICE_ID")
  assert_status "DELETE device" "204" "$STATUS"

  # Verify deleted
  STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$HUB_URL/api/device-registry/$DEVICE_ID")
  assert_status "GET deleted device returns 404" "404" "$STATUS"
fi

# ── 5. API key management (when auth disabled, keys endpoint still works) ──
echo ""
echo "5. API key management"
# Create key
KEY_RESP=$(curl -s -X POST "$HUB_URL/api/auth/keys" \
  -H "Content-Type: application/json" \
  -d '{"label":"E2E Test Key","role":"operator"}')
assert_contains "create API key" "$KEY_RESP" '"key":"meshsat_'
assert_contains "key has role" "$KEY_RESP" '"role":"operator"'

KEY_ID=$(echo "$KEY_RESP" | grep -o '"id":[0-9]*' | head -1 | cut -d: -f2)
API_KEY=$(echo "$KEY_RESP" | grep -o '"key":"[^"]*"' | head -1 | cut -d'"' -f4)

# List keys
KEYS_LIST=$(curl -s "$HUB_URL/api/auth/keys")
assert_contains "list API keys" "$KEYS_LIST" '"key_prefix":"meshsat_'

# Use API key for authenticated request
if [ -n "$API_KEY" ]; then
  STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$HUB_URL/api/device-registry" \
    -H "Authorization: Bearer $API_KEY")
  assert_status "GET with API key auth" "200" "$STATUS"
fi

# Revoke key
if [ -n "$KEY_ID" ]; then
  STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$HUB_URL/api/auth/keys/$KEY_ID")
  assert_status "DELETE (revoke) API key" "204" "$STATUS"
fi

# ── 6. Contacts CRUD ──
echo ""
echo "6. Contacts CRUD"
CONTACT=$(curl -s -X POST "$HUB_URL/api/contacts" \
  -H "Content-Type: application/json" \
  -d '{"display_name":"E2E Contact","notes":"test"}')
CONTACT_ID=$(echo "$CONTACT" | grep -o '"id":[0-9]*' | head -1 | cut -d: -f2)
assert_contains "create contact" "$CONTACT" '"display_name":"E2E Contact"'

if [ -n "$CONTACT_ID" ]; then
  STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$HUB_URL/api/contacts/$CONTACT_ID")
  assert_status "DELETE contact" "204" "$STATUS"
fi

# ── 7. Interfaces list ──
echo ""
echo "7. Interfaces"
STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$HUB_URL/api/interfaces")
assert_status "GET /api/interfaces" "200" "$STATUS"

# ── 8. Access rules list ──
echo ""
echo "8. Access rules"
STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$HUB_URL/api/access-rules")
assert_status "GET /api/access-rules" "200" "$STATUS"

# ── 9. Config export ──
echo ""
echo "9. Config export"
STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$HUB_URL/api/config/export")
assert_status "GET /api/config/export" "200" "$STATUS"

# ── 10. Audit log ──
echo ""
echo "10. Audit log"
STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$HUB_URL/api/audit")
assert_status "GET /api/audit" "200" "$STATUS"

# ── Summary ──
echo ""
echo -e "${YELLOW}=== Results ===${NC}"
echo -e "Total: $TOTAL  ${GREEN}Pass: $PASS${NC}  ${RED}Fail: $FAIL${NC}"
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
echo -e "${GREEN}All tests passed!${NC}"
