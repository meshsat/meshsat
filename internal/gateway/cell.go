package gateway

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/smpp"
	"meshsat/internal/transport"
)

// CellGateway carries texts between phones on the MeshSat 2G cell and the
// Bridge over one SMPP 3.4 transceiver session to osmo-msc. A phone's
// text to the kit's short code becomes an inbound message beside sms_0
// (source "cell", sender "cell:<msisdn>"); a delivery on cell_0 addressed
// "cell:<msisdn>" goes out as submit_sm from the short code. [MESHSAT-1557]
//
// Facts of the server this is written against (osmo-msc 1.16.0, verified
// in its source): submit_sm in forward mode answers only when the phone
// acknowledged (status 0) or the delivery failed; a phone that is not
// registered is refused at once (0x0B) and never queued, so the ledger
// keeps the row (transport.ErrSubscriberAbsent); deliver_sm_resp must go
// back within 5 s; data_coding 0x00/0x01 carries GSM 03.38 septet values;
// one bound session per system id, the newest bind takes the route.
type CellGateway struct {
	config     CellConfig
	db         *database.DB
	instanceID string
	client     *smpp.Client
	inCh       chan InboundMessage
	reasm      *smpp.Reassembler

	msgsIn    atomic.Int64
	msgsOut   atomic.Int64
	errors    atomic.Int64
	refusedIn atomic.Int64
	receipts  atomic.Int64
	absent    atomic.Int64
	alerts    atomic.Int64
	lastIn    atomic.Int64
	lastOut   atomic.Int64
	ref       atomic.Uint32 // concatenation and user_message_reference counter

	submitsMu sync.Mutex
	submits   map[uint16]cellSubmit // recent submits by user_message_reference, for receipts

	nodeNameFn func(uint32) string
	emitEvent  EventEmitFunc

	packetMu    sync.RWMutex
	packetSink  PacketSink
	packetIface string

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type cellSubmit struct {
	msisdn string
	at     time.Time
	msgRef string
}

// NewCellGateway makes a gateway for the instance (cell_0).
func NewCellGateway(cfg CellConfig, db *database.DB, instanceID string) *CellGateway {
	if instanceID == "" {
		instanceID = "cell_0"
	}
	return &CellGateway{
		config:     cfg,
		db:         db,
		instanceID: instanceID,
		inCh:       make(chan InboundMessage, 32),
		reasm:      smpp.NewReassembler(10 * time.Minute),
		submits:    map[uint16]cellSubmit{},
	}
}

// SetEventEmitter sets the SSE event emitter callback.
func (g *CellGateway) SetEventEmitter(fn EventEmitFunc) { g.emitEvent = fn }

// SetNodeNameResolver sets the mesh node name resolver for attribution.
func (g *CellGateway) SetNodeNameResolver(fn func(uint32) string) { g.nodeNameFn = fn }

// SetPacketSink installs the live packet feed sink. [MESHSAT-826]
func (g *CellGateway) SetPacketSink(sink PacketSink, iface string) {
	g.packetMu.Lock()
	g.packetSink = sink
	g.packetIface = iface
	g.packetMu.Unlock()
}

// Config returns the configuration as validated.
func (g *CellGateway) Config() CellConfig { return g.config }

// InstanceID is the gateway's interface id (cell_0).
func (g *CellGateway) InstanceID() string { return g.instanceID }

func (g *CellGateway) emit(eventType, message string) {
	if g.emitEvent != nil {
		g.emitEvent(eventType, message)
	}
}

func (g *CellGateway) record(rec PacketRecord) {
	g.packetMu.RLock()
	sink, iface := g.packetSink, g.packetIface
	g.packetMu.RUnlock()
	if sink == nil {
		return
	}
	rec.Iface = iface
	sink(rec)
}

// Type returns the gateway type identifier.
func (g *CellGateway) Type() string { return "cell" }

// Receive returns the inbound message channel.
func (g *CellGateway) Receive() <-chan InboundMessage { return g.inCh }

// Start binds to the cell and keeps the session up until Stop.
func (g *CellGateway) Start(ctx context.Context) error {
	if g.config.Host == "" {
		return errors.New("cell: smpp host is not set (CELL_SMPP_HOST or the host field)")
	}
	password := g.config.Password
	if password == "" {
		password = os.Getenv("CELL_SMPP_PASSWORD")
		if password == "" {
			password = os.Getenv("MESHSAT_CELL_SMPP_PASSWORD")
		}
	}
	if password == "" {
		password = CellHostPassword() // handed over by the kit's host at adoption [MESHSAT-1578]
	}
	if password == "" {
		return errors.New("cell: no smpp password (CELL_SMPP_PASSWORD, the password field, or the host's cell.env)")
	}
	if len(password) > 8 {
		return errors.New("cell: the smpp password is longer than 8 characters (SMPP 3.4)")
	}
	ctx, g.cancel = context.WithCancel(ctx)
	g.client = smpp.New(smpp.Config{
		Addr:     g.config.Host + ":" + strconv.Itoa(g.config.Port),
		SystemID: g.config.SystemID,
		Password: password,
	}, smpp.Callbacks{
		OnDeliver: g.onDeliver,
		OnAlert:   g.onAlert,
		OnState:   g.onState,
	})
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		g.client.Run(ctx)
	}()
	log.Info().Str("host", g.config.Host).Int("port", g.config.Port).Str("system_id", g.config.SystemID).
		Str("short_code", g.config.ShortCode).Msg("cell gateway started")
	return nil
}

