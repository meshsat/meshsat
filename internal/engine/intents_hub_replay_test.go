package engine

import (
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/hubroute"
)

// A lost Hub event stays asked for until kit-a keeps the operation's outcome (r9 review
// HM-R4): a repeat going out clears nothing, a process start asks once for every operation
// not settled (a lost event may have left no mark at all), a silent Hub is asked less often,
// a disconnected route asks nothing and forgets nothing, and at the bound kit-a stops and
// says the outcome is unknown. Every case ends with one ledger row under the event id the
// Hub gave the completion, and kit-b stored the message once. [MESHSAT-1584]

// toggleLink is kit-a's Hub session with a switch, and an optional hook that runs once a
// publish went (a Hub answer that comes before the publish returns).
type toggleLink struct {
	*hubLink
	down  bool
	after func(kind string)
}

func (l *toggleLink) IsConnected() bool { return !l.down }
func (l *toggleLink) PublishMsg(kind string, payload []byte, wait time.Duration) error {
	err := l.hubLink.PublishMsg(kind, payload, wait)
	if err == nil && l.after != nil {
		l.after(kind)
	}
	return err
}

// routeA builds kit-a's route over link, as a process start does, with the startup recorder.
func (p *hubPair) routeA(link hubroute.Publisher, cfg hubroute.Config) *hubroute.Route {
	a := hubroute.New(p.h.db, link, cfg)
	rec := NewIntentReceiptRecorder(p.h.db)
	rec.SetNotify(p.h.esc.Notify)
	a.SetIntentEvidenceSink(rec.Apply)
	p.hub.mu.Lock()
	p.hub.routes["bridge:kit-a"] = a
	p.hub.mu.Unlock()
	p.a = a
	return a
}

// reopenA is kit-a's process stopping and starting: the database is closed and opened again
// over the same file, and the dispatcher, the Escalator and the route are new.
func (p *hubPair) reopenA(t *testing.T) *hubroute.Route {
	t.Helper()
	var file string
	if err := p.h.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&file); err != nil || file == "" {
		t.Fatalf("database file: %q %v", file, err)
	}
	if err := p.h.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := database.New(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := p.h
	h.db = db
	h.d = NewDispatcher(db, channel.NewRegistry(), nil, h.mesh)
	h.store = directory.NewSQLStore(db.DB)
	h.resolver = &countingResolver{RecipientResolver: h.store}
	h.d.SetRecipientResolver(h.resolver)
	h.d.SetLXMFSender(h.lx)
	h.start(true)
	a := p.routeA(&hubLink{hub: p.hub, principal: "bridge:kit-a"}, hubroute.Config{})
	h.d.SetHubRoute(a)
	h.workers["hub_0"] = &DeliveryWorker{db: db, channelID: "hub_0", intents: h.esc, hubRoute: h.d.hubRoute}
	return a
}

func (p *hubPair) owedSince(t *testing.T) int64 {
	t.Helper()
	op, err := p.h.db.GetHubOp(onlyOp(t, p).OpID)
	if err != nil || op == nil {
		t.Fatalf("%+v %v", op, err)
	}
	return op.ReplayAt
}

// settledOnce checks the end of every case: delivered to the app, one ledger row under the
// completion's own id, no repeat owed, the message stored once at kit-b.
func (p *hubPair) settledOnce(t *testing.T, id, ev string) {
	t.Helper()
	if s := p.h.snap(id); s.Intent.Outcome != database.OutcomeDeliveredApp {
		t.Fatalf("kit-a: %s/%s", s.Intent.State, s.Intent.Outcome)
	}
	if n := p.ledgerRows(t, ev); n != 1 {
		t.Fatalf("%d ledger rows under %s", n, ev)
	}
	if at := p.owedSince(t); at != 0 {
		t.Fatal("still owed a repeat once settled")
	}
	if n := storedRows(t, p.dbB); n != 1 {
		t.Fatalf("kit-b stored %d", n)
	}
}

