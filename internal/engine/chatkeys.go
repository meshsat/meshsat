package engine

// Per-chat keys. MeshSat Android keeps a key per conversation (the chat's
// lock, K/data/ConversationKeyRepository.kt) and seals every SMS to that
// number with it, even with Messaging's encryption off, and opens every SMS
// from that number with it (K/service/GatewayService.kt sendSmsMessage,
// K/sms/SmsReceiver.kt processIncoming). The Bridge keeps these keys in its
// keystore as sms:<number>, and sms:* (the Hub's wildcard) for every number
// without one of its own. The link's transform chain is what is left when
// neither exists, exactly as before.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/rs/zerolog/log"

	"meshsat/internal/codec"
	"meshsat/internal/compress"
	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/transport"
)

const (
	// ChatChannelSMS is the keystore channel type of an SMS chat's key.
	ChatChannelSMS = "sms"
	// ChatWildcard is the address of the key for every SMS number that has
	// none of its own (MeshSat Android's "Hub wildcard sms:*").
	ChatWildcard = "*"

	// chatSealOverhead is what AES-256-GCM adds: the 12-byte nonce in front
	// and the 16-byte tag behind.
	chatSealOverhead = 12 + 16
)

// ChatKeyResolver finds the key kept for one chat. The keystore implements it.
type ChatKeyResolver interface {
	// ChatKeyHex returns the active key of channelType:address as 64 hex
	// characters; found is false when there is none. An error is a key that
	// exists but cannot be read, or a store that cannot answer.
	ChatKeyHex(channelType, address string) (hexKey string, found bool, err error)
}

type chatKeySource struct{ r ChatKeyResolver }

// SetChatKeyResolver wires the per-chat keys; nil turns them off.
func (tp *TransformPipeline) SetChatKeyResolver(r ChatKeyResolver) {
	if r == nil {
		tp.chatKeys.Store(nil)
		return
	}
	tp.chatKeys.Store(&chatKeySource{r: r})
}

// chatKey is one key a chat SMS is sealed or opened with. ref names it
// ("sms:+31612345678", "sms:*") for the log; the key itself is never logged.
type chatKey struct {
	ref string
	hex string
}

// hasChatKeys reports whether per-chat keys are wired at all.
func (tp *TransformPipeline) hasChatKeys() bool {
	return tp != nil && tp.chatKeys.Load() != nil
}

// smsChatKeys returns the keys of the SMS chat with number in the order they
// apply: the number's own, then the wildcard. first stops at the first one
// found, and at the first lookup that fails: a send uses one key and never
// guesses past one it cannot read. A receive tries every key it can read.
func (tp *TransformPipeline) smsChatKeys(number string, first bool) ([]chatKey, error) {
	if !tp.hasChatKeys() {
		return nil, nil
	}
	src := tp.chatKeys.Load()
	addrs := []string{ChatWildcard}
	if number != "" && number != ChatWildcard {
		addrs = []string{number, ChatWildcard}
	}
	var keys []chatKey
	var errs error
	for _, addr := range addrs {
		hexKey, found, err := src.r.ChatKeyHex(ChatChannelSMS, addr)
		if err != nil {
			err = fmt.Errorf("chat key %s:%s: %w", ChatChannelSMS, addr, err)
			if first {
				return nil, err
			}
			errs = errors.Join(errs, err)
			continue
		}
		if !found {
			continue
		}
		keys = append(keys, chatKey{ref: ChatChannelSMS + ":" + addr, hex: hexKey})
		if first {
			break
		}
	}
	return keys, errs
}

// SMSChatKey resolves the key an SMS to number is sealed with: the number's
// own (sms:<number>), else the one for every number (sms:*). found is false
// when neither exists; the link's own chain then decides, as before.
func (tp *TransformPipeline) SMSChatKey(number string) (hexKey, ref string, found bool, err error) {
	keys, err := tp.smsChatKeys(number, true)
	if err != nil || len(keys) == 0 {
		return "", "", false, err
	}
	return keys[0].hex, keys[0].ref, true, nil
}

