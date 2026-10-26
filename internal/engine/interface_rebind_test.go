package engine

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"meshsat/internal/database"
)

// rebindRig is an interface manager over an in-memory database, loaded the
// way Start loads it, with the state changes it fires.
type rebindRig struct {
	db      *database.DB
	m       *InterfaceManager
	mu      sync.Mutex
	changes []string
}

func newRebindRig(t *testing.T, links ...database.Interface) *rebindRig {
	t.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// The migrations seed links of their own; this rig has only its own.
	seeded, _ := db.GetAllInterfaces()
	for _, l := range seeded {
		if err := db.DeleteInterface(l.ID); err != nil {
			t.Fatal(err)
		}
	}
	for i := range links {
		if err := db.InsertInterface(&links[i]); err != nil {
			t.Fatal(err)
		}
	}
	r := &rebindRig{db: db, m: NewInterfaceManager(db)}
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel() // the scan loop ends before its first tick; the test drives reconcile
	r.m.SetStateChangeCallback(func(id, _ string, s InterfaceState) {
		r.mu.Lock()
		r.changes = append(r.changes, id+" "+s.String())
		r.mu.Unlock()
	})
	return r
}

func (r *rebindRig) status(t *testing.T, id string) *InterfaceStatus {
	t.Helper()
	st, err := r.m.GetStatus(id)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func (r *rebindRig) stored(t *testing.T, id string) *database.Interface {
	t.Helper()
	l, err := r.db.GetInterface(id)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func (r *rebindRig) fired() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string{}, r.changes...)
	sort.Strings(out)
	return out
}

// The devices of the kits after the 18 Sep T-Echo swap and the 20 Sep
// rebuild: the XIAO spare and the other kit's T-Call are gone, the T-Echo
// and the kit's own T-Call are on the bus, and so is the PicoAPRS, whose
// shared VID:PID the scan reads as ambiguous.
var (
	xiaoID        = "2886:0059+XIAO0001"
	otherKitCall  = "1a86:55d4+OTHERKIT"
	tEcho         = DetectedDevice{Port: "/dev/ttyACM1", VIDPID: "239a:4405", DeviceID: "239a:4405+TECHO0001", DeviceType: "meshtastic"}
	ownCall       = DetectedDevice{Port: "/dev/ttyACM0", VIDPID: "1a86:55d4", DeviceID: "1a86:55d4+OWNKIT", DeviceType: "cellular"}
	picoAPRS      = DetectedDevice{Port: "/dev/ttyUSB0", VIDPID: "10c4:ea60", DeviceID: "10c4:ea60+PICO0001", DeviceType: "ambiguous"}
	spareXIAO     = DetectedDevice{Port: "/dev/ttyACM3", VIDPID: "2886:0059", DeviceID: xiaoID, DeviceType: "meshtastic"}
	rebindT0      = time.Date(2026, 10, 24, 16, 37, 0, 0, time.UTC)
	withinGrace   = 30 * time.Second // a radio's reset or a hub port cycle
	pastTheGrace  = 3 * time.Minute  // a swap: the old radio is not coming back
	kitMesh       = database.Interface{ID: "mesh_0", ChannelType: "mesh", Label: "Meshtastic LoRa", Enabled: true, DeviceID: xiaoID, Config: "{}"}
	kitCellular   = database.Interface{ID: "cellular_0", ChannelType: "cellular", Label: "Cellular SMS", Enabled: true, DeviceID: otherKitCall, Config: "{}"}
	kitDevicesNow = []DetectedDevice{tEcho, ownCall, picoAPRS}
)

// A link whose bound device is gone, while exactly one device of its type is
// on the bus and no other link has it, follows that device: the kits'
// mesh_0 (bound to the XIAO spare since before the T-Echo swap) and
// cellular_0 (bound to the other kit's T-Call since the rebuild). Not at
// once: a radio that resets or a hub port that cycles comes back on its own.
// [MESHSAT-1678]
func TestInterfaceRebindFollowsTheDeviceAKitHas(t *testing.T) {
	r := newRebindRig(t, kitMesh, kitCellular)
	fr := NewFailoverResolver(r.db, r.m)

	r.m.reconcile(kitDevicesNow, rebindT0)
	r.m.reconcile(kitDevicesNow, rebindT0.Add(withinGrace))
	if st := r.status(t, "mesh_0"); st.DeviceID != xiaoID || st.State != StateOffline {
		t.Fatalf("mesh_0 within the grace: bound %q, %s; want still the XIAO, offline", st.DeviceID, st.State)
	}

	r.m.reconcile(kitDevicesNow, rebindT0.Add(pastTheGrace))
	for _, c := range []struct {
		id  string
		dev DetectedDevice
	}{{"mesh_0", tEcho}, {"cellular_0", ownCall}} {
		st := r.status(t, c.id)
		if st.DeviceID != c.dev.DeviceID || st.DevicePort != c.dev.Port || st.State != StateOnline {
			t.Errorf("%s: bound %q on %q, %s; want %q on %q, online", c.id, st.DeviceID, st.DevicePort, st.State, c.dev.DeviceID, c.dev.Port)
		}
		if l := r.stored(t, c.id); l.DeviceID != c.dev.DeviceID || l.DevicePort != c.dev.Port {
			t.Errorf("%s stored as %q on %q, want %q on %q", c.id, l.DeviceID, l.DevicePort, c.dev.DeviceID, c.dev.Port)
		}
	}
	if got, want := r.fired(), []string{"cellular_0 online", "mesh_0 online"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("state changes %v, want %v (the delivery workers start on them)", got, want)
	}
	if up, known := fr.Online("mesh_0"); !up || !known {
		t.Errorf("mesh_0 reads up=%v known=%v after the rebind, want up", up, known)
	}
	for _, d := range r.m.GetDetectedDevices() {
		want := map[string]string{tEcho.DeviceID: "mesh_0", ownCall.DeviceID: "cellular_0"}[d.DeviceID]
		if d.BoundTo != want {
			t.Errorf("device %s listed bound to %q, want %q", d.DeviceID, d.BoundTo, want)
		}
	}
}

