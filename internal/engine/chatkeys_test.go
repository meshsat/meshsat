package engine

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"

	"meshsat/internal/codec"
	"meshsat/internal/compress"
	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/keystore"
	"meshsat/internal/routing"
	"meshsat/internal/transport"
)

// Per-chat keys: an SMS to a number with a key of its own (sms:<number>), or
// the wildcard's (sms:*), is sealed with it as MeshSat Android seals it; one
// from that number opens with it; the link's chain is for the rest.

var (
	chatKeyNumber   = strings.Repeat("a1", 32) // sms:+31612345678
	chatKeyWildcard = strings.Repeat("b2", 32) // sms:*
	chatKeyChain    = strings.Repeat("c3", 32) // the SMS link's own chain
	chatKeyStranger = strings.Repeat("d4", 32) // nobody's here
)

const (
	chatPeer  = "+31612345678"
	chatOther = "+31600000009"
)

// fakeChatKeys is a ChatKeyResolver over a map from "sms:<address>"; err
// fails every lookup, errFor the lookups of one "sms:<address>".
type fakeChatKeys struct {
	mu     sync.Mutex
	keys   map[string]string
	err    error
	errFor map[string]error
}

func newFakeChatKeys(pairs ...string) *fakeChatKeys {
	f := &fakeChatKeys{keys: map[string]string{}}
	for i := 0; i+1 < len(pairs); i += 2 {
		f.keys[pairs[i]] = pairs[i+1]
	}
	return f
}

func (f *fakeChatKeys) set(ref, hexKey string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[ref] = hexKey
}

func (f *fakeChatKeys) remove(ref string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.keys, ref)
}

func (f *fakeChatKeys) ChatKeyHex(channelType, address string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", false, f.err
	}
	if err := f.errFor[channelType+":"+address]; err != nil {
		return "", false, err
	}
	k, ok := f.keys[channelType+":"+address]
	return k, ok, nil
}

// androidSeal is MeshSat Android's SmsSender.send with a key (SMAZ2 on, as
// it is whenever a key is used without MSVQ-SC): SMAZ2 when shorter,
// AES-256-GCM, the version byte, base64.
func androidSeal(t *testing.T, text, hexKey string) string {
	t.Helper()
	payload := []byte(text)
	if z := compress.Compress(payload, compress.DictDefault); len(z) < len(payload) {
		payload = z
	}
	sealed, err := encryptAESGCM(payload, hexKey)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(codec.PrependVersionByte(sealed))
}

// androidOpen is MeshSat Android's SmsReceiver.processIncoming with a key:
// base64, the version byte off, AES-256-GCM, then SMAZ2 when that gives
// printable ASCII, else the bytes as UTF-8. (Android reads only ASCII right:
// its SMAZ2 step turns any other UTF-8 into bigrams.)
func androidOpen(wire, hexKey string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(wire))
	if err != nil || len(raw) < 2 {
		return "", false
	}
	_, payload := codec.StripVersionByte(raw)
	p, err := decryptAESGCM(payload, hexKey)
	if err != nil {
		return "", false
	}
	if s, err := decompressSMAZ2(p); err == nil && len(s) > 0 {
		printable := true
		for _, c := range s {
			if (c < 0x20 || c > 0x7e) && c != '\n' && c != '\r' && c != '\t' {
				printable = false
			}
		}
		if printable {
			return string(s), true
		}
	}
	return string(p), true
}

// sealWithNonce seals payload the way the encrypt step does, with a chosen
// nonce, so a test can make one that starts with the version byte's value.
func sealWithNonce(t *testing.T, payload []byte, hexKey string, nonce []byte) []byte {
	t.Helper()
	key, _ := hex.DecodeString(hexKey)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return gcm.Seal(append([]byte{}, nonce...), nonce, payload, nil)
}

// --- sealing and opening -------------------------------------------------

