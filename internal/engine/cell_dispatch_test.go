package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

// Texts between the mesh and the MeshSat 2G cell: one ledger row per phone,
// and a phone that is off the cell waits on its own clock. [MESHSAT-1557]

type cellProv struct{ cg *gateway.CellGateway }

func (p cellProv) Gateways() []gateway.Gateway { return []gateway.Gateway{p.cg} }
func (p cellProv) GatewayByInterfaceID(id string) gateway.Gateway {
	if id == "cell_0" {
		return p.cg
	}
	return nil
}

func cellRuleDispatcher(t *testing.T, cfg gateway.CellConfig, rule database.AccessRule) (*Dispatcher, *database.DB) {
	t.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	cg := gateway.NewCellGateway(cfg, db, "cell_0") // not started: Destinations needs no session
	d := NewDispatcher(db, reg, cellProv{cg}, nil)
	db.InsertInterface(&database.Interface{ID: "mesh_0", ChannelType: "mesh", Enabled: true})
	if _, err := db.InsertAccessRule(&rule); err != nil {
		t.Fatal(err)
	}
	ae := rules.NewAccessEvaluator(db)
	if err := ae.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}
	d.SetAccessEvaluator(ae)
	return d, db
}

func TestCellLinkSeeded(t *testing.T) {
	_, db := setupTestDispatcher(t)
	iface, err := db.GetInterface("cell_0")
	if err != nil {
		t.Fatalf("cell_0 not seeded: %v", err)
	}
	if iface.ChannelType != "cell" || !iface.Enabled {
		t.Fatalf("cell_0 = %+v", iface)
	}
	if channelNeedsDevice("cell") {
		t.Fatal("cell_0 has no device: it must be online when on, like mqtt")
	}
	if !isTextBearer("cell") {
		t.Fatal("a mesh text goes to a phone as text, not as the JSON envelope")
	}
}

func TestDispatchAccess_MeshTextToCellPhones(t *testing.T) {
	opts, _ := json.Marshal(map[string]any{"cell_destinations": []string{"1001", "cell:2002", "cell:1001", "nope"}})
	d, db := cellRuleDispatcher(t, gateway.DefaultCellConfig(), database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Mesh to the cell", Enabled: true,
		Priority: 1, Action: "forward", ForwardTo: "cell_0", Filters: "{}", ForwardOptions: string(opts), QoSLevel: 1,
	})
	msg := rules.RouteMessage{Text: "hello phones", From: "!0000abcd", PortNum: 1}
	if n := d.DispatchAccess("mesh_0", msg, []byte(msg.Text)); n != 2 {
		t.Fatalf("want one row per distinct valid phone (2), got %d", n)
	}
	rows, err := db.GetPendingDeliveries("cell_0", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Destination] = true
		if string(r.Payload) != "hello phones" || r.TextPreview != "hello phones" {
			t.Fatalf("a text bearer carries the text, got payload %q", r.Payload)
		}
		if r.TTLSeconds != 86400 {
			t.Fatalf("the cell's default TTL is a day, got %d", r.TTLSeconds)
		}
	}
	if !got["cell:1001"] || !got["cell:2002"] || len(got) != 2 {
		t.Fatalf("destinations %v", got)
	}
}

