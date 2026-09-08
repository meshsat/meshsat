package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshsat/internal/database"
)

func ttcCall(t *testing.T, s *Server, method, path, body string) (int, ttcFlowStatus, string) {
	t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, req)
	var st ttcFlowStatus
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	return w.Code, st, w.Body.String()
}

func ttcEnabledPaths(st ttcFlowStatus) []string {
	var out []string
	for _, p := range ttcPaths {
		if r, ok := st.Rules[p]; ok && r.Enabled {
			out = append(out, p)
		}
	}
	return out
}

// Setup creates the contacts and the three egress rules, the inbound
// rules, and PUT enables exactly one path at a time.
func TestTTCFlow_SetupThenSelect(t *testing.T) {
	s := newTestServerWithDB(t)
	if err := s.db.InsertFailoverGroup(&database.FailoverGroup{ID: ttcPeerGroupID, Label: "peer", Mode: "priority"}); err != nil {
		t.Fatalf("group: %v", err)
	}

	code, st, body := ttcCall(t, s, "POST", "/api/ttc/flow/setup", `{"peer_number":"+31653207829","hub_number":"+3197010258258"}`)
	if code != http.StatusOK {
		t.Fatalf("setup: %d %s", code, body)
	}
	if len(st.Rules) != 4 {
		t.Fatalf("expected 4 rules after setup, got %d: %s", len(st.Rules), body)
	}
	if st.Rules["imt"].ForwardTo != ttcIMTIface || st.Rules["imt"].Contact != "" {
		t.Errorf("imt rule: %+v", st.Rules["imt"])
	}
	if st.IMT == nil || st.IMT.Running || st.IMT.Interface != ttcIMTIface {
		t.Errorf("imt status without a gateway manager: %+v", st.IMT)
	}
	if st.Rules["aprs"].ForwardTo != ttcPeerGroupID {
		t.Errorf("aprs rule should forward to %s, got %s", ttcPeerGroupID, st.Rules["aprs"].ForwardTo)
	}
	if st.Rules["b2b_sms"].Contact != "+31653207829" || st.Rules["hub_sms"].Contact != "+3197010258258" {
		t.Errorf("sms contacts not resolved: %+v", st.Rules)
	}
	if st.Path != "" || len(ttcEnabledPaths(st)) != 0 {
		t.Errorf("no path should be enabled right after setup: %s", body)
	}
	all, _ := s.db.GetAllAccessRules()
	inbound := 0
	for _, r := range all {
		if r.Direction == "ingress" && r.ForwardTo == "mesh_0" && r.Enabled && (r.InterfaceID == "aprs_0" || r.InterfaceID == "cellular_0" || r.InterfaceID == ttcIMTIface) {
			inbound++
		}
		if strings.HasPrefix(r.Name, "ttc:") && r.InterfaceID == "mesh_0" && r.Direction != "ingress" {
			t.Errorf("relay rule %s must be an ingress rule on mesh_0, got %s", r.Name, r.Direction)
		}
	}
	if inbound != 3 {
		t.Errorf("expected three enabled inbound rules, got %d", inbound)
	}

	for _, p := range []string{"hub_sms", "aprs", "imt", "b2b_sms"} {
		code, st, body = ttcCall(t, s, "PUT", "/api/ttc/flow", `{"path":"`+p+`"}`)
		if code != http.StatusOK {
			t.Fatalf("select %s: %d %s", p, code, body)
		}
		if got := ttcEnabledPaths(st); len(got) != 1 || got[0] != p {
			t.Errorf("select %s: enabled %v", p, got)
		}
		if p == "imt" {
			// No gateway manager in the test: the one issue is the modem note.
			if st.Path != p || len(st.Issues) != 1 || !strings.Contains(st.Issues[0], "9704") {
				t.Errorf("select imt: path=%s issues=%v", st.Path, st.Issues)
			}
			continue
		}
		if st.Path != p || !st.Ready {
			t.Errorf("select %s: path=%s ready=%v issues=%v", p, st.Path, st.Ready, st.Issues)
		}
	}
	code, st, _ = ttcCall(t, s, "GET", "/api/ttc/flow", "")
	if code != http.StatusOK || st.Path != "b2b_sms" {
		t.Errorf("GET after select: %d path=%s", code, st.Path)
	}
	// Setup again is idempotent: still three rules, same IDs, selection kept.
	ids := map[string]int64{}
	for p, r := range st.Rules {
		ids[p] = r.ID
	}
	_, st2, _ := ttcCall(t, s, "POST", "/api/ttc/flow/setup", `{}`)
	for p, r := range st2.Rules {
		if ids[p] != r.ID {
			t.Errorf("setup twice changed rule %s id %d -> %d", p, ids[p], r.ID)
		}
	}
	if st2.Path != "b2b_sms" {
		t.Errorf("setup twice lost the selection: %s", st2.Path)
	}
	contacts, _ := s.db.GetSMSContacts()
	if len(contacts) != 2 {
		t.Errorf("expected 2 contacts after two setups, got %d", len(contacts))
	}
}