// Stop unbinds and ends the session.
func (g *CellGateway) Stop() error {
	if g.cancel != nil {
		g.cancel()
	}
	g.wg.Wait()
	log.Info().Msg("cell gateway stopped")
	return nil
}

// Enqueue sends in the background (legacy callers); Forward is the path.
func (g *CellGateway) Enqueue(msg *transport.MeshMessage) error {
	return g.Forward(context.Background(), msg)
}

// Status reports the link and the counters.
func (g *CellGateway) Status() GatewayStatus {
	s := GatewayStatus{
		Type:        "cell",
		MessagesIn:  g.msgsIn.Load(),
		MessagesOut: g.msgsOut.Load(),
		Errors:      g.errors.Load(),
		Mode:        "smpp",
	}
	last := g.lastIn.Load()
	if o := g.lastOut.Load(); o > last {
		last = o
	}
	if last > 0 {
		s.LastActivity = time.Unix(last, 0)
	}
	if g.client != nil {
		cs := g.client.Status()
		s.Connected = cs.State == smpp.StateBound
		s.State = string(cs.State)
		s.LastError = cs.LastError
		if cs.BoundSince != nil {
			s.ConnectionUptime = time.Since(*cs.BoundSince).Truncate(time.Second).String()
		}
	} else {
		s.State = string(smpp.StateDisconnected)
	}
	return s
}

// CellStatus is the detail GET /api/cell/status returns.
type CellStatus struct {
	Host                 string       `json:"host"`
	Port                 int          `json:"port"`
	SystemID             string       `json:"system_id"`
	ShortCode            string       `json:"short_code"`
	ReplyWindowHours     int          `json:"reply_window_hours"`
	DeliveryReceipts     bool         `json:"delivery_receipts"`
	AcceptAnyDestination bool         `json:"accept_any_destination"`
	Link                 *smpp.Status `json:"link,omitempty"`
	MessagesIn           int64        `json:"messages_in"`
	MessagesOut          int64        `json:"messages_out"`
	RefusedIn            int64        `json:"refused_in"`
	Receipts             int64        `json:"receipts"`
	PhoneAbsent          int64        `json:"phone_absent"`
	Alerts               int64        `json:"alerts"`
	Errors               int64        `json:"errors"`
	PartsPending         int          `json:"parts_pending"`
}

// CellStatus returns the detail for the API.
func (g *CellGateway) CellStatus() CellStatus {
	st := CellStatus{
		Host: g.config.Host, Port: g.config.Port, SystemID: g.config.SystemID, ShortCode: g.config.ShortCode,
		ReplyWindowHours: g.config.ReplyWindowHours, DeliveryReceipts: g.config.DeliveryReceipts,
		AcceptAnyDestination: g.config.AcceptAnyDestination,
		MessagesIn:           g.msgsIn.Load(), MessagesOut: g.msgsOut.Load(), RefusedIn: g.refusedIn.Load(),
		Receipts: g.receipts.Load(), PhoneAbsent: g.absent.Load(), Alerts: g.alerts.Load(), Errors: g.errors.Load(),
		PartsPending: g.reasm.Pending(),
	}
	if g.client != nil {
		cs := g.client.Status()
		st.Link = &cs
	}
	return st
}

