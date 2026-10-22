package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"meshsat/internal/directory"
)

func directoryKeyB64(t *testing.T, curve elliptic.Curve) (string, []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(der), der
}

// The bundle's directory key is pinned from the Hub settings when none is,
// never over another one, never kept in the settings; a key that is not
// P-256 is refused before anything is saved. [MESHSAT-1642]
func TestHubSettings_BundleDirectoryKeyIsPinnedOnce(t *testing.T) {
	s := newTestServerWithDB(t)
	p384, _ := directoryKeyB64(t, elliptic.P384())
	if code, _ := hubCall(t, s, http.MethodPut, `{"url":"wss://mqtt-hub.example/mqtt","dir_sign_pub":"`+p384+`"}`); code != http.StatusBadRequest {
		t.Fatalf("a P-384 key: %d, want 400", code)
	}
	if code, _ := hubCall(t, s, http.MethodPut, `{"dir_sign_pub":"not base64!"}`); code != http.StatusBadRequest {
		t.Fatalf("not base64: %d, want 400", code)
	}
	if _, got := hubCall(t, s, http.MethodGet, ""); got["url"] != nil && got["url"] != "" {
		t.Fatalf("a refused PUT saved settings: %v", got)
	}

	first, firstDER := directoryKeyB64(t, elliptic.P256())
	code, got := hubCall(t, s, http.MethodPut, `{"url":"wss://mqtt-hub.example/mqtt","dir_sign_pub":"`+first+`"}`)
	if code != 200 || strings.Contains(got["warning"].(string), "not pinned") {
		t.Fatalf("first key: %d %v", code, got)
	}
	if v, _ := s.db.GetSystemConfig(directory.RotationKeyConfig); v != hex.EncodeToString(firstDER) {
		t.Fatalf("pinned %q", v)
	}
	if raw, _ := s.db.GetSystemConfig(hubConfigKey); strings.Contains(raw, "dir_sign_pub") || strings.Contains(raw, first) {
		t.Fatal("the key went into the Hub settings")
	}

	// The same key again changes nothing and says nothing.
	if _, got := hubCall(t, s, http.MethodPut, `{"dir_sign_pub":"`+first+`"}`); strings.Contains(got["warning"].(string), "not pinned") {
		t.Fatalf("same key: %v", got)
	}
	// Another key is not pinned over it, and the answer says so.
	other, _ := directoryKeyB64(t, elliptic.P256())
	code, got = hubCall(t, s, http.MethodPut, `{"dir_sign_pub":"`+other+`"}`)
	if code != 200 || !strings.Contains(got["warning"].(string), "was not pinned") {
		t.Fatalf("another key: %d %v", code, got)
	}
	if v, _ := s.db.GetSystemConfig(directory.RotationKeyConfig); v != hex.EncodeToString(firstDER) {
		t.Fatalf("the pinned key was replaced: %q", v)
	}
}
