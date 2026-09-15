package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

// feedGateway is a gateway whose inbound channel the test owns, so a frame can
// be pushed into StartGatewayReceiver exactly as the APRS or cellular gateway
// would hand it over.
type feedGateway struct {
	typ string
	ch  chan gateway.InboundMessage
}

func (g *feedGateway) Start(ctx context.Context) error                               { return nil }
func (g *feedGateway) Stop() error                                                   { return nil }
func (g *feedGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error { return nil }
func (g *feedGateway) Receive() <-chan gateway.InboundMessage                        { return g.ch }
func (g *feedGateway) Status() gateway.GatewayStatus                                 { return gateway.GatewayStatus{Connected: true} }
func (g *feedGateway) Enqueue(msg *transport.MeshMessage) error                      { return nil }
func (g *feedGateway) Type() string                                                  { return g.typ }

const authTestChain = `[{"type": "smaz2"}, {"type": "encrypt", "params": {"key_ref": "aprs:shared"}}, {"type": "base64"}]`

// newInboundAuthHarness wires a processor whose dispatcher carries a transform
// pipeline with a fixed key, one bearer interface with the given ingress chain,
// mesh_0, and an ingress rule that relays every text from the bearer to mesh_0
// (the booth aprs -> mesh rule, which has no sender filter).
func newInboundAuthHarness(t *testing.T, ifaceID, channelType, ingress string) (*Processor, *database.DB, *TransformPipeline, *feedGateway) {
	t.Helper()
	d, db := setupTestDispatcher(t)
	t.Cleanup(func() { db.Close() })

	tp := NewTransformPipeline()
	tp.SetKeyResolver(stubKeyResolver{hexKey: strings.Repeat("5a", 32)})
	d.SetTransformPipeline(tp)

	// The schema seeds the standard interfaces; make sure both exist and set
	// the bearer's chain.
	for id, ct := range map[string]string{"mesh_0": "mesh", ifaceID: channelType} {
		if _, err := db.Exec(`INSERT OR IGNORE INTO interfaces (id, channel_type, label, enabled, config, ingress_transforms, egress_transforms)
			VALUES (?, ?, ?, 1, '{}', '[]', '[]')`, id, ct, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE interfaces SET enabled = 1, ingress_transforms = ?, egress_transforms = ? WHERE id = ?`,
		ingress, ingress, ifaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertAccessRule(&database.AccessRule{InterfaceID: ifaceID, Direction: "ingress", Name: "booth relay",
		Enabled: true, Priority: 10, Action: "forward", ForwardTo: "mesh_0", Filters: `{}`}); err != nil {
		t.Fatal(err)
	}
	ae := rules.NewAccessEvaluator(db)
	if err := ae.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}
	d.SetAccessEvaluator(ae)

	p := NewProcessor(db, nil)
	p.SetDispatcher(d)

	gw := &feedGateway{typ: channelType, ch: make(chan gateway.InboundMessage, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p.StartGatewayReceiver(ctx, gw)
	return p, db, tp, gw
}

// deliver pushes one inbound message and waits for the receiver to either
// persist it or drop it.
func deliver(t *testing.T, p *Processor, db *database.DB, gw *feedGateway, msg gateway.InboundMessage) (persisted []database.Message, dropped uint64) {
	t.Helper()
	iface := msg.Source + "_0"
	before := p.InboundDropped()[iface]
	_, beforeCount, _ := db.GetMessages(database.MessageFilter{Limit: 1})
	gw.ch <- msg
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msgs, n, err := db.GetMessages(database.MessageFilter{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if n > beforeCount {
			return msgs, p.InboundDropped()[iface] - before
		}
		if d := p.InboundDropped()[iface] - before; d > 0 {
			return nil, d
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("receiver neither persisted nor dropped the frame within 3s")
	return nil, 0
}

func pendingTo(t *testing.T, db *database.DB, iface string) int {
	t.Helper()
	dl, err := db.GetPendingDeliveries(iface, 10)
	if err != nil {
		t.Fatal(err)
	}
	return len(dl)
}

// A frame produced with the shared key decrypts, is stored as its plaintext
// and is relayed to the mesh: the peer kit's path is unchanged.
func TestGatewayReceiver_EncryptedPeerFrameIsRelayed(t *testing.T) {
	p, db, tp, gw := newInboundAuthHarness(t, "aprs_0", "aprs", authTestChain)
	wire, err := tp.ApplyEgress([]byte("hello from the peer kit"), authTestChain)
	if err != nil {
		t.Fatal(err)
	}
	msgs, dropped := deliver(t, p, db, gw, gateway.InboundMessage{Text: string(wire), Source: "aprs", FromAddr: "MSPRLX-10"})
	if dropped != 0 {
		t.Fatalf("peer frame dropped")
	}
	if len(msgs) == 0 || msgs[0].DecodedText != "hello from the peer kit" {
		t.Fatalf("peer frame not stored as plaintext: %+v", msgs)
	}
	if n := pendingTo(t, db, "mesh_0"); n != 1 {
		t.Fatalf("want one relay delivery to mesh_0, got %d", n)
	}
}

// A plaintext APRS message that the gateway let through (addressed to our
// callsign, or a stranger's position carrying the MeshSat marker) fails the
// chain on an encrypted interface and is dropped: no message row, no delivery,
// nothing for the booth screen. This is the PD0PYL-5 path of 14 Sep.
// [MESHSAT-1128]
func TestGatewayReceiver_PlaintextOnEncryptedInterfaceIsDropped(t *testing.T) {
	p, db, _, gw := newInboundAuthHarness(t, "aprs_0", "aprs", authTestChain)
	events, unsub := p.Subscribe()
	defer unsub()

	for _, text := range []string{
		"hello MSTSRT-10, are you there",    // an APRS message to our callsign
		"[MeshSat parallax] Test Yaesu FTM", // a position comment with the public marker
	} {
		msgs, dropped := deliver(t, p, db, gw, gateway.InboundMessage{Text: text, Source: "aprs", FromAddr: "PD0PYL-5"})
		if dropped != 1 || len(msgs) != 0 {
			t.Fatalf("%q: want dropped, got dropped=%d persisted=%d", text, dropped, len(msgs))
		}
	}
	if n := pendingTo(t, db, "mesh_0"); n != 0 {
		t.Fatalf("dropped frames must not reach the mesh, got %d deliveries", n)
	}
	if _, n, _ := db.GetMessages(database.MessageFilter{Limit: 5}); n != 0 {
		t.Fatalf("dropped frames must not be persisted, got %d rows", n)
	}
	if got := p.InboundDropped()["aprs_0"]; got != 2 {
		t.Fatalf("want 2 drops counted on aprs_0, got %d", got)
	}
	select {
	case ev := <-events:
		if ev.Type != "inbound_dropped" {
			t.Fatalf("want an inbound_dropped event first, got %q", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("no inbound_dropped event emitted")
	}
}

// A frame in the encrypted wire format but not produced with the shared key
// (wrong key, or fabricated base64) fails the AES-GCM tag and is dropped.
func TestGatewayReceiver_ForgedCiphertextIsDropped(t *testing.T) {
	p, db, _, gw := newInboundAuthHarness(t, "aprs_0", "aprs", authTestChain)
	other := NewTransformPipeline()
	other.SetKeyResolver(stubKeyResolver{hexKey: strings.Repeat("a5", 32)})
	wrongKey, err := other.ApplyEgress([]byte("forged"), authTestChain)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(wrongKey), "bm90IGEgcmVhbCBmcmFtZQ=="} {
		msgs, dropped := deliver(t, p, db, gw, gateway.InboundMessage{Text: text, Source: "aprs", FromAddr: "N0CALL-1"})
		if dropped != 1 || len(msgs) != 0 {
			t.Fatalf("forged frame: want dropped, got dropped=%d persisted=%d", dropped, len(msgs))
		}
	}
	if n := pendingTo(t, db, "mesh_0"); n != 0 {
		t.Fatalf("forged frames must not reach the mesh, got %d deliveries", n)
	}
}

// A configured plaintext peer (the Hub's SMS number on cellular_0) is marked
// Plain by its gateway, skips the chain and is relayed as before: the hub_sms
// booth lane does not change.
func TestGatewayReceiver_PlainPeerSkipsTheChain(t *testing.T) {
	p, db, _, gw := newInboundAuthHarness(t, "cellular_0", "cellular", authTestChain)
	msgs, dropped := deliver(t, p, db, gw, gateway.InboundMessage{Text: "relayed by the hub", Source: "cellular", FromAddr: "+3197010258258", Plain: true})
	if dropped != 0 || len(msgs) == 0 || msgs[0].DecodedText != "relayed by the hub" {
		t.Fatalf("plain peer message not relayed: dropped=%d msgs=%+v", dropped, msgs)
	}
	if n := pendingTo(t, db, "mesh_0"); n != 1 {
		t.Fatalf("want one relay delivery to mesh_0, got %d", n)
	}
}

// An interface with no decrypt step has nothing to authenticate against:
// plaintext passes (a plaintext-mode peer), and a chain that merely fails to
// decode keeps the old store-raw behaviour.
func TestGatewayReceiver_NoDecryptStepKeepsPlaintext(t *testing.T) {
	p, db, _, gw := newInboundAuthHarness(t, "aprs_0", "aprs", "[]")
	msgs, dropped := deliver(t, p, db, gw, gateway.InboundMessage{Text: "plain mode peer", Source: "aprs", FromAddr: "MSPRLX-10"})
	if dropped != 0 || len(msgs) == 0 || msgs[0].DecodedText != "plain mode peer" {
		t.Fatalf("plaintext on an open interface not relayed: dropped=%d msgs=%+v", dropped, msgs)
	}

	p2, db2, _, gw2 := newInboundAuthHarness(t, "aprs_0", "aprs", `[{"type": "base64"}]`)
	msgs, dropped = deliver(t, p2, db2, gw2, gateway.InboundMessage{Text: "not base64!", Source: "aprs", FromAddr: "MSPRLX-10"})
	if dropped != 0 || len(msgs) == 0 || msgs[0].DecodedText != "not base64!" {
		t.Fatalf("decode-only chain must store raw, got dropped=%d msgs=%+v", dropped, msgs)
	}
}

func TestTransformsAuthenticate(t *testing.T) {
	cases := map[string]bool{
		"":                     false,
		"[]":                   false,
		`[{"type": "base64"}]`: false,
		`[{"type": "smaz2"}, {"type": "base64"}]`: false,
		authTestChain: true,
		`[{"type": "decrypt", "params": {"key_ref": "sms:shared"}}]`: true,
		`not json`: true,
	}
	for chain, want := range cases {
		if got := TransformsAuthenticate(chain); got != want {
			t.Errorf("TransformsAuthenticate(%q) = %v, want %v", chain, got, want)
		}
	}
}