// What the Bridge seals is what MeshSat Android sends and reads: base64 of
// the version byte and the encrypt step's own AES-256-GCM (nonce, ciphertext,
// tag), SMAZ2 inside when it made the text shorter. The sealed part opens
// through the chain's existing decrypt step.
func TestSealChatSMS_AndroidWireFormat(t *testing.T) {
	tp := NewTransformPipeline()
	for _, text := range []string{
		"the team is at the bridge, meet there at six", // SMAZ2 makes it shorter
		"ok 42",       // SMAZ2 does not
		"QRV 145.500", // nor here
	} {
		wire, err := tp.SealChatSMS([]byte(text), chatKeyNumber, "[]")
		if err != nil {
			t.Fatal(err)
		}
		if !gateway.IsGSMSafe(wire) {
			t.Fatalf("%q: on-air text not GSM 7-bit safe: %q", text, wire)
		}
		blob, err := base64.StdEncoding.DecodeString(wire)
		if err != nil {
			t.Fatalf("%q: not standard padded base64: %v", text, err)
		}
		if blob[0] != codec.ProtoVersion1 {
			t.Fatalf("%q: first byte 0x%02x, want the version byte inside the base64", text, blob[0])
		}
		z := compress.Compress([]byte(text), compress.DictDefault)
		payload := []byte(text)
		chain := `[{"type":"decrypt","params":{"key":"` + chatKeyNumber + `"}}]`
		if len(z) < len(text) {
			payload = z
			chain = `[{"type":"smaz2"},{"type":"decrypt","params":{"key":"` + chatKeyNumber + `"}}]`
		}
		if want := 1 + chatSealOverhead + len(payload); len(blob) != want {
			t.Fatalf("%q: %d bytes, want %d (version, nonce, payload, tag)", text, len(blob), want)
		}
		// The chain's existing decrypt step (and smaz2 when used) reads it.
		got, err := tp.ApplyIngress(blob[1:], chain)
		if err != nil || string(got) != text {
			t.Fatalf("%q: through the decrypt transform: %q, %v", text, got, err)
		}
		if got, ok := androidOpen(wire, chatKeyNumber); !ok || got != text {
			t.Fatalf("%q: MeshSat Android reads %q (%v)", text, got, ok)
		}
		if _, ok := androidOpen(wire, chatKeyWildcard); ok {
			t.Fatalf("%q: opened with another key", text)
		}
	}
}

// A link whose chain compresses with MSVQ-SC gets no SMAZ2; with no MSVQ-SC
// encoder beside the Bridge the text goes as typed, as Android sends it when
// its encoder fails, not in the chain's SMAZ2 fallback.
func TestSealChatSMS_MSVQSCChainWithoutEncoder(t *testing.T) {
	tp := NewTransformPipeline()
	const text = "the team is at the bridge, meet there at six"
	wire, err := tp.SealChatSMS([]byte(text), chatKeyNumber, `[{"type":"msvqsc","params":{"stages":"3"}},{"type":"encrypt","params":{"key":"`+chatKeyChain+`"}},{"type":"base64"}]`)
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := base64.StdEncoding.DecodeString(wire)
	p, err := decryptAESGCM(blob[1:], chatKeyNumber)
	if err != nil || string(p) != text {
		t.Fatalf("payload %q, %v; want the text as typed", p, err)
	}
}