// SealChatSMS is the on-air text of an SMS sealed with a chat key: the app
// SMS wire v2 (codec.SMSWireV2, r40 review R40-R1). The frame names its codec
// (smsFrame), AES-256-GCM in the encrypt step's own format (12-byte nonce,
// ciphertext, 16-byte tag) seals it, version and mode go in front, then
// base64. The link's other steps do not apply. MeshSat Android's SmsReceiver,
// MeshSat iOS, the Hub and OpenChatSMS read it.
func (tp *TransformPipeline) SealChatSMS(text []byte, hexKey, egressChain string) (string, error) {
	sealed, err := tp.applyTransform(chatCipherStep(hexKey), tp.smsFrame(text, egressChain))
	if err != nil {
		return "", err
	}
	wire := append(codec.SMSWireV2Header(codec.SMSModeAESGCM), sealed...)
	return base64.StdEncoding.EncodeToString(wire), nil
}

// smsFrame is the codec byte and the body of a v2 SMS, in MeshSat Android's
// order (SmsSender): on a link whose send chain compresses with MSVQ-SC, its
// wire, or the text as typed when it cannot encode (Android sends it so, not
// the chain's SMAZ2 fallback); else SMAZ2 with the default table when that
// is shorter, else the UTF-8 text as it is.
func (tp *TransformPipeline) smsFrame(text []byte, egressChain string) []byte {
	if step, ok := chainStep(egressChain, "msvqsc"); ok {
		if wire, err := tp.msvqscEncode(step, text); err == nil {
			return append([]byte{codec.SMSCodecMSVQSC}, wire...)
		}
		return append([]byte{codec.SMSCodecText}, text...)
	}
	if z := compress.Compress(text, compress.DictDefault); len(z) > 0 && len(z) < len(text) {
		return append([]byte{codec.SMSCodecSMAZ2}, z...)
	}
	return append([]byte{codec.SMSCodecText}, text...)
}

// smsFrameWords reads a v2 frame. ok is false for a codec this Bridge does
// not know, or a body that does not read under the codec it names.
func (tp *TransformPipeline) smsFrameWords(frame []byte) ([]byte, bool) {
	if len(frame) == 0 {
		return nil, false
	}
	body := frame[1:]
	switch frame[0] {
	case codec.SMSCodecText:
		if !utf8.Valid(body) {
			return nil, false
		}
		return body, true
	case codec.SMSCodecSMAZ2:
		words, err := decompressSMAZ2(body)
		if err != nil || !utf8.Valid(words) {
			return nil, false
		}
		return words, true
	case codec.SMSCodecMSVQSC:
		words, err := tp.reverseTransform(TransformSpec{Type: "msvqsc"}, body)
		if err != nil {
			return nil, false
		}
		return words, true
	}
	return nil, false
}

// chatCipherStep is the link chain's own encrypt step with a chat's key:
// sealing runs it forward, opening in reverse (its decrypt).
func chatCipherStep(hexKey string) TransformSpec {
	return TransformSpec{Type: "encrypt", Params: map[string]string{"key": hexKey}}
}

// SMSRead is what a Bridge makes of an SMS from a MeshSat app (r41 review
// V1): its words or the SMS as it came, and, apart from that, whether a key
// opened it.
type SMSRead struct {
	// Text is the words, or the SMS as it came when Kept.
	Text string
	// Encrypted: one of the chat's keys opened it, so its tag authenticated
	// it. A clear v2 frame is never encrypted.
	Encrypted bool
	// Kept: app SMS wire v2 this Bridge does not read (a mode or codec it
	// does not know, a body that does not read, a sealed frame no key
	// opens). Nothing is read into it.
	Kept bool
	// Key names the key that opened it, for the log.
	Key string
}

// OpenChatSMS reads an SMS from number with that chat's keys, the number's
// own first, and returns its words and the name of the key that opened it.
// It takes MeshSat Android's form (base64 of the version byte, nonce,
// ciphertext and tag) and the one a Bridge's own chain sends (the version
// byte in front of the base64), skipping what is not base64 as Android's
// decoder does: a GSM 0x01 can arrive as "£". AES-GCM authenticates, so text
// a key did not seal never opens.
func (tp *TransformPipeline) OpenChatSMS(number, raw string) ([]byte, string, bool) {
	r, ok := tp.openChatSMS(number, raw)
	return []byte(r.Text), r.Key, ok
}

