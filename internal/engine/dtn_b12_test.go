package engine

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/codec"
	"meshsat/internal/database"
	"meshsat/internal/rules"
)

// B12 (r3 F8, F13): a relay over a bearer's size limit is queued whole,
// never as DTN fragments nothing reassembles; a custody payload this node
// takes is queued where a worker sends it, or not taken. [MESHSAT-1584]

func TestDispatchAccess_ALongRelayIsOneWholeRowNotFragments(t *testing.T) {
	d, db := setupTestDispatcher(t)
	for _, i := range []database.Interface{
		{ID: "mesh_0", ChannelType: "mesh"}, {ID: "cellular_0", ChannelType: "cellular"},
		{ID: "iridium_0", ChannelType: "iridium"}, {ID: "zigbee_0", ChannelType: "zigbee"},
	} {
		i.Enabled = true
		db.InsertInterface(&i)
	}
	dests := []string{"cellular_0", "iridium_0", "zigbee_0"}
	for i, dest := range dests {
		db.InsertAccessRule(&database.AccessRule{InterfaceID: "mesh_0", Direction: "ingress", Name: dest, Enabled: true,
			Priority: i + 1, Action: "forward", ForwardTo: dest, Filters: "{}"})
	}
	ae := rules.NewAccessEvaluator(db)
	if err := ae.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}
	d.SetAccessEvaluator(ae)
	text := strings.Repeat("long relay ", 20)[:200]                                                      // over SMS (160) and ZigBee (100)
	envelope := []byte(`{"from":1,"to":4294967295,"text":"x","pad":"` + strings.Repeat("y", 400) + `"}`) // over SBD (340) and ZigBee (100)
	if n := d.DispatchAccess("mesh_0", rules.RouteMessage{Text: text, From: "!00000001", PortNum: 1}, envelope); n != len(dests) {
		t.Fatalf("%d deliveries; want one per destination (%d)", n, len(dests))
	}
	for _, dest := range dests {
		dl, _ := db.GetPendingDeliveries(dest, 10)
		if len(dl) != 1 {
			t.Fatalf("%s: %d rows; want one whole row", dest, len(dl))
		}
		if strings.HasPrefix(dl[0].TextPreview, "[frag") {
			t.Fatalf("%s: a fragment row %q", dest, dl[0].TextPreview[:12])
		}
		want := text
		if !isTextBearer(strings.TrimSuffix(dest, "_0")) {
			want = string(envelope) // a byte bearer (SBD, ZigBee) keeps the envelope
		}
		if string(dl[0].Payload) != want {
			t.Fatalf("%s: payload %d B; want the whole %d B", dest, len(dl[0].Payload), len(want))
		}
	}
}

