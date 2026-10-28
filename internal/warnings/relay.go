package warnings

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// What goes on the air (docs/warning-relay.md "What the kit does"). A mesh
// text is at most MeshMaxBytes, what a direct send's row keeps and the mesh
// worker sends; an APRS bulletin at most APRSMaxChars.
const (
	MeshMaxBytes = 200
	APRSMaxChars = 67
	// aprsLabel is the label an APRS bulletin ends with: the full one does
	// not fit 67 characters.
	aprsLabel = " -MeshSat relay"
)

// Deps connect the relay to the rest of the Bridge. Each send is one row in
// the delivery ledger under the key given, so a digest delivered twice (a
// QoS 1 redelivery, the satellite copy after the Hub's) queues nothing
// twice; a nil func turns that bearer off.
type Deps struct {
	// Key answers the pinned Hub directory key, PKIX DER; nil when none is.
	Key func() ([]byte, error)
	// SendAlert queues text to the mesh on ALERT_APP (port 11) with BEL.
	SendAlert func(key, text string) error
	// SendText queues text to the mesh as a text broadcast.
	SendText func(key, text string) error
	// SendBulletin queues text to APRS as the bulletin addressee given
	// (BLN0..BLN9); nil or APRSUp false sends none.
	SendBulletin func(key, addressee, text string) error
	// APRSUp reports whether the APRS gateway runs.
	APRSUp func() bool
}

// Relay verifies, keeps and fans out the digests the Hub sends.
type Relay struct {
	db   *database.DB
	deps Deps
	now  func() time.Time
	mu   sync.Mutex
}

// New returns a Relay.
func New(db *database.DB, deps Deps) *Relay {
	return &Relay{db: db, deps: deps, now: time.Now}
}

// Refusals.
var (
	ErrSeen    = errors.New("warnings: seen before")
	ErrExpired = errors.New("warnings: expired")
)

// Handle takes one digest as delivered (via names the link: "hub", or the
// satellite interface). It verifies the signature with the pinned key,
// refuses and logs anything else, keeps the warning, honours an Update or
// a Cancel, and puts it on each bearer once. The error says why nothing
// went out; a digest seen before is ErrSeen.
func (r *Relay) Handle(ctx context.Context, wire []byte, via string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deps.Key == nil {
		return ErrNoKey
	}
	pub, err := r.deps.Key()
	if err != nil {
		log.Warn().Err(err).Str("via", via).Msg("warnings: the pinned Hub key could not be read, digest refused")
		return err
	}
	if len(pub) == 0 {
		log.Warn().Str("via", via).Int("bytes", len(wire)).Msg("warnings: no Hub directory key pinned, digest refused")
		return ErrNoKey
	}
	d, err := Verify(pub, wire)
	if err != nil {
		log.Warn().Err(err).Str("via", via).Int("bytes", len(wire)).Msg("warnings: digest refused")
		return err
	}
	id, msgType := d.IDHex(), d.TypeName()
	seen, err := r.db.WarningSeen(id, msgType)
	if err != nil {
		return err
	}
	if seen {
		log.Info().Str("id", id).Str("type", msgType).Str("via", via).Msg("warnings: digest seen before, not relayed again")
		return ErrSeen
	}
	now := r.now()
	row := &database.Warning{ID: id, Ref: d.RefHex(), MsgType: msgType, Test: d.Test, Source: SourceName(d.Source),
		Sent: d.Sent, Expires: d.Expires, Severity: SeverityName(d.Severity), Urgency: UrgencyName(d.Urgency),
		Certainty: CertaintyName(d.Certainty), Category: CategoryName(d.Category), Event: EventName(d.Event),
		Language: d.Language, Text: d.Text, Raw: wire, Via: via, ReceivedAt: now, Cancelled: d.MsgType == TypeCancel}
	if err := r.db.InsertWarning(row); err != nil {
		return err
	}
	log.Info().Str("id", id).Str("ref", d.RefHex()).Str("type", msgType).Bool("test", d.Test).Str("source", row.Source).
		Str("event", row.Event).Str("severity", row.Severity).Time("sent", d.Sent).Time("expires", d.Expires).
		Str("via", via).Int("bytes", len(wire)).Str("text", d.Text).Msg("warnings: digest taken")

	switch d.MsgType {
	case TypeCancel:
		if d.RefHex() != "" {
			had, err := r.db.CancelWarning(d.RefHex())
			if err != nil {
				return err
			}
			log.Info().Str("ref", d.RefHex()).Bool("held", had).Msg("warnings: cancel")
		}
	case TypeUpdate:
		if d.RefHex() != "" {
			had, err := r.db.MarkWarningReplaced(d.RefHex())
			if err != nil {
				return err
			}
			log.Info().Str("ref", d.RefHex()).Bool("held", had).Msg("warnings: update replaces an earlier message")
		}
	}
	if !d.Expires.IsZero() && d.Expires.Before(now) && d.MsgType != TypeCancel {
		log.Info().Str("id", id).Time("expires", d.Expires).Msg("warnings: expired on arrival, kept and not relayed")
		return ErrExpired
	}

	var bearers []string
	key := "warn:" + id + ":" + strings.ToLower(msgType)
	mesh := MeshText(d)
	if r.deps.SendAlert != nil {
		if err := r.deps.SendAlert(key+":alert", mesh); err != nil {
			log.Warn().Err(err).Str("id", id).Msg("warnings: mesh alert not queued")
		} else {
			bearers = append(bearers, "mesh-alert")
			log.Info().Str("id", id).Str("text", mesh).Msg("warnings: queued to the mesh on ALERT_APP")
		}
	}
	if r.deps.SendText != nil {
		if err := r.deps.SendText(key+":text", mesh); err != nil {
			log.Warn().Err(err).Str("id", id).Msg("warnings: mesh text not queued")
		} else {
			bearers = append(bearers, "mesh-text")
			log.Info().Str("id", id).Msg("warnings: queued to the mesh as a text")
		}
	}
	if r.deps.SendBulletin != nil && r.deps.APRSUp != nil && r.deps.APRSUp() {
		addr, text := BulletinAddressee(d), APRSText(d)
		if err := r.deps.SendBulletin(key+":aprs", addr, text); err != nil {
			log.Warn().Err(err).Str("id", id).Msg("warnings: APRS bulletin not queued")
		} else {
			bearers = append(bearers, "aprs")
			log.Info().Str("id", id).Str("addressee", addr).Str("text", text).Msg("warnings: queued to APRS as a bulletin")
		}
	}
	b, _ := json.Marshal(bearers)
	if err := r.db.SetWarningBearers(id, string(b)); err != nil {
		log.Warn().Err(err).Msg("warnings: bearers not recorded")
	}
	return nil
}

