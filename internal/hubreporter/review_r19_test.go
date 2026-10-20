package hubreporter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReviewR19BootstrapRejectsHTTPSDowngrade(t *testing.T) {
	k := newTestKey(t)
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"key_id": KeyID(k.pkix), "pkix": k.pkix, "alg": "ES256"})
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer secure.Close()
	saved := directoryKeyClient
	directoryKeyClient = secure.Client()
	defer func() { directoryKeyClient = saved }()
	got, err := FetchDirectoryKey(t.Context(), secure.URL)
	t.Logf("key accepted from redirected HTTP endpoint=%v error=%v", len(got) > 0, err)
	if err == nil {
		t.Fatal("HTTPS bootstrap accepted key fetched over unauthenticated HTTP")
	}
}
