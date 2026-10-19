package engine

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/hubmsg"
	"meshsat/internal/hubroute"
	"meshsat/internal/rules"
)

// What kit-b's access rules make of a message the Hub delivered is made exactly once,
// whichever write fails and whenever the process stops (r8 review HM-R2): the hand-off is
// main.go's (Dispatcher.HubHandoff), each piece of work is listed under the stored message in
// the transaction that queues it, a hand-off with work it could not make stays pending and,
// tried again, makes only that, and no matching rule is a hand-off done. [MESHSAT-1584]

type handoffRig struct {
	t    *testing.T
	path string
	db   *database.DB
	d    *Dispatcher
	r    *hubroute.Route
	ae   *rules.AccessEvaluator
	seen []string // what the stored notify heard
}

func newHandoffRig(t *testing.T, rulesFn func(db *database.DB)) *handoffRig {
	t.Helper()
	g := &handoffRig{t: t, path: filepath.Join(t.TempDir(), "kit-b.db")}
	g.open()
	ensureInterfaces(t, g.db, map[string]string{"hub_0": "hub", "iridium_imt_0": "iridium_imt", "cellular_0": "cellular", "lxmf_0": "lxmf"})
	if rulesFn != nil {
		rulesFn(g.db)
	}
	g.start()
	return g
}

func (g *handoffRig) open() {
	db, err := database.New(g.path)
	if err != nil {
		g.t.Fatal(err)
	}
	g.db = db
	g.t.Cleanup(func() { _ = db.Close() })
}

// start builds the dispatcher, the rules and the route over the database, as a process start
// does: the dispatcher's in-memory dedup cache is new.
func (g *handoffRig) start() {
	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	g.d = NewDispatcher(g.db, reg, nil, nil)
	g.ae = rules.NewAccessEvaluator(g.db)
	if err := g.ae.ReloadFromDB(); err != nil {
		g.t.Fatal(err)
	}
	g.d.SetAccessEvaluator(g.ae)
	g.r = hubroute.New(g.db, &hubLink{hub: newContractHub(), principal: "bridge:kit-b"}, hubroute.Config{})
	g.r.SetHandoff(g.d.HubHandoff)
	g.r.SetStoredNotify(func(from, text string) { g.seen = append(g.seen, from+": "+text) })
}

// restart closes the database and starts again over the same file.
func (g *handoffRig) restart() {
	if err := g.db.Close(); err != nil {
		g.t.Fatal(err)
	}
	g.open()
	g.start()
}

func (g *handoffRig) deliver(text string) {
	g.t.Helper()
	now := time.Now()
	s, err := hubmsg.NewSubmission(hubmsg.NewV7(now), hubmsg.NewV7(now), "bridge:kit-b", text, now.Add(time.Hour).UnixMilli(), now.Add(time.Minute).UnixMilli())
	if err != nil {
		g.t.Fatal(err)
	}
	raw, _ := json.Marshal(hubmsg.Delivery{V: 1, OpID: s.OpID, Mid: s.Mid, From: "bridge:kit-a", To: s.To, ContentType: s.ContentType,
		MidExpiresAt: s.MidExpiresAt, ExpiresAt: s.ExpiresAt, ContentSHA256: s.ContentSHA256, Body: s.Body})
	g.r.HandleIn(raw)
	g.r.HandleIn(raw) // the Hub's copy
}

func (g *handoffRig) rows(channel string) int {
	g.t.Helper()
	var n int
	if err := g.db.QueryRow(`SELECT COUNT(*) FROM message_deliveries WHERE channel = ?`, channel).Scan(&n); err != nil {
		g.t.Fatal(err)
	}
	return n
}

func (g *handoffRig) state() string {
	g.t.Helper()
	var s string
	if err := g.db.QueryRow(`SELECT state FROM hub_inbox_handoff`).Scan(&s); err != nil {
		g.t.Fatal(err)
	}
	return s
}

