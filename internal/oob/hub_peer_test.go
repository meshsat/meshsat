package oob

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

// A Hub-provisioned management key (key_rotate, channel_type "mgmt") must
// leave a usable peer behind: control role, importer on this side, enabled,
// and the id derived from the key so HandleInbound finds it. [MESHSAT-964]
func TestRegisterHubPeer_CreatesControlPeer(t *testing.T) {
	h := newHarness(t, RoleControl, false)
	key := bytes.Repeat([]byte{0xA5}, KeyLen)
	if _, err := h.keys.StoreKey("mgmt", "hub", key); err != nil {
		t.Fatal(err)
	}

	p, err := h.svc.RegisterHubPeer("hub", key)
	if err != nil {
		t.Fatal(err)
	}
	if p.PeerID != PeerIDFromKey(key) {
		t.Errorf("peer id %d, want the id derived from the key %d", p.PeerID, PeerIDFromKey(key))
	}
	if p.Alias != "hub" || p.Role != RoleControl || !p.Enabled || p.LocalRole != int(RoleImporter) {
		t.Errorf("peer: %+v", p)
	}
	if p.KeyRef != "mgmt:hub" {
		t.Errorf("key ref %q, want mgmt:hub", p.KeyRef)
	}
}

// A rotation keeps the alias and changes the derived id: the old row goes,
// so the alias never points at a key the store no longer holds.
func TestRegisterHubPeer_RotationReplacesTheRow(t *testing.T) {
	h := newHarness(t, RoleControl, false)
	first := bytes.Repeat([]byte{0x11}, KeyLen)
	second := bytes.Repeat([]byte{0x22}, KeyLen)

	p1, err := h.svc.RegisterHubPeer("hub", first)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := h.svc.RegisterHubPeer("hub", second)
	if err != nil {
		t.Fatal(err)
	}
	if p1.PeerID == p2.PeerID {
		t.Fatal("the derived id must change with the key")
	}
	if _, err := h.db.GetOOBPeer(p1.PeerID); err == nil {
		t.Error("the old peer row survived the rotation")
	}
	peers, err := h.db.ListOOBPeers()
	if err != nil {
		t.Fatal(err)
	}
	hubs := 0
	for _, p := range peers {
		if p.Alias == "hub" {
			hubs++
		}
	}
	if hubs != 1 {
		t.Errorf("expected exactly one hub peer after the rotation, got %d: %+v", hubs, peers)
	}
}

// Registering the same key twice is idempotent and re-enables a peer an
// operator had disabled, so a Hub re-provision always lands somewhere usable.
func TestRegisterHubPeer_Idempotent(t *testing.T) {
	h := newHarness(t, RoleControl, false)
	key := bytes.Repeat([]byte{0x33}, KeyLen)
	p, err := h.svc.RegisterHubPeer("hub", key)
	if err != nil {
		t.Fatal(err)
	}
	p.Enabled = false
	p.Role = RoleReadonly
	if err := h.db.UpdateOOBPeer(p); err != nil {
		t.Fatal(err)
	}
	again, err := h.svc.RegisterHubPeer("hub", key)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Enabled || again.Role != RoleControl {
		t.Errorf("re-provision did not restore control: %+v", again)
	}
}

func TestRegisterHubPeer_RejectsBadInput(t *testing.T) {
	h := newHarness(t, RoleControl, false)
	if _, err := h.svc.RegisterHubPeer("", bytes.Repeat([]byte{1}, KeyLen)); err == nil {
		t.Error("empty alias must be refused")
	}
	if _, err := h.svc.RegisterHubPeer("hub", []byte{1, 2, 3}); err == nil {
		t.Error("short key must be refused")
	}
}

// ExportKey hands the raw key to an operator pairing a counterpart that
// takes a key rather than a bundle URL (the Hub).
func TestExportKey(t *testing.T) {
	h := newHarness(t, RoleControl, false)
	key := bytes.Repeat([]byte{0x5C}, KeyLen)
	if _, err := h.keys.StoreKey("mgmt", "hub", key); err != nil {
		t.Fatal(err)
	}
	p, err := h.svc.RegisterHubPeer("hub", key)
	if err != nil {
		t.Fatal(err)
	}
	alias, keyHex, err := h.svc.ExportKey(p.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	if alias != "hub" {
		t.Errorf("alias %q", alias)
	}
	if got, _ := hex.DecodeString(keyHex); !bytes.Equal(got, key) {
		t.Errorf("exported key does not round trip: %s", keyHex)
	}
	if strings.ToLower(keyHex) != keyHex {
		t.Errorf("key hex should be lower case for pasting: %s", keyHex)
	}
}

// A reply sized for the 9603 must stay bare printable text under 120
// characters: that is exactly what canSendPlaintext (internal/gateway,
// iridium.go) requires before it sends an SBD MO as text. Over that limit
// the gateway wraps the frame in the compact binary envelope and the Hub
// cannot read it. [MESHSAT-964]
func TestSBDReplyStaysBareText(t *testing.T) {
	key := bytes.Repeat([]byte{0x7E}, KeyLen)
	body := strings.Repeat("W", BearerBudget("iridium_0")-ReplyHeaderLen)
	wire, err := Seal(Frame{
		Reply:   true,
		PeerID:  0xBEEF,
		Counter: MaxCounter - 1,
		Cmd:     CmdStatusNet,
		Args:    EncodeReplyArgs(ReplyArgs{RC: RCOK, ReqCounterLo: 0xFFFF, Seq: 1, Total: 1, Body: []byte(body)}),
	}, key, RoleImporter)
	if err != nil {
		t.Fatal(err)
	}
	text := Encode(wire)
	if len(text) > 120 {
		t.Fatalf("an SBD reply encodes to %d chars; canSendPlaintext caps text at 120, so this would be sent as compact binary", len(text))
	}
	if !strings.HasPrefix(text, Sentinel) {
		t.Fatalf("reply must carry the %q sentinel: %q", Sentinel, text)
	}
	for i := 0; i < len(text); i++ {
		if text[i] < 0x20 || text[i] > 0x7E {
			t.Fatalf("byte %d (%q) is not printable ASCII; canSendPlaintext would refuse the whole frame", i, text[i])
		}
	}
}
