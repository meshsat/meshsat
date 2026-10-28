package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"

	"meshsat/internal/crewchat"
	"meshsat/internal/transport"
)

// TAK direct messages (MESHSAT-1705): a dispatcher in a TAK client, or an
// operator on the Hub's map, writes to one crew, and the crew's answer goes
// back to whoever wrote. Every such message is a GeoChat in ATAK's form, so a
// TAK client shows it in the right conversation. The Hub hands this Bridge
// only the chats addressed to a crew it owns, on the Bridge's own
// {root}/bridge/<id>/tak/cot/in; a chat to the room, or to somebody else, never
// reaches a radio.

// TAKChatRoom is ATAK's name and id for the room every contact is in.
const TAKChatRoom = "All Chat Rooms"

// TAKChatMaxBytes is the most of a chat that goes to a radio: a Meshtastic
// text carries about 230 bytes, and the sender's callsign goes in front.
const TAKChatMaxBytes = 200

// TAKChatThreadLife is how long a crew's text to this Bridge's node is taken
// as an answer to the last one who wrote to it.
const TAKChatThreadLife = 24 * time.Hour

// takChatStale is a GeoChat's lifetime, ATAK's own: a day.
const takChatStale = 24 * time.Hour

// ChatPeer is one end of a chat: a TAK uid and its callsign.
type ChatPeer struct {
	UID      string
	Callsign string
}

func (p ChatPeer) isRoom() bool { return p.UID == TAKChatRoom || p.Callsign == TAKChatRoom }

// ChatAddressees returns whom a GeoChat is for, and room true when it went to
// All Chat Rooms. The server's own routing (<marti><dest>) is read first, as
// the server reads it; a dest that names only a callsign or only a uid takes
// the other from __chat when the two agree. Without a dest, __chat names the
// recipient. Anything but a GeoChat answers nothing.
func ChatAddressees(ev *CotEvent) (to []ChatPeer, room bool) {
	if ev == nil || ev.Type != CotEventTypeChat || ev.Detail == nil {
		return nil, false
	}
	d := ev.Detail
	var chatPeer ChatPeer
	if d.Chat != nil {
		chatPeer = ChatPeer{UID: d.Chat.ID, Callsign: d.Chat.Chatroom}
		if chatPeer.UID == "" && d.Chat.Group != nil {
			chatPeer.UID = d.Chat.Group.UID1
		}
	}
	if chatPeer.UID == "" && d.Remarks != nil {
		chatPeer.UID = d.Remarks.To
	}
	if d.Marti != nil {
		for _, dest := range d.Marti.Dests {
			if dest.Mission != "" || (dest.UID == "" && dest.Callsign == "") {
				continue
			}
			p := ChatPeer{UID: dest.UID, Callsign: dest.Callsign}
			if p.UID == "" && p.Callsign == chatPeer.Callsign {
				p.UID = chatPeer.UID
			}
			if p.Callsign == "" && p.UID == chatPeer.UID {
				p.Callsign = chatPeer.Callsign
			}
			to = append(to, p)
		}
		if len(to) > 0 {
			return to, false
		}
	}
	if chatPeer.isRoom() {
		return nil, true
	}
	if chatPeer.UID == "" && chatPeer.Callsign == "" {
		return nil, false
	}
	return []ChatPeer{chatPeer}, false
}

// ChatSender returns who wrote a GeoChat: the link a client puts on its own
// chats, else chatgrp's uid0; the callsign from __chat, else the contact.
func ChatSender(ev *CotEvent) ChatPeer {
	var p ChatPeer
	if ev == nil || ev.Detail == nil {
		return p
	}
	d := ev.Detail
	if d.Link != nil {
		p.UID = d.Link.UID
	}
	if d.Chat != nil {
		if p.UID == "" && d.Chat.Group != nil {
			p.UID = d.Chat.Group.UID0
		}
		p.Callsign = d.Chat.SenderCallsign
	}
	if p.Callsign == "" && d.Contact != nil {
		p.Callsign = d.Contact.Callsign
	}
	return p
}

