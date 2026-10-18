package hubreporter

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type hubFixture struct {
	Name      string          `json:"name"`
	PublicKey []byte          `json:"public_key"`
	Payload   json.RawMessage `json:"payload"`
}

func loadHubFixtures(t *testing.T) []hubFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/hub-directory-push-a98e938.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx []hubFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx) != 3 {
		t.Fatalf("%d fixtures; want 3", len(fx))
	}
	return fx
}

// The bridge-hub contract for directory_push (B5-R2): payloads made by
// the Hub's own types, SignSnapshot and DirectoryPushCommand at
// meshsat-hub 3b6413e (generator in the B5 review bundle; ephemeral key,
// public half only). One of them sets every field of every Hub type,
// including contact metadata, keys, groups and policy fields the bridge
// does not use. Each verifies, reaches the applier intact, and the reply
// carries the per-row report. [MESHSAT-1584]
func TestDirectoryPush_HubSignedContract(t *testing.T) {
	for _, fx := range loadHubFixtures(t) {
		t.Run(fx.Name, func(t *testing.T) {
			ch, app, anchor := newHandlerWithDeps(t)
			anchor.pub = fx.PublicKey
			app.report = DirectoryApplyReport{PoliciesApplied: []string{"pol-supported-default"},
				PoliciesRejected: []PolicyRejection{{ID: "pol-unsupported-flash", Reason: "unsupported_strategy"}}}
			resp, err := ch.handleDirectoryPush(Command{Cmd: "directory_push", RequestID: fx.Name, Payload: fx.Payload})
			if err != nil {
				t.Fatalf("Hub-signed snapshot refused: %v", err)
			}
			// Nothing of the Hub's payload was lost on the way to the
			// applier: marshalled again it is the payload byte for byte.
			again, _ := json.Marshal(app.applied)
			var compact bytes.Buffer
			_ = json.Compact(&compact, fx.Payload)
			if !bytes.Equal(again, compact.Bytes()) {
				t.Fatalf("snapshot changed between the Hub and the applier:\n hub %s\n got %s", compact.Bytes(), again)
			}
			if !strings.Contains(string(resp), `"policies_rejected":[{"id":"pol-unsupported-flash"`) {
				t.Fatalf("reply %s", resp)
			}
		})
	}
}

// Fail closed stays fail closed: a changed metadata value, a policy
// switched to another strategy, or another anchor is refused, and the
// applier never runs.
func TestDirectoryPush_HubSignedContractTampered(t *testing.T) {
	fx := loadHubFixtures(t)[2]
	cases := map[string]func(string) string{
		"metadata": func(p string) string { return strings.Replace(p, `"callsign":"MS-B5"`, `"callsign":"MS-XX"`, 1) },
		"strategy": func(p string) string {
			return strings.Replace(p, `"strategy":"HEMB_BONDED"`, `"strategy":"ALL_BEARERS"`, 1)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			ch, app, anchor := newHandlerWithDeps(t)
			anchor.pub = fx.PublicKey
			changed := mutate(string(fx.Payload))
			if changed == string(fx.Payload) {
				t.Fatal("mutation did not apply")
			}
			if _, err := ch.handleDirectoryPush(Command{Cmd: "directory_push", Payload: json.RawMessage(changed)}); err == nil || app.applied != nil {
				t.Fatalf("tampered snapshot: err=%v applied=%v", err, app.applied != nil)
			}
		})
	}
	t.Run("other_anchor", func(t *testing.T) {
		ch, app, anchor := newHandlerWithDeps(t)
		_, other, _ := newSignedSnapshot(t)
		anchor.pub = other
		if _, err := ch.handleDirectoryPush(Command{Cmd: "directory_push", Payload: fx.Payload}); err == nil || app.applied != nil {
			t.Fatalf("other anchor: err=%v applied=%v", err, app.applied != nil)
		}
	})
}

// A Hub newer than the bridge signs a field the bridge's types do not
// carry: the push is still refused, and the error says why.
func TestDirectoryPush_UnknownHubFieldNamed(t *testing.T) {
	ch, app, anchor := newHandlerWithDeps(t)
	snap, pubPKIX, priv := newSignedSnapshot(t)
	anchor.pub = pubPKIX
	snap.Signature = nil
	canonical, _ := json.Marshal(snap)
	signed := bytes.Replace(canonical, []byte(`"display_name"`), []byte(`"future_field":"x","display_name"`), 1)
	digest := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	b64, _ := json.Marshal(sig)
	payload := bytes.Replace(signed, []byte(`"contacts"`), append(append([]byte(`"signature":`), b64...), []byte(`,"contacts"`)...), 1)
	_, err = ch.handleDirectoryPush(Command{Cmd: "directory_push", Payload: payload})
	if err == nil || app.applied != nil || !strings.Contains(err.Error(), `unknown field "future_field"`) {
		t.Fatalf("err=%v applied=%v; want a refusal naming future_field", err, app.applied != nil)
	}
}
