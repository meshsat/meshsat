package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/transport"
)

// Fakes and a harness for the delivery-intent acceptance cases (HeMB-NG
// stage 1a: Meshtastic unicast and LXMF, fake clock, fake adapters).
// Assertions read durable state and the fakes' received calls only.
// [MESHSAT-1581]

type fakeIntentClock struct {
	mu      sync.Mutex
	mono    time.Time
	wall    time.Time
	trusted bool
}

func newFakeIntentClock() *fakeIntentClock {
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	return &fakeIntentClock{mono: t0, wall: t0, trusted: true}
}

func (c *fakeIntentClock) Now() time.Time    { c.mu.Lock(); defer c.mu.Unlock(); return c.mono }
func (c *fakeIntentClock) Wall() time.Time   { c.mu.Lock(); defer c.mu.Unlock(); return c.wall }
func (c *fakeIntentClock) WallTrusted() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.trusted }

// Advance moves both clocks, as time passing does.
func (c *fakeIntentClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.mono = c.mono.Add(d)
	c.wall = c.wall.Add(d)
	c.mu.Unlock()
}

// JumpWall moves the wall clock only, as a clock step does.
func (c *fakeIntentClock) JumpWall(d time.Duration) {
	c.mu.Lock()
	c.wall = c.wall.Add(d)
	c.mu.Unlock()
}

// fakeIDMesh is a mesh radio that sends under its own packet ids.
type fakeIDMesh struct {
	transport.MeshTransport // unused methods are not called
	mu                      sync.Mutex
	err                     error // returned by every send while set
	nextID                  uint32
	fixedID                 uint32 // every send under this id (ambiguity cases)
	attempts                int
	sent                    []transport.SendRequest
	ids                     []uint32
}

func (m *fakeIDMesh) SendMessageID(_ context.Context, req transport.SendRequest) (uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts++
	if m.err != nil {
		return 0, m.err
	}
	m.nextID++
	id := m.nextID
	if m.fixedID != 0 {
		id = m.fixedID
	}
	m.sent = append(m.sent, req)
	m.ids = append(m.ids, id)
	return id, nil
}

// SendMessageWithID is the prepared-id send the kits' radio offers.
func (m *fakeIDMesh) SendMessageWithID(_ context.Context, req transport.SendRequest, id uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts++
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, req)
	m.ids = append(m.ids, id)
	return nil
}

// reuseMeshID gives an intent's mesh transmissions the packet id id, as
// a radio that reused one would have sent them (the engine itself never
// hands out an id a kept transmission has).
func (h *intentHarness) reuseMeshID(intentID string, id string) {
	h.t.Helper()
	if _, err := h.db.Exec(`UPDATE delivery_transmissions SET corr_value = ? WHERE intent_id = ? AND corr_kind = ?`, id, intentID, meshCorrKind); err != nil {
		h.t.Fatal(err)
	}
}

func (m *fakeIDMesh) SendMessage(ctx context.Context, req transport.SendRequest) error {
	_, err := m.SendMessageID(ctx, req)
	return err
}

func (m *fakeIDMesh) sends() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.sent) }
func (m *fakeIDMesh) lastID() uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.ids) == 0 {
		return 0
	}
	return m.ids[len(m.ids)-1]
}

// fakeLXMF is an LXMF router: each call takes the next scripted result
// (nil = the proof came back); the message hash is the payload's, so the
// same payload gives the same hash, as a fixed timestamp does upstream.
type fakeLXMF struct {
	mu      sync.Mutex
	results []error
	calls   []database.MessageDelivery
	paid    bool
	block   chan struct{} // when set, Deliver waits on it (after signalling entered)
	entered chan struct{}
}

func (f *fakeLXMF) Deliver(ctx context.Context, del database.MessageDelivery) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, del)
	var err error
	if len(f.results) > 0 {
		err, f.results = f.results[0], f.results[1:]
	}
	block, entered := f.block, f.entered
	f.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if block != nil {
		<-block
	}
	sum := sha256.Sum256(del.Payload)
	return hex.EncodeToString(sum[:]), err
}

