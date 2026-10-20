package hubreporter

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeApplier captures the snapshot passed to ApplySnapshot so the
// test can assert the handler unpacked the payload correctly.
type fakeApplier struct {
	mu         sync.Mutex
	applied    *DirectorySnapshot
	applyError error
	report     DirectoryApplyReport
	rotTenant  string
	rotVersion int64
	rotKey     []byte
	rotReport  *DirectoryApplyReport
}

func (f *fakeApplier) ApplySnapshot(_ context.Context, s *DirectorySnapshot) (DirectoryApplyReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = s
	return f.report, f.applyError
}

// ApplyRotation records the rotation and answers rotReport, applied by
// default with the record's version.
func (f *fakeApplier) ApplyRotation(_ context.Context, tenantID string, version int64, key []byte) (DirectoryApplyReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rotTenant, f.rotVersion, f.rotKey = tenantID, version, key
	if f.rotReport != nil {
		return *f.rotReport, nil
	}
	return DirectoryApplyReport{Applied: true, AppliedVersion: version}, nil
}

// State answers what the fake holds: the last snapshot and rotation
// versions it was given.
func (f *fakeApplier) State(context.Context) (DirectoryState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := DirectoryState{KeyVersion: f.rotVersion}
	if f.applied != nil {
		st.TenantID, st.Version = f.applied.TenantID, f.applied.Version
	}
	return st, nil
}

// memTrustAnchorStore is an in-memory TrustAnchorStore for tests.
type memTrustAnchorStore struct {
	pub []byte
}

func (m *memTrustAnchorStore) SetDirectoryTrustAnchor(p []byte) error { m.pub = p; return nil }
func (m *memTrustAnchorStore) GetDirectoryTrustAnchor() ([]byte, error) {
	return m.pub, nil
}

// signSnap signs the canonical form of snap (Signature field zeroed)
// with priv and stamps the result on snap.Signature. Mirrors
// meshsat-hub/internal/api.SignSnapshot so we can verify the bridge
// handler end-to-end without linking the Hub package.
func signSnap(t *testing.T, priv *ecdsa.PrivateKey, snap *DirectorySnapshot) {
	t.Helper()
	snap.Signature = nil
	canonical, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	digest := sha256.Sum256(canonical)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	snap.Signature = sig
}

// newSignedSnapshot builds a test snapshot signed by a freshly
// generated ECDSA-P256 keypair and returns both the snapshot and the
// PKIX-DER pubkey the bridge should pin.
func newSignedSnapshot(t *testing.T) (*DirectorySnapshot, []byte, *ecdsa.PrivateKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	pubPKIX, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("pkix: %v", err)
	}
	snap := &DirectorySnapshot{
		TenantID: "acme",
		Version:  7,
		SignedAt: time.Now().UTC().Truncate(time.Second),
		Contacts: []DirectoryContact{
			{
				ID:          "00000000-0000-4000-8000-000000000001",
				TenantID:    "acme",
				DisplayName: "Alice Kowalski",
				Team:        "Red",
				Role:        "Medic",
				Origin:      "hub",
				Addresses: []DirectoryAddress{
					{Kind: "SMS", Value: "+31612345678", PrimaryRank: 0, Label: "Mobile"},
					{Kind: "MESHTASTIC", Value: "!abcd1234", PrimaryRank: 1},
				},
				CreatedAt: time.Now().UTC().Truncate(time.Second),
				UpdatedAt: time.Now().UTC().Truncate(time.Second),
			},
		},
	}
	signSnap(t, priv, snap)
	return snap, pubPKIX, priv
}

func newHandlerWithDeps(t *testing.T) (*CommandHandler, *fakeApplier, *memTrustAnchorStore) {
	t.Helper()
	ch := &CommandHandler{
		handlers: make(map[string]func(cmd Command) (json.RawMessage, error)),
	}
	ch.handlers["directory_push"] = ch.handleDirectoryPush
	ch.handlers["directory_trust_anchor_rotate"] = ch.handleDirectoryTrustAnchorRotate
	ch.handlers["directory_rotate"] = ch.handleDirectoryRotate
	app := &fakeApplier{report: DirectoryApplyReport{Applied: true}}
	anchor := &memTrustAnchorStore{}
	ch.SetDirectoryApplier(app)
	ch.SetTrustAnchorStore(anchor)
	return ch, app, anchor
}

// --- MESHSAT-540 Bridge handler tests ------------------------------------

func TestDirectoryPush_HappyPath(t *testing.T) {
	ch, app, anchor := newHandlerWithDeps(t)
	snap, pubPKIX, _ := newSignedSnapshot(t)
	anchor.pub = pubPKIX

	payload, _ := json.Marshal(snap)
	cmd := Command{Cmd: "directory_push", Payload: payload, RequestID: "req-1"}
	resp, err := ch.handleDirectoryPush(cmd)
	if err != nil {
		t.Fatalf("handleDirectoryPush: %v", err)
	}
	if app.applied == nil {
		t.Fatal("applier not called")
	}
	if app.applied.TenantID != "acme" || app.applied.Version != 7 {
		t.Errorf("wrong snapshot applied: %+v", app.applied)
	}
	if len(app.applied.Contacts) != 1 || app.applied.Contacts[0].DisplayName != "Alice Kowalski" {
		t.Errorf("contacts not preserved through apply: %+v", app.applied.Contacts)
	}

	var result map[string]any
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("response decode: %v", err)
	}
	if result["applied"] != true {
		t.Errorf("response.applied: %v, want true", result["applied"])
	}
}

