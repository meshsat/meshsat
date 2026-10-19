package engine

// The r8 review's reproductions of HM-R1 and HM-R2, kept as regressions. Two lines follow
// the fixed wiring: the startup recorder goes in through the route's sink with a result
// (SetIntentEvidenceSink with Apply, not SetIntentReceiptSink with Record, which cannot say
// the ledger refused an event), and the hand-off is main.go's (Dispatcher.HubHandoff, set
// again on the dispatcher a restart rebuilds), not the old callback that dropped
// DispatchAccess's result. On 96532aa2 both fail as the review describes. [MESHSAT-1584]

import (
	"encoding/json"
	"meshsat/internal/database"
	"meshsat/internal/hubmsg"
	"meshsat/internal/hubroute"
	"meshsat/internal/rules"
	"testing"
	"time"
)

func TestReviewR8StatusApplyFailureReplay(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	id := p.send(t, "stored at recipient")
	_, err := p.h.db.Exec(`CREATE TRIGGER review_fail_evidence BEFORE INSERT ON delivery_evidence WHEN NEW.event='recipient_app_stored' BEGIN SELECT RAISE(ABORT,'review transient evidence failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	p.hub.pump(t)
	p.h.drain()
	if _, err = p.h.db.Exec(`DROP TRIGGER review_fail_evidence`); err != nil {
		t.Fatal(err)
	}
	// Recreate the route over the same durable DB, and install the startup receipt recorder.
	again := hubroute.New(p.h.db, &hubLink{hub: p.hub, principal: "bridge:kit-a"}, hubroute.Config{})
	rec := NewIntentReceiptRecorder(p.h.db)
	rec.SetNotify(p.h.esc.Notify)
	again.SetIntentEvidenceSink(rec.Apply)
	var completion hubmsg.Event
	for _, f := range p.hub.statuses {
		again.HandleStatus(f.payload)
		var e hubmsg.Event
		_ = json.Unmarshal(f.payload, &e)
		if e.Class == "completed" {
			completion = e
		}
	}
	p.h.drain()
	got := p.h.snap(id).Intent.Outcome
	if got != database.OutcomeDeliveredApp {
		t.Errorf("after transient failure + route restart + exact replay: outcome=%s want delivered_app", got)
	}
	var n int
	_ = p.h.db.QueryRow(`SELECT COUNT(*) FROM hub_op_events WHERE kind='recipient_app_stored' AND valid=1`).Scan(&n)
	t.Logf("durable valid completion events=%d", n)
	// Positive control: same evidence with a fresh id still reaches the real ledger.
	completion.EventID = hubmsg.NewV7(time.Now())
	raw, _ := json.Marshal(completion)
	again.HandleStatus(raw)
	p.h.drain()
	if got = p.h.snap(id).Intent.Outcome; got != database.OutcomeDeliveredApp {
		t.Fatalf("fresh-id control: %s", got)
	}
	t.Log("fresh-id completion control reaches delivered_app")
}
func TestReviewR8HandoffDurability(t *testing.T) {
	for _, mode := range []string{"mark_failure", "queue_failure"} {
		t.Run(mode, func(t *testing.T) {
			d, db := setupTestDispatcher(t)
			defer db.Close()
			for id, ct := range map[string]string{"hub_0": "hub", "iridium_imt_0": "iridium_imt"} {
				_, err := db.Exec(`INSERT OR IGNORE INTO interfaces (id,channel_type,label,enabled,config,ingress_transforms,egress_transforms) VALUES (?,?,?,1,'{}','[]','[]')`, id, ct, id)
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := db.InsertAccessRule(&database.AccessRule{InterfaceID: "hub_0", Direction: "ingress", Priority: 1, Name: "review relay", Enabled: true, Action: "forward", ForwardTo: "iridium_imt_0", Filters: "{}"})
			if err != nil {
				t.Fatal(err)
			}
			ae := rules.NewAccessEvaluator(db)
			if err = ae.ReloadFromDB(); err != nil {
				t.Fatal(err)
			}
			d.SetAccessEvaluator(ae)
			r := hubroute.New(db, &hubLink{hub: newContractHub(), principal: "bridge:kit-b"}, hubroute.Config{})
			// main.go's hand-off.
			r.SetHandoff(d.HubHandoff)
			now := time.Now()
			s, err := hubmsg.NewSubmission(hubmsg.NewV7(now), hubmsg.NewV7(now), "bridge:kit-b", "relay once", now.Add(time.Hour).UnixMilli(), now.Add(time.Minute).UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(hubmsg.Delivery{V: 1, OpID: s.OpID, Mid: s.Mid, From: "bridge:kit-a", To: s.To, ContentType: s.ContentType, MidExpiresAt: s.MidExpiresAt, ExpiresAt: s.ExpiresAt, ContentSHA256: s.ContentSHA256, Body: s.Body})
			r.HandleIn(raw)
			trigger := `CREATE TRIGGER review_fail BEFORE UPDATE ON hub_inbox_handoff BEGIN SELECT RAISE(ABORT,'review mark failed'); END`
			if mode == "queue_failure" {
				trigger = `CREATE TRIGGER review_fail BEFORE INSERT ON message_deliveries BEGIN SELECT RAISE(ABORT,'review queue failed'); END`
			}
			if _, err = db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			r.HandOff()
			if _, err = db.Exec(`DROP TRIGGER review_fail`); err != nil {
				t.Fatal(err)
			}
			// A process restart rebuilds the dispatcher and its in-memory dedup cache.
			d = NewDispatcher(db, d.registry, nil, nil)
			d.SetAccessEvaluator(ae)
			r.SetHandoff(d.HubHandoff)
			r.HandOff()
			var n int
			_ = db.QueryRow(`SELECT COUNT(*) FROM message_deliveries WHERE channel='iridium_imt_0'`).Scan(&n)
			var state string
			_ = db.QueryRow(`SELECT state FROM hub_inbox_handoff`).Scan(&state)
			if n != 1 {
				t.Errorf("after transient %s and retry: downstream deliveries=%d want 1; handoff=%s", mode, n, state)
			}
		})
	}
}
