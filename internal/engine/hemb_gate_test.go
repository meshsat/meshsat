package engine

import (
	"errors"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/hemb"
	"meshsat/internal/rules"
)

// With HeMB experimental and off, no dispatcher path sends a bonded frame:
// a rule that forwards to a bond group, SendViaBondGroup (POST
// /api/hemb/send) and ForwardHeMBFrame (the TUN adapter). Switched on, the
// same rule and frame go out again, which shows the switch is what stops
// them. [MESHSAT-1581]
func TestHeMBGate_OffSendsNothing(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")
	h.setOnline("mqtt_0")
	gw := h.addGateway("mqtt_0", "mqtt")
	if err := h.db.InsertBondGroup(&database.BondGroup{ID: "bond1", Label: "test bond"}); err != nil {
		t.Fatal(err)
	}
	if err := h.db.InsertBondMember(&database.BondMember{GroupID: "bond1", InterfaceID: "mqtt_0", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.InsertAccessRule(&database.AccessRule{InterfaceID: "mesh_0", Direction: "ingress", Name: "to the bond",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "bond1", Filters: "{}", QoSLevel: 1}); err != nil {
		t.Fatal(err)
	}
	h.loadRules(t)
	msg := rules.RouteMessage{Text: "bonded?", From: "!0000abcd", PortNum: 1}

	t.Setenv("MESHSAT_HEMB_EXPERIMENTAL", "")
	if n := h.dispatch.DispatchAccess("mesh_0", msg, []byte(msg.Text)); n != 0 {
		t.Errorf("rule to a bond group with HeMB off: %d sent, want 0", n)
	}
	if _, err := h.dispatch.SendViaBondGroup("bond1", []byte("x")); !errors.Is(err, hemb.ErrExperimentalOff) {
		t.Errorf("SendViaBondGroup with HeMB off: %v", err)
	}
	if err := h.dispatch.ForwardHeMBFrame("mqtt_0", []byte("x")); !errors.Is(err, hemb.ErrExperimentalOff) {
		t.Errorf("ForwardHeMBFrame with HeMB off: %v", err)
	}
	if n := len(gw.messages()); n != 0 {
		t.Fatalf("HeMB off: the bearer's gateway was handed %d frames, want 0", n)
	}

	t.Setenv("MESHSAT_HEMB_EXPERIMENTAL", "true")
	if err := h.dispatch.ForwardHeMBFrame("mqtt_0", []byte("x")); err != nil {
		t.Fatalf("ForwardHeMBFrame with HeMB on: %v", err)
	}
	if n := h.dispatch.DispatchAccess("mesh_0", msg, []byte(msg.Text)); n == 0 {
		t.Errorf("rule to a bond group with HeMB on sent nothing")
	}
	if n := len(gw.messages()); n < 2 {
		t.Fatalf("HeMB on: the bearer's gateway was handed %d frames, want the TUN frame and the bonded send", n)
	}
}