func (f *fakeLXMF) CrossesPaidLink(string) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.paid }
func (f *fakeLXMF) n() int                      { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

// countingResolver counts the resolver's calls (S14: a retried request is
// answered without resolving again).
type countingResolver struct {
	RecipientResolver
	mu       sync.Mutex
	resolves int
}

func (c *countingResolver) Resolve(ctx context.Context, id string) (*directory.Contact, error) {
	c.mu.Lock()
	c.resolves++
	c.mu.Unlock()
	return c.RecipientResolver.Resolve(ctx, id)
}

type intentHarness struct {
	t        *testing.T
	ctx      context.Context
	db       *database.DB
	d        *Dispatcher
	store    *directory.SQLStore
	resolver *countingResolver
	esc      *Escalator
	clock    *fakeIntentClock
	cfg      EscalatorConfig
	mesh     *fakeIDMesh
	lx       *fakeLXMF
	workers  map[string]*DeliveryWorker
}

func testEscalatorConfig() EscalatorConfig {
	return EscalatorConfig{Heartbeat: 10 * time.Second, QueueAllowS: 120, ReceiptAllowS: 120, ExpiryAllowS: 3600,
		MeshMTU: 100, RestartSlack: 10 * time.Second, MaxDowntime: 24 * time.Hour}
}

func newIntentHarness(t *testing.T, tune ...func(*EscalatorConfig)) *intentHarness {
	t.Helper()
	db, err := database.New(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`DELETE FROM interfaces`); err != nil {
		t.Fatal(err)
	}
	for _, i := range []database.Interface{
		{ID: "mesh_0", ChannelType: "mesh"}, {ID: "lxmf_0", ChannelType: "lxmf"}, {ID: "cellular_0", ChannelType: "cellular"},
	} {
		i.Label, i.Enabled, i.Config, i.IngressTransforms, i.EgressTransforms = i.ID, true, "{}", "[]", "[]"
		if err := db.InsertInterface(&i); err != nil {
			t.Fatal(err)
		}
	}
	mesh := &fakeIDMesh{nextID: 0x1000}
	d := NewDispatcher(db, channel.NewRegistry(), nil, mesh)
	store := directory.NewSQLStore(db.DB)
	res := &countingResolver{RecipientResolver: store}
	d.SetRecipientResolver(res)
	lx := &fakeLXMF{}
	d.SetLXMFSender(lx)
	cfg := testEscalatorConfig()
	for _, f := range tune {
		f(&cfg)
	}
	h := &intentHarness{t: t, ctx: context.Background(), db: db, d: d, store: store, resolver: res,
		clock: newFakeIntentClock(), cfg: cfg, mesh: mesh, lx: lx}
	h.start(true)
	return h
}

// start builds a new Escalator, as a process start does: its clock bases
// are new, and Recover runs before any worker.
func (h *intentHarness) start(accepting bool) {
	h.esc = NewEscalator(h.db, h.d, h.cfg, h.clock, accepting)
	h.d.SetEscalator(h.esc)
	if err := h.esc.Recover(); err != nil {
		h.t.Fatalf("recover: %v", err)
	}
	h.esc.SetReady()
	h.workers = map[string]*DeliveryWorker{}
	for _, id := range []string{"mesh_0", "lxmf_0"} {
		h.workers[id] = &DeliveryWorker{db: h.db, channelID: id, mesh: h.mesh, lxmf: h.lx, intents: h.esc,
			desc: channel.ChannelDescriptor{ID: id, CanSend: true}}
	}
}

// restart is a stop and a start: whatever was in memory is gone.
func (h *intentHarness) restart() { h.start(h.esc.Accepting()) }

const (
	annMesh = "!0000abcd"
	annLXMF = "0d2e456bc1a14db717c3f070765650fa"
)

func (h *intentHarness) contact(addrs ...directory.Address) string {
	h.t.Helper()
	c := &directory.Contact{DisplayName: "Ann"}
	if err := h.store.CreateContact(h.ctx, c); err != nil {
		h.t.Fatal(err)
	}
	for _, a := range addrs {
		a := a
		a.ContactID = c.ID
		if err := h.store.AddAddress(h.ctx, &a); err != nil {
			h.t.Fatal(err)
		}
	}
	return c.ID
}

func meshAddr(rank int) directory.Address {
	return directory.Address{Kind: directory.KindMeshtastic, Value: annMesh, PrimaryRank: rank}
}

func lxmfAddr(rank int) directory.Address {
	return directory.Address{Kind: directory.KindReticulum, Value: annLXMF, PrimaryRank: rank}
}

func (h *intentHarness) submit(contactID, text string, s directory.Strategy) string {
	h.t.Helper()
	sub, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: contactID, Body: []byte(text), Strategy: s})
	if err != nil {
		h.t.Fatalf("submit: %v", err)
	}
	return sub.Intents[0].ID
}

