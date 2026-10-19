package engine

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/hubmsg"
	"meshsat/internal/hubreporter"
	"meshsat/internal/hubroute"
)

// The Hub app route (hub_msg, contract C4-C6) through the engine with the real route adapter
// and a fake Hub session: a contact's "bridge:<id>" plans first by default, the broker's
// PUBACK is accepted, the recipient's stored answer through the Hub completes it as delivered
// to the MeshSat app, a refusal or a failure hands over to the next route, silence past the
// receipt allowance does too, and late completion still raises the outcome. [MESHSAT-1584]

type fakeHubSession struct {
	mu   sync.Mutex
	up   bool
	err  error
	pubs []struct {
		kind    string
		payload []byte
	}
}

func (f *fakeHubSession) PublishMsg(kind string, payload []byte, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.pubs = append(f.pubs, struct {
		kind    string
		payload []byte
	}{kind, append([]byte(nil), payload...)})
	return nil
}
func (f *fakeHubSession) IsConnected() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.up }
func (f *fakeHubSession) Principal() string { return "bridge:kit-a" }

func (f *fakeHubSession) submissions(t *testing.T) []hubmsg.Submission {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []hubmsg.Submission
	for _, p := range f.pubs {
		if p.kind != "out" {
			continue
		}
		var s hubmsg.Submission
		if err := json.Unmarshal(p.payload, &s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

const annBridge = "bridge:kit-b"

func hubAddr(rank int) directory.Address {
	return directory.Address{Kind: directory.KindMQTT, Value: annBridge, PrimaryRank: rank}
}

// hubAllowances are the defaults of DefaultEscalatorConfig.
func hubAllowances(c *EscalatorConfig) { c.HubQueueAllowS, c.HubReceiptAllowS = 30, 120 }

// orderedFallback gives the contact a policy of its own, so its sends resolve to
// ORDERED_FALLBACK without an explicit strategy in the request.
func orderedFallback(t *testing.T, h *intentHarness, contactID string) string {
	t.Helper()
	if err := h.store.UpsertPolicy(h.ctx, &directory.DispatchPolicy{ScopeType: directory.ScopeContact, ScopeID: contactID,
		Strategy: directory.StrategyOrderedFallback}); err != nil {
		t.Fatal(err)
	}
	return contactID
}

func useHub(t *testing.T, h *intentHarness, sess *fakeHubSession) *hubroute.Route {
	t.Helper()
	if err := h.db.InsertInterface(&database.Interface{ID: "hub_0", ChannelType: "hub", Label: "hub_0", Enabled: true,
		Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"}); err != nil {
		t.Fatal(err)
	}
	r := hubroute.New(h.db, sess, hubroute.Config{})
	h.d.SetHubRoute(r)
	h.workers["hub_0"] = &DeliveryWorker{db: h.db, channelID: "hub_0", intents: h.esc, hubRoute: h.d.hubRoute,
		desc: channel.ChannelDescriptor{ID: "hub_0", CanSend: true}}
	return r
}

// status publishes one event of op on the bridge's msg/status, as the Hub would.
func status(t *testing.T, r *hubroute.Route, s hubmsg.Submission, kind, class, authority, reason string) {
	t.Helper()
	e := hubmsg.Event{V: 1, OpID: s.OpID, Mid: s.Mid, Recipient: s.To, EventID: hubmsg.NewV7(time.Now()), Rev: 1,
		Class: class, Kind: kind, Reason: reason, Authority: authority, At: time.Now().UTC().Format(time.RFC3339)}
	b, _ := json.Marshal(e)
	r.HandleStatus(b)
}

func TestIntentHub_PlansFirstAndTheAppsStoredAnswerCompletesIt(t *testing.T) {
	h := newIntentHarness(t, hubAllowances)
	sess := &fakeHubSession{up: true}
	r := useHub(t, h, sess)
	// The mesh address ranks first in the contact; by default the Hub route plans before it.
	sub, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: orderedFallback(t, h, h.contact(meshAddr(1), hubAddr(2))),
		Body: []byte("through the Hub")})
	if err != nil {
		t.Fatal(err)
	}
	in := sub.Intents[0]
	p := in.RoutePlan[0]
	if !p.Admitted || p.Paid || p.AdapterMode != modeHub || p.InterfaceID != "hub_0" || p.CompletionRule != database.OutcomeDeliveredApp ||
		p.QueueAllowS != 30 || p.ReceiptAllowS != 120 || p.RouteIndex != 1 {
		t.Fatalf("plan %+v", p)
	}
	h.work("hub_0")
	h.drain()
	subs := sess.submissions(t)
	if len(subs) != 1 {
		t.Fatalf("%d submissions", len(subs))
	}
	s := subs[0]
	want, _ := hubmsg.ContentSHA256(in.ID, annBridge, hubmsg.ContentTypeText, s.MidExpiresAt, []byte("through the Hub"))
	if s.Mid != in.ID || s.To != annBridge || s.Body != "through the Hub" || s.ContentSHA256 != want || !hubmsg.ValidV7(s.OpID) ||
		s.MidExpiresAt != messageDeadline(in).UnixMilli() || s.ExpiresAt > s.MidExpiresAt || s.ExpiresAt > time.Now().Add(121*time.Second).UnixMilli() {
		t.Fatalf("submission %+v", s)
	}
	if st := h.snap(in.ID); st.Intent.State == database.IntentClosed || st.Intent.Outcome != database.OutcomePending {
		t.Fatalf("closed on the broker's PUBACK: %s/%s", st.Intent.State, st.Intent.Outcome)
	}
	status(t, r, s, "hub_forwarded", "forwarded", "hub", "")
	h.drain()
	status(t, r, s, "recipient_app_stored", "completed", "recipient", "")
	h.drain()
	st := h.snap(in.ID)
	if st.Intent.State != database.IntentClosed || st.Intent.Outcome != database.OutcomeDeliveredApp {
		t.Fatalf("%s/%s; want closed delivered_app", st.Intent.State, st.Intent.Outcome)
	}
	if h.mesh.sends() != 0 {
		t.Fatal("the mesh was used after the app stored it")
	}
	if outcomeLabel(st.Intent.Outcome) != "delivered to the MeshSat app" {
		t.Fatalf("label %q", outcomeLabel(st.Intent.Outcome))
	}
}

func TestIntentHub_AnExplicitStrategyKeepsTheContactsOrder(t *testing.T) {
	h := newIntentHarness(t)
	useHub(t, h, &fakeHubSession{up: true})
	sub, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: h.contact(meshAddr(1), hubAddr(2)), Body: []byte("x"),
		Strategy: directory.StrategyOrderedFallback})
	if err != nil {
		t.Fatal(err)
	}
	if p := sub.Intents[0].RoutePlan; p[0].AdapterMode != modeMeshUnicast || p[1].AdapterMode != modeHub {
		t.Fatalf("plan %+v", p)
	}
}

