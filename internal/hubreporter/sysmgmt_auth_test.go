package hubreporter

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The Hub-command shim presents the process-local API token when one is
// installed, and no header at all when none is (today's call), so a Hub
// command still reaches the local handlers with the API token on and the
// loopback trust off. [MESHSAT-1738]
func TestCallLocalAPICarriesTheProcessToken(t *testing.T) {
	var got []string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer stub.Close()
	t.Setenv("MESHSAT_LOCAL_API_BASE", stub.URL)

	SetLocalAPIAuth(nil)
	t.Cleanup(func() { SetLocalAPIAuth(nil) })
	if _, err := callLocalAPI(http.MethodGet, "/api/system/bluetooth/status", nil); err != nil {
		t.Fatal(err)
	}
	SetLocalAPIAuth(func() string { return "" })
	if _, err := callLocalAPI(http.MethodGet, "/api/system/bluetooth/status", nil); err != nil {
		t.Fatal(err)
	}
	SetLocalAPIAuth(func() string { return "process-local-token" })
	if _, err := callLocalAPI(http.MethodGet, "/api/system/bluetooth/status", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"", "", "Bearer process-local-token"}
	if len(got) != len(want) {
		t.Fatalf("calls %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call %d: Authorization %q, want %q", i, got[i], want[i])
		}
	}
}
