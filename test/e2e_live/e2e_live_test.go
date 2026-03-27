//go:build e2e_live

// Package e2e_live validates the full MeshSat stack against real Bridge + Hub
// infrastructure. These tests require:
//   - BRIDGE_URL: Bridge API (e.g. http://mule01.internal:6050)
//   - HUB_URL: Hub API (e.g. https://hub.meshsat.net)
//   - HUB_API_KEY: Hub API key for authentication
//
// Run: go test -tags=e2e_live -v -timeout=300s ./test/e2e_live/...
//
// MESHSAT-338: E2E full-stack validation — Hub + Bridge + Reticulum + dual satellite modems
package e2e_live

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Config from environment
// ---------------------------------------------------------------------------

var (
	bridgeURL = envOrDefault("BRIDGE_URL", "http://localhost:6050")
	hubURL    = envOrDefault("HUB_URL", "https://hub.meshsat.net")
	hubAPIKey = os.Getenv("HUB_API_KEY")
)

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func bridgeGet(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	return httpGet(t, bridgeURL+path, "")
}

func hubGet(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	return httpGet(t, hubURL+path, hubAPIKey)
}

func httpGet(t *testing.T, url, apiKey string) map[string]interface{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		t.Fatalf("request %s: %v", url, err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		t.Fatalf("GET %s: %d %s", url, resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("GET %s: unmarshal: %v (body: %s)", url, err, string(body))
	}
	return result
}

func httpGetArray(t *testing.T, url, apiKey string) []interface{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		t.Fatalf("request %s: %v", url, err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		t.Fatalf("GET %s: %d %s", url, resp.StatusCode, string(body))
	}

	var result []interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		// Try as wrapped array in object (common pattern)
		var obj map[string]interface{}
		if err2 := json.Unmarshal(body, &obj); err2 == nil {
			for _, v := range obj {
				if arr, ok := v.([]interface{}); ok {
					return arr
				}
			}
		}
		t.Fatalf("GET %s: unmarshal array: %v (body: %s)", url, err, string(body))
	}
	return result
}

