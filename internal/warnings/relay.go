package warnings

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"

	"meshsat/internal/cell"
	"meshsat/internal/database"
	"meshsat/internal/transport"
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
	// The 2G cell leg (docs/cell-broadcast.md): CellUp reports whether the
	// kit's own cell has its radio keyed up; CellBroadcast puts a warning
	// on the broadcast channel the request names, under the digest's id,
	// through the cell's allow-list and TX log; CellCancel takes every
	// broadcast about ref off the air and answers how many there were.
	// nil CellBroadcast turns the leg off.
	CellUp        func() bool
	CellBroadcast func(ctx context.Context, req cell.BroadcastRequest) error
	CellCancel    func(ctx context.Context, ref string) (int, error)
	// The LXMF leg: LXMFPeers lists the lxmf.delivery destinations this
	// kit knows, when each last announced and whether its path leaves on a
	// paid link; SendLXMF queues text to one of them under the key given.
	// nil LXMFPeers or SendLXMF turns the leg off.
	LXMFPeers func() []LXMFPeer
	SendLXMF  func(key, dest, text string) error
	// Emit publishes one event on the Bridge's event stream (/api/events)
	// for every digest kept, type EventType; nil publishes none.
	Emit func(transport.MeshEvent)
}

// LXMFPeer is one LXMF destination the kit knows, as the leg sees it.
type LXMFPeer struct {
	Dest     string    // the destination hash, 32 hex
	LastSeen time.Time // when it last announced; zero for never
	Paid     bool      // its path leaves this kit on a paid link
}

// The LXMF leg's bounds: a peer heard within LXMFPeerWindow gets the
// warning, at most LXMFMaxPeers of them per digest, the most recently
// heard first, and never one whose path crosses a paid link.
const (
	LXMFPeerWindow = 24 * time.Hour
	LXMFMaxPeers   = 50
)

// EventType is the type of the event the relay publishes on the Bridge's
// event stream for every digest kept. Its data is the warning's row as
// GET /api/warnings lists it, plus the label and whether it arrived expired.
const EventType = "warning"

// Event is the data of an EventType event.
type Event struct {
	database.Warning
	Label   string `json:"label"`
	Expired bool   `json:"expired"` // arrived expired: kept, relayed nowhere
}

// NoExpiryInForce is how long a warning that names no expiry counts as in
// force (InForce): until an Update or a Cancel names it, at most this long
// after it was sent.
const NoExpiryInForce = 24 * time.Hour

// The cell leg's timing: a warning repeats about once a minute (32 x
// 1.883 s) until it expires, at least CellMinBroadcasts times and at most
// a day's worth; CellDefaultBroadcasts when the digest names no expiry.
const (
	CellRepetitionPeriod  = 32
	CellMinBroadcasts     = 10
	CellMaxBroadcasts     = 1440
	CellDefaultBroadcasts = 60
)

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
		r.emit(d, row, true)
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
	if r.deps.CellBroadcast != nil {
		if bearer := r.relayCell(ctx, d, id, mesh); bearer != "" {
			bearers = append(bearers, bearer)
		}
	}
	if r.deps.LXMFPeers != nil && r.deps.SendLXMF != nil {
		if bearer := r.relayLXMF(id, key, mesh, now); bearer != "" {
			bearers = append(bearers, bearer)
		}
	}
	if bearers == nil {
		bearers = []string{} // "[]" in the row, never "null"
	}
	b, _ := json.Marshal(bearers)
	if err := r.db.SetWarningBearers(id, string(b)); err != nil {
		log.Warn().Err(err).Msg("warnings: bearers not recorded")
	}
	row.Bearers = string(b)
	r.emit(d, row, false)
	return nil
}