// HandleSatellite is Handle for what a satellite link delivered: it answers
// whether the bytes were a digest (consumed), whatever became of them.
func (r *Relay) HandleSatellite(ctx context.Context, ifaceID string, wire []byte) bool {
	if !IsDigest(wire) {
		return false
	}
	_ = r.Handle(ctx, wire, ifaceID)
	return true
}

// Label is the line that says what a relayed warning is and is not.
func Label(d *Digest) string {
	return "Relay of " + SourceLabel(d.Source) + " warning, issued " + d.Sent.UTC().Format("2 Jan 15:04") + " UTC via MeshSat, not an official NL-Alert"
}

// MeshText is what goes to the mesh: the warning's words, then the label,
// cut to MeshMaxBytes with the label kept whole.
func MeshText(d *Digest) string {
	label := Label(d)
	head := headline(d)
	room := MeshMaxBytes - len(label) - 1
	if room < 20 {
		room = 20
	}
	return cutBytes(head, room) + "\n" + cutBytes(label, MeshMaxBytes-len(cutBytes(head, room))-1)
}

// headline is the first line: who, what, how bad, until when.
func headline(d *Digest) string {
	var b strings.Builder
	if d.Test {
		b.WriteString("TEST ")
	}
	switch d.MsgType {
	case TypeCancel:
		b.WriteString("CANCELLED: " + EventName(d.Event))
		if d.Text != "" {
			b.WriteString(" - " + d.Text)
		}
		return b.String()
	case TypeUpdate:
		b.WriteString("UPDATE: ")
	}
	b.WriteString("WARNING " + EventName(d.Event) + " " + SeverityName(d.Severity))
	if !d.Expires.IsZero() {
		b.WriteString(" until " + d.Expires.UTC().Format("15:04") + " UTC")
	}
	if d.Text != "" {
		b.WriteString(": " + d.Text)
	}
	return b.String()
}

// APRSText is the bulletin's text: at most APRSMaxChars of printable ASCII
// (APRS message text may not hold '|' or '~'), ending with the short label.
func APRSText(d *Digest) string {
	var b strings.Builder
	if d.Test {
		b.WriteString("TEST ")
	}
	switch d.MsgType {
	case TypeCancel:
		b.WriteString("CANCELLED " + strings.ToUpper(EventName(d.Event)))
	default:
		if d.MsgType == TypeUpdate {
			b.WriteString("UPD ")
		}
		b.WriteString(strings.ToUpper(EventName(d.Event)) + " " + strings.ToUpper(SeverityName(d.Severity)))
		if !d.Expires.IsZero() {
			b.WriteString(" til " + d.Expires.UTC().Format("15:04"))
		}
		if d.Text != "" {
			b.WriteString(": " + d.Text)
		}
	}
	text := asciiOnly(b.String())
	room := APRSMaxChars - len(aprsLabel)
	if len(text) > room {
		text = strings.TrimRight(text[:room], " ")
	}
	return text + aprsLabel
}

// BulletinAddressee is BLN0..BLN9 from the digest's id, so an Update or a
// Cancel of the same warning does not share a slot with another by chance
// more than one in ten.
func BulletinAddressee(d *Digest) string {
	return fmt.Sprintf("BLN%d", int(d.ID[7])%10)
}

// asciiOnly keeps printable ASCII, folding what APRS cannot carry.
func asciiOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == 'ä' || r == 'Ä':
			b.WriteString("ae")
		case r == 'ö' || r == 'Ö':
			b.WriteString("oe")
		case r == 'ü' || r == 'Ü':
			b.WriteString("ue")
		case r == 'ß':
			b.WriteString("ss")
		case r == '|' || r == '~' || r == '{':
			b.WriteByte(' ')
		case r >= 0x20 && r < 0x7F:
			b.WriteRune(r)
		case r == 'é' || r == 'è' || r == 'ë' || r == 'ê':
			b.WriteByte('e')
		case r == 'á' || r == 'à' || r == 'â':
			b.WriteByte('a')
		case r == 'ó' || r == 'ò' || r == 'ô':
			b.WriteByte('o')
		case r == 'í' || r == 'ì' || r == 'ï':
			b.WriteByte('i')
		case r == 'ú' || r == 'ù':
			b.WriteByte('u')
		}
	}
	return b.String()
}

// cutBytes cuts s to at most n bytes on a character boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimRight(s[:n], " ")
}

// KeyFromConfig reads the pinned key out of system_config (hex PKIX DER),
// as the directory code stores it.
func KeyFromConfig(db *database.DB, key string) func() ([]byte, error) {
	return func() ([]byte, error) {
		v, err := db.GetSystemConfig(key)
		if err != nil || v == "" {
			return nil, err
		}
		return hex.DecodeString(v)
	}
}
