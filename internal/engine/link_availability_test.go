package engine

import (
	"testing"

	"meshsat/internal/database"
)

// The links of a kit with no cell configured: cell_0 is on (the v60 seed),
// the cellular modem's gateway serves cellular_0, and sms_0 is Reticulum's
// packet interface over that same modem, with no device of its own.
var (
	kitCell  = database.Interface{ID: "cell_0", ChannelType: "cell", Label: "MeshSat cell SMS", Enabled: true, Config: "{}"}
	kitSMS   = database.Interface{ID: "sms_0", ChannelType: "sms", Label: "Reticulum SMS", Enabled: true, Config: "{}"}
	kitCellG = database.Interface{ID: "cellular_0", ChannelType: "cellular", Label: "Cellular SMS", Enabled: true, Config: "{}"}
	kitAPRS  = database.Interface{ID: "aprs_0", ChannelType: "aprs", Label: "APRS", Enabled: true, Config: "{}"}
)

// gateways is the gateway manager's InterfaceConnected for a test: the
// links whose gateway runs and is connected.
type gateways map[string]bool

func (g gateways) connected(id string) bool { return g[id] }

// cell_0 is device-less, so the interface manager reads it online whenever it
// is on; it carries nothing unless the cell gateway runs. It reads available
// only while that gateway is up, in the hop set and in a failover group.
// [MESHSAT-1678]
func TestCellReadsAvailableOnlyWhileItsGatewayRuns(t *testing.T) {
	r := newRebindRig(t, kitCell, kitAPRS)
	fr := NewFailoverResolver(r.db, r.m)
	gw := gateways{"aprs_0": true}
	fr.SetGatewayOnline(gw.connected)

	if up, known := fr.Online("cell_0"); up || !known {
		t.Errorf("cell_0 with no cell gateway: up=%v known=%v, want known absent", up, known)
	}
	r.db.InsertFailoverGroup(&database.FailoverGroup{ID: "texts", Label: "Cell first", Mode: "priority"})
	r.db.InsertFailoverMember(&database.FailoverMember{GroupID: "texts", InterfaceID: "cell_0", Priority: 1})
	r.db.InsertFailoverMember(&database.FailoverMember{GroupID: "texts", InterfaceID: "aprs_0", Priority: 2})
	if got := fr.Resolve("texts"); got != "aprs_0" {
		t.Errorf("group with no cell gateway resolved to %q, want aprs_0", got)
	}
	if got := fr.ResolveExcluding("texts", "none"); got != "aprs_0" {
		t.Errorf("ResolveExcluding with no cell gateway: %q, want aprs_0", got)
	}

	gw["cell_0"] = true // the cell is adopted and bound to its SMPP server
	if up, known := fr.Online("cell_0"); !up || !known {
		t.Errorf("cell_0 with its gateway up: up=%v known=%v, want up", up, known)
	}
	if got := fr.Resolve("texts"); got != "cell_0" {
		t.Errorf("group with the cell gateway up resolved to %q, want cell_0", got)
	}

	// No gateway manager at all: no cell gateway can run.
	bare := NewFailoverResolver(r.db, r.m)
	if up, _ := bare.Online("cell_0"); up {
		t.Error("cell_0 up with no gateway check installed")
	}
}

// sms_0 never binds a device: its availability is the cellular modem's, read
// as cellular_0 is read (its gateway first, then its binding), so the two
// names of one modem never disagree. [MESHSAT-1678]
func TestSMSFollowsTheCellularGateway(t *testing.T) {
	r := newRebindRig(t, kitSMS, kitCellG, kitAPRS)
	fr := NewFailoverResolver(r.db, r.m)
	gw := gateways{"cellular_0": true, "aprs_0": true}
	fr.SetGatewayOnline(gw.connected)

	if up, known := fr.Online("sms_0"); !up || !known {
		t.Errorf("sms_0 with the cellular gateway up: up=%v known=%v, want up", up, known)
	}
	r.db.InsertFailoverGroup(&database.FailoverGroup{ID: "rns", Label: "SMS first", Mode: "priority"})
	r.db.InsertFailoverMember(&database.FailoverMember{GroupID: "rns", InterfaceID: "sms_0", Priority: 1})
	r.db.InsertFailoverMember(&database.FailoverMember{GroupID: "rns", InterfaceID: "aprs_0", Priority: 2})
	if got := fr.Resolve("rns"); got != "sms_0" {
		t.Errorf("group with the cellular gateway up resolved to %q, want sms_0", got)
	}

	gw["cellular_0"] = false
	if up, known := fr.Online("sms_0"); up || !known {
		t.Errorf("sms_0 with the cellular gateway down: up=%v known=%v, want known absent", up, known)
	}
	if got := fr.Resolve("rns"); got != "aprs_0" {
		t.Errorf("group with the cellular gateway down resolved to %q, want aprs_0", got)
	}
	// sms_0 reads exactly what cellular_0 reads.
	for _, connected := range []bool{true, false} {
		gw["cellular_0"] = connected
		s, _ := fr.Online("sms_0")
		c, _ := fr.Online("cellular_0")
		if s != c {
			t.Errorf("gateway connected=%v: sms_0 up=%v but cellular_0 up=%v", connected, s, c)
		}
	}
}

// The hop set reads the links a kit really has, through the failover
// groups' online check: after a radio swap the mesh is there, an
// unconfigured cell is a link that is absent (no phantom free link for the
// floor), and sms_0 is up while the modem is. [MESHSAT-1678]
func TestHopSetReadsTheLinksAKitHas(t *testing.T) {
	r := newRebindRig(t, kitMesh, kitCellular, kitSMS, kitCell)
	fr := NewFailoverResolver(r.db, r.m)
	fr.SetGatewayOnline(gateways{"cellular_0": true}.connected)
	hs := NewHopSetManager(DefaultHopSetConfig(), r.db.GetAllInterfaces, HopWatch)
	hs.SetAvailability(fr.Online)

	r.m.reconcile(kitDevicesNow, rebindT0)
	r.m.reconcile(kitDevicesNow, rebindT0.Add(pastTheGrace))

	want := map[string]bool{"mesh_0": false, "cellular_0": false, "sms_0": false, "cell_0": true}
	got := map[string]bool{}
	for _, l := range hs.Status().Links {
		got[l.InterfaceID] = l.Absent
	}
	for id, absent := range want {
		if a, ok := got[id]; !ok || a != absent {
			t.Errorf("hop set %s: listed=%v absent=%v, want absent=%v", id, ok, a, absent)
		}
	}
}