func (g *handoffRig) listed() int {
	g.t.Helper()
	var n int
	if err := g.db.QueryRow(`SELECT COUNT(*) FROM dispatch_work`).Scan(&n); err != nil {
		g.t.Fatal(err)
	}
	return n
}

// ensureInterfaces makes the interfaces the migrations did not seed.
func ensureInterfaces(t *testing.T, db *database.DB, ifaces map[string]string) {
	t.Helper()
	for id, ct := range ifaces {
		if _, err := db.Exec(`INSERT OR IGNORE INTO interfaces (id, channel_type, label, enabled, config, ingress_transforms, egress_transforms)
			VALUES (?, ?, ?, 1, '{}', '[]', '[]')`, id, ct, id); err != nil {
			t.Fatal(err)
		}
	}
}

func relayRule(target, opts string) func(db *database.DB) {
	return func(db *database.DB) {
		if opts == "" {
			opts = "{}"
		}
		if _, err := db.InsertAccessRule(&database.AccessRule{InterfaceID: "hub_0", Direction: "ingress", Priority: 1,
			Name: "relay to " + target, Enabled: true, Action: "forward", ForwardTo: target, Filters: "{}", ForwardOptions: opts}); err != nil {
			panic(err)
		}
	}
}

func both(fs ...func(db *database.DB)) func(db *database.DB) {
	return func(db *database.DB) {
		for _, f := range fs {
			f(db)
		}
	}
}

// The queue write fails, then the mark that the hand-off is done fails; each tried again
// in the same process, after a restart of the dispatcher, and after the database closed:
// one row every time, and the hand-off done.
func TestHubHandoff_AFailedWriteIsTriedAgainAndMakesOneRow(t *testing.T) {
	for _, fault := range []string{
		`BEFORE INSERT ON message_deliveries`,
		`BEFORE UPDATE ON hub_inbox_handoff`,
	} {
		for _, recovery := range []string{"same process", "dispatcher restart", "database reopen"} {
			t.Run(fault+"/"+recovery, func(t *testing.T) {
				g := newHandoffRig(t, relayRule("iridium_imt_0", ""))
				g.deliver("relay once")
				heal := failOn(t, g.db, fault)
				g.r.HandOff()
				heal()
				if g.state() != "pending" {
					t.Fatalf("a hand-off that failed was marked %s", g.state())
				}
				switch recovery {
				case "dispatcher restart":
					g.start()
				case "database reopen":
					g.restart()
				}
				g.r.HandOff()
				g.r.HandOff() // a done hand-off is not taken again
				if n := g.rows("iridium_imt_0"); n != 1 || g.state() != "done" || g.listed() != 0 {
					t.Fatalf("rows %d, hand-off %s, %d listed", n, g.state(), g.listed())
				}
			})
		}
	}
}

// Two rules, one of whose writes fails: the hand-off stays pending with the other's row made,
// and tried again (in the process, and after a reopen) it makes only the missing row.
func TestHubHandoff_PartialFanOutMakesOnlyTheMissingWork(t *testing.T) {
	for _, recovery := range []string{"same process", "database reopen"} {
		t.Run(recovery, func(t *testing.T) {
			g := newHandoffRig(t, both(relayRule("iridium_imt_0", ""), relayRule("cellular_0", "")))
			g.deliver("to two bearers")
			heal := failOn(t, g.db, `BEFORE INSERT ON message_deliveries WHEN NEW.channel = 'cellular_0'`)
			g.r.HandOff()
			heal()
			if g.rows("iridium_imt_0") != 1 || g.rows("cellular_0") != 0 || g.state() != "pending" || g.listed() != 1 {
				t.Fatalf("after the partial failure: imt %d, cellular %d, %s, %d listed", g.rows("iridium_imt_0"), g.rows("cellular_0"), g.state(), g.listed())
			}
			if recovery == "database reopen" {
				g.restart()
			}
			g.r.HandOff()
			if g.rows("iridium_imt_0") != 1 || g.rows("cellular_0") != 1 || g.state() != "done" {
				t.Fatalf("tried again: imt %d, cellular %d, %s", g.rows("iridium_imt_0"), g.rows("cellular_0"), g.state())
			}
		})
	}
}