// OpenChatSMS reads MeshSat Android's form, the form a Bridge's own chain
// sends (the version byte before the base64, raw or as GSM's "£"), and the
// older one with no version byte; it opens nothing a key did not seal.
func TestOpenChatSMS_Forms(t *testing.T) {
	tp := NewTransformPipeline()
	tp.SetChatKeyResolver(newFakeChatKeys("sms:"+chatPeer, chatKeyNumber))
	chainOut := `[{"type":"smaz2"},{"type":"encrypt","params":{"key":"` + chatKeyNumber + `"}},{"type":"base64"}]`
	bridge, err := tp.ApplyEgress([]byte("sent by a kit's chain"), chainOut)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := encryptAESGCM([]byte("no version byte"), chatKeyNumber)
	withNonce01 := sealWithNonce(t, []byte("nonce starts with 01"), chatKeyNumber, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})

	for in, want := range map[string]string{
		androidSeal(t, "meet at the bridge at six", chatKeyNumber):                        "meet at the bridge at six",
		androidSeal(t, "Καλημέρα από τη γέφυρα", chatKeyNumber):                           "Καλημέρα από τη γέφυρα", // Android sends it as UTF-8 (SMAZ2 does not shrink it)
		androidSeal(t, "🆘 ok", chatKeyNumber):                                             "🆘 ok",
		"\x01" + string(bridge):                                                           "sent by a kit's chain",
		"£" + string(bridge):                                                              "sent by a kit's chain",
		base64.StdEncoding.EncodeToString(legacy):                                         "no version byte",
		"\x01" + base64.StdEncoding.EncodeToString(withNonce01):                           "nonce starts with 01",
		base64.StdEncoding.EncodeToString(codec.PrependVersionByte(withNonce01)) + "\r\n": "nonce starts with 01",
	} {
		got, ref, ok := tp.OpenChatSMS(chatPeer, in)
		if !ok || string(got) != want || ref != "sms:"+chatPeer {
			t.Errorf("OpenChatSMS(%q) = %q, %q, %v; want %q", in, got, ref, ok, want)
		}
	}
	for _, in := range []string{
		"see you at six",                                           // plain text
		androidSeal(t, "hi", chatKeyStranger),                      // another key
		"bm90IGEgcmVhbCBmcmFtZSBhdCBhbGwsIGp1c3QgYmFzZTY0IHRleHQ=", // base64, not sealed
		"",
	} {
		if got, _, ok := tp.OpenChatSMS(chatPeer, in); ok {
			t.Errorf("OpenChatSMS(%q) opened %q", in, got)
		}
	}
	// No chat key for the sender: nothing opens, the chain decides.
	if _, _, ok := tp.OpenChatSMS(chatOther, androidSeal(t, "hi", chatKeyNumber)); ok {
		t.Error("a number without a key opened with another number's key")
	}
}

// The words of an opened SMS: UTF-8 text as it is, SMAZ2 decompressed,
// anything else (a brevity code, a cut SMAZ2) as it came, never a panic.
func TestChatWords(t *testing.T) {
	tp := NewTransformPipeline()
	smaz := compress.Compress([]byte("the weather is getting worse, heading down"), compress.DictDefault)
	mixed := compress.Compress([]byte("meet at the café by the bridge"), compress.DictDefault)
	for in, want := range map[string]string{
		"plain ascii text":             "plain ascii text",
		"Καλημέρα":                     "Καλημέρα",
		"tab\tand\nlines":              "tab\tand\nlines",
		string(smaz):                   "the weather is getting worse, heading down",
		string(mixed):                  "meet at the café by the bridge",
		string([]byte{0xCA, 0x01}):     string([]byte{0xCA, 0x01}), // brevity code: the receiver reads it later
		string([]byte{0x05, 'a', 'b'}): string([]byte{0x05, 'a', 'b'}),
		string([]byte{0x06}):           string([]byte{0x06}),
		string([]byte{0x31, 0x00, 0x01, 0x00, 0x02, 0x00, 0x03}): string([]byte{0x31, 0x00, 0x01, 0x00, 0x02, 0x00, 0x03}), // MSVQ-SC, no codebook
	} {
		if got := string(tp.chatWords([]byte(in))); got != want {
			t.Errorf("chatWords(%q) = %q, want %q", in, got, want)
		}
	}
	// SMAZ2 the other end would take for text is not sent as SMAZ2.
	if z := compress.Compress([]byte("beat"), compress.DictDefault); len(z) < 4 && readsAsText(z) {
		wire, err := tp.SealChatSMS([]byte("beat"), chatKeyNumber, "")
		if err != nil {
			t.Fatal(err)
		}
		blob, _ := base64.StdEncoding.DecodeString(wire)
		if p, _ := decryptAESGCM(blob[1:], chatKeyNumber); string(p) != "beat" {
			t.Fatalf("ambiguous SMAZ2 sent: payload %q", p)
		}
	}
}