// ChatText is a GeoChat's words.
func ChatText(ev *CotEvent) string {
	if ev == nil || ev.Detail == nil || ev.Detail.Remarks == nil {
		return ""
	}
	return strings.TrimSpace(ev.Detail.Remarks.Text)
}

// chatMessageID is the GeoChat's own id, which a duplicate repeats.
func chatMessageID(ev *CotEvent) string {
	if ev.Detail != nil && ev.Detail.Chat != nil && ev.Detail.Chat.MessageID != "" {
		return ev.Detail.Chat.MessageID
	}
	return ev.UID
}

// BuildGeoChat writes a chat from one peer to another (or to the room, when to
// is TAKChatRoom) in the form ATAK writes: __chat and chatgrp name both ends,
// link the sender, remarks the words and the recipient, and a dest asks the
// server to deliver it to the recipient only.
func BuildGeoChat(from, to ChatPeer, text string) CotEvent {
	return buildGeoChatWithID(from, to, text, newChatMessageID())
}

// buildGeoChatWithID is BuildGeoChat under a given message id: a chat that
// came as a frame keeps the id the Hub gave it. [MESHSAT-1744]
func buildGeoChatWithID(from, to ChatPeer, text, id string) CotEvent {
	now := time.Now().UTC()
	ts := now.Format(cotTimeFormat)
	ev := CotEvent{
		Version: "2.0",
		UID:     "GeoChat." + from.UID + "." + to.UID + "." + id,
		Type:    CotEventTypeChat,
		How:     "h-g-i-g-o",
		Time:    ts,
		Start:   ts,
		Stale:   now.Add(takChatStale).Format(cotTimeFormat),
		Point:   CotPoint{Hae: 9999999, Ce: 9999999, Le: 9999999},
		Detail: &CotDetail{
			Chat: &CotChat{
				Parent:         "RootContactGroup",
				GroupOwner:     "false",
				MessageID:      id,
				Chatroom:       to.Callsign,
				ID:             to.UID,
				SenderCallsign: from.Callsign,
				Group:          &CotChatGroup{UID0: from.UID, UID1: to.UID, ID: to.UID},
			},
			Link:    &CotLink{UID: from.UID, Type: CotEventTypePosition, Relation: "p-p"},
			Remarks: &CotRemarks{Source: "BAO.F.ATAK." + from.UID, To: to.UID, Time: ts, Text: text},
		},
	}
	if !to.isRoom() {
		ev.Detail.Marti = &CotMarti{Dests: []CotDest{{UID: to.UID, Callsign: to.Callsign}}}
	}
	return ev
}