// Bound reports whether the session is bound right now.
func (g *CellGateway) Bound() bool { return g.client != nil && g.client.Bound() }

// Destinations resolves where a rule's text goes, as channel-qualified
// addresses: the rule's own list, else the gateway's destinations, else
// every phone that texted the short code within the reply window. Empty
// when none of those names a phone.
func (g *CellGateway) Destinations(ruleDests []string) []string {
	var raw []string
	for _, d := range ruleDests {
		if m, ok := ParseCellAddress(d); ok {
			raw = append(raw, m)
		}
	}
	if len(raw) == 0 {
		raw = append(raw, g.config.Destinations...)
	}
	if len(raw) == 0 && g.config.ReplyWindowHours > 0 && g.db != nil {
		since := time.Now().Add(-time.Duration(g.config.ReplyWindowHours) * time.Hour)
		if recent, err := g.db.RecentCellPeers(since); err == nil {
			raw = append(raw, recent...)
		} else {
			log.Warn().Err(err).Msg("cell: could not read the recent peers")
		}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, m := range raw {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, CellAddress(m))
	}
	return out
}

// Forward sends one delivery to the one phone it names, synchronously,
// and reports what the cell said. [transport.ErrNotConnected while the
// session is down, transport.ErrSubscriberAbsent when the phone is not on
// the cell, transport.ErrNoRecipient without an address.]
func (g *CellGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	if msg.Destination == "" {
		return fmt.Errorf("cell: %w (address it cell:<msisdn>)", transport.ErrNoRecipient)
	}
	msisdn, ok := ParseCellAddress(msg.Destination)
	if !ok {
		return fmt.Errorf("cell: %q is not a phone on the cell (digits, or cell:<digits>)", msg.Destination)
	}
	if g.client == nil || !g.client.Bound() {
		return fmt.Errorf("cell: session %s: %w", g.linkState(), transport.ErrNotConnected)
	}

	text := g.outboundText(msg)
	if strings.TrimSpace(text) == "" {
		return errors.New("cell: nothing to send")
	}
	ref := uint16(g.ref.Add(1))
	parts, err := smpp.SplitText(text, byte(ref))
	if err != nil {
		return fmt.Errorf("cell: %w", err)
	}
	g.submitsMu.Lock()
	g.submits[ref] = cellSubmit{msisdn: msisdn, at: time.Now(), msgRef: msg.MsgRef}
	for k, v := range g.submits {
		if time.Since(v.at) > time.Hour {
			delete(g.submits, k)
		}
	}
	g.submitsMu.Unlock()

	var registered byte
	if g.config.DeliveryReceipts {
		registered = 1
	}
	for i, part := range parts {
		esm := smpp.ESMModeForward
		if part.UDH {
			esm |= smpp.ESMUDHI
		}
		sm := smpp.SM{
			SourceTON: smpp.TONInternational, SourceNPI: smpp.NPIISDN, SourceAddr: g.config.ShortCode,
			DestTON: smpp.TONInternational, DestNPI: smpp.NPIISDN, DestAddr: msisdn,
			ESMClass: esm, DataCoding: part.DataCoding, RegisteredDelivery: registered,
			ShortMessage: part.ShortMessage,
			TLVs:         []smpp.TLV{smpp.U16TLV(smpp.TagUserMessageReference, ref)},
		}
		_, status, err := g.client.Submit(ctx, sm)
		if err != nil {
			g.errors.Add(1)
			if errors.Is(err, smpp.ErrNotBound) {
				return fmt.Errorf("cell: %w", transport.ErrNotConnected)
			}
			return fmt.Errorf("cell: submit_sm part %d/%d: %w", i+1, len(parts), err)
		}
		switch status {
		case smpp.StatusOK:
		case smpp.StatusInvalidDstAddr, smpp.StatusDeliveryFailure:
			g.absent.Add(1)
			if i > 0 {
				log.Warn().Str("to", msisdn).Int("part", i+1).Int("parts", len(parts)).
					Msg("cell: a later part of a concatenated text was refused; the retry sends every part again")
			}
			return fmt.Errorf("cell: %s: %w", smpp.StatusText(status), transport.ErrSubscriberAbsent)
		default:
			g.errors.Add(1)
			return fmt.Errorf("cell: submit_sm refused: %s (0x%02x)", smpp.StatusText(status), status)
		}
	}

	g.msgsOut.Add(1)
	g.lastOut.Store(time.Now().Unix())
	if g.db != nil {
		if err := g.db.TouchCellPeerTx(msisdn); err != nil {
			log.Warn().Err(err).Msg("cell: peer not recorded")
		}
	}
	rec := PacketRecord{Time: time.Now(), Bearer: BearerSMS, Dir: DirTX, From: g.config.ShortCode, To: msisdn,
		Bytes: len(text), MsgRef: msg.MsgRef}
	if !msg.Encrypted {
		rec.Text = CapPacketText(text)
	}
	g.record(rec)
	log.Info().Str("to", msisdn).Int("parts", len(parts)).Int("chars", len([]rune(text))).Msg("cell: text delivered to the phone")
	g.emit("cell", fmt.Sprintf("Text delivered to %s on the cell", msisdn))
	return nil
}