func TestIntentHub_NotConnectedIsNotAdmittedAndTheMeshGoesFirst(t *testing.T) {
	h := newIntentHarness(t)
	useHub(t, h, &fakeHubSession{up: false})
	id := h.submit(orderedFallback(t, h, h.contact(meshAddr(2), hubAddr(1))), "x", "")
	plan := h.snap(id).Intent.RoutePlan
	if plan[0].AdapterMode != modeHub || plan[0].Admitted || plan[0].Reason != "skipped_offline" ||
		plan[1].AdapterMode != modeMeshUnicast || !plan[1].Admitted {
		t.Fatalf("plan %+v", plan)
	}
	h.work("hub_0")
	h.work("mesh_0")
	h.drain()
	if h.mesh.sends() != 1 {
		t.Fatalf("%d mesh sends", h.mesh.sends())
	}
}

// Under PRIMARY_ONLY (the seeded Routine policy) the contact's primary stays the route: the
// plan never moves the Hub route into its place.
func TestIntentHub_PrimaryOnlyKeepsTheContactsPrimary(t *testing.T) {
	h := newIntentHarness(t)
	sess := &fakeHubSession{up: true}
	useHub(t, h, sess)
	id := h.submit(h.contact(meshAddr(1), hubAddr(2)), "the mesh is the primary", "")
	in := h.snap(id).Intent
	if in.EffectiveStrategy != string(directory.StrategyPrimaryOnly) || in.RoutePlan[0].AdapterMode != modeMeshUnicast {
		t.Fatalf("%s, plan %+v", in.EffectiveStrategy, in.RoutePlan)
	}
	h.work("hub_0")
	h.work("mesh_0")
	h.drain()
	if h.mesh.sends() != 1 || len(sess.submissions(t)) != 0 {
		t.Fatalf("%d mesh sends, %d Hub submissions", h.mesh.sends(), len(sess.submissions(t)))
	}
}