// The key an SMS to a number is sealed with: its own, else the wildcard,
// else none (the chain). A store that cannot answer is an error, not "none".
func TestSMSChatKey_ResolutionOrder(t *testing.T) {
	tp := NewTransformPipeline()
	if _, _, found, err := tp.SMSChatKey(chatPeer); found || err != nil {
		t.Fatalf("no resolver: found=%v err=%v", found, err)
	}
	keys := newFakeChatKeys()
	tp.SetChatKeyResolver(keys)
	check := func(number, wantKey, wantRef string) {
		t.Helper()
		k, ref, found, err := tp.SMSChatKey(number)
		if err != nil || found != (wantKey != "") || k != wantKey || ref != wantRef {
			t.Fatalf("SMSChatKey(%q) = %q, %q, %v, %v; want %q, %q", number, k, ref, found, err, wantKey, wantRef)
		}
	}
	check(chatPeer, "", "")
	keys.set("sms:*", chatKeyWildcard)
	check(chatPeer, chatKeyWildcard, "sms:*")
	keys.set("sms:"+chatPeer, chatKeyNumber)
	check(chatPeer, chatKeyNumber, "sms:"+chatPeer)
	check(chatOther, chatKeyWildcard, "sms:*")
	keys.remove("sms:" + chatPeer)
	check(chatPeer, chatKeyWildcard, "sms:*")
	keys.remove("sms:*")
	check(chatPeer, "", "")

	keys.err = errors.New("database is locked")
	if _, _, found, err := tp.SMSChatKey(chatPeer); found || err == nil {
		t.Fatalf("store error: found=%v err=%v, want an error", found, err)
	}
	keys.err = nil

	// The number's key cannot be read: a send does not fall through to the
	// wildcard (it fails closed), a receive still tries the wildcard.
	keys.set("sms:*", chatKeyWildcard)
	keys.errFor = map[string]error{"sms:" + chatPeer: errors.New("unwrap key: message authentication failed")}
	if k, _, found, err := tp.SMSChatKey(chatPeer); found || err == nil || k != "" {
		t.Fatalf("unreadable number key on send: found=%v err=%v", found, err)
	}
	if got, ref, ok := tp.OpenChatSMS(chatPeer, androidSeal(t, "via the wildcard", chatKeyWildcard)); !ok || string(got) != "via the wildcard" || ref != "sms:*" {
		t.Fatalf("unreadable number key on receive: %q %q %v", got, ref, ok)
	}
	// And the wildcard unreadable, the number without a key: a send fails
	// closed too, since the wildcard may be its key.
	keys.errFor = map[string]error{"sms:*": errors.New("database is locked")}
	if _, _, found, err := tp.SMSChatKey(chatOther); found || err == nil {
		t.Fatalf("unreadable wildcard on send: found=%v err=%v", found, err)
	}
}

// --- the delivery worker, through the real cellular gateway ---------------

// recordingCell is a modem that keeps what it was told to send.
type recordingCell struct {
	transport.CellTransport
	mu   sync.Mutex
	sent map[string][]string
}

func (c *recordingCell) SendSMS(ctx context.Context, to, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent[to] = append(c.sent[to], text)
	return nil
}

func (c *recordingCell) last(t *testing.T, to string) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent[to]) == 0 {
		t.Fatalf("nothing sent to %s", to)
	}
	return c.sent[to][len(c.sent[to])-1]
}

func (c *recordingCell) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, s := range c.sent {
		n += len(s)
	}
	return n
}

type cellProvider struct{ gw *gateway.CellularGateway }

func (p cellProvider) Gateways() []gateway.Gateway { return []gateway.Gateway{p.gw} }
func (p cellProvider) GatewayByInterfaceID(id string) gateway.Gateway {
	if id == "cellular_0" {
		return p.gw
	}
	return nil
}

type chatSendHarness struct {
	h    *e2eHarness
	ks   *keystore.KeyStore
	w    *DeliveryWorker
	cell *recordingCell
}