func (tp *TransformPipeline) openChatSMS(number, raw string) (SMSRead, bool) {
	keys, err := tp.smsChatKeys(number, false)
	if err != nil {
		log.Warn().Err(err).Str("from", number).Msg("chat keys: a lookup failed; the other keys and the link's chain read the SMS")
	}
	if len(keys) == 0 {
		return SMSRead{}, false
	}
	blob, ok := chatBase64(raw)
	if !ok {
		return SMSRead{}, false
	}
	// Version 2 says how to read it. A frame a key opens but whose codec
	// this Bridge does not know is kept as it came, never guessed at. A
	// version 1 blob whose nonce happens to start 0x02 0x01 opens nowhere
	// here and is read below.
	if mode, payload, v2 := codec.ParseSMSWireV2(blob); v2 && mode == codec.SMSModeAESGCM {
		for _, k := range keys {
			frame, err := tp.reverseTransform(chatCipherStep(k.hex), payload)
			if err != nil {
				continue
			}
			if words, ok := tp.smsFrameWords(frame); ok {
				return SMSRead{Text: string(words), Encrypted: true, Key: k.ref}, true
			}
			log.Warn().Str("from", number).Hex("codec", frame[:min(1, len(frame))]).
				Msg("chat keys: an SMS with a codec this Bridge does not read, kept as it came")
			return SMSRead{Text: raw, Encrypted: true, Kept: true, Key: k.ref}, true
		}
	}
	// Version 1 and unversioned: the codec is guessed (chatWords), with the
	// ambiguity that guess cannot escape. A key that opens one outranks the
	// clear v2 reading its bytes may also have (readAppSMS, r41 review V2).
	for _, k := range keys {
		if p, ok := tp.openChatBlob(blob, k.hex); ok {
			return SMSRead{Text: string(tp.chatWords(p)), Encrypted: true, Key: k.ref}, true
		}
	}
	return SMSRead{}, false
}

// readAppSMS reads an app SMS wire v2 frame that no key opened (r41 review
// V1): a clear frame by the codec it names; a frame with a mode or codec
// this Bridge does not read, a body that does not read, or a sealed frame,
// kept as it came. ok is false for anything that is not v2. Only an SMS that
// is base64 and nothing else qualifies: a Bridge chain puts its version byte
// (0x01, or "£" after GSM) in front of its base64, so its SMAZ2 or ciphertext
// is never read as a frame here. Nothing authenticates what this returns.
func (tp *TransformPipeline) readAppSMS(raw string) (SMSRead, bool) {
	blob, ok := appSMSBase64(raw)
	if !ok {
		return SMSRead{}, false
	}
	mode, payload, v2 := codec.ParseSMSWireV2(blob)
	if !v2 {
		return SMSRead{}, false
	}
	if mode == codec.SMSModeClear {
		if words, ok := tp.smsFrameWords(payload); ok && len(words) > 0 {
			return SMSRead{Text: string(words)}, true
		}
	}
	log.Warn().Hex("mode", blob[1:2]).Hex("codec", payload[:1]).
		Msg("sms: app SMS wire v2 this Bridge does not read, or sealed with no key that opens it; kept as it came")
	return SMSRead{Text: raw, Kept: true}, true
}

// appSMSBase64 decodes an SMS that is standard base64 and nothing else,
// padded or not, as the apps send it.
func appSMSBase64(raw string) ([]byte, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=') {
			return nil, false
		}
	}
	blob, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
	return blob, err == nil
}

// chatBase64 decodes the base64 in an SMS, skipping every character that is
// not base64 (padding included) the way Android's Base64.DEFAULT decoder does.
func chatBase64(raw string) ([]byte, bool) {
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' {
			b.WriteByte(c)
		}
	}
	s := b.String()
	if len(s) < base64.RawStdEncoding.EncodedLen(chatSealOverhead) {
		return nil, false
	}
	blob, err := base64.RawStdEncoding.DecodeString(s)
	return blob, err == nil
}

