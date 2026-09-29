package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/keystore"
	"meshsat/internal/routing"
	"meshsat/internal/transport"
)

// fakeModem is a whole cellular modem for a running cellular gateway: it
// answers every call and keeps the SMS it is asked to send.
type fakeModem struct {
	mu     sync.Mutex
	sent   map[string][]string
	events chan transport.CellEvent
}

func newFakeModem() *fakeModem {
	return &fakeModem{sent: map[string][]string{}, events: make(chan transport.CellEvent)}
}

func (m *fakeModem) Subscribe(ctx context.Context) (<-chan transport.CellEvent, error) {
	return m.events, nil
}
func (m *fakeModem) SendSMS(ctx context.Context, to, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent[to] = append(m.sent[to], text)
	return nil
}
func (m *fakeModem) GetSignal(ctx context.Context) (*transport.CellSignalInfo, error) {
	return &transport.CellSignalInfo{}, nil
}
func (m *fakeModem) GetSignalFast(ctx context.Context) (*transport.CellSignalInfo, error) {
	return &transport.CellSignalInfo{}, nil
}
func (m *fakeModem) GetStatus(ctx context.Context) (*transport.CellStatus, error) {
	return &transport.CellStatus{Connected: true}, nil
}
func (m *fakeModem) GetDataStatus(ctx context.Context) (*transport.CellDataStatus, error) {
	return &transport.CellDataStatus{}, nil
}
func (m *fakeModem) ConnectData(ctx context.Context, apn string) error { return nil }
func (m *fakeModem) DisconnectData(ctx context.Context) error          { return nil }
func (m *fakeModem) UnlockPIN(ctx context.Context, pin string) error   { return nil }
func (m *fakeModem) GetCellInfo(ctx context.Context) (*transport.CellInfo, error) {
	return &transport.CellInfo{}, nil
}
func (m *fakeModem) ExecAT(ctx context.Context, command string, timeout time.Duration) (string, error) {
	return "OK", nil
}
func (m *fakeModem) Close() error { return nil }

func (m *fakeModem) texts(to string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.sent[to]...)
}

func (m *fakeModem) all() map[string][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string][]string, len(m.sent))
	for to, texts := range m.sent {
		out[to] = append([]string{}, texts...)
	}
	return out
}

func (m *fakeModem) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.sent {
		n += len(s)
	}
	return n
}

// smsPipeline is the whole SMS send path behind the API: the handlers, the
// ledger, the delivery worker, the cellular gateway (max_sms_segments 3) and a
// fake modem that encodes any text (as ModemManager does), with the Bridge's
// keystore for the chat keys and chain on cellular_0.
type smsPipeline struct {
	s     *Server
	modem *fakeModem
	ks    *keystore.KeyStore
	tp    *engine.TransformPipeline
	d     *engine.Dispatcher
	ctx   context.Context
}

func newSMSPipeline(t *testing.T, chain string) *smsPipeline {
	t.Helper()
	s := newTestServerWithDB(t)
	id, err := routing.NewIdentity(s.db)
	if err != nil {
		t.Fatal(err)
	}
	ks, err := keystore.NewKeyStore(s.db, id, "plain-sms-test")
	if err != nil {
		t.Fatal(err)
	}
	s.SetKeyStore(ks)
	tp := engine.NewTransformPipeline()
	tp.SetKeyResolver(ks)
	tp.SetChatKeyResolver(ks)
	s.SetTransformPipeline(tp)

	if _, err := s.db.Exec(`INSERT OR IGNORE INTO interfaces (id, channel_type, label, enabled, config) VALUES ('cellular_0', 'cellular', 'SMS', 1, '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE interfaces SET enabled = 1, egress_transforms = ?, ingress_transforms = ? WHERE id = 'cellular_0'`, chain, chain); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	modem := newFakeModem()
	mgr := gateway.NewManager(s.db, nil)
	mgr.SetCellTransport(modem)
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mgr.ConfigureInstance(ctx, "cellular", "cellular_0", true, `{"max_sms_segments":3}`); err != nil {
		t.Fatalf("configure cellular: %v", err)
	}
	s.gwManager = mgr
	s.SetCellTransport(modem)

	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	d := engine.NewDispatcher(s.db, reg, mgr, nil)
	d.SetTransformPipeline(tp)
	s.SetDispatcher(d)
	t.Cleanup(func() {
		cancel()
		d.Wait()
		mgr.Stop()
	})
	return &smsPipeline{s: s, modem: modem, ks: ks, tp: tp, d: d, ctx: ctx}
}

// setKey keeps hexKey as the chat key of sms:<address>.
func (p *smsPipeline) setKey(t *testing.T, address, hexKey string) {
	t.Helper()
	raw, _ := hex.DecodeString(hexKey)
	if _, err := p.ks.StoreKey("sms", address, raw); err != nil {
		t.Fatal(err)
	}
}