// relayLXMF is the LXMF leg: the mesh text, as one LXMF message to each
// peer RecentLXMFPeers picks, each under its own key so a redelivered
// digest queues nothing twice. It answers "lxmf" when at least one was
// queued, "" otherwise.
func (r *Relay) relayLXMF(id, key, text string, now time.Time) string {
	peers := RecentLXMFPeers(r.deps.LXMFPeers(), now)
	if len(peers) == 0 {
		log.Debug().Str("id", id).Msg("warnings: no LXMF peer heard recently, nothing queued to LXMF")
		return ""
	}
	queued := 0
	for _, p := range peers {
		if err := r.deps.SendLXMF(key+":lxmf:"+p.Dest, p.Dest, text); err != nil {
			log.Warn().Err(err).Str("id", id).Str("dest", p.Dest).Msg("warnings: LXMF message not queued")
			continue
		}
		queued++
	}
	if queued == 0 {
		return ""
	}
	log.Info().Str("id", id).Int("peers", queued).Msg("warnings: queued to the LXMF peers heard recently")
	return "lxmf"
}

// RecentLXMFPeers picks the peers the LXMF leg writes to: those heard
// within LXMFPeerWindow of now whose path does not cross a paid link, the
// most recently heard first, at most LXMFMaxPeers, each destination once.
func RecentLXMFPeers(all []LXMFPeer, now time.Time) []LXMFPeer {
	var out []LXMFPeer
	seen := map[string]bool{}
	for _, p := range all {
		if p.Dest == "" || p.Paid || p.LastSeen.IsZero() || now.Sub(p.LastSeen) > LXMFPeerWindow || seen[p.Dest] {
			continue
		}
		seen[p.Dest] = true
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	if len(out) > LXMFMaxPeers {
		out = out[:LXMFMaxPeers]
	}
	return out
}

// emit publishes the kept digest on the Bridge's event stream: type
// EventType, the headline as the message, the row with its label as the
// data, so the web app and a notifier reading /api/events learn of it
// without a poll.
func (r *Relay) emit(d *Digest, row *database.Warning, expired bool) {
	if r.deps.Emit == nil || row == nil {
		return
	}
	if row.Bearers == "" {
		row.Bearers = "[]" // as the row keeps it (an expired arrival went nowhere)
	}
	data, err := json.Marshal(Event{Warning: *row, Label: Label(d), Expired: expired})
	if err != nil {
		return
	}
	r.deps.Emit(transport.MeshEvent{Type: EventType, Message: headline(d), Data: data, Time: r.now().UTC().Format(time.RFC3339)})
}

// InForce lists the warnings a person at this kit should still see: kept,
// not cancelled or replaced, not expired (one that names no expiry counts
// for NoExpiryInForce after it was sent), newest sent first. The Post
// portal's banner reads it.
func InForce(db *database.DB, now time.Time) ([]database.Warning, error) {
	if db == nil {
		return nil, nil
	}
	list, err := db.ListWarnings(500)
	if err != nil {
		return nil, err
	}
	var out []database.Warning
	for _, w := range list {
		if w.Cancelled || w.MsgType == "Cancel" {
			continue
		}
		if w.Expires.IsZero() {
			if now.Sub(w.Sent) > NoExpiryInForce {
				continue
			}
		} else if !w.Expires.After(now) {
			continue
		}
		out = append(out, w)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Sent.After(out[j].Sent) })
	return out, nil
}

// InForce is InForce over the relay's database, at the relay's clock.
func (r *Relay) InForce() ([]database.Warning, error) {
	return InForce(r.db, r.now())
}