// newChatSendHarness wires the delivery worker for cellular_0 to a real
// cellular gateway with the given config (a fake modem behind it), the
// Bridge's keystore as the chat keys, and cellular_0's send chain.
func newChatSendHarness(t *testing.T, cfg gateway.CellularConfig, egress string) *chatSendHarness {
	t.Helper()
	h := setupE2E(t)
	t.Cleanup(func() { h.db.Close() })
	h.addInterface(t, "cellular_0", "cellular", true)
	h.setOnline("cellular_0")
	if _, err := h.db.Exec(`UPDATE interfaces SET egress_transforms = ? WHERE id = 'cellular_0'`, egress); err != nil {
		t.Fatal(err)
	}
	id, err := routing.NewIdentity(h.db)
	if err != nil {
		t.Fatal(err)
	}
	ks, err := keystore.NewKeyStore(h.db, id, "chat-key-test")
	if err != nil {
		t.Fatal(err)
	}
	tp := h.dispatch.TransformPipeline()
	tp.SetKeyResolver(ks)
	tp.SetChatKeyResolver(ks)

	if cfg.MaxSMSSegments == 0 {
		cfg.MaxSMSSegments = 3
	}
	cell := &recordingCell{sent: map[string][]string{}}
	gw := gateway.NewCellularGateway(cfg, cell, h.db)
	w := newTestWorker(h, "cellular_0", "cellular")
	w.gwProv = cellProvider{gw: gw}
	w.transforms = tp
	return &chatSendHarness{h: h, ks: ks, w: w, cell: cell}
}

func (c *chatSendHarness) setKey(t *testing.T, address, hexKey string) {
	t.Helper()
	raw, _ := hex.DecodeString(hexKey)
	if _, err := c.ks.StoreKey("sms", address, raw); err != nil {
		t.Fatal(err)
	}
}

func (c *chatSendHarness) removeKey(t *testing.T, address string) {
	t.Helper()
	if err := c.ks.RevokeKey("sms", address); err != nil {
		t.Fatal(err)
	}
}

// send queues text as the app's send does (one recipient; "" for the
// gateway's own numbers) and runs the worker once.
func (c *chatSendHarness) send(t *testing.T, text, to string) *database.MessageDelivery {
	t.Helper()
	id, _, err := c.h.dispatch.QueueDirectSendTo("cellular_0", text, DirectSendOptions{Destination: to})
	if err != nil {
		t.Fatal(err)
	}
	del, err := c.h.db.GetDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	c.w.deliver(context.Background(), *del)
	after, err := c.h.db.GetDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	return after
}

// lastHistory is the newest SMS history row.
func lastHistory(t *testing.T, db *database.DB) database.SMSMessageRecord {
	t.Helper()
	// Rows of one second tie on the timestamp the list is ordered by; the
	// newest is the highest id.
	rows, err := db.GetSMSMessages(500, 0)
	if err != nil || len(rows) == 0 {
		t.Fatalf("no SMS history: %v", err)
	}
	newest := rows[0]
	for _, r := range rows[1:] {
		if r.ID > newest.ID {
			newest = r
		}
	}
	return newest
}

// chainOpen reads what a link's chain sent: the version byte off, then the
// chain in reverse.
func chainOpen(tp *TransformPipeline, onAir, chain string) (string, bool) {
	_, body := codec.StripVersionByte([]byte(onAir))
	out, err := tp.ApplyIngress(body, chain)
	return string(out), err == nil
}

