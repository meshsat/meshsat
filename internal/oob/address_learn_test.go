package oob

import (
	"context"
	"encoding/json"
	"testing"

	"meshsat/internal/database"
)

func peerAddresses(t *testing.T, db *database.DB, id uint16) map[string]string {
	t.Helper()
	p, err := db.GetOOBPeer(id)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	if err := json.Unmarshal([]byte(p.Addresses), &m); err != nil {
		t.Fatalf("addresses %q: %v", p.Addresses, err)
	}
	return m
}

func (h *harness) auditCount(ev string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, a := range h.audits {
		if a == ev {
			n++
		}
	}
	return n
}

// parallax's radio came back from a reboot with a new node number and
// tesseract's stored mesh address went stale silently. An authenticated
// frame from the peer over mesh now moves the stored address to the node
// that actually sent it. [MESHSAT-1102]
func TestHandleInbound_LearnsPeerMeshAddress(t *testing.T) {
	h := newHarness(t, RoleReadonly, false)
	ctx := context.Background()
	if _, err := h.svc.UpdatePeer(h.peer.PeerID, PeerSpec{Addresses: map[string]string{
		"cellular_0": "+31653207829", "aprs_0": "PD0XYZ-7", "mesh_0": "!235779ff",
	}}, nil); err != nil {
		t.Fatal(err)
	}

	text := h.peerFrame(t, 1, CmdPing, nil, true, true)
	if !h.svc.HandleInbound(ctx, "mesh_0", "!402D9E7B", text) {
		t.Fatal("frame not consumed")
	}
	waitFor(t, func() bool { return h.auditCount("oob_command") == 1 })
	got := peerAddresses(t, h.db, h.peer.PeerID)
	if got["mesh_0"] != "!402d9e7b" || got["cellular_0"] != "+31653207829" || got["aprs_0"] != "PD0XYZ-7" {
		t.Fatalf("addresses after learning: %v", got)
	}
	entries, _ := h.db.ListOOBLog(20, int(h.peer.PeerID))
	learned := 0
	for _, e := range entries {
		if e.Kind == "address_learn" && e.Detail == "!235779ff -> !402d9e7b" && e.Bearer == "mesh_0" {
			learned++
		}
	}
	if learned != 1 || h.auditCount("oob_address_learn") != 1 {
		t.Fatalf("address_learn log rows %d, audits %d, want 1 and 1", learned, h.auditCount("oob_address_learn"))
	}

	// The same frame replayed from another node is rejected before learning.
	if !h.svc.HandleInbound(ctx, "mesh_0", "!11111111", text) {
		t.Fatal("replay not consumed")
	}
	// A frame sealed with the wrong key is rejected before learning.
	wrong := make([]byte, len(h.key))
	wrong[0] = 0x42
	wire, err := Seal(Frame{Enc: true, NoReply: true, PeerID: h.peer.PeerID, Counter: 5, Cmd: CmdPing}, wrong, Role(h.peer.LocalRole).Other())
	if err != nil {
		t.Fatal(err)
	}
	if !h.svc.HandleInbound(ctx, "mesh_0", "!22222222", Encode(wire)) {
		t.Fatal("bad-tag frame not consumed")
	}
	// A valid SMS frame from another number never touches the SMS address.
	sms := h.peerFrame(t, 2, CmdPing, nil, true, true)
	if !h.svc.HandleInbound(ctx, "cellular_0", "+31600000000", sms) {
		t.Fatal("SMS frame not consumed")
	}
	waitFor(t, func() bool { return h.auditCount("oob_command") == 2 })

	got = peerAddresses(t, h.db, h.peer.PeerID)
	if got["mesh_0"] != "!402d9e7b" || got["cellular_0"] != "+31653207829" {
		t.Fatalf("addresses changed by a rejected or non-mesh frame: %v", got)
	}
}

// No mesh address stored: nothing is learned, so a frame typed on a handheld
// does not invent a mesh route for the peer.
func TestHandleInbound_NoStoredMeshAddressLearnsNothing(t *testing.T) {
	h := newHarness(t, RoleReadonly, false)
	text := h.peerFrame(t, 1, CmdPing, nil, true, true)
	if !h.svc.HandleInbound(context.Background(), "mesh_0", "!402d9e7b", text) {
		t.Fatal("frame not consumed")
	}
	waitFor(t, func() bool { return h.auditCount("oob_command") == 1 })
	if got := peerAddresses(t, h.db, h.peer.PeerID); got["mesh_0"] != "" {
		t.Fatalf("mesh address invented: %v", got)
	}
}

// The update only applies while the stored value is still the one the frame
// saw, so an operator's concurrent PUT is never overwritten.
func TestSetOOBPeerAddress_CompareAndSwap(t *testing.T) {
	h := newHarness(t, RoleReadonly, false)
	id := h.peer.PeerID
	changed, err := h.db.SetOOBPeerAddress(id, "cellular_0", "+31600000000", "+31611111111")
	if err != nil || changed {
		t.Fatalf("stale old value: changed %v err %v, want false nil", changed, err)
	}
	if got := peerAddresses(t, h.db, id); got["cellular_0"] != "+31653207829" {
		t.Fatalf("stale compare overwrote the address: %v", got)
	}
	changed, err = h.db.SetOOBPeerAddress(id, "cellular_0", "+31653207829", "+31611111111")
	if err != nil || !changed {
		t.Fatalf("matching old value: changed %v err %v, want true nil", changed, err)
	}
	if got := peerAddresses(t, h.db, id); got["cellular_0"] != "+31611111111" || got["aprs_0"] != "PD0XYZ-7" {
		t.Fatalf("addresses after swap: %v", got)
	}
	if _, err := h.db.SetOOBPeerAddress(id, `mesh_0"].x`, "a", "b"); err == nil {
		t.Fatal("invalid interface id accepted")
	}
}
