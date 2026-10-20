package hubreporter

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestEnvelopeFixtureOnTheBridge: the bridge's verifier agrees with the
// reference on every case of the pinned C7 fixture (fixtures-v1.1).
func TestEnvelopeFixtureOnTheBridge(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "hembfixtures", "testdata", "v1", "directory_v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Keys map[string]struct {
			KeyID   string `json:"key_id"`
			PKIXB64 string `json:"pkix_b64"`
		} `json:"keys"`
		Cases []struct {
			Case       string          `json:"case"`
			TrustedKey string          `json:"trusted_key"`
			Envelope   json.RawMessage `json:"envelope"`
			Verify     bool            `json:"verify"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) != 6 {
		t.Fatalf("%d cases", len(fx.Cases))
	}
	for _, c := range fx.Cases {
		der, _ := base64.StdEncoding.DecodeString(fx.Keys[c.TrustedKey].PKIXB64)
		if KeyID(der) != fx.Keys[c.TrustedKey].KeyID {
			t.Fatalf("%s: fixture key id", c.Case)
		}
		_, err := OpenEnvelope(c.Envelope, der)
		if (err == nil) != c.Verify {
			t.Errorf("%s: verified=%v (%v), fixture says %v", c.Case, err == nil, err, c.Verify)
		}
	}
}

type testKey struct {
	priv *ecdsa.PrivateKey
	pkix []byte
}

func newTestKey(t *testing.T) testKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkix, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	return testKey{priv, pkix}
}

func (k testKey) seal(t *testing.T, payload []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(payload)
	sig, err := ecdsa.SignASN1(rand.Reader, k.priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	env, _ := json.Marshal(map[string]any{"v": 2, "key_id": KeyID(k.pkix), "alg": "ES256", "payload": payload, "sig": sig})
	return env
}

const v2Snapshot = `{"type":"snapshot","tenant_id":"t-1","version":12,"signed_at":"2026-10-20T12:00:00Z",
 "contacts":[{"id":"c-1","tenant_id":"t-1","display_name":"Kit B","origin":"hub:t-1","created_at":"2026-10-20T12:00:00Z","updated_at":"2026-10-20T12:00:00Z",
   "addresses":[{"kind":"MQTT","value":"bridge:kit-b","primary_rank":0,"origin":"hub:t-1"}]}],
 "groups":[{"id":"g-1","tenant_id":"t-1","name":"North","kind":"TEAM","member_ids":["c-1"],"origin":"hub:t-1","created_at":"2026-10-20T12:00:00Z","updated_at":"2026-10-20T12:00:00Z"}],
 "policies":[{"id":"p-1","scope_type":"precedence","scope_id":"Flash","strategy":"HEMB_BONDED","seeded":true,"origin":"hub:t-1","created_at":"2026-10-20T12:00:00Z","updated_at":"2026-10-20T12:00:00Z"},
   {"id":"p-2","scope_type":"default","strategy":"PRIMARY_ONLY","seeded":false,"origin":"hub:t-1","created_at":"2026-10-20T12:00:00Z","updated_at":"2026-10-20T12:00:00Z"}],
 "x_future_field":{"a":1}}`

func TestDirectoryPushV2(t *testing.T) {
	ch, app, anchor := newHandlerWithDeps(t)
	k := newTestKey(t)
	anchor.pub = k.pkix
	app.report = DirectoryApplyReport{Applied: true, AppliedVersion: 12}
	resp, err := ch.handleDirectoryPush(Command{Cmd: "directory_push", Payload: k.seal(t, []byte(v2Snapshot))})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	snap := app.applied
	if snap == nil || snap.Format != "v2" || snap.TenantID != "t-1" || snap.Version != 12 || len(snap.Contacts) != 1 || len(snap.Groups) != 1 {
		t.Fatalf("applied %+v", snap)
	}
	if p := snap.Policies; len(p) != 2 || p[0].Seeded == nil || !*p[0].Seeded || p[1].Seeded == nil || *p[1].Seeded {
		t.Errorf("seeded marks %+v", p)
	}
	var ans struct {
		TenantID       string `json:"tenant_id"`
		Version        int64  `json:"version"`
		Applied        bool   `json:"applied"`
		AppliedVersion int64  `json:"applied_version"`
		KeyID          string `json:"key_id"`
		Format         string `json:"format"`
	}
	if err := json.Unmarshal(resp, &ans); err != nil {
		t.Fatal(err)
	}
	if !ans.Applied || ans.Version != 12 || ans.AppliedVersion != 12 || ans.KeyID != KeyID(k.pkix) || ans.Format != "v2" || ans.TenantID != "t-1" {
		t.Errorf("answer %+v", ans)
	}

	// What must not reach the applier.
	other := newTestKey(t)
	tampered := k.seal(t, []byte(v2Snapshot))
	var env map[string]any
	_ = json.Unmarshal(tampered, &env)
	env["payload"] = base64.StdEncoding.EncodeToString([]byte(`{"type":"snapshot","tenant_id":"t-1","version":99}`))
	tampered, _ = json.Marshal(env)
	for name, payload := range map[string][]byte{
		"a payload changed after signing": tampered,
		"a rotation record":               k.seal(t, []byte(`{"type":"rotation","tenant_id":"t-1","version":13}`)),
	} {
		app.applied = nil
		if _, err := ch.handleDirectoryPush(Command{Cmd: "directory_push", Payload: payload}); err == nil || app.applied != nil {
			t.Errorf("%s: err=%v applied=%v", name, err, app.applied != nil)
		}
	}
	// Signed by a key the bridge does not trust (a replica that had not
	// reloaded the new key): not applied, and the answer names the key the
	// bridge trusts and what it holds (r19 C7-R1).
	app.applied = nil
	resp, err = ch.handleDirectoryPush(Command{Cmd: "directory_push", Payload: other.seal(t, []byte(v2Snapshot))})
	if err != nil || app.applied != nil {
		t.Fatalf("signed by another key: err=%v applied=%v", err, app.applied != nil)
	}
	var unknown struct {
		Applied        bool   `json:"applied"`
		Reason         string `json:"reason"`
		KeyID          string `json:"key_id"`
		AppliedVersion int64  `json:"applied_version"`
	}
	_ = json.Unmarshal(resp, &unknown)
	if unknown.Applied || unknown.Reason != RefuseUnknownKey || unknown.KeyID != KeyID(k.pkix) {
		t.Errorf("signed by another key: answer %+v", unknown)
	}
	// A refusal is an answer, not an error, and says what the bridge holds.
	app.report = DirectoryApplyReport{Applied: false, Reason: "not_newer", AppliedVersion: 20}
	resp, err = ch.handleDirectoryPush(Command{Cmd: "directory_push", Payload: k.seal(t, []byte(v2Snapshot))})
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(resp, &ans)
	if ans.Applied || ans.AppliedVersion != 20 {
		t.Errorf("refusal answer %+v", ans)
	}
}

func rotation(t *testing.T, signer testKey, tenant string, version int64, to testKey, keyID string) []byte {
	t.Helper()
	if keyID == "" {
		keyID = KeyID(to.pkix)
	}
	payload, _ := json.Marshal(map[string]any{"type": "rotation", "tenant_id": tenant, "version": version,
		"new_key_id": keyID, "new_key_pkix": to.pkix})
	return signer.seal(t, payload)
}

func TestDirectoryRotateTakesOnlyARecordSignedByTheTrustedKey(t *testing.T) {
	ch, app, anchor := newHandlerWithDeps(t)
	k1, k2 := newTestKey(t), newTestKey(t)
	anchor.pub = k1.pkix
	resp, err := ch.handleDirectoryRotate(Command{Cmd: "directory_rotate", Payload: rotation(t, k1, "t-1", 9, k2, "")})
	if err != nil {
		t.Fatal(err)
	}
	if app.rotTenant != "t-1" || app.rotVersion != 9 || string(app.rotKey) != string(k2.pkix) {
		t.Errorf("rotation applied as %s %d", app.rotTenant, app.rotVersion)
	}
	var ans struct {
		Applied bool   `json:"applied"`
		KeyID   string `json:"key_id"`
	}
	_ = json.Unmarshal(resp, &ans)
	if !ans.Applied || ans.KeyID != KeyID(k2.pkix) {
		t.Errorf("answer %+v", ans)
	}
	// A payload of another type that carries the rotation's fields: only
	// the type keeps it from moving the key (domain separation).
	disguised, _ := json.Marshal(map[string]any{"type": "snapshot", "tenant_id": "t-1", "version": 9,
		"new_key_id": KeyID(k2.pkix), "new_key_pkix": k2.pkix})
	for name, payload := range map[string][]byte{
		"a snapshot with a rotation's fields": k1.seal(t, disguised),
		"signed by the new key only":          rotation(t, k2, "t-1", 9, k2, ""),
		"a new key that misses its id":        rotation(t, k1, "t-1", 9, k2, KeyID(k1.pkix)),
		"a snapshot sent as a rotation":       k1.seal(t, []byte(v2Snapshot)),
		"not an envelope (the old command)":   []byte(`{"public_key":"AAAA","algorithm":"ecdsa-p256"}`),
	} {
		if name == "signed by the new key only" {
			continue // below: a key the bridge does not trust
		}
		app.rotKey = nil
		if _, err := ch.handleDirectoryRotate(Command{Cmd: "directory_rotate", Payload: payload}); err == nil || app.rotKey != nil {
			t.Errorf("%s: err=%v applied=%v", name, err, app.rotKey != nil)
		}
	}
	// Signed by a key the bridge does not trust: never applied; the answer
	// names the key it trusts, so a Hub that lost the answer to the record
	// before learns the bridge already moved (r19 C7-R1).
	for name, payload := range map[string][]byte{
		"signed by the new key only":    rotation(t, k2, "t-1", 9, k2, ""),
		"signed by a key nobody trusts": rotation(t, newTestKey(t), "t-1", 9, k2, ""),
	} {
		app.rotKey = nil
		resp, err := ch.handleDirectoryRotate(Command{Cmd: "directory_rotate", Payload: payload})
		var ans struct {
			Applied bool   `json:"applied"`
			Reason  string `json:"reason"`
			KeyID   string `json:"key_id"`
		}
		_ = json.Unmarshal(resp, &ans)
		if err != nil || app.rotKey != nil || ans.Applied || ans.Reason != RefuseUnknownKey || ans.KeyID != KeyID(k1.pkix) {
			t.Errorf("%s: err=%v applied=%v answer %+v", name, err, app.rotKey != nil, ans)
		}
	}
	// The store's refusal (not newer) is an answer that keeps the old key.
	app.rotReport = &DirectoryApplyReport{Applied: false, Reason: "not_newer", AppliedVersion: 10}
	resp, err = ch.handleDirectoryRotate(Command{Cmd: "directory_rotate", Payload: rotation(t, k1, "t-1", 9, k2, "")})
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(resp, &ans)
	if ans.Applied || ans.KeyID != KeyID(k1.pkix) {
		t.Errorf("refused rotation answer %+v", ans)
	}
}

func TestFetchDirectoryKey(t *testing.T) {
	k := newTestKey(t)
	answer := map[string]any{"key_id": KeyID(k.pkix), "pkix": k.pkix, "alg": "ES256"}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/relay/directory-key" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(answer)
	}))
	defer srv.Close()
	saved := directoryKeyClient
	directoryKeyClient = srv.Client()
	defer func() { directoryKeyClient = saved }()

	got, err := FetchDirectoryKey(t.Context(), srv.URL)
	if err != nil || string(got) != string(k.pkix) {
		t.Fatalf("%v", err)
	}
	if _, err := FetchDirectoryKey(t.Context(), "http://"+srv.Listener.Addr().String()); err == nil {
		t.Error("a plain http URL was used")
	}
	answer["key_id"] = KeyID(newTestKey(t).pkix)
	if _, err := FetchDirectoryKey(t.Context(), srv.URL); err == nil {
		t.Error("a key that does not hash to its id was taken")
	}
}