// Sending: the number's key, else the wildcard, else the link's chain; a key
// removed falls back to the next.
func TestChatKeys_SendResolutionOrder(t *testing.T) {
	chain := `[{"type":"encrypt","params":{"key":"` + chatKeyChain + `"}},{"type":"base64"}]`
	c := newChatSendHarness(t, gateway.CellularConfig{}, chain)
	tp := c.h.dispatch.TransformPipeline()
	const text = "meet at the bridge at six"

	step := func(name, wantKey string) {
		t.Helper()
		del := c.send(t, text, chatPeer)
		if del.Status != "sent" && del.Status != "delivered" {
			t.Fatalf("%s: delivery %s %q", name, del.Status, del.LastError)
		}
		onAir := c.cell.last(t, chatPeer)
		for _, k := range []string{chatKeyNumber, chatKeyWildcard} {
			got, ok := androidOpen(onAir, k)
			if opened := ok && got == text; opened != (k == wantKey) {
				t.Fatalf("%s: opens with %s… = %v, want %v (on air %q)", name, k[:4], opened, k == wantKey, onAir)
			}
		}
		if got, ok := chainOpen(tp, onAir, chain); (ok && got == text) != (wantKey == chatKeyChain) {
			t.Fatalf("%s: the chain reads it = %v, want %v", name, ok && got == text, wantKey == chatKeyChain)
		}
		if h := lastHistory(t, c.h.db); h.Text != text || !h.Encrypted || h.Phone != chatPeer {
			t.Fatalf("%s: history %+v, want the words, encrypted", name, h)
		}
	}

	step("no chat key: the chain", chatKeyChain)
	c.setKey(t, "*", chatKeyWildcard)
	step("wildcard", chatKeyWildcard)
	c.setKey(t, chatPeer, chatKeyNumber)
	step("the number's own key before the wildcard", chatKeyNumber)
	c.removeKey(t, chatPeer)
	step("number key removed: the wildcard", chatKeyWildcard)
	c.removeKey(t, "*")
	step("wildcard removed: the chain", chatKeyChain)
}

// A number with its own key gets sealed SMS even when the link does not
// encrypt at all; other numbers keep getting the text as typed.
func TestChatKeys_NumberKeySealsWithTheLinksEncryptionOff(t *testing.T) {
	for _, chain := range []string{"[]", `[{"type":"smaz2"},{"type":"base64"}]`} {
		c := newChatSendHarness(t, gateway.CellularConfig{}, chain)
		const text = "the team is at the bridge"

		c.send(t, text, chatPeer)
		before := c.cell.last(t, chatPeer)
		if _, ok := androidOpen(before, chatKeyNumber); ok {
			t.Fatalf("%s: sealed before any key was set", chain)
		}
		if h := lastHistory(t, c.h.db); h.Encrypted {
			t.Fatalf("%s: history says encrypted before any key: %+v", chain, h)
		}

		c.setKey(t, chatPeer, chatKeyNumber)
		c.send(t, text, chatPeer)
		sealed := c.cell.last(t, chatPeer)
		if got, ok := androidOpen(sealed, chatKeyNumber); !ok || got != text {
			t.Fatalf("%s: with the number's key, on air %q reads %q (%v)", chain, sealed, got, ok)
		}
		if strings.Contains(sealed, "bridge") {
			t.Fatalf("%s: the words are on air: %q", chain, sealed)
		}
		if h := lastHistory(t, c.h.db); h.Text != text || !h.Encrypted {
			t.Fatalf("%s: history %+v", chain, h)
		}

		c.send(t, text, chatOther)
		if other := c.cell.last(t, chatOther); other != before {
			t.Fatalf("%s: a number without a key got %q, want %q as before", chain, other, before)
		}
	}
}

// Each destination gets its own chat's key; one without a key gets what the
// chain gives, and when every destination has a key the chain does not run.
func TestChatKeys_EveryDestinationItsOwnKey(t *testing.T) {
	const a, b = "+31600000001", "+31600000002"
	c := newChatSendHarness(t, gateway.CellularConfig{DestinationNumbers: []string{a, b}}, "[]")
	c.setKey(t, a, chatKeyNumber)
	const text = "rain from the west"

	c.send(t, text, "")
	if got, ok := androidOpen(c.cell.last(t, a), chatKeyNumber); !ok || got != text {
		t.Fatalf("%s: %q (%v)", a, got, ok)
	}
	if got := c.cell.last(t, b); got != text {
		t.Fatalf("%s without a key: %q, want the text as typed", b, got)
	}
	rows, _ := c.h.db.GetSMSMessages(10, 0)
	enc := map[string]bool{}
	for _, r := range rows {
		if r.Text != text {
			t.Fatalf("history row %+v, want the words", r)
		}
		enc[r.Phone] = r.Encrypted
	}
	if !enc[a] || enc[b] {
		t.Fatalf("history encrypted flags %v", enc)
	}

	// Every destination with a key: a link chain that could not encrypt
	// does not stop the send, since it no longer runs.
	if _, err := c.h.db.Exec(`UPDATE interfaces SET egress_transforms = ? WHERE id = 'cellular_0'`,
		`[{"type":"encrypt","params":{"key":"not-a-key"}},{"type":"base64"}]`); err != nil {
		t.Fatal(err)
	}
	c.setKey(t, "*", chatKeyWildcard)
	del := c.send(t, text, "")
	if del.Status != "sent" && del.Status != "delivered" {
		t.Fatalf("delivery %s %q", del.Status, del.LastError)
	}
	if got, ok := androidOpen(c.cell.last(t, a), chatKeyNumber); !ok || got != text {
		t.Fatalf("%s: %q (%v), want its own key", a, got, ok)
	}
	if got, ok := androidOpen(c.cell.last(t, b), chatKeyWildcard); !ok || got != text {
		t.Fatalf("%s: %q (%v), want the wildcard", b, got, ok)
	}
}