func TestDispatchAccess_CellReplyGoesToRecentPhones(t *testing.T) {
	cfg := gateway.DefaultCellConfig()
	cfg.ReplyWindowHours = 24
	d, db := cellRuleDispatcher(t, cfg, database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Mesh replies", Enabled: true,
		Priority: 1, Action: "forward", ForwardTo: "cell_0", Filters: "{}", ForwardOptions: "{}", QoSLevel: 1,
	})
	// Nobody texted yet: one row with no phone, which ends "no recipient"
	// in the ledger where the operator sees it.
	if n := d.DispatchAccess("mesh_0", rules.RouteMessage{Text: "anyone?", PortNum: 1}, []byte("anyone?")); n != 1 {
		t.Fatalf("got %d rows", n)
	}
	rows, _ := db.GetPendingDeliveries("cell_0", 10)
	if len(rows) != 1 || rows[0].Destination != "" {
		t.Fatalf("rows %+v", rows)
	}
	// The Pixel texted the kit: the reply finds it without a rule target.
	if err := db.TouchCellPeerRx("1001", "1000"); err != nil {
		t.Fatal(err)
	}
	if n := d.DispatchAccess("mesh_0", rules.RouteMessage{Text: "yes, here", PortNum: 1}, []byte("yes, here")); n != 1 {
		t.Fatalf("got %d rows", n)
	}
	rows, _ = db.GetPendingDeliveries("cell_0", 10)
	var toPixel int
	for _, r := range rows {
		if r.Destination == "cell:1001" && r.TextPreview == "yes, here" {
			toPixel++
		}
	}
	if toPixel != 1 {
		t.Fatalf("rows %+v", rows)
	}
}

// A phone off the cell (0x0B, 0xFE) defers without a retry consumed: a
// minute at first, five minutes after ten, dead after a day.
func TestHandleFailure_PhoneOffTheCell(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "cell_0", "cell", true)
	var emitted []transport.MeshEvent
	w := newTestWorker(h, "cell_0", "cell")
	w.emit = func(e transport.MeshEvent) { emitted = append(emitted, e) }
	absent := fmt.Errorf("cell: %w", transport.ErrSubscriberAbsent)

	insert := func(age time.Duration) database.MessageDelivery {
		row := database.MessageDelivery{MsgRef: "m", Channel: "cell_0", Status: "sending", Priority: 1,
			Payload: []byte("x"), TextPreview: "x", MaxRetries: 3, QoSLevel: 1, Destination: "cell:1001"}
		id, err := h.db.InsertDelivery(row)
		if err != nil {
			t.Fatal(err)
		}
		created := time.Now().Add(-age).UTC().Format("2006-01-02 15:04:05")
		if _, err := h.db.Exec(`UPDATE message_deliveries SET created_at = ? WHERE id = ?`, created, id); err != nil {
			t.Fatal(err)
		}
		row.ID = id
		row.CreatedAt = created
		return row
	}
	check := func(row database.MessageDelivery, wantWait time.Duration) {
		t.Helper()
		got, err := h.db.GetDelivery(row.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "retry" || got.Retries != 0 || got.NextRetry == nil {
			t.Fatalf("row %+v, want retry with no retry consumed and a next_retry", got)
		}
		wait := time.Until(*got.NextRetry)
		if wait < wantWait-10*time.Second || wait > wantWait+10*time.Second {
			t.Fatalf("next_retry in %s, want about %s", wait.Truncate(time.Second), wantWait)
		}
	}

	young := insert(0)
	w.handleFailure(young, absent)
	check(young, cellAbsentRetryEarly)

	older := insert(15 * time.Minute)
	w.handleFailure(older, absent)
	check(older, cellAbsentRetryLate)

	old := insert(25 * time.Hour)
	w.handleFailure(old, absent)
	got, _ := h.db.GetDelivery(old.ID)
	if got.Status != "dead" || got.LastError == "" {
		t.Fatalf("after a day the row ends dead with the reason, got %+v", got)
	}
	if len(emitted) != 3 || emitted[0].Type != "delivery_deferred" || emitted[1].Type != "delivery_deferred" || emitted[2].Type != "delivery_dead" {
		t.Fatalf("events %+v", emitted)
	}

	// A plain "not connected" still takes the bearer-down path, not this one.
	down := insert(0)
	w.handleFailure(down, fmt.Errorf("cell: %w", transport.ErrNotConnected))
	got, _ = h.db.GetDelivery(down.ID)
	if got.Status != "retry" || !errors.Is(transport.ErrNotConnected, transport.ErrNotConnected) {
		t.Fatalf("%+v", got)
	}
}