func newChatMessageID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// NodeFromTAKUID returns the mesh node a uid this Bridge exports names:
// "meshsat-" and eight hex digits.
func NodeFromTAKUID(uid string) (uint32, bool) {
	hexID, ok := strings.CutPrefix(uid, "meshsat-")
	if !ok || len(hexID) != 8 {
		return 0, false
	}
	v, err := strconv.ParseUint(hexID, 16, 32)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

// TAKNodeUID is the uid this Bridge exports a mesh node under.
func TAKNodeUID(node uint32) string { return fmt.Sprintf("meshsat-%08x", node) }

// TAKNodeCallsign is the callsign this Bridge exports a mesh node under: the
// gateway's callsign prefix and the node id's low 16 bits.
func TAKNodeCallsign(prefix string, node uint32) string {
	return fmt.Sprintf("%s-%04x", prefix, node&0xFFFF)
}

// takChatLine is what goes to a radio: who wrote, then the words, cut to
// TAKChatMaxBytes on a character boundary.
func takChatLine(from ChatPeer, text string) (string, bool) {
	name := from.Callsign
	if name == "" {
		name = from.UID
	}
	line := name + ": " + text
	if len(line) <= TAKChatMaxBytes {
		return line, false
	}
	cut := TAKChatMaxBytes
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return line[:cut], true
}

// TAKChatThreads remember who last wrote to a node, for its answer.
// *database.DB is one.
type TAKChatThreads interface {
	OpenTAKChatThread(node uint32, peerUID, peerCallsign string, at time.Time) error
	TAKChatThread(node uint32, since time.Time) (peerUID, peerCallsign string, ok bool, err error)
}

// What this Bridge tells the Hub became of a chat it was handed, one report
// per addressee (MESHSAT-1746). The words are the Hub's contract
// (meshsat-hub docs/bridge-hub-protocol.md, "TAK Direct Messages").
const (
	// TAKChatQueued: the words are in the delivery ledger, a row for the
	// radio. The ledger's "delivered" is never reported from here: a mesh
	// send through the ledger reads no routing ack, so "delivered" would
	// only mean the radio took the frame.
	TAKChatQueued = "queued"
	// TAKChatFailed: the ledger gave up on the row.
	TAKChatFailed = "failed"
	// TAKChatUndelivered: refused before the ledger, with a reason.
	TAKChatUndelivered = "undelivered"

	takChatReasonNoText     = "no_text"
	takChatReasonNoSender   = "no_sender"
	takChatReasonOwnChat    = "own_chat"
	takChatReasonNotHeard   = "not_heard"
	takChatReasonAmbiguous  = "ambiguous_callsign"
	takChatReasonNoRadio    = "no_radio"
	takChatReasonSendFailed = "send_failed"
)

// TAKChatStatus is one report: what became of chat MessageID for CrewUID.
// Bearer is the bearer the chat came on as a frame ("sms", "sbd", "imt";
// empty for a chat from the Hub's MQTT or a TAK server), and ReplyHere says
// the Hub asked for the word on that bearer even with its link up. The
// frame goes out from send; the Hub reporter's MQTT report is separate.
// [MESHSAT-1744]
type TAKChatStatus struct {
	MessageID string
	CrewUID   string
	Status    string // TAKChatQueued, TAKChatFailed or TAKChatUndelivered
	Reason    string // one machine word, empty for queued
	Detail    string // one line for a person
	At        time.Time
	Bearer    string
	ReplyHere bool
}

// TAKChatThreadsVia is the part of TAKChatThreads that remembers how a
// chat came, so the answer goes back the same way: the bearer and the
// message id of the frame, and whether the Hub asked for the answer on that
// bearer. *database.DB is one; a Threads without it answers over MQTT only.
// [MESHSAT-1744]
type TAKChatThreadsVia interface {
	OpenTAKChatThreadVia(node uint32, peerUID, peerCallsign, bearer, messageID string, replyHere bool, at time.Time) error
	TAKChatThreadVia(node uint32, since time.Time) (peerUID, peerCallsign, bearer, messageID string, replyHere bool, ok bool, err error)
}

// TAKChatFrames are the ways back to a Hub that wrote over satellite or
// SMS (MESHSAT-1744): a crew's answer (C2) and the kit's word (C3) as
// uplink frames on the bearer the chat came on, and whether the Hub's MQTT
// link is up, which decides whether a frame is owed at all.
type TAKChatFrames struct {
	SendAnswer func(bearer string, frame []byte) error
	SendStatus func(bearer string, frame []byte) error
	HubUp      func() bool
}

// TAKChatDeps connect the chat to the rest of the Bridge. Each may be nil,
// which turns off what it is for.
type TAKChatDeps struct {
	// Heard lists the mesh nodes this Bridge's radio heard within the last
	// day: the crews this Bridge puts on the map, and so the only nodes a
	// chat may be sent to from here.
	Heard func() []uint32
	// LocalNode is this Bridge's own radio.
	LocalNode func() (uint32, bool)
	// SendDM queues text to one node through the delivery ledger and
	// answers the row's id, which the ledger's delivery_* events carry.
	SendDM func(node uint32, text string) (int64, error)
	// Broadcast queues text to the whole mesh through the delivery ledger.
	Broadcast func(text string) (int64, error)
	// Threads remember who wrote to which node.
	Threads TAKChatThreads
	// Gateway is the running TAK gateway, which sends an answer and names
	// this Bridge's crews; nil when none runs.
	Gateway func() *TAKGateway
}

// TAKChat delivers addressed GeoChats to the mesh and sends the crews'
// answers back.
type TAKChat struct {
	deps TAKChatDeps
	now  func() time.Time

	mu      sync.Mutex
	seen    map[string]time.Time
	report  func(TAKChatStatus)
	frames  *TAKChatFrames
	pending map[int64]takChatPending // ledger row -> the chat it carries
}

// takChatPending is a queued chat whose ledger outcome is still owed.
type takChatPending struct {
	messageID string
	crewUID   string
	at        time.Time
	via       via
}

// via is how a chat came: nothing for the Hub's MQTT or a TAK server, else
// the frame's bearer and whether the Hub asked for the answer on it.
type via struct {
	bearer    string
	replyHere bool
}

// takChatSeenFor is how long a chat's id is remembered against a second
// delivery of the same chat (a QoS 1 redelivery, a client sending again).
const takChatSeenFor = 10 * time.Minute

// takChatPendingFor is how long a queued chat's ledger row is watched for
// its outcome: three retries with the ledger's backoff are over well within.
const takChatPendingFor = 6 * time.Hour

// NewTAKChat returns a TAKChat with deps.
func NewTAKChat(deps TAKChatDeps) *TAKChat {
	return &TAKChat{deps: deps, now: time.Now, seen: map[string]time.Time{}, pending: map[int64]takChatPending{}}
}

// SetReport makes fn hear what became of each chat (the Hub reporter
// publishes it on this bridge's tak/chat/status). Nil reports nothing, as a
// kit with no Hub. fn may block; it runs on the goroutine that delivered
// or observed. [MESHSAT-1746]
func (c *TAKChat) SetReport(fn func(TAKChatStatus)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.report = fn
}

// SetFrames gives the chat its ways back over satellite and SMS. Nil turns
// them off: a chat that came as a frame is then answered over MQTT only.
// [MESHSAT-1744]
func (c *TAKChat) SetFrames(f *TAKChatFrames) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frames = f
}