// A plaintext peer (the Hub) keeps the clear text, wildcard or not.
func TestChatKeys_PlaintextPeerStaysClear(t *testing.T) {
	const hub = "+3197010258258"
	c := newChatSendHarness(t, gateway.CellularConfig{PlaintextPeers: []string{hub}}, "[]")
	c.setKey(t, "*", chatKeyWildcard)
	c.send(t, "status report", hub)
	if got := c.cell.last(t, hub); got != "status report" {
		t.Fatalf("the Hub got %q", got)
	}
}

// A chat key that exists but cannot be read never lets the text go in the
// clear or under the link's key: the delivery gives up with the reason.
func TestChatKeys_UnreadableKeyNeverSendsTheText(t *testing.T) {
	c := newChatSendHarness(t, gateway.CellularConfig{}, "[]")
	c.setKey(t, chatPeer, chatKeyNumber)
	if _, err := c.h.db.Exec(`UPDATE key_bundles SET encrypted_key = x'00' WHERE address = ?`, chatPeer); err != nil {
		t.Fatal(err)
	}
	del := c.send(t, "do not leak me", chatPeer)
	if del.Status != "dead" || !strings.Contains(del.LastError, "encryption failed") {
		t.Fatalf("delivery %s %q, want dead with the reason", del.Status, del.LastError)
	}
	if n := c.cell.count(); n != 0 {
		t.Fatalf("%d SMS left the modem", n)
	}
}

// --- receiving ------------------------------------------------------------

// Receiving: the sender's key, then the wildcard, then the link's chain; a
// key removed falls back. The SMS history keeps the words and the lock.
func TestChatKeys_ReceiveResolutionOrder(t *testing.T) {
	// The Linux app's receive chain: an optional decrypt with the link's key.
	ingress := `[{"type":"decrypt","params":{"key":"` + chatKeyChain + `","optional":"true"}},{"type":"base64"}]`
	rec, db, tp := newSMSRecorderHarness(t, ingress)
	keys := newFakeChatKeys("sms:"+chatPeer, chatKeyNumber, "sms:*", chatKeyWildcard)
	tp.SetChatKeyResolver(keys)
	chainWire, err := tp.ApplyEgress([]byte("sealed by the link's key"), `[{"type":"encrypt","params":{"key":"`+chatKeyChain+`"}},{"type":"base64"}]`)
	if err != nil {
		t.Fatal(err)
	}
	byNumber := androidSeal(t, "sealed by the number's key", chatKeyNumber)
	byWildcard := androidSeal(t, "sealed by the wildcard", chatKeyWildcard)

	receive := func(name, onAir, wantText string, wantEncrypted bool) {
		t.Helper()
		rec.handleSMSReceived(smsEvent(t, chatPeer, onAir))
		if h := lastSMS(t, db); h.Text != wantText || h.Encrypted != wantEncrypted {
			t.Fatalf("%s: history %q encrypted=%v, want %q encrypted=%v", name, h.Text, h.Encrypted, wantText, wantEncrypted)
		}
		if _, err := db.Exec(`DELETE FROM sms_messages`); err != nil {
			t.Fatal(err)
		}
	}
	receive("the number's key", byNumber, "sealed by the number's key", true)
	receive("the wildcard after the number's", byWildcard, "sealed by the wildcard", true)
	receive("the chain after both", "\x01"+string(chainWire), "sealed by the link's key", true)
	receive("a text in the clear", "see you at six", "see you at six", false)

	keys.remove("sms:" + chatPeer)
	receive("number key removed: its SMS stays sealed", byNumber, byNumber, false)
	receive("number key removed: the wildcard still opens", byWildcard, "sealed by the wildcard", true)
	keys.remove("sms:*")
	receive("both removed: the chain", "\x01"+string(chainWire), "sealed by the link's key", true)
	receive("both removed: the wildcard's SMS stays sealed", byWildcard, byWildcard, false)
}