// openChatBlob opens decoded chat SMS bytes with one key, through the
// chain's decrypt step: without a leading version byte first (Android's
// form), then as they are (a Bridge chain's version byte sits outside the
// base64, and a nonce may start with 0x01).
func (tp *TransformPipeline) openChatBlob(blob []byte, hexKey string) ([]byte, bool) {
	tries := [][]byte{blob}
	if len(blob) > 0 && blob[0] == codec.ProtoVersion1 {
		tries = [][]byte{blob[1:], blob}
	}
	for _, b := range tries {
		if len(b) < chatSealOverhead {
			continue
		}
		if p, err := tp.reverseTransform(chatCipherStep(hexKey), b); err == nil {
			return p, true
		}
	}
	return nil, false
}

// chatWords turns an opened chat SMS back into its words. MeshSat Android
// seals SMAZ2 when it made the text shorter, MSVQ-SC when set to, else the
// UTF-8 text itself. Bytes that read as UTF-8 text are the text: ASCII
// reads the same through SMAZ2 (bytes 9-127 are its literals) and any other
// script would come out of it as bigrams. Then SMAZ2, then MSVQ-SC, in the
// order Android's SmsReceiver tries them.
func (tp *TransformPipeline) chatWords(p []byte) []byte {
	if readsAsText(p) {
		return p
	}
	if words, ok := smaz2Words(p); ok {
		return words
	}
	if looksLikeMSVQSC(p) {
		if words, err := tp.reverseTransform(TransformSpec{Type: "msvqsc"}, p); err == nil {
			return words
		}
	}
	return p
}

// readsAsText reports whether b is UTF-8 text with no control character but
// tab and line breaks: no SMAZ2 code byte (1-8), no NUL.
func readsAsText(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || r == 0x7f {
			return false
		}
	}
	return true
}

// errSMAZ2Malformed: the bytes are not SMAZ2.
var errSMAZ2Malformed = errors.New("smaz2: malformed")

// smaz2Words decompresses SMAZ2 with the default dictionary (MeshSat
// Android's Smaz2 is a port of it) when the result reads as text. The walk
// refuses what is not well formed: the decoder indexes past the end of a cut
// escape instead of failing, and this runs on bytes from the air.
func smaz2Words(b []byte) ([]byte, bool) {
	for i := 0; i < len(b); {
		switch c := b[i]; {
		case c&0x80 != 0: // bigram
			i++
		case c >= 1 && c <= 5: // c bytes as they are
			i += 1 + int(c)
			if i > len(b) {
				return nil, false
			}
		case c >= 6 && c <= 8: // word, with a space before or after
			if i+1 >= len(b) {
				return nil, false
			}
			i += 2
		default:
			i++
		}
	}
	words, err := decompressSMAZ2(b)
	if err != nil || len(words) == 0 || !readsAsText(words) {
		return nil, false
	}
	return words, true
}

func decompressSMAZ2(b []byte) (out []byte, err error) {
	defer func() {
		if recover() != nil {
			out, err = nil, errSMAZ2Malformed
		}
	}()
	return compress.Decompress(b, compress.DictDefault)
}

// looksLikeMSVQSC is MeshSat Android's MsvqscWire.looksLikeMsvqsc: a header
// of stages (1-8) and wire version 1, then two bytes per stage, nothing more.
func looksLikeMSVQSC(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	stages, version := int(b[0]>>4), int(b[0]&0x0f)
	return version == 1 && stages >= 1 && stages <= 8 && len(b) == 1+2*stages
}

// chainStep returns the first step of a type in a transform chain.
func chainStep(chain, typ string) (TransformSpec, bool) {
	specs, err := parseTransforms(chain)
	if err != nil {
		return TransformSpec{}, false
	}
	for _, s := range specs {
		if s.Type == typ {
			return s, true
		}
	}
	return TransformSpec{}, false
}