func (c *TAKChat) framesSet() *TAKChatFrames {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frames
}

// hubUp reports whether the Hub's MQTT link is up; with no way to tell it
// counts as up, so nothing is spent on a frame.
func (c *TAKChat) hubUp() bool {
	f := c.framesSet()
	if f == nil || f.HubUp == nil {
		return true
	}
	return f.HubUp()
}

// owesFrame says whether a chat that came on bearer gets its answer and
// word back as a frame: when the Hub asked (ReplyHere), or its link is down.
func (c *TAKChat) owesFrame(v via) bool {
	return v.bearer != "" && c.framesSet() != nil && (v.replyHere || !c.hubUp())
}

// DeliverFrame takes a chat the Hub sent as a frame over satellite or SMS
// (crewchat C1) and delivers it exactly as the same GeoChat on tak/cot/in:
// from the operator the kit knows by hash, to the crew, under the Hub's
// message id. The report and the answer go back on bearer when the Hub
// asked or its link is down. [MESHSAT-1744]
func (c *TAKChat) DeliverFrame(f crewchat.Chat, bearer string) int {
	from := ChatPeer{UID: f.OperatorUID(), Callsign: f.Callsign}
	// The frame names the crew by node alone; the callsign it is exported
	// under is this kit's to give, so a report reads as the Hub shows it.
	to := ChatPeer{UID: TAKNodeUID(f.Node)}
	gw := c.gateway()
	if gw != nil {
		to.Callsign = TAKNodeCallsign(gw.config.CallsignPrefix, f.Node)
	}
	if f.ToKit {
		to.UID = OwnTAKUID(fmt.Sprintf("%08x", f.Node))
		to.Callsign = ""
		if gw != nil {
			_, to.Callsign = gw.own()
		}
	}
	ev := buildGeoChatWithID(from, to, f.Text, f.MessageID)
	log.Info().Str("bearer", bearer).Str("from", f.Callsign).Str("to", to.UID).Bool("reply_here", f.ReplyHere).
		Msg("tak chat: a chat came as a frame")
	return c.deliver(&ev, via{bearer: bearer, replyHere: f.ReplyHere})
}

// Deliver takes one GeoChat addressed to crews of this Bridge and queues it to
// each of them: a direct message to a node it hears, a broadcast when the chat
// is to this Bridge itself. A chat to the room, to nobody this Bridge reaches,
// with no words, with no sender to answer, or one seen already, goes nowhere.
// Every addressee the chat names gets a report (SetReport): queued, or
// undelivered with the reason; a chat seen already gets none, the first
// delivery's report stands. It answers how many sends it queued.
func (c *TAKChat) Deliver(ev *CotEvent) int {
	return c.deliver(ev, via{})
}

