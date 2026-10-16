package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/codec"
	"meshsat/internal/transport"
)

// SMS through the kit's modem as a delivery-intent route (HeMB-NG stage 1b,
// a paid route under B9; r3 D9 and B7(l)). [MESHSAT-1584]
//
// One SMS of at most 160 characters per submission, never cut or changed:
// a ciphertext goes as its base64 (the version byte is dropped, as MeshSat
// Android expects), a plain text only when every character is one this path
// carries unchanged (printable ASCII in the GSM 03.38 basic table). The
// modem is held to that contract on every send (the TE character set IRA,
// text-mode coding GSM 7-bit; transport.smsTextContract), or nothing goes.
// Accepted by the network (+CMGS: <mr>) is accepted by the carrier, which
// is all the first release knows (status reports are B7(i)); the outcome
// is never "delivered to the recipient". With a prepaid bundle set, an SMS
// goes only while what is left covers it (r3 D8): checked here to spare a
// queue, decided by the modem's I/O loop, which holds each SMS's share
// before its first byte (B9FIX-R1).

// SMSIntentCorrKind is the correlation kind of an SMS submission. No answer
// carries it yet; it names the submission in the ledger.
const SMSIntentCorrKind = "sms_ref"

var smsIntentNumberRE = regexp.MustCompile(`^\+?[0-9]{3,15}$`)

// smsSubmitter is a modem that can say how far an SMS submission got and
// whether its prepaid bundle can take one more.
type smsSubmitter interface {
	SubmitSMS(ctx context.Context, to, text string) (transport.SMSSubmit, error)
	SMSBundleCheck(segments int) error
}

// smsBundleRefusal is the admission answer for a bundle check's error: a
// counter not set up yet is a route not ready (B9FIX-R3), anything else
// a spent bundle.
func smsBundleRefusal(err error) (reason, detail string) {
	if errors.Is(err, transport.ErrSMSBundleNotReady) {
		return "skipped_offline", err.Error()
	}
	return "skipped_budget", err.Error()
}

// IntentCorrKind implements IntentAdapter.
func (g *CellularGateway) IntentCorrKind() string { return SMSIntentCorrKind }

// IntentNewCorr implements IntentAdapter.
func (g *CellularGateway) IntentNewCorr() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SetIntentReceiptSink implements IntentAdapter; no SMS answer reaches the
// ledger yet (status reports are B7(i)).
func (g *CellularGateway) SetIntentReceiptSink(func(IntentReceipt)) {}

// IntentBilled implements IntentBilled: every SMS is counted by the carrier.
func (g *CellularGateway) IntentBilled() bool { return true }

// smsIntentText is the text an intent's wire becomes on the air.
func smsIntentText(wire []byte, encrypted bool) string {
	if encrypted {
		if _, stripped := codec.StripVersionByte(wire); stripped != nil {
			return string(stripped)
		}
	}
	return string(wire)
}

// smsBasicASCII reports whether r is printable ASCII in the GSM 03.38 basic
// table, one septet that AT text mode carries unchanged.
func smsBasicASCII(r rune) bool {
	if r < 0x20 || r > 0x7e {
		return false
	}
	switch r {
	case '[', '\\', ']', '^', '{', '|', '}', '~', '`':
		return false
	}
	return true
}

// IntentAdmit implements IntentAdapter.
func (g *CellularGateway) IntentAdmit(dest string, wire []byte, encrypted bool) (reason, detail string) {
	number := strings.TrimSpace(dest)
	if !smsIntentNumberRE.MatchString(number) {
		return "skipped_bad_address", fmt.Sprintf("%q is not a phone number", dest)
	}
	sub, ok := g.cell.(smsSubmitter)
	if !ok {
		return "skipped_unsupported", "this modem cannot say whether an SMS left"
	}
	if encrypted && g.config.IsPlaintextPeer(number) {
		return "skipped_encoding", "this number takes plain text and the interface encrypts"
	}
	text := smsIntentText(wire, encrypted)
	for _, r := range text {
		if !smsBasicASCII(r) {
			return "skipped_encoding", fmt.Sprintf("character %q is not carried unchanged by the modem", r)
		}
	}
	if len(text) > 160 {
		return "skipped_oversize", fmt.Sprintf("%d characters, one SMS takes 160", len(text))
	}
	if text == "" {
		return "skipped_encoding", "nothing to send"
	}
	if err := sub.SMSBundleCheck(1); err != nil {
		return smsBundleRefusal(err)
	}
	return "", ""
}

// IntentSend implements IntentAdapter: one SMS, and how far it got.
func (g *CellularGateway) IntentSend(ctx context.Context, s IntentSend) IntentOutcome {
	if reason, detail := g.IntentAdmit(s.Destination, []byte(s.Text), s.Encrypted); reason != "" {
		// A route not ready yet (no bundle counter, B9FIX-R3) waits.
		return IntentOutcome{Class: IntentNotSent, Reason: reason, Detail: detail, Retryable: reason == "skipped_offline"}
	}
	number := strings.TrimSpace(s.Destination)
	text := smsIntentText([]byte(s.Text), s.Encrypted)
	res, err := g.cell.(smsSubmitter).SubmitSMS(ctx, number, text)
	words := text
	if s.Encrypted && s.PlainText != "" {
		words = s.PlainText
	}
	history := func(status string) {
		if g.db != nil {
			g.db.InsertSMSMessageEncrypted("tx", number, words, status, time.Now().Unix(), s.Encrypted)
		}
	}
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	switch res.Phase {
	case transport.SMSAccepted:
		g.msgsOut.Add(1)
		g.lastActive.Store(time.Now().Unix())
		history("sent")
		log.Info().Str("to", number).Int64("ref", res.Ref).Str("corr", s.CorrValue).Msg("cellular: intent SMS accepted by the network")
		return IntentOutcome{Class: IntentAccepted, AcceptedBy: "carrier", Charged: true, ChargedUnits: 1,
			Detail: fmt.Sprintf("accepted by the network, reference %d", res.Ref)}
	case transport.SMSRefused:
		g.errors.Add(1)
		history("failed")
		return IntentOutcome{Class: IntentNotSent, Retryable: true, ConsumeRetry: true, Reason: "not_sent",
			Detail: "refused by the network: " + detail}
	case transport.SMSHandedOver:
		g.errors.Add(1)
		return IntentOutcome{Class: IntentUnknown, Detail: "handed to the modem, no acceptance from the network: " + detail}
	case transport.SMSUnfinished:
		// The modem command still runs and may yet submit it (B9SMS-R1):
		// unknown, so its price stays counted as possible and nothing is
		// sent again for it.
		g.errors.Add(1)
		return IntentOutcome{Class: IntentUnknown, Detail: "may still be submitted: " + detail}
	}
	if errors.Is(err, transport.ErrSMSBundle) || errors.Is(err, transport.ErrSMSBundleNotReady) {
		// Refused on the I/O loop before any byte: the bundle had no room
		// once the command ahead settled (B9FIX-R1), or no counter is set.
		reason, _ := smsBundleRefusal(err)
		return IntentOutcome{Class: IntentNotSent, Retryable: reason == "skipped_offline", Reason: reason, Detail: detail}
	}
	g.errors.Add(1)
	if errors.Is(err, transport.ErrNotConnected) {
		return IntentOutcome{Class: IntentNotSent, Retryable: true, Reason: "skipped_offline", Detail: detail}
	}
	return IntentOutcome{Class: IntentNotSent, Retryable: true, ConsumeRetry: true, Reason: "not_sent", Detail: detail}
}