// ReadSMS reads an SMS that came in on a cellular link from `from` in the
// order the Hub and both apps read one (r41 review V1, V2):
//
//  1. what one of the chat's keys opens (sms:<from>, then sms:*): a v2
//     sealed frame, then version 1 or unversioned;
//  2. what the link's own key opens: ok is false, and the link's chain reads
//     it as before;
//  3. an app SMS wire v2 frame nothing authenticated: a clear frame by the
//     codec it names, anything else v2 kept as it came.
//
// A key's reading always wins over a clear one, so a sealed SMS whose nonce
// starts like a v2 header never reads as clear text. On a link whose chain
// authenticates, step 3 does not apply: the chain's gate still drops what no
// key sealed. ok is false for everything else, which the chain reads.
func (d *Dispatcher) ReadSMS(sourceIface, from, raw string) (SMSRead, bool) {
	if d == nil || raw == "" || !strings.HasPrefix(sourceIface, "cellular") {
		return SMSRead{}, false
	}
	tp := d.TransformPipeline()
	if tp == nil {
		return SMSRead{}, false
	}
	if r, ok := tp.openChatSMS(from, raw); ok {
		log.Debug().Str("iface", sourceIface).Str("from", from).Str("key", r.Key).Bool("kept", r.Kept).Msg("chat keys: SMS opened with its chat's key")
		return r, true
	}
	r, ok := tp.readAppSMS(raw)
	if !ok {
		return SMSRead{}, false
	}
	if chain := d.ingressChain(sourceIface); TransformsAuthenticate(chain) || tp.linkKeyOpens(chain, raw) {
		return SMSRead{}, false
	}
	log.Debug().Str("iface", sourceIface).Str("from", from).Bool("kept", r.Kept).Msg("sms: app SMS wire v2, clear")
	return r, true
}

// ingressChain is an interface's ingress transforms, "" when it has none or
// cannot be read.
func (d *Dispatcher) ingressChain(ifaceID string) string {
	if d.db == nil {
		return ""
	}
	iface, err := d.db.GetInterface(ifaceID)
	if err != nil {
		return ""
	}
	return iface.IngressTransforms
}

// linkKeyOpens reports whether the decrypt steps of a link's ingress chain
// open raw, an optional one included: AES-GCM authenticates, so it opens
// only what the link's key sealed.
func (tp *TransformPipeline) linkKeyOpens(chain, raw string) bool {
	specs, err := parseTransforms(chain)
	if err != nil || !ChainEncrypts(chain) {
		return false
	}
	strict := make([]TransformSpec, len(specs))
	for i, s := range specs {
		strict[i] = TransformSpec{Type: s.Type, Params: map[string]string{}}
		for k, v := range s.Params {
			if k != "optional" {
				strict[i].Params[k] = v
			}
		}
	}
	strictJSON, err := json.Marshal(strict)
	if err != nil {
		return false
	}
	_, body := codec.StripVersionByte([]byte(raw))
	_, err = tp.ApplyIngress(body, string(strictJSON))
	return err == nil
}

// sealChatSMS seals a cellular delivery's text for every destination that
// has a chat key (its own, else the wildcard), as MeshSat Android does, and
// returns the on-air text per number, and the first destination's. all is
// true when every destination has one: the link's chain then does not run.
// A key that exists but cannot seal is an error, so the text never goes in
// the clear, or under another key, to a chat that has its own. A plaintext
// peer (the Hub) keeps the clear text it gets today.
func (w *DeliveryWorker) sealChatSMS(del database.MessageDelivery) (texts map[string]string, first string, all bool, err error) {
	if !w.transforms.hasChatKeys() {
		return nil, "", false, nil
	}
	text := chatPlaintext(del)
	if text == "" {
		return nil, "", false, nil
	}
	dests := w.smsDestinations(del)
	if len(dests) == 0 {
		return nil, "", false, nil
	}
	cfg := w.cellularConfig()
	chain := ""
	if iface, ierr := w.db.GetInterface(w.channelID); ierr == nil {
		chain = iface.EgressTransforms
	}
	seen := make(map[string]bool, len(dests))
	for _, n := range dests {
		if seen[n] {
			continue
		}
		seen[n] = true
		if cfg != nil && cfg.IsPlaintextPeer(n) {
			continue
		}
		hexKey, ref, found, kerr := w.transforms.SMSChatKey(n)
		if kerr != nil {
			return nil, "", false, kerr
		}
		if !found {
			continue
		}
		wire, serr := w.transforms.SealChatSMS([]byte(text), hexKey, chain)
		if serr != nil {
			return nil, "", false, fmt.Errorf("%s: %w", ref, serr)
		}
		if texts == nil {
			texts = make(map[string]string)
			first = wire
		}
		texts[n] = wire
		log.Debug().Int64("id", del.ID).Str("to", n).Str("key", ref).Msg("chat keys: SMS sealed with its chat's key")
	}
	return texts, first, len(texts) > 0 && len(texts) == len(seen), nil
}