func (c *TAKChat) deliver(ev *CotEvent, v via) int {
	text := ChatText(ev)
	from := ChatSender(ev)
	to, _ := ChatAddressees(ev)
	id := chatMessageID(ev)
	if len(to) == 0 {
		return 0 // the room, or nobody: nobody to tell either
	}
	switch {
	case text == "":
		c.refuseAll(id, to, takChatReasonNoText, "the chat carries no words", v)
		return 0
	case from.UID == "" && from.Callsign == "":
		c.refuseAll(id, to, takChatReasonNoSender, "the chat names no sender for the answer to go to", v)
		return 0
	}
	if _, crew := NodeFromTAKUID(from.UID); crew || from.UID == c.ownUID() {
		// A crew's own chat, or this Bridge's: never back to the air.
		c.refuseAll(id, to, takChatReasonOwnChat, "a crew's own chat, or the kit's, never goes back to the air", v)
		return 0
	}
	if c.seenBefore(id) {
		log.Debug().Str("chat", id).Msg("tak chat: seen already, not sent again")
		return 0
	}
	line, cut := takChatLine(from, text)
	if cut {
		log.Info().Str("from", from.Callsign).Int("bytes", len(text)).
			Msg("tak chat: longer than a radio text, sent cut")
	}

	heard := c.heard()
	n := 0
	done := map[uint32]bool{}
	for _, p := range to {
		if c.isOwn(p) {
			if c.deps.Broadcast == nil {
				c.refuse(id, p, takChatReasonNoRadio, "the kit has no mesh radio to send on", v)
				continue
			}
			delID, err := c.deps.Broadcast(line)
			if err != nil {
				log.Warn().Err(err).Str("from", from.Callsign).Msg("tak chat: broadcast not queued")
				c.refuse(id, p, takChatReasonSendFailed, "not queued to the mesh: "+clipDetail(err.Error()), v)
				continue
			}
			log.Info().Str("from", from.Callsign).Msg("tak chat: to this Bridge, queued to the whole mesh")
			c.queued(id, p, delID, "queued to the whole mesh on "+c.ownCallsign(p), v)
			n++
			continue
		}
		node, reason := c.resolve(p, heard)
		switch reason {
		case "":
		case takChatReasonAmbiguous:
			log.Info().Str("from", from.Callsign).Str("callsign", p.Callsign).
				Msg("tak chat: two heard nodes answer to the callsign, not sent")
			c.refuse(id, p, reason, "two nodes the kit heard answer to "+p.Callsign+"; name the uid", v)
			continue
		default:
			// The Hub lists a crew for a day after its last export; this
			// Bridge sends only to a node its radio heard in that time. Say
			// so, or a dispatcher's message vanishes without a trace.
			log.Info().Str("from", from.Callsign).Str("to", p.UID).Str("callsign", p.Callsign).
				Msg("tak chat: to a crew this Bridge has not heard for a day, not sent")
			c.refuse(id, p, reason, "the kit has not heard "+peerName(p)+" in the last day", v)
			continue
		}
		if done[node] {
			continue
		}
		done[node] = true
		if c.deps.SendDM == nil {
			c.refuse(id, p, takChatReasonNoRadio, "the kit has no mesh radio to send on", v)
			continue
		}
		delID, err := c.deps.SendDM(node, line)
		if err != nil {
			log.Warn().Err(err).Str("to", fmt.Sprintf("!%08x", node)).Msg("tak chat: direct message not queued")
			c.refuse(id, p, takChatReasonSendFailed, "not queued to the radio: "+clipDetail(err.Error()), v)
			continue
		}
		c.openThread(node, from, id, v)
		log.Info().Str("from", from.Callsign).Str("to", fmt.Sprintf("!%08x", node)).
			Msg("tak chat: queued as a direct message")
		c.queued(id, p, delID, fmt.Sprintf("queued as a direct message to !%08x", node), v)
		n++
	}
	return n
}