func httpPost(t *testing.T, url, apiKey, body string) (int, map[string]interface{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(respBody, &result)
	return resp.StatusCode, result
}

// ---------------------------------------------------------------------------
// Scenario 1: Bridge boots with 9603+9704, both auto-detected, both online
// ---------------------------------------------------------------------------

func TestE2E_DualModemBoot(t *testing.T) {
	start := time.Now()

	gateways := bridgeGet(t, "/api/gateways")
	gwList, ok := gateways["gateways"].([]interface{})
	if !ok {
		t.Fatal("unexpected gateways response format")
	}

	foundSBD := false
	foundIMT := false
	for _, gwRaw := range gwList {
		gw, ok := gwRaw.(map[string]interface{})
		if !ok {
			continue
		}
		gwType, _ := gw["type"].(string)
		connected, _ := gw["connected"].(bool)

		switch gwType {
		case "iridium":
			foundSBD = true
			if !connected {
				t.Error("SBD gateway (9603) not connected")
			}
			t.Logf("SBD gateway: connected=%v, out=%v, errors=%v",
				connected, gw["messages_out"], gw["errors"])
		case "iridium_imt":
			foundIMT = true
			if !connected {
				t.Error("IMT gateway (9704) not connected")
			}
			t.Logf("IMT gateway: connected=%v, out=%v, errors=%v",
				connected, gw["messages_out"], gw["errors"])
		}
	}

	if !foundSBD {
		t.Error("SBD gateway (type=iridium) not found — 9603 not detected")
	}
	if !foundIMT {
		t.Error("IMT gateway (type=iridium_imt) not found — 9704 not detected")
	}

	t.Logf("dual modem boot check: %v (SBD=%v, IMT=%v)", time.Since(start).Round(time.Millisecond), foundSBD, foundIMT)
}

// ---------------------------------------------------------------------------
// Scenario 2: Signal from both modems
// ---------------------------------------------------------------------------

func TestE2E_DualSignal(t *testing.T) {
	sig := bridgeGet(t, "/api/iridium/signal/fast")
	bars, _ := sig["bars"].(float64)
	source, _ := sig["source"].(string)
	assessment, _ := sig["assessment"].(string)

	t.Logf("active modem signal: bars=%v, source=%s, assessment=%s", bars, source, assessment)

	if source == "" {
		t.Error("signal source is empty — no active modem")
	}

	// Check signal history for both sources
	history := bridgeGet(t, "/api/iridium/signal/history?source=sbd")
	t.Logf("SBD signal history entries: %v", countEntries(history))

	historyIMT := bridgeGet(t, "/api/iridium/signal/history?source=imt")
	t.Logf("IMT signal history entries: %v", countEntries(historyIMT))
}

func countEntries(resp map[string]interface{}) int {
	for _, v := range resp {
		if arr, ok := v.([]interface{}); ok {
			return len(arr)
		}
	}
	// Response might be the array itself
	return 0
}

// ---------------------------------------------------------------------------
// Scenario 3: Bridge telemetry visible in Hub
// ---------------------------------------------------------------------------

func TestE2E_BridgeTelemetryInHub(t *testing.T) {
	if hubAPIKey == "" {
		t.Skip("HUB_API_KEY not set — skipping Hub integration test")
	}

	bridges := httpGetArray(t, hubURL+"/api/bridges", hubAPIKey)
	if len(bridges) == 0 {
		t.Fatal("no bridges registered in Hub")
	}

	for _, brRaw := range bridges {
		br, ok := brRaw.(map[string]interface{})
		if !ok {
			continue
		}
		bridgeID, _ := br["id"].(string)
		if bridgeID == "" {
			bridgeID, _ = br["bridge_id"].(string)
		}
		online, _ := br["online"].(bool)
		lastSeen, _ := br["last_seen"].(string)
		health, _ := br["health"].(map[string]interface{})

		t.Logf("bridge %s: online=%v, last_seen=%s", bridgeID, online, lastSeen)
		if health != nil {
			t.Logf("  health: cpu=%v%%, mem=%v%%, disk=%v%%, uptime=%vs, interfaces=%v",
				health["cpu_pct"], health["mem_pct"], health["disk_pct"],
				health["uptime_sec"], health["interfaces"])
		}

		if !online {
			t.Errorf("bridge %s is offline in Hub", bridgeID)
		}

		// Verify staleness
		if lastSeen != "" {
			ts, err := time.Parse(time.RFC3339, lastSeen)
			if err == nil {
				age := time.Since(ts)
				if age > 5*time.Minute {
					t.Errorf("bridge %s last seen %v ago (>5m stale)", bridgeID, age.Round(time.Second))
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Scenario 4: Interface status — both satellite interfaces online
// ---------------------------------------------------------------------------

func TestE2E_InterfaceStatus(t *testing.T) {
	ifaces := httpGetArray(t, bridgeURL+"/api/interfaces", "")

	foundSBD := false
	foundIMT := false
	for _, ifRaw := range ifaces {
		iface, ok := ifRaw.(map[string]interface{})
		if !ok {
			continue
		}
		chType, _ := iface["channel_type"].(string)
		state, _ := iface["state"].(string)
		id, _ := iface["id"].(string)

		switch chType {
		case "iridium":
			foundSBD = true
			t.Logf("SBD interface %s: state=%s", id, state)
			if state != "online" {
				t.Errorf("SBD interface %s state=%s, want online", id, state)
			}
		case "iridium_imt":
			foundIMT = true
			t.Logf("IMT interface %s: state=%s", id, state)
			if state != "online" {
				t.Errorf("IMT interface %s state=%s, want online", id, state)
			}
		}
	}

	if !foundSBD {
		t.Log("WARN: no SBD (iridium) interface found")
	}
	if !foundIMT {
		t.Log("WARN: no IMT (iridium_imt) interface found")
	}
}

// ---------------------------------------------------------------------------
// Scenario 5: MO message flow verification (check recent messages)
// ---------------------------------------------------------------------------

func TestE2E_MOMessageFlow(t *testing.T) {
	// Check bridge-side recent messages
	msgs := bridgeGet(t, "/api/messages?limit=10")
	if messages, ok := msgs["messages"].([]interface{}); ok {
		t.Logf("bridge recent messages: %d", len(messages))
		for i, mRaw := range messages {
			if i >= 3 {
				break
			}
			if m, ok := mRaw.(map[string]interface{}); ok {
				t.Logf("  [%d] from=%v, text=%v, ts=%v",
					i, m["from"], truncate(fmt.Sprint(m["text"]), 60), m["timestamp"])
			}
		}
	}

	if hubAPIKey == "" {
		t.Skip("HUB_API_KEY not set — skipping Hub message verification")
	}

	// Check hub-side recent messages
	hubMsgs := httpGetArray(t, hubURL+"/api/messages?limit=10", hubAPIKey)
	t.Logf("hub recent messages: %d", len(hubMsgs))
	for i, mRaw := range hubMsgs {
		if i >= 3 {
			break
		}
		if m, ok := mRaw.(map[string]interface{}); ok {
			t.Logf("  [%d] device=%v, text=%v, ts=%v",
				i, m["device_id"], truncate(fmt.Sprint(m["text"]), 60), m["created_at"])
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------------------------------------------------------------------------
// Scenario 6: Delivery ledger status (queued/sending/sent/dead)
// ---------------------------------------------------------------------------

func TestE2E_DeliveryLedger(t *testing.T) {
	deliveries := bridgeGet(t, "/api/deliveries?limit=20")
	if items, ok := deliveries["deliveries"].([]interface{}); ok {
		statusCounts := map[string]int{}
		for _, dRaw := range items {
			if d, ok := dRaw.(map[string]interface{}); ok {
				st, _ := d["status"].(string)
				statusCounts[st]++
			}
		}
		t.Logf("delivery ledger (last 20): %v", statusCounts)

		dead := statusCounts["dead"]
		if dead > 5 {
			t.Errorf("high dead-letter count: %d (>5 in last 20 deliveries)", dead)
		}
	}
}

// ---------------------------------------------------------------------------
// Scenario 7: Reticulum routing status
// ---------------------------------------------------------------------------

func TestE2E_ReticulumRouting(t *testing.T) {
	routing := bridgeGet(t, "/api/routing/status")
	t.Logf("reticulum routing: identity=%v, routes=%v, links=%v, announces=%v",
		routing["identity_hash"], routing["route_count"], routing["link_count"], routing["announces_relayed"])

	// Check TCP interface
	ifaces := bridgeGet(t, "/api/routing/interfaces")
	if ifList, ok := ifaces["interfaces"].([]interface{}); ok {
		for _, ifRaw := range ifList {
			if iface, ok := ifRaw.(map[string]interface{}); ok {
				t.Logf("  reticulum iface: name=%v, type=%v, online=%v, cost=%v",
					iface["name"], iface["type"], iface["online"], iface["cost"])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Scenario 8: Hub Reticulum topology
// ---------------------------------------------------------------------------

func TestE2E_HubReticulumTopology(t *testing.T) {
	if hubAPIKey == "" {
		t.Skip("HUB_API_KEY not set — skipping Hub Reticulum check")
	}

	topo := hubGet(t, "/api/reticulum/topology")
	t.Logf("hub reticulum topology: destinations=%v", topo["destinations"])

	relayStatus := hubGet(t, "/api/reticulum/relay/status")
	t.Logf("hub relay: forwarded=%v, dropped=%v, no_route=%v, rate_limited=%v",
		relayStatus["forwarded"], relayStatus["dropped"],
		relayStatus["no_route"], relayStatus["rate_limited"])
}

// ---------------------------------------------------------------------------
// Scenario 9: End-to-end latency measurement (bridge→hub health)
// ---------------------------------------------------------------------------

func TestE2E_Latency_BridgeHealth(t *testing.T) {
	start := time.Now()
	bridgeGet(t, "/api/gateways")
	bridgeLatency := time.Since(start)

	if hubAPIKey == "" {
		t.Logf("bridge API latency: %v", bridgeLatency.Round(time.Millisecond))
		t.Skip("HUB_API_KEY not set — skipping Hub latency check")
	}

	start = time.Now()
	hubGet(t, "/api/bridges")
	hubLatency := time.Since(start)

	t.Logf("latency: bridge_api=%v, hub_api=%v",
		bridgeLatency.Round(time.Millisecond), hubLatency.Round(time.Millisecond))

	if bridgeLatency > 5*time.Second {
		t.Errorf("bridge API latency too high: %v", bridgeLatency)
	}
	if hubLatency > 10*time.Second {
		t.Errorf("hub API latency too high: %v", hubLatency)
	}
}

// ---------------------------------------------------------------------------
// Scenario 10: Burst queue status
// ---------------------------------------------------------------------------

func TestE2E_BurstQueue(t *testing.T) {
	burst := bridgeGet(t, "/api/burst/status")
	t.Logf("burst queue: pending=%v, flushed=%v, mode=%v",
		burst["pending"], burst["flushed"], burst["mode"])
}

// ---------------------------------------------------------------------------
// Scenario 11: Pass scheduler status (both SBD + IMT)
// ---------------------------------------------------------------------------

func TestE2E_PassScheduler(t *testing.T) {
	scheduler := bridgeGet(t, "/api/iridium/scheduler")
	t.Logf("pass scheduler: state=%v, next_pass=%v, pass_quality=%v",
		scheduler["state"], scheduler["next_pass"], scheduler["quality"])
}

// ---------------------------------------------------------------------------
// Scenario 12: MT send capability check (dry-run — no actual satellite send)
// ---------------------------------------------------------------------------

func TestE2E_MTSendCapability(t *testing.T) {
	if hubAPIKey == "" {
		t.Skip("HUB_API_KEY not set — skipping MT capability check")
	}

	// Check that Hub has devices registered
	devices := httpGetArray(t, hubURL+"/api/devices", hubAPIKey)
	if len(devices) == 0 {
		t.Skip("no devices registered in Hub — cannot verify MT path")
	}

	for _, dRaw := range devices {
		if d, ok := dRaw.(map[string]interface{}); ok {
			imei, _ := d["imei"].(string)
			label, _ := d["label"].(string)
			devType, _ := d["type"].(string)
			t.Logf("hub device: imei=%s, label=%s, type=%s", imei, label, devType)
		}
	}

	t.Log("MT send path: Hub dashboard → /api/devices/{imei}/mt → Cloudloop → modem → bridge")
	t.Log("NOTE: actual MT send requires satellite credits — verify manually")
}

// ---------------------------------------------------------------------------
// Scenario 13: Failover group configuration
// ---------------------------------------------------------------------------

func TestE2E_FailoverConfig(t *testing.T) {
	// Check failover groups exist for satellite interfaces
	ifaces := httpGetArray(t, bridgeURL+"/api/interfaces", "")

	satInterfaces := 0
	for _, ifRaw := range ifaces {
		if iface, ok := ifRaw.(map[string]interface{}); ok {
			chType, _ := iface["channel_type"].(string)
			if chType == "iridium" || chType == "iridium_imt" {
				satInterfaces++
				failoverGroup, _ := iface["failover_group"].(string)
				priority, _ := iface["priority"].(float64)
				t.Logf("sat interface %v: failover_group=%s, priority=%v",
					iface["id"], failoverGroup, priority)
			}
		}
	}

	if satInterfaces >= 2 {
		t.Logf("dual satellite interfaces detected (%d) — failover possible", satInterfaces)
	} else {
		t.Logf("WARN: only %d satellite interface(s) — failover requires 2+", satInterfaces)
	}
}

// ---------------------------------------------------------------------------
// Full stack summary
// ---------------------------------------------------------------------------

func TestE2E_Summary(t *testing.T) {
	t.Log("=== MESHSAT-338 E2E Validation Summary ===")

	// Bridge health
	gateways := bridgeGet(t, "/api/gateways")
	if gwList, ok := gateways["gateways"].([]interface{}); ok {
		t.Logf("bridge gateways: %d running", len(gwList))
	}

	sig := bridgeGet(t, "/api/iridium/signal/fast")
	t.Logf("satellite signal: bars=%v source=%v", sig["bars"], sig["source"])

	routing := bridgeGet(t, "/api/routing/status")
	t.Logf("reticulum: identity=%v routes=%v", routing["identity_hash"], routing["route_count"])

	if hubAPIKey != "" {
		bridges := httpGetArray(t, hubURL+"/api/bridges", hubAPIKey)
		onlineCount := 0
		for _, brRaw := range bridges {
			if br, ok := brRaw.(map[string]interface{}); ok {
				if online, _ := br["online"].(bool); online {
					onlineCount++
				}
			}
		}
		t.Logf("hub: %d/%d bridges online", onlineCount, len(bridges))
	}

	t.Log("=== End Summary ===")
}