// outboundText is the words a phone gets: verbatim for an OOB frame, a
// sealed text or a person's plain words; as typed for a text written on
// this kit; "<prefix> <sender>: <text>" for a text relayed from the mesh.
func (g *CellGateway) outboundText(msg *transport.MeshMessage) string {
	if msg.Encrypted || msg.RawText || msg.AsWritten || msg.From == 0 {
		return msg.DecodedText
	}
	sender := fmt.Sprintf("!%08x", msg.From)
	if g.nodeNameFn != nil {
		if name := g.nodeNameFn(msg.From); name != "" {
			sender = name
		}
	}
	if msg.Channel > 0 {
		return fmt.Sprintf("%s %s ch%d: %s", g.config.SMSPrefix, sender, msg.Channel, msg.DecodedText)
	}
	return fmt.Sprintf("%s %s: %s", g.config.SMSPrefix, sender, msg.DecodedText)
}

func (g *CellGateway) linkState() string {
	if g.client == nil {
		return "not started"
	}
	cs := g.client.Status()
	if cs.LastError != "" {
		return string(cs.State) + " (" + cs.LastError + ")"
	}
	return string(cs.State)
}

// onDeliver answers every deliver_sm, on the reader goroutine, within the
// 5 s the MSC allows. The status it returns reaches the phone as an RP
// cause: 0 delivered, 0x0B "number unassigned", 0x14 congestion.
func (g *CellGateway) onDeliver(sm smpp.SM) uint32 {
	from, dialled := sm.SourceAddr, sm.DestAddr
	if sm.IsReceipt() {
		g.receipts.Add(1)
		g.onReceipt(sm)
		return smpp.StatusOK
	}
	if !g.config.AcceptAnyDestination && !sameNumber(dialled, g.config.ShortCode) {
		g.refusedIn.Add(1)
		log.Warn().Str("from", from).Str("dialled", dialled).Str("short_code", g.config.ShortCode).
			Msg("cell: text to a number this kit does not serve, refused (the phone sees number unassigned)")
		g.emit("cell_refused", fmt.Sprintf("Text from %s to %s refused: this kit answers %s", from, dialled, g.config.ShortCode))
		return smpp.StatusInvalidDstAddr
	}
	body := sm.ShortMessage
	if sm.HasUDH() {
		udh, rest, err := smpp.SplitUDH(body)
		if err != nil {
			log.Warn().Err(err).Str("from", from).Msg("cell: bad user data header, text dropped")
			return smpp.StatusInvalidMsgLen
		}
		body = rest
		if c, ok := smpp.ParseConcat(udh); ok {
			whole, done := g.reasm.Add(from, c, sm.DataCoding, rest)
			if !done {
				log.Info().Str("from", from).Uint16("ref", c.Ref).Uint8("part", c.Seq).Uint8("of", c.Total).Msg("cell: part of a long text held for the rest")
				return smpp.StatusOK
			}
			body = whole
		}
	}
	text := smpp.DecodeText(sm.DataCoding, body)
	if g.db != nil {
		if err := g.db.TouchCellPeerRx(from, dialled); err != nil {
			log.Warn().Err(err).Msg("cell: peer not recorded")
		}
	}
	inbound := InboundMessage{
		Text:     text,
		Source:   "cell",
		FromAddr: CellAddress(from), // reply address and attribution; never used for authentication
	}
	select {
	case g.inCh <- inbound:
	default:
		g.errors.Add(1)
		log.Warn().Str("from", from).Msg("cell: inbound queue full, the phone is told to retry (congestion)")
		return smpp.StatusMsgQueueFull
	}
	g.msgsIn.Add(1)
	g.lastIn.Store(time.Now().Unix())
	g.record(PacketRecord{Time: time.Now(), Bearer: BearerSMS, Dir: DirRX, From: from, To: dialled, Bytes: len(sm.ShortMessage), Text: CapPacketText(text)})
	log.Info().Str("from", from).Str("dialled", dialled).Uint8("data_coding", sm.DataCoding).Str("text", text).Msg("cell: text received, forwarding")
	g.emit("cell", fmt.Sprintf("Text received from %s on the cell, forwarding", from))
	return smpp.StatusOK
}