// relayCell is the 2G cell leg. A Cancel takes the referenced warning off
// the air whether or not the radio is up (osmo-cbc holds it either way).
// An Alert or an Update goes on the air only while the radio is keyed up,
// on the channel CellChannel picks, and on the ordinary channel when the
// cell's allow-list refuses that one (a real alert identifier the owner
// has not cleared). It answers the bearer name for the warning's row, ""
// when nothing happened.
func (r *Relay) relayCell(ctx context.Context, d *Digest, id, text string) string {
	if d.MsgType == TypeCancel {
		if r.deps.CellCancel == nil || d.RefHex() == "" {
			return ""
		}
		n, err := r.deps.CellCancel(ctx, d.RefHex())
		if err != nil {
			log.Warn().Err(err).Str("ref", d.RefHex()).Msg("warnings: the cell broadcast was not taken off the air")
			return ""
		}
		if n == 0 {
			return ""
		}
		log.Info().Str("ref", d.RefHex()).Int("broadcasts", n).Msg("warnings: cell broadcast off the air")
		return "cell-cancel"
	}
	if r.deps.CellUp == nil || !r.deps.CellUp() {
		log.Debug().Str("id", id).Msg("warnings: the cell's radio is not keyed up, nothing broadcast on it")
		return ""
	}
	ch := CellChannel(d)
	req := cell.BroadcastRequest{Channel: ch, Text: text, Language: d.Language, Ref: id, Replaces: d.RefHex(),
		Purpose: CellPurpose(d, id), Test: d.Test, Period: CellRepetitionPeriod, Count: CellBroadcasts(d, r.now())}
	err := r.deps.CellBroadcast(ctx, req)
	if errors.Is(err, cell.ErrChannelRefused) && ch != cell.OrdinaryChannel {
		log.Warn().Err(err).Str("id", id).Int("channel", ch).Int("instead", cell.OrdinaryChannel).
			Msg("warnings: the cell refused the alert identifier, the ordinary channel carries the warning")
		req.Channel = cell.OrdinaryChannel
		err = r.deps.CellBroadcast(ctx, req)
	}
	if err != nil {
		log.Warn().Err(err).Str("id", id).Int("channel", req.Channel).Msg("warnings: not broadcast on the cell")
		return ""
	}
	log.Info().Str("id", id).Int("channel", req.Channel).Int("broadcasts", req.Count).Bool("test", d.Test).Msg("warnings: broadcast on the cell")
	return "cell"
}

// CellChannel is the cell broadcast identifier a digest goes out on: the
// EU-Alert / CMAS test identifier 4380 for a test; by the TS 23.041
// 9.4.1.2.2 table for a real Extreme (4371-4374) or Severe (4375-4378)
// warning, the four of each being observed or likely times immediate or
// expected; the ordinary channel for anything milder, which has no alert
// identifier. The cell's allow-list decides whether a real identifier may
// go out; relayCell falls back to the ordinary channel when not.
func CellChannel(d *Digest) int {
	if d.Test {
		return cell.ChannelEUAlertTest
	}
	var base int
	switch d.Severity {
	case 4: // Extreme
		base = cell.ChannelExtremeFirst
	case 3: // Severe
		base = cell.ChannelSevereFirst
	default:
		return cell.OrdinaryChannel
	}
	off := 0
	if d.Certainty != 4 { // not Observed: likely (or less)
		off++
	}
	if d.Urgency != 3 { // not Immediate: expected (or anything else)
		off += 2
	}
	return base + off
}

// CellBroadcasts is how many times a warning repeats on the cell: once a
// minute until it expires, within the bounds.
func CellBroadcasts(d *Digest, now time.Time) int {
	if d.Expires.IsZero() {
		return CellDefaultBroadcasts
	}
	every := time.Duration(float64(CellRepetitionPeriod) * cell.RepetitionUnit * float64(time.Second))
	n := int(d.Expires.Sub(now) / every)
	if n < CellMinBroadcasts {
		return CellMinBroadcasts
	}
	if n > CellMaxBroadcasts {
		return CellMaxBroadcasts
	}
	return n
}

// CellPurpose is the TX-log row's purpose for a relayed warning.
func CellPurpose(d *Digest, id string) string {
	p := "warning relay " + id + ": " + d.TypeName() + " from " + SourceName(d.Source) + " (" + EventName(d.Event) + " " + SeverityName(d.Severity) + ")"
	if d.Test {
		p += " [test]"
	}
	return p
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
	return LabelFor(SourceName(d.Source), d.Sent)
}

// LabelFor is Label for a kept row: the source as the row names it
// (SourceName) and when the warning was sent. Every surface that shows a
// relayed warning (the mesh, the Post portal's banner, the event stream)
// renders the line here, so they say the same thing.
func LabelFor(source string, sent time.Time) string {
	return "Relay of " + SourceLabel(sourceIDByName(source)) + " warning, issued " + sent.UTC().Format("2 Jan 15:04") + " UTC via MeshSat, not an official NL-Alert"
}

// sourceIDByName is SourceName's inverse; an unknown name reads as unknown.
func sourceIDByName(name string) int {
	for _, id := range []int{SourceDWD, SourceMeteoAlarmNL, SourceTest} {
		if SourceName(id) == name {
			return id
		}
	}
	return 0
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
