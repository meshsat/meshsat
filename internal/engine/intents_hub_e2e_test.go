package engine

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/hubmsg"
	"meshsat/internal/hubroute"
)

// Bridge to Bridge through a Hub that follows the contract (docs/bridge-hub-protocol.md
// "Messaging v1" in meshsat-hub; its own tests run the Hub's real code): kit-a's Escalator
// sends to kit-b's "bridge:kit-b", the Hub delivers on kit-b's msg/in, kit-b commits, answers
// and hands the message on, the Hub turns the answer into kit-a's evidence. The Hub here keeps
// what it is asked to carry in a queue the test pumps, so order, copies, restarts and expiry
// are the test's to choose. [MESHSAT-1584]

type hubLink struct {
	hub       *contractHub
	principal string
}

func (l *hubLink) PublishMsg(kind string, payload []byte, _ time.Duration) error {
	l.hub.mu.Lock()
	defer l.hub.mu.Unlock()
	l.hub.queue = append(l.hub.queue, hubFrame{from: l.principal, kind: kind, payload: append([]byte(nil), payload...)})
	return nil
}
func (l *hubLink) IsConnected() bool { return true }
func (l *hubLink) Principal() string { return l.principal }

type hubFrame struct {
	from, kind string
	payload    []byte
}

// contractHub does what the Hub does with a frame: a submission is admitted and delivered on
// the recipient's msg/in with hub_forwarded to the sender, and a repeat of one is answered
// with every event of it again; an answer becomes the sender's evidence by C5/C6a.
type contractHub struct {
	mu      sync.Mutex
	queue   []hubFrame
	routes  map[string]*hubroute.Route // by principal
	ops     map[string]hubmsg.Submission
	senders map[string]string // op id -> sender principal
	// deliveries and statuses are what the Hub published, kept to publish again.
	deliveries []hubFrame
	statuses   []hubFrame
	hold       bool // keep what is published instead of handing it on
	replays    int  // repeats answered with every event again
}

func newContractHub() *contractHub {
	return &contractHub{routes: map[string]*hubroute.Route{}, ops: map[string]hubmsg.Submission{}, senders: map[string]string{}}
}

func (c *contractHub) event(s hubmsg.Submission, kind, class, authority, reason string) []byte {
	b, _ := json.Marshal(hubmsg.Event{V: 1, OpID: s.OpID, Mid: s.Mid, Recipient: s.To, EventID: hubmsg.NewV7(time.Now()), Rev: 1,
		Class: class, Kind: kind, Reason: reason, Authority: authority, At: time.Now().UTC().Format(time.RFC3339)})
	return b
}

func (c *contractHub) publish(to, kind string, payload []byte) {
	f := hubFrame{from: to, kind: kind, payload: payload}
	c.mu.Lock()
	if kind == "in" {
		c.deliveries = append(c.deliveries, f)
	} else {
		c.statuses = append(c.statuses, f)
	}
	hold := c.hold
	r := c.routes[to]
	c.mu.Unlock()
	if hold || r == nil {
		return
	}
	if kind == "in" {
		r.HandleIn(payload)
	} else {
		r.HandleStatus(payload)
	}
}

// replay publishes again what was published (a QoS 1 redelivery, a reconnect).
func (c *contractHub) replay(f hubFrame) {
	c.mu.Lock()
	r := c.routes[f.from]
	c.mu.Unlock()
	if f.kind == "in" {
		r.HandleIn(f.payload)
	} else {
		r.HandleStatus(f.payload)
	}
}

// pump carries every queued frame until none is left.
func (c *contractHub) pump(t *testing.T) {
	t.Helper()
	for {
		c.mu.Lock()
		if len(c.queue) == 0 {
			c.mu.Unlock()
			return
		}
		f := c.queue[0]
		c.queue = c.queue[1:]
		c.mu.Unlock()
		switch f.kind {
		case "out":
			var s hubmsg.Submission
			if err := json.Unmarshal(f.payload, &s); err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			if prev, ok := c.ops[s.OpID]; ok && c.senders[s.OpID] == f.from && prev == s {
				// A repeat (C4): every event of the operation again, under its own id.
				var again []hubFrame
				for _, st := range c.statuses {
					var e hubmsg.Event
					if json.Unmarshal(st.payload, &e) == nil && e.OpID == s.OpID {
						again = append(again, st)
					}
				}
				c.replays++
				c.mu.Unlock()
				for _, st := range again {
					c.replay(st)
				}
				continue
			}
			c.ops[s.OpID], c.senders[s.OpID] = s, f.from
			c.mu.Unlock()
			d, _ := json.Marshal(hubmsg.Delivery{V: 1, OpID: s.OpID, Mid: s.Mid, From: f.from, To: s.To, ContentType: s.ContentType,
				MidExpiresAt: s.MidExpiresAt, ExpiresAt: s.ExpiresAt, ContentSHA256: s.ContentSHA256, Body: s.Body})
			c.publish(s.To, "in", d)
			c.publish(f.from, "status", c.event(s, "hub_forwarded", "forwarded", "hub", ""))
		case "ack":
			var a hubmsg.Ack
			_ = json.Unmarshal(f.payload, &a)
			c.mu.Lock()
			s, ok := c.ops[a.OpID]
			sender := c.senders[a.OpID]
			c.mu.Unlock()
			if !ok || s.To != f.from || sender != a.From || s.Mid != a.Mid {
				continue
			}
			switch {
			case a.Result == "stored":
				c.publish(sender, "status", c.event(s, "recipient_app_stored", "completed", "recipient", ""))
			case a.Reason == "receiver_busy":
				c.publish(sender, "status", c.event(s, "recipient_busy", "unknown", "recipient", a.Reason))
			case a.Reason == "expired_at_recipient":
				c.publish(sender, "status", c.event(s, "recipient_refused", "expired", "recipient", a.Reason))
			default:
				c.publish(sender, "status", c.event(s, "recipient_refused", "failed", "recipient", a.Reason))
			}
		}
	}
}