// sealedSMSTooLong says why a direct SMS, sealed with a chat key or encrypted
// by the chain, cannot go: it is longer on air than the kit sends (160
// characters per max_sms_segments, as the gateway cuts and as the API
// checks). "" when it fits, when nothing was sealed, or when the size is
// unknown. encrypted means the delivery's text is the chain's ciphertext
// (or every number's sealed text).
func (w *DeliveryWorker) sealedSMSTooLong(del database.MessageDelivery, encrypted bool, chatTexts map[string]string) string {
	cfg := w.cellularConfig()
	if cfg == nil || cfg.MaxSMSSegments <= 0 {
		return ""
	}
	onAir := 0
	if encrypted {
		onAir = len(del.TextPreview)
	}
	for _, sealed := range chatTexts {
		onAir = max(onAir, len(sealed))
	}
	limit := 160 * cfg.MaxSMSSegments
	if onAir <= limit {
		return ""
	}
	return fmt.Sprintf("not sent: too long: %d characters on air sealed, this kit sends at most %d (max_sms_segments); shorten it", onAir, limit)
}

// smsDestinations lists the numbers a cellular delivery goes to, chosen as
// forwardToGateway and the cellular gateway choose them: the row's own
// destination and the rule's SMS contacts, else the gateway's numbers.
func (w *DeliveryWorker) smsDestinations(del database.MessageDelivery) []string {
	var dests []string
	if del.Destination != "" {
		dests = append(dests, del.Destination)
	}
	dests = append(dests, w.ruleSMSDestinations(del)...)
	if len(dests) == 0 {
		if cfg := w.cellularConfig(); cfg != nil {
			dests = append(dests, cfg.DestinationNumbers...)
		}
	}
	return dests
}

// cellularConfig is the config of the cellular gateway behind this worker,
// nil when there is none.
func (w *DeliveryWorker) cellularConfig() *gateway.CellularConfig {
	if w.gwProv == nil {
		return nil
	}
	cg, ok := w.gwProv.GatewayByInterfaceID(w.channelID).(*gateway.CellularGateway)
	if !ok || cg == nil {
		return nil
	}
	cfg := cg.Config()
	return &cfg
}

// chatPlaintext is the text a cellular delivery sends: a direct send's whole
// text, else the row's text, else the text of the mesh message it carries
// (what forwardToGateway sends).
func chatPlaintext(del database.MessageDelivery) string {
	if whole, ok := directSendText(del); ok {
		return whole
	}
	if del.TextPreview != "" {
		return del.TextPreview
	}
	var m transport.MeshMessage
	if len(del.Payload) > 0 && json.Unmarshal(del.Payload, &m) == nil {
		return m.DecodedText
	}
	return ""
}

// directSendText is the whole text of a direct send (QueueDirectSendTo): its
// payload is the text as given, and its preview only the first 200 bytes of
// it, cut where byte 200 falls, possibly inside a character. The preview used
// to be what was sealed, so a longer SMS went out cut although POST
// /api/cellular/sms/send had checked the whole text against the kit's SMS
// size. ok is false for every other row: a message a rule routed (its payload
// may be the JSON envelope or a DTN fragment, and nothing checked its length;
// it keeps its preview, as before), and a direct send that queued a binary
// payload under a label.
func directSendText(del database.MessageDelivery) (string, bool) {
	if del.RuleID != nil || del.TextPreview == "" {
		return "", false
	}
	whole := string(del.Payload)
	if !strings.HasPrefix(whole, del.TextPreview) || !utf8.ValidString(whole) {
		return "", false
	}
	return whole, true
}