func TestAcceptCustody_QueuesOnTheMeshInterfaceOrTakesNothing(t *testing.T) {
	d, db := setupTestDispatcher(t)
	db.InsertInterface(&database.Interface{ID: "mesh_0", ChannelType: "mesh", Enabled: true})
	id, err := d.AcceptCustody([]byte("custody payload"))
	if err != nil || id == 0 {
		t.Fatalf("accept: %d %v", id, err)
	}
	dl, _ := db.GetPendingDeliveries("mesh_0", 10)
	if len(dl) != 1 || dl[0].ID != id || dl[0].TextPreview != "custody payload" {
		t.Fatalf("mesh_0 queue: %+v; want the payload where mesh_0's worker sends it", dl)
	}
	if stray, _ := db.GetPendingDeliveries("mesh", 10); len(stray) != 0 {
		t.Fatalf("%d rows on channel mesh, which no worker serves", len(stray))
	}

	// No enabled mesh interface: nothing queued, an error (no ACK goes).
	d2, db2 := setupTestDispatcher(t)
	if _, err := db2.Exec(`DELETE FROM interfaces`); err != nil {
		t.Fatal(err)
	}
	if err := db2.InsertInterface(&database.Interface{ID: "mesh_0", ChannelType: "mesh", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if id, err := d2.AcceptCustody([]byte("nowhere")); err == nil || id != 0 {
		t.Fatalf("accept with the mesh off: %d %v; want an error", id, err)
	}
	var n int
	_ = db2.QueryRow(`SELECT COUNT(*) FROM message_deliveries`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows queued with no mesh to send them", n)
	}
}

// B12-R1: the custody offer's whole path. The ACK goes only once the
// payload is queued for a worker that sends it whole; a repeat of a taken
// id is ACKed again and never queued twice; a refusal is not remembered.

func custodyOffer(t *testing.T, id byte, payload []byte) []byte {
	t.Helper()
	o := &CustodyOffer{Payload: payload}
	o.CustodyID[0] = id
	return MarshalCustodyOffer(o)
}

type ackLog struct {
	mu   sync.Mutex
	acks []string
	fail error
}

func (a *ackLog) send(iface string, o *CustodyOffer) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail != nil {
		return a.fail
	}
	a.acks = append(a.acks, fmt.Sprintf("%s:%x", iface, o.CustodyID[0]))
	return nil
}
func (a *ackLog) n() int { a.mu.Lock(); defer a.mu.Unlock(); return len(a.acks) }

func custodyDispatcher(t *testing.T, egress string, enabled bool) (*Dispatcher, *database.DB) {
	t.Helper()
	d, db := setupTestDispatcher(t)
	if _, err := db.Exec(`DELETE FROM interfaces`); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertInterface(&database.Interface{ID: "mesh_0", ChannelType: "mesh", Enabled: enabled, EgressTransforms: egress}); err != nil {
		t.Fatal(err)
	}
	d.SetTransformPipeline(NewTransformPipeline())
	return d, db
}

func rowCount(t *testing.T, db *database.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM message_deliveries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCustodyAcceptor_ACKsOnlyWhatTheWorkerSendsWhole(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
		egress  string
		enabled bool
		taken   bool
	}{
		{"20 bytes", bytes.Repeat([]byte("x"), 20), "", true, true},
		{"200 bytes, the preview's bound", bytes.Repeat([]byte("x"), 200), "", true, true},
		{"201 bytes", bytes.Repeat([]byte("x"), 201), "", true, false},
		{"220 bytes", bytes.Repeat([]byte("x"), 220), "", true, false},
		{"not text", []byte{0xff, 0xfe, 0x00, 0x01}, "", true, false},
		{"the mesh switched off", []byte("hello"), "", false, false},
		{"150 bytes through base64: 201 on the mesh", bytes.Repeat([]byte("y"), 150), `[{"type":"base64"}]`, true, true},
		{"180 bytes through base64: 241 on the mesh", bytes.Repeat([]byte("y"), 180), `[{"type":"base64"}]`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, db := custodyDispatcher(t, tc.egress, tc.enabled)
			acks := &ackLog{}
			take, err := NewCustodyAcceptor(d, acks.send).HandleOffer(custodyOffer(t, 1, tc.payload), "tcp_0")
			if (err == nil) != tc.taken || take.Acked != tc.taken || acks.n() != map[bool]int{true: 1, false: 0}[tc.taken] {
				t.Fatalf("take %+v err %v acks %d; want taken %v", take, err, acks.n(), tc.taken)
			}
			if !tc.taken {
				if rows := rowCount(t, db); rows != 0 {
					t.Fatalf("%d rows queued for an offer not taken", rows)
				}
				return
			}
			// What was taken goes to the radio whole.
			mesh := &mockMeshTransport{}
			w := &DeliveryWorker{channelID: "mesh_0", db: db, mesh: mesh, transforms: d.transforms}
			w.processBatch(context.Background())
			if len(mesh.sent) != 1 {
				t.Fatalf("%d mesh sends", len(mesh.sent))
			}
			want := string(tc.payload)
			if tc.egress != "" {
				out, _ := d.transforms.ApplyEgress(tc.payload, tc.egress)
				want = string(codec.PrependVersionByte(out))
			}
			if mesh.sent[0].Text != want {
				t.Fatalf("taken %d bytes, the radio got %d", len(want), len(mesh.sent[0].Text))
			}
		})
	}
}

