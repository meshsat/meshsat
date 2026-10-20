package hubreporter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestFetchDirectoryKeyFollowsNoRedirect (r19 C7-R4): the key is taken only
// from the https URL the bridge was given. A direct answer is taken; an
// http URL, an https answer that redirects to http, to another https host,
// or to another path on the same host is refused, and the server the
// redirect names is never asked.
func TestFetchDirectoryKeyFollowsNoRedirect(t *testing.T) {
	k := newTestKey(t)
	serveKey := func(hits *atomic.Int32) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"key_id": KeyID(k.pkix), "pkix": k.pkix, "alg": "ES256"})
		}
	}
	var plainHits, otherHits, hubHits atomic.Int32
	plain := httptest.NewServer(serveKey(&plainHits))
	defer plain.Close()
	other := httptest.NewTLSServer(serveKey(&otherHits))
	defer other.Close()
	var redirectTo atomic.Value
	hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if to, _ := redirectTo.Load().(string); to != "" && r.URL.Path == "/api/relay/directory-key" {
			http.Redirect(w, r, to, http.StatusFound)
			return
		}
		serveKey(&hubHits)(w, r)
	}))
	defer hub.Close()
	saved := directoryKeyClient
	directoryKeyClient = hub.Client() // trusts both TLS test servers (one test certificate)
	defer func() { directoryKeyClient = saved }()

	redirectTo.Store("")
	if got, err := FetchDirectoryKey(t.Context(), hub.URL+"/"); err != nil || string(got) != string(k.pkix) {
		t.Fatalf("direct https: %v", err)
	}
	for _, c := range []struct {
		name, url, to string
	}{
		{"an http URL", plain.URL, ""},
		{"an https URL without a host", "https:///api", ""},
		{"a redirect to http", hub.URL, plain.URL + "/api/relay/directory-key"},
		{"a redirect to another https host", hub.URL, other.URL + "/api/relay/directory-key"},
		{"a redirect on the same host", hub.URL, hub.URL + "/elsewhere"},
	} {
		redirectTo.Store(c.to)
		got, err := FetchDirectoryKey(t.Context(), c.url)
		if err == nil || got != nil {
			t.Errorf("%s: taken (%v)", c.name, err)
		}
		if c.to != "" && !strings.Contains(err.Error(), "redirect") {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if plainHits.Load() != 0 || otherHits.Load() != 0 {
		t.Errorf("the redirect targets were asked: http %d, other host %d", plainHits.Load(), otherHits.Load())
	}
}