// One rule with two LXMF destinations, one of whose rows fails: tried again, the rule's own
// earlier row is not taken for a recent copy of the payload, and the missing row is made.
func TestHubHandoff_APartlyMadeRuleIsFinished(t *testing.T) {
	a, b := "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"
	g := newHandoffRig(t, relayRule("lxmf_0", `{"lxmf_destinations":["`+a+`","`+b+`"]}`))
	g.deliver("to two lxmf peers")
	heal := failOn(t, g.db, `BEFORE INSERT ON message_deliveries WHEN NEW.destination = '`+b+`'`)
	g.r.HandOff()
	heal()
	if g.rows("lxmf_0") != 1 || g.state() != "pending" {
		t.Fatalf("after the partial failure: %d rows, %s", g.rows("lxmf_0"), g.state())
	}
	g.r.HandOff()
	var n int
	if err := g.db.QueryRow(`SELECT COUNT(DISTINCT destination) FROM message_deliveries WHERE channel = 'lxmf_0'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if g.rows("lxmf_0") != 2 || n != 2 || g.state() != "done" {
		t.Fatalf("tried again: %d rows to %d destinations, %s", g.rows("lxmf_0"), n, g.state())
	}
}

// No rule takes hub_0 (the kits as they are): the hand-off is done with nothing made, and the
// stored notify heard the message once although the Hub delivered it twice.
func TestHubHandoff_NoMatchingRuleIsAHandoffDone(t *testing.T) {
	g := newHandoffRig(t, nil)
	g.deliver("nobody relays this")
	g.r.HandOff()
	var n int
	if err := g.db.QueryRow(`SELECT COUNT(*) FROM message_deliveries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 || g.state() != "done" || len(g.seen) != 1 || g.seen[0] != "bridge:kit-a: nobody relays this" {
		t.Fatalf("%d rows, %s, heard %v", n, g.state(), g.seen)
	}
	res := g.d.DispatchAccessKeyed("hub_0", rules.RouteMessage{Text: "x", PortNum: 1, Plain: true}, []byte("x"), "hub:999")
	if res != (DispatchResult{}) {
		t.Fatalf("no rule: %+v", res)
	}
}

// Outside the Hub route: a dispatch whose write failed is not taken for its own recent copy
// when the same payload comes again, so the second one is queued.
func TestDispatchAccess_AFailedWriteIsNotARecentCopy(t *testing.T) {
	d, db := setupTestDispatcher(t)
	defer db.Close()
	ensureInterfaces(t, db, map[string]string{"mesh_0": "mesh", "iridium_imt_0": "iridium_imt"})
	if _, err := db.InsertAccessRule(&database.AccessRule{InterfaceID: "mesh_0", Direction: "ingress", Priority: 1, Name: "relay",
		Enabled: true, Action: "forward", ForwardTo: "iridium_imt_0", Filters: "{}"}); err != nil {
		t.Fatal(err)
	}
	ae := rules.NewAccessEvaluator(db)
	if err := ae.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}
	d.SetAccessEvaluator(ae)
	msg := rules.RouteMessage{Text: "same text", PortNum: 1, Plain: true}
	heal := failOn(t, db, `BEFORE INSERT ON message_deliveries`)
	if n := d.DispatchAccess("mesh_0", msg, []byte("same text")); n != 0 {
		t.Fatalf("queued %d through a failing write", n)
	}
	heal()
	if n := d.DispatchAccess("mesh_0", msg, []byte("same text")); n != 1 {
		t.Fatalf("the second dispatch queued %d, want 1", n)
	}
	if n := d.DispatchAccess("mesh_0", msg, []byte("same text")); n != 0 {
		t.Fatalf("a recent copy queued %d", n)
	}
}