// Both writes fail (the completion and the mark that a repeat is owed), then the process
// stops before anything recovers: the database is reopened, and the start asks for the
// operation it finds without its outcome.
func TestHubReplay_BothWritesFailThenTheProcessRestarts(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	id := p.send(t, "both writes fail")
	healEv := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind = 'recipient_app_stored'`)
	if _, err := p.h.db.Exec(`CREATE TRIGGER test_fail_mark BEFORE UPDATE OF replay_at ON hub_ops BEGIN SELECT RAISE(ABORT, 'test'); END`); err != nil {
		t.Fatal(err)
	}
	ev := hubCases()[0].produce(t, p)
	p.h.drain()
	if p.owedSince(t) != 0 {
		t.Fatal("the mark was kept")
	}
	healEv()
	if _, err := p.h.db.Exec(`DROP TRIGGER test_fail_mark`); err != nil {
		t.Fatal(err)
	}
	a := p.reopenA(t)
	a.ApplyPending()
	a.RequestReplays()
	p.hub.pump(t)
	p.h.drain()
	p.settledOnce(t, id, ev)
	if p.hub.replays != 1 {
		t.Fatalf("%d repeats", p.hub.replays)
	}
	a.RequestReplays() // settled, and the start's asking is done: nothing more
	p.hub.pump(t)
	if p.hub.replays != 1 {
		t.Fatalf("asked again once settled: %d repeats", p.hub.replays)
	}
}

// The same with the mark kept: the reopened process asks for it as owed.
func TestHubReplay_AnOwedRepeatSurvivesAReopen(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	id := p.send(t, "owed across a reopen")
	heal := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind = 'recipient_app_stored'`)
	ev := hubCases()[0].produce(t, p)
	p.h.drain()
	if p.owedSince(t) == 0 {
		t.Fatal("no repeat owed")
	}
	heal()
	a := p.reopenA(t)
	a.RequestReplays()
	p.hub.pump(t)
	p.h.drain()
	p.settledOnce(t, id, ev)
}

// The answer to a repeat comes late: the repeat going out cleared nothing, a silent Hub is
// not asked again at once, and the late answer settles it.
func TestHubReplay_ALateAnswerSettlesIt(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	id := p.send(t, "a late answer")
	heal := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind = 'recipient_app_stored'`)
	ev := hubCases()[0].produce(t, p)
	p.h.drain()
	heal()
	link := &toggleLink{hubLink: &hubLink{hub: p.hub, principal: "bridge:kit-a"}}
	a := p.routeA(link, hubroute.Config{})
	a.RequestReplays() // goes out; the Hub has not answered yet
	if p.owedSince(t) == 0 {
		t.Fatal("a repeat going out cleared the owed mark")
	}
	a.RequestReplays() // the Hub is silent: not again at once
	p.hub.mu.Lock()
	queued := len(p.hub.queue)
	p.hub.mu.Unlock()
	if queued != 1 {
		t.Fatalf("%d repeats queued while the Hub was silent", queued)
	}
	p.hub.pump(t) // the answer, late
	p.h.drain()
	p.settledOnce(t, id, ev)
}

// The answer comes before the publish returns, and is kept: settled at once, and the
// publish returning clears nothing it should not.
func TestHubReplay_AnEarlyAnswerSettlesIt(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	id := p.send(t, "an early answer")
	heal := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind = 'recipient_app_stored'`)
	ev := hubCases()[0].produce(t, p)
	p.h.drain()
	heal()
	link := &toggleLink{hubLink: &hubLink{hub: p.hub, principal: "bridge:kit-a"}}
	link.after = func(kind string) {
		if kind == "out" {
			p.hub.pump(t)
		}
	}
	p.routeA(link, hubroute.Config{}).RequestReplays()
	p.h.drain()
	p.settledOnce(t, id, ev)
}

// The completion fails at every answer for three repeats: each answered repeat may be
// followed at once, the mark stays through all of them, and the first answer that can be
// kept settles it. After an answered repeat, a silent one is not followed at once.
func TestHubReplay_RepeatedFailuresKeepItOwed(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	id := p.send(t, "failing three times")
	heal := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind = 'recipient_app_stored'`)
	ev := hubCases()[0].produce(t, p)
	p.h.drain()
	link := &toggleLink{hubLink: &hubLink{hub: p.hub, principal: "bridge:kit-a"}}
	a := p.routeA(link, hubroute.Config{})
	for i := 1; i <= 3; i++ {
		a.RequestReplays()
		p.hub.pump(t)
		p.h.drain()
		if p.hub.replays != i || p.owedSince(t) == 0 || p.ledgerRows(t, ev) != 0 {
			t.Fatalf("round %d: %d repeats, owed %d, %d rows", i, p.hub.replays, p.owedSince(t), p.ledgerRows(t, ev))
		}
	}
	// A repeat the Hub does not answer: the next pass waits.
	a.RequestReplays()
	p.hub.mu.Lock()
	p.hub.queue = nil
	p.hub.mu.Unlock()
	a.RequestReplays()
	p.hub.mu.Lock()
	queued := len(p.hub.queue)
	p.hub.mu.Unlock()
	if queued != 0 {
		t.Fatalf("a silent Hub was asked again at once: %d", queued)
	}
	heal()
	b := p.routeA(&hubLink{hub: p.hub, principal: "bridge:kit-a"}, hubroute.Config{}) // a new process: no wait carried over
	b.RequestReplays()
	p.hub.pump(t)
	p.h.drain()
	p.settledOnce(t, id, ev)
}

// Off the Hub nothing goes out and nothing is forgotten; back on, it is asked for.
func TestHubReplay_OffTheHubItWaits(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	id := p.send(t, "off the Hub")
	heal := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind = 'recipient_app_stored'`)
	ev := hubCases()[0].produce(t, p)
	p.h.drain()
	heal()
	link := &toggleLink{hubLink: &hubLink{hub: p.hub, principal: "bridge:kit-a"}, down: true}
	a := p.routeA(link, hubroute.Config{})
	for i := 0; i < 3; i++ {
		a.RequestReplays()
	}
	p.hub.mu.Lock()
	queued := len(p.hub.queue)
	p.hub.mu.Unlock()
	if queued != 0 || p.owedSince(t) == 0 {
		t.Fatalf("off the Hub: %d published, owed %d", queued, p.owedSince(t))
	}
	link.down = false
	a.RequestReplays()
	p.hub.pump(t)
	p.h.drain()
	p.settledOnce(t, id, ev)
}