// Where the device a link should follow is not obvious, the link is left as
// it is (offline on its own binding) and the choice stays the operator's.
func TestInterfaceRebindLeavesAmbiguityAlone(t *testing.T) {
	otherMesh := func(id, dev string, on bool) database.Interface {
		return database.Interface{ID: id, ChannelType: "mesh", Label: id, Enabled: on, DeviceID: dev, Config: "{}"}
	}
	secondEcho := DetectedDevice{Port: "/dev/ttyACM4", VIDPID: "239a:4405", DeviceID: "239a:4405+TECHO0002", DeviceType: "meshtastic"}
	cases := []struct {
		name    string
		links   []database.Interface
		devices []DetectedDevice
	}{
		{"two radios of the type on the bus", []database.Interface{kitMesh}, []DetectedDevice{tEcho, secondEcho}},
		{"the one radio there is another link's", []database.Interface{kitMesh, otherMesh("mesh_1", tEcho.DeviceID, true)}, []DetectedDevice{tEcho}},
		{"the one radio there is another link's by VID:PID", []database.Interface{kitMesh, otherMesh("mesh_1", tEcho.VIDPID, true)}, []DetectedDevice{tEcho}},
		{"two links of the type lost their radio", []database.Interface{kitMesh, otherMesh("mesh_1", "239a:4405+GONE", true)}, []DetectedDevice{tEcho}},
		{"the link is switched off", []database.Interface{otherMesh("mesh_0", xiaoID, false)}, []DetectedDevice{tEcho}},
		{"nothing of the type, only an ambiguous port", []database.Interface{kitMesh}, []DetectedDevice{picoAPRS}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRebindRig(t, c.links...)
			before := map[string]string{}
			for _, l := range c.links {
				before[l.ID] = l.DeviceID
			}
			r.m.reconcile(c.devices, rebindT0)
			r.m.reconcile(c.devices, rebindT0.Add(pastTheGrace))
			r.m.reconcile(c.devices, rebindT0.Add(2*pastTheGrace))
			for id, dev := range before {
				if st := r.status(t, id); st.DeviceID != dev {
					t.Errorf("%s rebound to %q, want left on %q", id, st.DeviceID, dev)
				}
				if l := r.stored(t, id); l.DeviceID != dev {
					t.Errorf("%s stored as %q, want left on %q", id, l.DeviceID, dev)
				}
			}
		})
	}
}

// A radio that leaves the bus and comes back inside the grace keeps its
// link, even with a spare of its type plugged in meanwhile, and the grace
// starts again each time it leaves.
func TestInterfaceRebindWaitsOutAReset(t *testing.T) {
	echoLink := database.Interface{ID: "mesh_0", ChannelType: "mesh", Label: "Meshtastic LoRa", Enabled: true, DeviceID: tEcho.DeviceID, Config: "{}"}
	r := newRebindRig(t, echoLink)
	at := rebindT0
	step := func(d time.Duration, devs ...DetectedDevice) {
		at = at.Add(d)
		r.m.reconcile(devs, at)
	}
	step(0, tEcho)
	step(time.Minute, spareXIAO) // the T-Echo resets; a spare is on the bus
	step(time.Minute, spareXIAO)
	step(withinGrace, tEcho, spareXIAO) // back inside the grace
	step(time.Minute, spareXIAO)        // leaves again: the grace starts over
	step(time.Minute+withinGrace, spareXIAO)
	if st := r.status(t, "mesh_0"); st.DeviceID != tEcho.DeviceID {
		t.Fatalf("mesh_0 rebound to %q during resets, want left on the T-Echo", st.DeviceID)
	}
	step(time.Minute, tEcho, spareXIAO)
	if st := r.status(t, "mesh_0"); st.DeviceID != tEcho.DeviceID || st.State != StateOnline {
		t.Fatalf("mesh_0 bound %q, %s; want the T-Echo, online", st.DeviceID, st.State)
	}
}