func TestIntentHub_ARefusalOrAFailureHandsOverToTheNextRoute(t *testing.T) {
	for _, tc := range []struct {
		name string
		send func(t *testing.T, r *hubroute.Route, s hubmsg.Submission)
	}{
		{"the Hub refused the submission", func(t *testing.T, r *hubroute.Route, s hubmsg.Submission) {
			b, _ := json.Marshal(hubmsg.Refusal{V: 1, Type: "refusal", OpID: s.OpID, Mid: s.Mid, Reason: "refused_unknown_recipient"})
			r.HandleStatus(b)
		}},
		{"the recipient refused it", func(t *testing.T, r *hubroute.Route, s hubmsg.Submission) {
			status(t, r, s, "recipient_refused", "failed", "recipient", "mid_conflict")
		}},
		{"the Hub expired it", func(t *testing.T, r *hubroute.Route, s hubmsg.Submission) {
			status(t, r, s, "hub_expired", "expired", "hub", "expired")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newIntentHarness(t)
			sess := &fakeHubSession{up: true}
			r := useHub(t, h, sess)
			id := h.submit(h.contact(hubAddr(1), meshAddr(2)), "hub then mesh", directory.StrategyOrderedFallback)
			h.work("hub_0")
			h.drain()
			tc.send(t, r, sess.submissions(t)[0])
			h.drain()
			h.work("mesh_0")
			h.drain()
			if h.mesh.sends() != 1 {
				t.Fatalf("%d mesh sends; want the mesh to take over", h.mesh.sends())
			}
			if s := h.snap(id); s.Intent.ActiveRound != 2 {
				t.Fatalf("round %d", s.Intent.ActiveRound)
			}
		})
	}
}

func TestIntentHub_SilenceHandsOverAndALateStoredAnswerStillCounts(t *testing.T) {
	h := newIntentHarness(t, hubAllowances)
	sess := &fakeHubSession{up: true}
	r := useHub(t, h, sess)
	id := h.submit(h.contact(hubAddr(1), meshAddr(2)), "nobody answered in time", directory.StrategyOrderedFallback)
	h.work("hub_0")
	h.drain()
	s := sess.submissions(t)[0]
	status(t, r, s, "hub_forwarded", "forwarded", "hub", "")
	h.drain()
	h.clock.Advance(121 * time.Second)
	h.esc.Heartbeat()
	h.drain()
	h.work("mesh_0")
	h.drain()
	if h.mesh.sends() != 1 {
		t.Fatalf("%d mesh sends after the receipt allowance", h.mesh.sends())
	}
	// The recipient's app stored it after all (a delivery the broker had queued).
	status(t, r, s, "recipient_app_stored", "completed", "recipient", "")
	h.drain()
	h.clock.Advance(5 * time.Minute)
	h.esc.Heartbeat()
	h.drain()
	if st := h.snap(id); st.Intent.Outcome != database.OutcomeDeliveredApp {
		t.Fatalf("outcome %s; a late completion is recorded and raises it", st.Intent.Outcome)
	}
	if sends := len(sess.submissions(t)); sends != 1 {
		t.Fatalf("%d submissions; late evidence never sends again", sends)
	}
}

