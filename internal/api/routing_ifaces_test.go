package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"meshsat/internal/routing"
)

func dynIfaceCall(t *testing.T, h http.Handler, method, path, body string) (int, routing.DynIfaceStatus, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var st routing.DynIfaceStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	return rec.Code, st, rec.Body.String()
}

// The RNS TCP card's writes as the apps make them: POST on the first Save with
// a host, PUT with only enabled for the switch, PUT with only the config for
// TLS and Save. Everything stays on 127.0.0.1.
func TestDynIfacesTCPRNSOverHTTP(t *testing.T) {
	s := newTestServerWithDB(t)
	m := routing.NewIfaceManager(routing.IfaceManagerConfig{DB: s.db})
	if err := m.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.StopAll()
	s.SetIfaceManager(m)
	h := s.Router()

	// A Reticulum node that accepts and says nothing.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	plain := `{"host":"127.0.0.1","port":` + port + `,"tls":false}`
	withTLS := `{"host":"127.0.0.1","port":` + port + `,"tls":true}`

	for _, bad := range []struct{ body, want string }{
		{`{"type":"tcp_rns","config":{"host":""}}`, `{"error":"tcp_rns: host required"}`},
		{`{"type":"tcp_rns","config":{"host":"127.0.0.1","port":70000}}`, `{"error":"tcp_rns: port must be 1-65535"}`},
	} {
		if code, _, body := dynIfaceCall(t, h, http.MethodPost, "/api/routing/ifaces", bad.body); code != http.StatusBadRequest || strings.TrimSpace(body) != bad.want {
			t.Fatalf("POST %s: %d %s", bad.body, code, body)
		}
	}

	// First Save with the switch off: the record exists, stopped.
	code, st, body := dynIfaceCall(t, h, http.MethodPost, "/api/routing/ifaces", `{"type":"tcp_rns","enabled":false,"config":{"host":"127.0.0.1","port":`+port+`}}`)
	if code != http.StatusCreated || st.ID != "tcp_rns_0" || st.Type != "tcp_rns" || st.Enabled || st.Running || string(st.Config) != plain {
		t.Fatalf("POST: %d %s", code, body)
	}
	// The switch on: PUT without config keeps it.
	code, st, body = dynIfaceCall(t, h, http.MethodPut, "/api/routing/ifaces/tcp_rns_0", `{"enabled":true}`)
	if code != http.StatusOK || !st.Enabled || !st.Running || string(st.Config) != plain || st.Summary != "127.0.0.1:"+port {
		t.Fatalf("PUT enabled: %d %s", code, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !st.Online && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		_, st, body = dynIfaceCall(t, h, http.MethodGet, "/api/routing/ifaces/tcp_rns_0", "")
	}
	if !st.Online || st.LastError != "" {
		t.Fatalf("not connected: %s", body)
	}
	// TLS on: PUT without enabled keeps the switch.
	code, st, body = dynIfaceCall(t, h, http.MethodPut, "/api/routing/ifaces/tcp_rns_0", `{"config":`+withTLS+`}`)
	if code != http.StatusOK || !st.Enabled || !st.Running || string(st.Config) != withTLS || st.Summary != "127.0.0.1:"+port+", TLS" {
		t.Fatalf("PUT config: %d %s", code, body)
	}
	// The switch off: stopped, the config kept.
	code, st, body = dynIfaceCall(t, h, http.MethodPut, "/api/routing/ifaces/tcp_rns_0", `{"enabled":false}`)
	if code != http.StatusOK || st.Enabled || st.Running || st.Online || string(st.Config) != withTLS {
		t.Fatalf("PUT disabled: %d %s", code, body)
	}
	// A bad config on PUT is refused and the stored one kept.
	if code, _, body = dynIfaceCall(t, h, http.MethodPut, "/api/routing/ifaces/tcp_rns_0", `{"config":{"host":" "}}`); code != http.StatusBadRequest || !strings.Contains(body, "tcp_rns: host required") {
		t.Fatalf("PUT bad config: %d %s", code, body)
	}
	if _, st, body = dynIfaceCall(t, h, http.MethodGet, "/api/routing/ifaces/tcp_rns_0", ""); string(st.Config) != withTLS || st.Enabled {
		t.Fatalf("after a refused PUT: %s", body)
	}
}