// onReceipt logs a delivery receipt against the submit it answers. It is
// never routed as a message.
func (g *CellGateway) onReceipt(sm smpp.SM) {
	text := smpp.DecodeText(sm.DataCoding, sm.ShortMessage)
	stat := receiptField(text, "stat")
	ref, hasRef := sm.UserMessageReference()
	ev := log.Info().Str("from", sm.SourceAddr).Str("stat", stat).Str("receipt", text)
	if hasRef {
		g.submitsMu.Lock()
		sub, known := g.submits[ref]
		g.submitsMu.Unlock()
		ev = ev.Uint16("ref", ref)
		if known {
			ev = ev.Str("to", sub.msisdn).Str("msg_ref", sub.msgRef).Dur("after", time.Since(sub.at).Truncate(time.Millisecond))
		}
	}
	ev.Msg("cell: delivery receipt")
	g.emit("cell", fmt.Sprintf("Delivery receipt from %s: %s", sm.SourceAddr, stat))
}

// receiptField reads one "key:value" field of an SMPP delivery receipt.
func receiptField(text, key string) string {
	for _, f := range strings.Fields(text) {
		if strings.HasPrefix(f, key+":") {
			return strings.TrimPrefix(f, key+":")
		}
	}
	return ""
}

// onAlert acts on alert_notification: the cell says a phone attached
// (available) or detached. A phone that is back gets its waiting texts
// now instead of at the next scheduled retry.
func (g *CellGateway) onAlert(a smpp.Alert) {
	g.alerts.Add(1)
	msisdn := a.SourceAddr
	avail := a.Availability()
	log.Info().Str("msisdn", msisdn).Int("availability", avail).Msg("cell: alert_notification")
	if g.db == nil || msisdn == "" {
		return
	}
	if avail == 0 || avail == 2 {
		if err := g.db.SetCellPeerReachable(msisdn, avail == 0); err != nil {
			log.Warn().Err(err).Msg("cell: peer reachability not recorded")
		}
	}
	if avail != 0 {
		return
	}
	n, err := g.db.NudgeDeliveries(g.instanceID, []string{CellAddress(msisdn), msisdn})
	if err != nil {
		log.Warn().Err(err).Msg("cell: could not nudge the waiting deliveries")
		return
	}
	if n > 0 {
		log.Info().Str("msisdn", msisdn).Int64("deliveries", n).Msg("cell: phone is back, its waiting texts are due now")
		g.emit("cell", fmt.Sprintf("%s is back on the cell, %d waiting text(s) due now", msisdn, n))
	}
}

func (g *CellGateway) onState(state smpp.State, err error) {
	switch {
	case state == smpp.StateBound:
		g.emit("cell", "Bound to the cell's SMPP server")
	case err != nil:
		g.errors.Add(1)
		g.emit("cell", "Cell session down: "+err.Error())
	}
}

// sameNumber compares two dialled numbers on their digits only: the phone
// may add a + or a country prefix the short code never carries.
func sameNumber(dialled, shortCode string) bool {
	d := strings.TrimLeft(strings.TrimSpace(dialled), "+")
	s := strings.TrimLeft(strings.TrimSpace(shortCode), "+")
	return d == s
}