// The MESHSAT-857 relay rule (mesh_0 -> peer_link, not ttc-named) is
// adopted as ttc:aprs, keeping its ID and filters.
func TestTTCFlow_AdoptsExistingPeerLinkRule(t *testing.T) {
	s := newTestServerWithDB(t)
	_ = s.db.InsertFailoverGroup(&database.FailoverGroup{ID: ttcPeerGroupID, Label: "peer", Mode: "priority"})
	id, err := s.db.InsertAccessRule(&database.AccessRule{InterfaceID: "mesh_0", Direction: "ingress", Priority: 1,
		Name: "booth mesh to peer (APRS first, SMS fallback)", Enabled: true, Action: "forward", ForwardTo: ttcPeerGroupID,
		Filters: `{"portnums":"[1]"}`, ForwardOptions: "{}", RateLimitPerMin: 6, RateLimitWindow: 60})
	if err != nil {
		t.Fatal(err)
	}
	code, st, body := ttcCall(t, s, "PUT", "/api/ttc/flow", `{"path":"aprs"}`)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if st.Rules["aprs"].ID != id {
		t.Errorf("adopted rule id %d, want %d", st.Rules["aprs"].ID, id)
	}
	r, _ := s.db.GetAccessRule(id)
	if r.Name != "ttc:aprs" || r.Filters != `{"portnums":"[1]"}` {
		t.Errorf("adopted rule not renamed or filters lost: %+v", r)
	}
	// Without numbers the SMS paths cannot be built and say so.
	if _, ok := st.Rules["hub_sms"]; ok {
		t.Errorf("hub_sms should not exist without a Hub number")
	}
	if code, _, body := ttcCall(t, s, "PUT", "/api/ttc/flow", `{"path":"hub_sms"}`); code != http.StatusBadRequest {
		t.Errorf("selecting a path without a rule should be 400, got %d %s", code, body)
	}
}

func TestTTCFlow_RejectsUnknownPath(t *testing.T) {
	s := newTestServerWithDB(t)
	if code, _, _ := ttcCall(t, s, "PUT", "/api/ttc/flow", `{"path":"pigeon"}`); code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", code)
	}
	if code, _, _ := ttcCall(t, s, "POST", "/api/ttc/flow/setup", `{"peer_number":"0653207829"}`); code != http.StatusBadRequest {
		t.Errorf("expected 400 for a number without +, got %d", code)
	}
}

// An old ttc:imt rule that points elsewhere is repointed at the IMT interface.
func TestTTCFlow_IMTRuleRepointed(t *testing.T) {
	s := newTestServerWithDB(t)
	id, err := s.db.InsertAccessRule(&database.AccessRule{InterfaceID: "mesh_0", Direction: "ingress", Priority: 1,
		Name: "ttc:imt", Enabled: false, Action: "forward", ForwardTo: "iridium_0", Filters: "{}", ForwardOptions: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	code, st, body := ttcCall(t, s, "POST", "/api/ttc/flow/setup", `{}`)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if st.Rules["imt"].ID != id || st.Rules["imt"].ForwardTo != ttcIMTIface {
		t.Errorf("imt rule not repointed: %+v", st.Rules["imt"])
	}
}