// work runs one worker pass over the attempts that are due.
func (h *intentHarness) work(iface string) int {
	h.t.Helper()
	dels, err := h.db.GetPendingDeliveries(iface, 10)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, del := range dels {
		h.workers[iface].deliver(h.ctx, del)
	}
	return len(dels)
}

// due makes every waiting retry due now (the ledger's next_retry is wall
// time; the fake clock does not move it).
func (h *intentHarness) due() {
	if _, err := h.db.Exec(`UPDATE message_deliveries SET next_retry = NULL WHERE intent_id IS NOT NULL`); err != nil {
		h.t.Fatal(err)
	}
}

func (h *intentHarness) eval(id string) { h.esc.Evaluate(id) }

func (h *intentHarness) snap(id string) *database.IntentSnapshot {
	h.t.Helper()
	s, err := h.db.LoadIntentSnapshot(id)
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

// ack delivers a ROUTING_APP answer for packet id from node from.
func (h *intentHarness) ack(id uint32, from string, reason uint32) {
	n, _ := strconv.ParseUint(strings.TrimPrefix(from, "!"), 16, 32)
	h.esc.MeshRoutingReceipt("mesh_0", transport.MeshMessage{From: uint32(n), To: 0x0000aaaa, RequestID: id,
		PortNum: int(transport.PortNumRouting), Routing: &transport.RoutingInfo{ErrorReason: reason, ErrorName: routingName(reason)}})
	h.drain()
}

// drain does what the Escalator's loop does with the notices waiting:
// looks at each intent named.
func (h *intentHarness) drain() {
	for {
		select {
		case id := <-h.esc.notify:
			h.esc.Evaluate(id)
		default:
			return
		}
	}
}

func routingName(r uint32) string {
	if r == 0 {
		return "NONE"
	}
	if r == 5 {
		return "MAX_RETRANSMIT"
	}
	return "ERROR"
}

// setPlanPaid marks one route of an intent's frozen plan paid (a fixture
// for the paid-route rules: stage 1a admits no paid route).
func (h *intentHarness) setPlanPaid(id string, routeIndex int) {
	h.t.Helper()
	in, err := h.db.GetIntent(id)
	if err != nil {
		h.t.Fatal(err)
	}
	for i := range in.RoutePlan {
		if in.RoutePlan[i].RouteIndex == routeIndex {
			in.RoutePlan[i].Paid = true
		}
	}
	b, _ := json.Marshal(in.RoutePlan)
	if _, err := h.db.Exec(`UPDATE delivery_intents SET route_plan = ? WHERE id = ?`, string(b), id); err != nil {
		h.t.Fatal(err)
	}
}

func rowsByRound(s *database.IntentSnapshot) map[int]database.IntentAttempt {
	m := map[int]database.IntentAttempt{}
	for _, r := range s.Rows {
		m[r.Round] = r
	}
	return m
}

func txOf(s *database.IntentSnapshot, rowID int64) []database.DeliveryTransmission {
	var out []database.DeliveryTransmission
	for _, t := range s.Transmissions {
		if t.AttemptID == rowID {
			out = append(out, t)
		}
	}
	return out
}