// At the bound kit-a stops asking and says so: hub_outcome_unknown in the ledger once, the
// operation given up with its owed mark kept, nothing published, and the intent not shown
// as delivered.
func TestHubReplay_AtTheBoundItGivesUpAndSaysSo(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	id := p.send(t, "never settled")
	heal := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind = 'recipient_app_stored'`)
	hubCases()[0].produce(t, p)
	p.h.drain()
	heal()
	late := time.Now().Add(hubroute.ReplayWithin + time.Hour)
	a := p.routeA(&hubLink{hub: p.hub, principal: "bridge:kit-a"}, hubroute.Config{Now: func() time.Time { return late }})
	a.RequestReplays()
	a.RequestReplays()
	p.h.drain()
	p.hub.mu.Lock()
	queued := len(p.hub.queue)
	p.hub.mu.Unlock()
	op, _ := p.h.db.GetHubOp(onlyOp(t, p).OpID)
	var n int
	if err := p.h.db.QueryRow(`SELECT COUNT(*) FROM delivery_evidence WHERE event = 'hub_outcome_unknown' AND matched = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if queued != 0 || n != 1 || op.ReplayGivenUpAt == 0 || op.ReplayAt == 0 {
		t.Fatalf("%d published, %d unknown-outcome rows, given up %d, owed %d", queued, n, op.ReplayGivenUpAt, op.ReplayAt)
	}
	if s := p.h.snap(id); s.Intent.Outcome == database.OutcomeDeliveredApp {
		t.Fatal("given up reads as delivered")
	}
}

// A start asks once for each operation without its outcome, and not for one with it; an
// answer that leaves it unsettled (the recipient has not answered yet) owes nothing, so the
// next pass asks nothing.
func TestHubReplay_AStartAsksOnceForWhatIsNotSettled(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	done := p.send(t, "settled before the start")
	p.hub.pump(t)
	p.h.drain()
	if s := p.h.snap(done); s.Intent.Outcome != database.OutcomeDeliveredApp {
		t.Fatalf("first: %s", s.Intent.Outcome)
	}
	p.hub.hold = true
	p.send(t, "in flight at the start")
	p.hub.pump(t) // admitted and delivered at the Hub, nothing handed on: kit-a has no event of it
	p.hub.hold = false
	p.hub.mu.Lock()
	p.hub.routes["bridge:kit-b"] = nil // kit-b's answer does not come during this test
	p.hub.mu.Unlock()
	a := p.reopenA(t)
	a.RequestReplays()
	p.hub.pump(t)
	a.RequestReplays()
	p.hub.pump(t)
	if p.hub.replays != 1 {
		t.Fatalf("%d repeats at start, want 1 (the unsettled one, once)", p.hub.replays)
	}
}

// Both writes fail in a process that already made its start's asking: only the repeat it
// keeps in memory can bring the completion back, and it does.
func TestHubReplay_BothWritesFailInARunningProcess(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	a := p.routeA(&hubLink{hub: p.hub, principal: "bridge:kit-a"}, hubroute.Config{})
	a.RequestReplays() // the start's pass: nothing to ask for yet
	id := p.send(t, "both writes fail while running")
	healEv := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind = 'recipient_app_stored'`)
	if _, err := p.h.db.Exec(`CREATE TRIGGER test_fail_mark BEFORE UPDATE OF replay_at ON hub_ops BEGIN SELECT RAISE(ABORT, 'test'); END`); err != nil {
		t.Fatal(err)
	}
	ev := hubCases()[0].produce(t, p)
	p.h.drain()
	healEv()
	if _, err := p.h.db.Exec(`DROP TRIGGER test_fail_mark`); err != nil {
		t.Fatal(err)
	}
	if p.owedSince(t) != 0 {
		t.Fatal("the mark was kept")
	}
	a.RequestReplays()
	p.hub.pump(t)
	p.h.drain()
	p.settledOnce(t, id, ev)
}