func TestCustodyAcceptor_AQueueFailureSendsNoACK(t *testing.T) {
	d, db := custodyDispatcher(t, "", true)
	if _, err := db.Exec(`DROP TABLE message_deliveries`); err != nil {
		t.Fatal(err)
	}
	acks := &ackLog{}
	if take, err := NewCustodyAcceptor(d, acks.send).HandleOffer(custodyOffer(t, 1, []byte("hello")), "tcp_0"); err == nil || take.Acked || acks.n() != 0 {
		t.Fatalf("take %+v err %v acks %d; want no ACK after a failed queue", take, err, acks.n())
	}
}

func TestCustodyAcceptor_ARepeatIsACKedAgainAndQueuedOnce(t *testing.T) {
	d, db := custodyDispatcher(t, "", true)
	acks := &ackLog{}
	a := NewCustodyAcceptor(d, acks.send)
	offer := custodyOffer(t, 7, []byte("once"))
	first, err := a.HandleOffer(offer, "tcp_0")
	if err != nil || first.RowID == 0 || first.Repeat {
		t.Fatalf("first %+v %v", first, err)
	}
	again, err := a.HandleOffer(offer, "tcp_0")
	if err != nil || !again.Repeat || again.RowID != 0 || !again.Acked {
		t.Fatalf("repeat %+v %v; want ACKed again, nothing queued", again, err)
	}
	// Sixteen copies at once: none queued again.
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = a.HandleOffer(offer, "tcp_0") }()
	}
	wg.Wait()
	if rows := rowCount(t, db); rows != 1 || acks.n() != 18 {
		t.Fatalf("%d rows, %d ACKs; want 1 row, 18 ACKs", rows, acks.n())
	}
	// Past the window the id is new again.
	a.now = func() time.Time { return time.Now().Add(custodyRepeatWindow + time.Minute) }
	if late, err := a.HandleOffer(offer, "tcp_0"); err != nil || late.Repeat || late.RowID == 0 {
		t.Fatalf("past the window %+v %v", late, err)
	}
}

func TestCustodyAcceptor_ConcurrentFirstCopiesQueueOnce(t *testing.T) {
	d, db := custodyDispatcher(t, "", true)
	acks := &ackLog{}
	a := NewCustodyAcceptor(d, acks.send)
	offer := custodyOffer(t, 9, []byte("together"))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, _ = a.HandleOffer(offer, "tcp_0") }()
	}
	close(start)
	wg.Wait()
	if rows := rowCount(t, db); rows != 1 || acks.n() != 16 {
		t.Fatalf("%d rows, %d ACKs; want 1 row, 16 ACKs", rows, acks.n())
	}
}

// A refusal is not remembered: once the mesh is back, the next copy of the
// offer is taken.
func TestCustodyAcceptor_ARefusedOfferIsTakenWhenOfferedAgain(t *testing.T) {
	d, db := custodyDispatcher(t, "", false)
	acks := &ackLog{}
	a := NewCustodyAcceptor(d, acks.send)
	offer := custodyOffer(t, 3, []byte("later"))
	if _, err := a.HandleOffer(offer, "tcp_0"); err == nil {
		t.Fatal("taken with the mesh off")
	}
	if _, err := db.Exec(`UPDATE interfaces SET enabled = 1 WHERE id = 'mesh_0'`); err != nil {
		t.Fatal(err)
	}
	if take, err := a.HandleOffer(offer, "tcp_0"); err != nil || take.Repeat || take.RowID == 0 || acks.n() != 1 {
		t.Fatalf("offered again: %+v %v, %d ACKs", take, err, acks.n())
	}
}