// openThread remembers who wrote to node, and how, for the answer.
func (c *TAKChat) openThread(node uint32, from ChatPeer, id string, v via) {
	if c.deps.Threads == nil {
		return
	}
	var err error
	if tv, ok := c.deps.Threads.(TAKChatThreadsVia); ok {
		err = tv.OpenTAKChatThreadVia(node, from.UID, from.Callsign, v.bearer, id, v.replyHere, c.now())
	} else {
		err = c.deps.Threads.OpenTAKChatThread(node, from.UID, from.Callsign, c.now())
	}
	if err != nil {
		log.Warn().Err(err).Msg("tak chat: the answer's way back was not recorded")
	}
}

// ObserveDelivery hears the ledger's delivery_* events (engine.Processor
// Subscribe) and reports a queued chat whose row the ledger gave up on as
// failed. A row that went out is forgotten without a word: the mesh send
// reads no routing ack, so the ledger's "delivered" is not the node's
// receipt. [MESHSAT-1746]
func (c *TAKChat) ObserveDelivery(ev transport.MeshEvent) {
	switch ev.Type {
	case "delivery_dead", "delivery_acked", "delivery_sent":
	default:
		return
	}
	var data struct {
		ID    int64  `json:"id"`
		Error string `json:"error"`
	}
	if json.Unmarshal(ev.Data, &data) != nil || data.ID == 0 {
		return
	}
	c.mu.Lock()
	p, ok := c.pending[data.ID]
	delete(c.pending, data.ID)
	c.mu.Unlock()
	if !ok || ev.Type != "delivery_dead" {
		return
	}
	detail := "the kit's delivery ledger gave up on it"
	if data.Error != "" {
		detail += ": " + clipDetail(data.Error)
	}
	c.send(TAKChatStatus{MessageID: p.messageID, CrewUID: p.crewUID, Status: TAKChatFailed,
		Reason: takChatReasonSendFailed, Detail: detail, Bearer: p.via.bearer, ReplyHere: p.via.replyHere})
}

// queued reports a chat in the ledger and watches its row for the outcome.
func (c *TAKChat) queued(id string, p ChatPeer, delID int64, detail string, v via) {
	now := c.now()
	c.mu.Lock()
	for k, row := range c.pending {
		if now.Sub(row.at) > takChatPendingFor {
			delete(c.pending, k)
		}
	}
	if delID > 0 {
		c.pending[delID] = takChatPending{messageID: id, crewUID: crewUIDOf(p), at: now, via: v}
	}
	c.mu.Unlock()
	c.send(TAKChatStatus{MessageID: id, CrewUID: crewUIDOf(p), Status: TAKChatQueued, Detail: detail,
		Bearer: v.bearer, ReplyHere: v.replyHere})
}

// refuse reports a chat that goes nowhere for one addressee.
func (c *TAKChat) refuse(id string, p ChatPeer, reason, detail string, v via) {
	c.send(TAKChatStatus{MessageID: id, CrewUID: crewUIDOf(p), Status: TAKChatUndelivered, Reason: reason, Detail: detail,
		Bearer: v.bearer, ReplyHere: v.replyHere})
}

func (c *TAKChat) refuseAll(id string, to []ChatPeer, reason, detail string, v via) {
	for _, p := range to {
		c.refuse(id, p, reason, detail, v)
	}
}

// send hands a report to the Hub reporter, and, for a chat that came as a
// frame whose word is owed on that bearer, sends it as a C3 frame too: the
// Hub takes the same word twice as one. [MESHSAT-1744]
func (c *TAKChat) send(st TAKChatStatus) {
	if st.At.IsZero() {
		st.At = c.now().UTC()
	}
	if c.owesFrame(via{bearer: st.Bearer, replyHere: st.ReplyHere}) {
		c.sendStatusFrame(st)
	}
	c.mu.Lock()
	fn := c.report
	c.mu.Unlock()
	if fn == nil {
		return
	}
	fn(st)
}