// deliver starts cellular_0's delivery worker and waits for n SMS on the modem.
func (p *smsPipeline) deliver(t *testing.T, n int) {
	t.Helper()
	p.d.StartWorker(p.ctx, "cellular_0", "cellular")
	deadline := time.Now().Add(15 * time.Second)
	for p.modem.count() < n && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := p.modem.count(); got != n {
		t.Fatalf("the modem sent %d SMS, want %d: %v", got, n, p.modem.all())
	}
}

// An SOS or alarm-test SMS to an emergency contact leaves exactly as the app
// gave it, from either send endpoint, with plain: true — even with a key for
// that number, a wildcard key and an encrypting chain on cellular_0 — and the
// SMS history keeps it unencrypted. The same sends without plain are sealed
// as before. The whole path runs: the handler, the ledger, the delivery
// worker, the cellular gateway, the modem.
func TestPlainSMS_FromTheAPIReachesTheModemAsGiven(t *testing.T) {
	const contact, other = "+31612345678", "+31600000009"
	chain := `[{"type":"smaz2"},{"type":"encrypt","params":{"key":"` + strings.Repeat("c3", 32) + `"}},{"type":"base64"}]`
	p := newSMSPipeline(t, chain)
	p.setKey(t, contact, strings.Repeat("a1", 32))
	p.setKey(t, "*", strings.Repeat("b2", 32))
	s, modem, tp := p.s, p.modem, p.tp

	sos := "SOS: Anna needs help. At 52.37022, 4.89517 at 14:03. https://osm.org/?mlat=52.37022&mlon=4.89517"
	test := " Test: checking the MeshSat alarm routes [no help needed] " // as typed: not trimmed, not sanitised
	for _, c := range []struct {
		path, body string
		code       int
	}{
		{"/api/messages/send", `{"text":` + jsonQuote(sos) + `,"gateway":"cellular","to":"` + contact + `","plain":true}`, http.StatusOK},
		{"/api/cellular/sms/send", `{"to":"` + other + `","text":` + jsonQuote(test) + `,"plain":true}`, http.StatusAccepted},
		{"/api/messages/send", `{"text":"meet at the bridge","gateway":"cellular","to":"` + contact + `"}`, http.StatusOK},
		{"/api/cellular/sms/send", `{"to":"` + other + `","text":"meet at the bridge"}`, http.StatusAccepted},
	} {
		w := post(t, s, c.path, c.body)
		if w.Code != c.code {
			t.Fatalf("POST %s %s: %d %s", c.path, c.body, w.Code, w.Body.String())
		}
		var resp map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if want := strings.Contains(c.body, `"plain":true`); resp["plain"] != want {
			t.Fatalf("POST %s: response %v, want plain %v", c.path, resp, want)
		}
	}

	p.deliver(t, 4)

	for number, want := range map[string]struct {
		plain, sealedRef string
	}{
		contact: {sos, "sms:" + contact},
		other:   {test, "sms:*"},
	} {
		got := modem.texts(number)
		plain, sealed := 0, 0
		for _, onAir := range got {
			switch {
			case onAir == want.plain:
				plain++
			default:
				words, ref, ok := tp.OpenChatSMS(number, onAir)
				if !ok || string(words) != "meet at the bridge" || ref != want.sealedRef || strings.Contains(onAir, "bridge") {
					t.Fatalf("%s: %q is neither the plain text nor sealed with %s (%q %q %v)", number, onAir, want.sealedRef, words, ref, ok)
				}
				sealed++
			}
		}
		if plain != 1 || sealed != 1 {
			t.Fatalf("%s: on air %q, want the plain text once and one sealed", number, got)
		}
	}

	rows, err := s.db.GetSMSMessages(10, 0)
	if err != nil || len(rows) != 4 {
		t.Fatalf("history: %d rows, %v", len(rows), err)
	}
	for _, r := range rows {
		wantEncrypted := r.Text == "meet at the bridge"
		if r.Encrypted != wantEncrypted || (!wantEncrypted && r.Text != sos && r.Text != test) {
			t.Fatalf("history row %+v", r)
		}
	}
	for _, class := range []string{database.DeliveryClassPlain, database.DeliveryClassMessage} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM message_deliveries WHERE delivery_class = ?`, class).Scan(&n); err != nil || n != 2 {
			t.Fatalf("%d deliveries of class %s, want 2 (%v)", n, class, err)
		}
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// plain counts the text itself against the kit's SMS size, and an empty text
// is still refused.
func TestSendSMS_PlainLengthAndEmpty(t *testing.T) {
	s := newTestServerWithDB(t)
	s.SetCellTransport(newFakeModem())
	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	s.SetDispatcher(engine.NewDispatcher(s.db, reg, nil, nil))

	if w := post(t, s, "/api/cellular/sms/send", `{"to":"+31612345678","text":"   ","plain":true}`); w.Code != http.StatusBadRequest {
		t.Fatalf("blank plain text: %d, want 400", w.Code)
	}
	if n, limit, err := s.smsOnAirLength("+31612345678", strings.Repeat("x", 200), true); n != 0 || limit != 0 || err != nil {
		t.Fatalf("no gateway, no limit: %d of %d, %v", n, limit, err)
	}
	if w := post(t, s, "/api/cellular/sms/send", `{"to":"+31612345678","text":" as typed ","plain":true}`); w.Code != http.StatusAccepted {
		t.Fatalf("plain send: %d %s", w.Code, w.Body.String())
	}
	var text, class string
	if err := s.db.QueryRow(`SELECT payload, delivery_class FROM message_deliveries ORDER BY id DESC LIMIT 1`).Scan(&text, &class); err != nil {
		t.Fatal(err)
	}
	if text != " as typed " || class != database.DeliveryClassPlain {
		t.Fatalf("queued %q as %s, want the text untrimmed as plain", text, class)
	}
}

// A plain text reaches the modem without the clean-up the other SMS get, so
// both send endpoints refuse one with a control character other than a line
// break: on a kit's USB modem Ctrl-Z would end the SMS and run the rest as AT
// commands, and ESC would drop the SMS while the modem answers OK. Nothing is
// queued. Line breaks go through, exactly as written.
func TestPlainSMS_ControlCharactersRefused(t *testing.T) {
	const contact = "+31612345678"
	p := newSMSPipeline(t, "[]")

	for _, r := range []rune{0x1A, 0x1B, 0x00, 0x08, '\t', 0x0B, 0x0C, 0x0E, 0x1F, 0x7F, 0x85} {
		text := "SOS: Anna needs help." + string(r) + "AT+CFUN=0"
		for _, path := range []string{"/api/cellular/sms/send", "/api/messages/send"} {
			body := `{"to":"` + contact + `","text":` + jsonQuote(text) + `,"gateway":"cellular","plain":true}`
			w := post(t, p.s, path, body)
			if want := fmt.Sprintf("U+%04X", r); w.Code != http.StatusBadRequest ||
				!strings.Contains(w.Body.String(), "control characters") || !strings.Contains(w.Body.String(), want) {
				t.Fatalf("POST %s with %s: %d %s, want 400 naming it", path, want, w.Code, w.Body.String())
			}
		}
	}
	var queued int
	if err := p.s.db.QueryRow(`SELECT COUNT(*) FROM message_deliveries`).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("%d deliveries queued by refused sends (%v)", queued, err)
	}

	lines := "Test from Anna: checking the MeshSat alarm routes.\nNo help needed.\r\n"
	for path, code := range map[string]int{"/api/cellular/sms/send": http.StatusAccepted, "/api/messages/send": http.StatusOK} {
		body := `{"to":"` + contact + `","text":` + jsonQuote(lines) + `,"gateway":"cellular","plain":true}`
		if w := post(t, p.s, path, body); w.Code != code {
			t.Fatalf("POST %s with line breaks: %d %s", path, w.Code, w.Body.String())
		}
	}
	p.deliver(t, 2)
	for _, got := range p.modem.texts(contact) {
		if got != lines {
			t.Fatalf("modem got %q, want %q", got, lines)
		}
	}
}

// longSMS is 300 bytes in two scripts whose 200th byte falls inside a Greek
// letter.
const longSMS = "The weather is turning, we are heading down to the hut by the east ridge and will call again from there. Keep the kettle warm. " +
	"Ο καιρός χαλάει, κατεβαίνουμε στο καταφύγιο από την ανατολική κορυφογραμμή. " +
	"See you at six, all of us are OK."

// A 300-byte SMS to a keyed chat passes POST /api/cellular/sms/send's length
// check (sealed whole it fits max_sms_segments 3), and the delivery worker
// seals that same whole text, as long on air as the check predicted: the
// chat's key opens every word, where the 200-byte preview it used to seal
// was cut inside a Greek letter. POST /api/messages/send seals it whole too.
func TestSendSMS_KeyedLongTextGoesWhole(t *testing.T) {
	const contact = "+31612345678"
	if len(longSMS) != 300 || utf8.ValidString(longSMS[:200]) {
		t.Fatalf("the test text is %d bytes, its first 200 whole characters: %v", len(longSMS), utf8.ValidString(longSMS[:200]))
	}
	p := newSMSPipeline(t, "[]")
	p.setKey(t, contact, strings.Repeat("a1", 32))

	predicted, limit, err := p.s.smsOnAirLength(contact, longSMS, false)
	if err != nil || predicted == 0 || predicted > limit {
		t.Fatalf("the API's check: %d characters of %d (%v)", predicted, limit, err)
	}
	for path, code := range map[string]int{"/api/cellular/sms/send": http.StatusAccepted, "/api/messages/send": http.StatusOK} {
		body := `{"to":"` + contact + `","text":` + jsonQuote(longSMS) + `,"gateway":"cellular"}`
		if w := post(t, p.s, path, body); w.Code != code {
			t.Fatalf("POST %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	p.deliver(t, 2)
	for _, onAir := range p.modem.texts(contact) {
		words, ref, ok := p.tp.OpenChatSMS(contact, onAir)
		if !ok || string(words) != longSMS || ref != "sms:"+contact {
			t.Fatalf("opened %q with %s (%v), want the whole text", words, ref, ok)
		}
		if len(onAir) != predicted {
			t.Fatalf("%d characters on air, the API's check predicted %d", len(onAir), predicted)
		}
	}
}