func TestIntentHub_EvidenceForAnotherOperationOrACopyChangesNothing(t *testing.T) {
	h := newIntentHarness(t)
	sess := &fakeHubSession{up: true}
	r := useHub(t, h, sess)
	id := h.submit(h.contact(hubAddr(1), meshAddr(2)), "only my own evidence", directory.StrategyOrderedFallback)
	h.work("hub_0")
	h.drain()
	s := sess.submissions(t)[0]
	other := s
	other.To = "bridge:kit-z" // another recipient
	status(t, r, other, "recipient_app_stored", "completed", "recipient", "")
	wrongKind := s
	status(t, r, wrongKind, "hub_forwarded", "completed", "hub", "") // completed needs the recipient's authority
	h.drain()
	if st := h.snap(id); st.Intent.State == database.IntentClosed {
		t.Fatalf("closed on evidence that may not join it: %s", st.Intent.Outcome)
	}
	evs, valid, _ := h.db.HubOpEvents(s.OpID)
	if len(evs) != 2 || valid[0] || valid[1] {
		t.Fatalf("kept %d events, valid %v; want both rejected", len(evs), valid)
	}
	// One event delivered twice is one piece of evidence.
	e := hubmsg.Event{V: 1, OpID: s.OpID, Mid: s.Mid, Recipient: s.To, EventID: hubmsg.NewV7(time.Now()), Rev: 2,
		Class: "forwarded", Kind: "hub_forwarded", Authority: "hub", At: time.Now().UTC().Format(time.RFC3339)}
	b, _ := json.Marshal(e)
	r.HandleStatus(b)
	r.HandleStatus(b)
	h.drain()
	n := 0
	for _, ev := range h.snap(id).Evidence {
		if ev.Event == "hub_forwarded" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d hub_forwarded evidence rows; a copy is the same event", n)
	}
}

func TestIntentHub_APublishWithoutAPubackIsUnknownAndNotResent(t *testing.T) {
	h := newIntentHarness(t)
	sess := &fakeHubSession{up: true, err: hubreporter.ErrMsgNoPuback}
	useHub(t, h, sess)
	id := h.submit(h.contact(hubAddr(1), meshAddr(2)), "the broker never answered", directory.StrategyOrderedFallback)
	h.work("hub_0")
	h.drain()
	h.work("mesh_0")
	h.drain()
	st := h.snap(id)
	if len(st.Transmissions) == 0 || st.Transmissions[0].OutcomeClass != database.ClassUnknown {
		t.Fatalf("transmissions %+v", st.Transmissions)
	}
	if h.mesh.sends() != 1 || st.Intent.ActiveRound != 2 {
		t.Fatalf("%d mesh sends, round %d; an unknown outcome hands over", h.mesh.sends(), st.Intent.ActiveRound)
	}
	h.due()
	if n := h.work("hub_0"); n != 0 {
		t.Fatalf("the unknown attempt was claimed again: %d", n)
	}
}

func TestIntentHub_ADisconnectBeforeTheSendWaitsWithoutSpendingARetry(t *testing.T) {
	h := newIntentHarness(t)
	sess := &fakeHubSession{up: true}
	useHub(t, h, sess)
	id := h.submit(h.contact(hubAddr(1), meshAddr(2)), "the Hub went away", directory.StrategyOrderedFallback)
	sess.mu.Lock()
	sess.err = hubreporter.ErrMsgNotConnected
	sess.mu.Unlock()
	h.work("hub_0")
	h.drain()
	st := h.snap(id)
	if st.Rows[0].Status != "retry" || st.Rows[0].Retries != 0 {
		t.Fatalf("row %+v", st.Rows[0])
	}
	if len(sess.submissions(t)) != 0 {
		t.Fatal("published while down")
	}
}