// sendStatusFrame sends one report as a C3 frame on the chat's bearer.
func (c *TAKChat) sendStatusFrame(st TAKChatStatus) {
	f := c.framesSet()
	if f == nil || f.SendStatus == nil {
		return
	}
	node, _, ok := crewNodeOf(st.CrewUID)
	if !ok {
		return
	}
	frame, err := crewchat.EncodeStatus(crewchat.Status{MessageID: st.MessageID, Node: node, At: st.At,
		Status: st.Status, Reason: st.Reason, Detail: st.Detail})
	if err != nil {
		log.Warn().Err(err).Str("chat", st.MessageID).Msg("tak chat: the word on a chat could not be framed")
		return
	}
	if err := f.SendStatus(st.Bearer, frame); err != nil {
		log.Warn().Err(err).Str("chat", st.MessageID).Str("bearer", st.Bearer).
			Msg("tak chat: the word on a chat was not queued on its bearer")
		return
	}
	log.Info().Str("chat", st.MessageID).Str("bearer", st.Bearer).Str("status", st.Status).Int("bytes", len(frame)).
		Msg("tak chat: the word on a chat queued as a frame")
}

// crewNodeOf is the node a crew uid names: a mesh node (meshsat-<hex>) or
// this kit (MESHSAT-<hex>, toKit true).
func crewNodeOf(uid string) (node uint32, toKit bool, ok bool) {
	if node, ok := NodeFromTAKUID(uid); ok {
		return node, false, true
	}
	if hexID, found := strings.CutPrefix(uid, "MESHSAT-"); found && len(hexID) == 8 {
		if v, err := strconv.ParseUint(hexID, 16, 32); err == nil {
			return uint32(v), true, true
		}
	}
	return 0, false, false
}

// crewUIDOf is the addressee as the Hub keys it: the uid, else the callsign.
func crewUIDOf(p ChatPeer) string {
	if p.UID != "" {
		return p.UID
	}
	return p.Callsign
}

// peerName is the addressee as a person reads it.
func peerName(p ChatPeer) string {
	if p.Callsign != "" {
		return p.Callsign
	}
	return p.UID
}

func (c *TAKChat) ownCallsign(p ChatPeer) string {
	if gw := c.gateway(); gw != nil {
		if _, callsign := gw.own(); callsign != "" {
			return callsign
		}
	}
	return peerName(p)
}

// takChatDetailMax bounds a report's detail line.
const takChatDetailMax = 160

func clipDetail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= takChatDetailMax {
		return s
	}
	cut := takChatDetailMax
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// Reply sends a text node wrote to this Bridge's own node back to the one who
// last wrote to node from TAK or the Hub, within TAKChatThreadLife. It answers
// false when nobody did, or no TAK gateway runs to send it.
func (c *TAKChat) Reply(node uint32, text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || c.deps.Threads == nil || c.deps.Gateway == nil {
		return false
	}
	var peerUID, peerCallsign string
	var ok bool
	var err error
	var v via
	var messageID string
	if tv, isVia := c.deps.Threads.(TAKChatThreadsVia); isVia {
		peerUID, peerCallsign, v.bearer, messageID, v.replyHere, ok, err = tv.TAKChatThreadVia(node, c.now().Add(-TAKChatThreadLife))
	} else {
		peerUID, peerCallsign, ok, err = c.deps.Threads.TAKChatThread(node, c.now().Add(-TAKChatThreadLife))
	}
	if err != nil {
		log.Warn().Err(err).Msg("tak chat: the way back could not be read")
		return false
	}
	if !ok {
		return false
	}
	// The question came as a frame and the answer is owed the same way:
	// a C2 frame on that bearer, and no GeoChat, which the Hub would take
	// as a second answer. [MESHSAT-1744]
	if messageID != "" && c.owesFrame(v) {
		return c.replyFrame(node, text, peerUID, peerCallsign, messageID, v)
	}
	gw := c.deps.Gateway()
	if gw == nil {
		log.Warn().Str("from", fmt.Sprintf("!%08x", node)).
			Msg("tak chat: an answer with no TAK gateway running to send it")
		return false
	}
	peer := ChatPeer{UID: peerUID, Callsign: peerCallsign}
	if err := gw.SendChat(gw.NodePeer(node), peer, text); err != nil {
		log.Warn().Err(err).Str("to", peerCallsign).Msg("tak chat: the answer did not reach every TAK output")
	}
	c.openThread(node, ChatPeer{UID: peerUID, Callsign: peerCallsign}, messageID, v)
	log.Info().Str("from", fmt.Sprintf("!%08x", node)).Str("to", peerCallsign).
		Msg("tak chat: a crew's answer sent back")
	return true
}