func TestDirectoryPush_SignatureMismatchRejected(t *testing.T) {
	ch, app, anchor := newHandlerWithDeps(t)
	snap, pubPKIX, _ := newSignedSnapshot(t)
	anchor.pub = pubPKIX

	// Tamper with payload after signing — signature no longer covers this.
	snap.Contacts[0].DisplayName = "TAMPERED"
	payload, _ := json.Marshal(snap)

	cmd := Command{Cmd: "directory_push", Payload: payload}
	_, err := ch.handleDirectoryPush(cmd)
	if err == nil {
		t.Fatal("expected signature verification failure, got nil")
	}
	if app.applied != nil {
		t.Error("applier invoked despite failed signature")
	}
}

func TestDirectoryPush_NoPinnedAnchorRejected(t *testing.T) {
	ch, app, anchor := newHandlerWithDeps(t)
	snap, _, _ := newSignedSnapshot(t)
	_ = anchor // leave empty

	payload, _ := json.Marshal(snap)
	cmd := Command{Cmd: "directory_push", Payload: payload}
	_, err := ch.handleDirectoryPush(cmd)
	if err == nil {
		t.Fatal("expected rejection without pinned anchor")
	}
	if app.applied != nil {
		t.Error("applier invoked without pinned anchor")
	}
}

func TestDirectoryPush_UnsignedSnapshotRejected(t *testing.T) {
	ch, _, anchor := newHandlerWithDeps(t)
	snap, pubPKIX, _ := newSignedSnapshot(t)
	anchor.pub = pubPKIX
	snap.Signature = nil // strip signature

	payload, _ := json.Marshal(snap)
	cmd := Command{Cmd: "directory_push", Payload: payload}
	_, err := ch.handleDirectoryPush(cmd)
	if err == nil {
		t.Fatal("expected unsigned rejection")
	}
}

func TestDirectoryPush_WrongAnchorRejected(t *testing.T) {
	ch, _, anchor := newHandlerWithDeps(t)
	snap, _, _ := newSignedSnapshot(t)

	// Pin a DIFFERENT key than the one that signed the snapshot.
	otherPriv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherPKIX, _ := x509.MarshalPKIXPublicKey(&otherPriv.PublicKey)
	anchor.pub = otherPKIX

	payload, _ := json.Marshal(snap)
	cmd := Command{Cmd: "directory_push", Payload: payload}
	_, err := ch.handleDirectoryPush(cmd)
	if err == nil {
		t.Fatal("expected wrong-anchor rejection")
	}
}

func TestDirectoryPush_ApplierErrorSurfaces(t *testing.T) {
	ch, app, anchor := newHandlerWithDeps(t)
	snap, pubPKIX, _ := newSignedSnapshot(t)
	anchor.pub = pubPKIX
	app.applyError = errors.New("simulated storage failure")

	payload, _ := json.Marshal(snap)
	cmd := Command{Cmd: "directory_push", Payload: payload}
	_, err := ch.handleDirectoryPush(cmd)
	if err == nil {
		t.Fatal("expected applier error to propagate")
	}
}

// The unsigned rotate is refused whatever it carries: anyone who could
// publish it could make the bridge trust their key (C7.6). [MESHSAT-1584]
func TestDirectoryTrustAnchorRotate_Refused(t *testing.T) {
	ch, _, anchor := newHandlerWithDeps(t)
	pinned, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	anchor.pub, _ = x509.MarshalPKIXPublicKey(&pinned.PublicKey)
	before := append([]byte(nil), anchor.pub...)
	newPriv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	newPKIX, _ := x509.MarshalPKIXPublicKey(&newPriv.PublicKey)
	payload, _ := json.Marshal(map[string]any{"public_key": newPKIX, "algorithm": "ecdsa-p256", "version": 2})
	if _, err := ch.handleDirectoryTrustAnchorRotate(Command{Payload: payload}); err == nil {
		t.Fatal("an unsigned rotate was taken")
	}
	if string(anchor.pub) != string(before) {
		t.Error("the pinned key moved")
	}
}

// The reply to a directory_push names each policy row applied and each
// refused, with the reason (HeMB-NG r3 D12, S2). [MESHSAT-1584]
func TestDirectoryPush_ReplyNamesRefusedPolicies(t *testing.T) {
	ch, app, anchor := newHandlerWithDeps(t)
	snap, pubPKIX, _ := newSignedSnapshot(t)
	anchor.pub = pubPKIX
	app.report = DirectoryApplyReport{
		Applied:         true,
		PoliciesApplied: []string{"p-ok"},
		PoliciesRejected: []PolicyRejection{{ID: "p-bonded", ScopeType: "precedence", ScopeID: "Flash",
			Strategy: "HEMB_BONDED", Reason: "unsupported_strategy", Detail: "HEMB_BONDED is not implemented by this bridge"}},
	}
	payload, _ := json.Marshal(snap)
	resp, err := ch.handleDirectoryPush(Command{Cmd: "directory_push", Payload: payload, RequestID: "req-2"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Applied          bool              `json:"applied"`
		PoliciesApplied  []string          `json:"policies_applied"`
		PoliciesRejected []PolicyRejection `json:"policies_rejected"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Applied || len(got.PoliciesApplied) != 1 || got.PoliciesApplied[0] != "p-ok" ||
		len(got.PoliciesRejected) != 1 || got.PoliciesRejected[0].ID != "p-bonded" || got.PoliciesRejected[0].Reason != "unsupported_strategy" {
		t.Fatalf("reply %s", resp)
	}
}