// On a link whose chain authenticates (drops what its key did not seal), an
// SMS a chat key opens is kept, stored as its words and relayed as them;
// what no key opens is still dropped.
func TestChatKeys_ReceiverKeepsChatSealedSMS(t *testing.T) {
	chain := `[{"type":"encrypt","params":{"key":"` + chatKeyChain + `"}},{"type":"base64"}]`
	p, db, tp, gw := newInboundAuthHarness(t, "cellular_0", "cellular", chain)
	tp.SetChatKeyResolver(newFakeChatKeys("sms:"+chatPeer, chatKeyNumber))

	const text = "the team is at the bridge"
	msgs, dropped := deliver(t, p, db, gw, gateway.InboundMessage{Text: androidSeal(t, text, chatKeyNumber), Source: "cellular", FromAddr: chatPeer})
	if dropped != 0 || len(msgs) == 0 || msgs[0].DecodedText != text {
		t.Fatalf("chat-sealed SMS: dropped=%d stored=%+v", dropped, msgs)
	}
	dl, err := db.GetPendingDeliveries("mesh_0", 10)
	if err != nil || len(dl) != 1 || dl[0].TextPreview != text {
		t.Fatalf("relay to mesh_0: %+v %v, want the words", dl, err)
	}

	// Sealed with a key this chat does not have: the gate drops it.
	if _, dropped := deliver(t, p, db, gw, gateway.InboundMessage{Text: androidSeal(t, text, chatKeyStranger), Source: "cellular", FromAddr: chatPeer}); dropped != 1 {
		t.Fatalf("a stranger's ciphertext was not dropped")
	}
}

// Out and back in: what the worker sends with a chat key is what the far
// end's receive path, and the chain's decrypt step, read back as the words.
func TestChatKeys_RoundTripOutAndIn(t *testing.T) {
	c := newChatSendHarness(t, gateway.CellularConfig{}, "[]")
	c.setKey(t, chatPeer, chatKeyNumber)
	for _, text := range []string{
		"the weather is getting worse, heading down to the hut",
		"ok",
		"Καλημέρα, φτάσαμε στη γέφυρα",
	} {
		c.send(t, text, chatPeer)
		onAir := c.cell.last(t, chatPeer)

		// The far end: another Bridge with this number's chat key (it sees
		// this kit's number as the sender).
		rec, db, tp := newSMSRecorderHarness(t, "[]")
		tp.SetChatKeyResolver(newFakeChatKeys("sms:+31699999999", chatKeyNumber))
		rec.handleSMSReceived(smsEvent(t, "+31699999999", onAir))
		if h := lastSMS(t, db); h.Text != text || !h.Encrypted {
			t.Fatalf("%q came back as %q encrypted=%v", text, h.Text, h.Encrypted)
		}

		// The chain's existing decrypt step reads the sealed part.
		blob, err := base64.StdEncoding.DecodeString(onAir)
		if err != nil || blob[0] != codec.ProtoVersion1 {
			t.Fatalf("%q: on air %q", text, onAir)
		}
		p, err := tp.ApplyIngress(blob[1:], `[{"type":"decrypt","params":{"key":"`+chatKeyNumber+`"}}]`)
		if err != nil {
			t.Fatalf("%q: the decrypt transform: %v", text, err)
		}
		if got := string(tp.chatWords(p)); got != text {
			t.Fatalf("%q: decrypted to %q", text, got)
		}
	}
}