type hubPair struct {
	h        *intentHarness
	hub      *contractHub
	a        *hubroute.Route
	dbB      *database.DB
	b        *hubroute.Route
	handedB  *[]database.HubHandoff
	contactB string
}

func newHubPair(t *testing.T, cfgB hubroute.Config) *hubPair {
	t.Helper()
	h := newIntentHarness(t, hubAllowances)
	hub := newContractHub()
	if err := h.db.InsertInterface(&database.Interface{ID: "hub_0", ChannelType: "hub", Label: "hub_0", Enabled: true,
		Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"}); err != nil {
		t.Fatal(err)
	}
	a := hubroute.New(h.db, &hubLink{hub: hub, principal: "bridge:kit-a"}, hubroute.Config{})
	h.d.SetHubRoute(a)
	h.workers["hub_0"] = &DeliveryWorker{db: h.db, channelID: "hub_0", intents: h.esc, hubRoute: h.d.hubRoute}
	dbB, err := database.New(filepath.Join(t.TempDir(), "kit-b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbB.Close() })
	p := &hubPair{h: h, hub: hub, a: a, dbB: dbB}
	p.startB(t, cfgB)
	hub.routes["bridge:kit-a"] = a
	p.contactB = h.contact(hubAddr(1), meshAddr(2))
	return p
}

// startB starts kit-b's route, as a process start does.
func (p *hubPair) startB(t *testing.T, cfg hubroute.Config) {
	t.Helper()
	p.b = hubroute.New(p.dbB, &hubLink{hub: p.hub, principal: "bridge:kit-b"}, cfg)
	var handed []database.HubHandoff
	p.b.SetHandoff(func(h database.HubHandoff) error { handed = append(handed, h); return nil })
	p.handedB = &handed
	p.hub.mu.Lock()
	p.hub.routes["bridge:kit-b"] = p.b
	p.hub.mu.Unlock()
}

func (p *hubPair) send(t *testing.T, text string) string {
	t.Helper()
	id := p.h.submit(p.contactB, text, directory.StrategyOrderedFallback)
	p.h.work("hub_0")
	p.h.drain()
	return id
}

func (p *hubPair) storedAtB(t *testing.T) int {
	t.Helper()
	var n int
	if err := p.dbB.QueryRow(`SELECT COUNT(*) FROM messages WHERE transport = ?`, database.HubTransport).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestHubBridgeToBridge_DeliveredToTheAppAndHandedOnOnce(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	id := p.send(t, "Water at the north gate")
	p.hub.pump(t)
	p.h.drain()
	p.b.HandOff()
	if s := p.h.snap(id); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeDeliveredApp {
		t.Fatalf("kit-a: %s/%s", s.Intent.State, s.Intent.Outcome)
	}
	if n := p.storedAtB(t); n != 1 || len(*p.handedB) != 1 || (*p.handedB)[0].From != "bridge:kit-a" ||
		(*p.handedB)[0].Text != "Water at the north gate" {
		t.Fatalf("kit-b stored %d, handed on %+v", n, *p.handedB)
	}
	if p.h.mesh.sends() != 0 {
		t.Fatal("the mesh was used")
	}
}

// Every frame the Hub published, published again and in reverse: one message at kit-b, one
// hand-off, kit-a completed.
func TestHubBridgeToBridge_CopiesAndReorderChangeNothing(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	p.hub.hold = true
	id := p.send(t, "twice and backwards")
	p.hub.pump(t) // the delivery and hub_forwarded, held
	p.hub.hold = false
	for i := len(p.hub.deliveries) - 1; i >= 0; i-- {
		p.hub.replay(p.hub.deliveries[i])
		p.hub.replay(p.hub.deliveries[i])
	}
	p.hub.hold = true
	p.hub.pump(t) // the answers, two of them: two completed events, held
	p.hub.hold = false
	sts := append([]hubFrame(nil), p.hub.statuses...)
	for i := len(sts) - 1; i >= 0; i-- { // completed before forwarded, each twice
		p.hub.replay(sts[i])
		p.hub.replay(sts[i])
	}
	p.h.drain()
	p.b.HandOff()
	if s := p.h.snap(id); s.Intent.Outcome != database.OutcomeDeliveredApp {
		t.Fatalf("kit-a: %s", s.Intent.Outcome)
	}
	if n := p.storedAtB(t); n != 1 || len(*p.handedB) != 1 {
		t.Fatalf("kit-b stored %d, handed on %d", n, len(*p.handedB))
	}
	evs, _, _ := p.h.db.HubOpEvents(p.hub.ops[firstOp(p.hub)].OpID)
	if len(evs) != 3 { // hub_forwarded once, recipient_app_stored twice under two ids (two answers)
		t.Fatalf("kit-a kept %d events", len(evs))
	}
}

func firstOp(c *contractHub) string {
	for id := range c.ops {
		return id
	}
	return ""
}

// kit-b stops between its commit and its hand-off: the new process hands the message on, and
// the Hub's next delivery of it is answered, not stored again.
func TestHubBridgeToBridge_TheReceiverRestartsBeforeItsHandOff(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	id := p.send(t, "across a restart")
	p.hub.pump(t)
	p.startB(t, hubroute.Config{}) // the old process never ran its hand-off
	p.hub.replay(p.hub.deliveries[0])
	p.hub.pump(t)
	p.b.HandOff()
	p.h.drain()
	if n := p.storedAtB(t); n != 1 || len(*p.handedB) != 1 {
		t.Fatalf("kit-b stored %d, handed on %d", n, len(*p.handedB))
	}
	if s := p.h.snap(id); s.Intent.Outcome != database.OutcomeDeliveredApp {
		t.Fatalf("kit-a: %s", s.Intent.Outcome)
	}
}

// A delivery that reaches kit-b after the operation's deadline is refused at commit; kit-a
// hears it as expired and its next route takes over.
func TestHubBridgeToBridge_TooLateAtTheReceiverHandsOverToTheNextRoute(t *testing.T) {
	late := time.Now().Add(10 * time.Minute)
	p := newHubPair(t, hubroute.Config{Now: func() time.Time { return late }})
	id := p.send(t, "too late")
	p.hub.pump(t)
	p.h.drain()
	p.h.work("mesh_0")
	p.h.drain()
	if n := p.storedAtB(t); n != 0 {
		t.Fatalf("kit-b stored %d", n)
	}
	if s := p.h.snap(id); s.Intent.ActiveRound != 2 || p.h.mesh.sends() != 1 {
		t.Fatalf("round %d, %d mesh sends", s.Intent.ActiveRound, p.h.mesh.sends())
	}
}

// kit-b at capacity answers busy: kit-a hears unknown, which ends nothing; the receipt
// allowance does, and the next route takes over.
func TestHubBridgeToBridge_ABusyReceiverIsNotAFailure(t *testing.T) {
	p := newHubPair(t, hubroute.Config{Capacity: 1})
	// Fill kit-b's inbox with a live entry from someone else.
	now := time.Now()
	s, _ := hubmsg.NewSubmission(hubmsg.NewV7(now), hubmsg.NewV7(now), "bridge:kit-b", "x", now.Add(time.Hour).UnixMilli(), now.Add(time.Hour).UnixMilli())
	d, _ := json.Marshal(hubmsg.Delivery{V: 1, OpID: s.OpID, Mid: s.Mid, From: "bridge:kit-c", To: s.To, ContentType: s.ContentType,
		MidExpiresAt: s.MidExpiresAt, ExpiresAt: s.ExpiresAt, ContentSHA256: s.ContentSHA256, Body: s.Body})
	p.b.HandleIn(d)
	p.hub.mu.Lock()
	p.hub.queue = nil // kit-b's answer to kit-c, nobody's business here
	p.hub.mu.Unlock()
	id := p.send(t, "busy")
	p.hub.pump(t)
	p.h.drain()
	if s := p.h.snap(id); s.Intent.ActiveRound != 1 || s.Intent.State == database.IntentClosed {
		t.Fatalf("a busy answer ended it: round %d %s/%s", s.Intent.ActiveRound, s.Intent.State, s.Intent.Outcome)
	}
	p.h.clock.Advance(121 * time.Second)
	p.h.esc.Heartbeat()
	p.h.drain()
	p.h.work("mesh_0")
	p.h.drain()
	if s := p.h.snap(id); s.Intent.ActiveRound != 2 || p.h.mesh.sends() != 1 {
		t.Fatalf("round %d, %d mesh sends", s.Intent.ActiveRound, p.h.mesh.sends())
	}
}