// replyFrame sends a crew's answer as a C2 frame on the bearer the question
// came on, and keeps the thread open the same way.
func (c *TAKChat) replyFrame(node uint32, text, peerUID, peerCallsign, messageID string, v via) bool {
	f := c.framesSet()
	if f == nil || f.SendAnswer == nil {
		return false
	}
	frame, err := crewchat.EncodeAnswer(crewchat.Answer{MessageID: messageID, Node: node, At: c.now(), Text: text})
	if err != nil {
		log.Warn().Err(err).Str("chat", messageID).Msg("tak chat: a crew's answer could not be framed")
		return false
	}
	if err := f.SendAnswer(v.bearer, frame); err != nil {
		log.Warn().Err(err).Str("chat", messageID).Str("bearer", v.bearer).
			Msg("tak chat: a crew's answer was not queued on its bearer")
		return false
	}
	c.openThread(node, ChatPeer{UID: peerUID, Callsign: peerCallsign}, messageID, v)
	log.Info().Str("from", fmt.Sprintf("!%08x", node)).Str("to", peerCallsign).Str("bearer", v.bearer).Int("bytes", len(frame)).
		Msg("tak chat: a crew's answer queued as a frame")
	return true
}

// resolve finds the node a peer names among the nodes heard: by uid, or by a
// callsign exactly one heard node matches. A callsign carries only 16 bits of
// the node id, so two matches resolve to neither. The reason is empty when
// the node was found, else why not (not_heard, ambiguous_callsign).
func (c *TAKChat) resolve(p ChatPeer, heard map[uint32]bool) (uint32, string) {
	if node, ok := NodeFromTAKUID(p.UID); ok {
		if !heard[node] {
			return 0, takChatReasonNotHeard
		}
		return node, ""
	}
	if p.Callsign == "" {
		return 0, takChatReasonNotHeard
	}
	prefix := ""
	if gw := c.gateway(); gw != nil {
		prefix = gw.config.CallsignPrefix
	}
	var found uint32
	matches := 0
	for node := range heard {
		if strings.EqualFold(TAKNodeCallsign(prefix, node), p.Callsign) {
			found = node
			matches++
		}
	}
	switch matches {
	case 1:
		return found, ""
	case 0:
		return 0, takChatReasonNotHeard
	default:
		return 0, takChatReasonAmbiguous
	}
}

func (c *TAKChat) isOwn(p ChatPeer) bool {
	if own := c.ownUID(); own != "" && p.UID == own {
		return true
	}
	if gw := c.gateway(); gw != nil && p.UID == "" && p.Callsign != "" {
		_, callsign := gw.own()
		return strings.EqualFold(p.Callsign, callsign)
	}
	return false
}

func (c *TAKChat) ownUID() string {
	if c.deps.LocalNode == nil {
		return ""
	}
	if node, ok := c.deps.LocalNode(); ok {
		return OwnTAKUID(fmt.Sprintf("%08x", node))
	}
	return ""
}

func (c *TAKChat) gateway() *TAKGateway {
	if c.deps.Gateway == nil {
		return nil
	}
	return c.deps.Gateway()
}

func (c *TAKChat) heard() map[uint32]bool {
	out := map[uint32]bool{}
	if c.deps.Heard == nil {
		return out
	}
	local, hasLocal := uint32(0), false
	if c.deps.LocalNode != nil {
		local, hasLocal = c.deps.LocalNode()
	}
	for _, n := range c.deps.Heard() {
		if hasLocal && n == local {
			continue
		}
		out[n] = true
	}
	return out
}

func (c *TAKChat) seenBefore(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, at := range c.seen {
		if now.Sub(at) > takChatSeenFor {
			delete(c.seen, k)
		}
	}
	if _, ok := c.seen[id]; ok {
		return true
	}
	c.seen[id] = now
	return false
}
