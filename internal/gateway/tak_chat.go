package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"
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
	now := time.Now().UTC()
	ts := now.Format(cotTimeFormat)
	id := newChatMessageID()
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

// TAKChatDeps connect the chat to the rest of the Bridge. Each may be nil,
// which turns off what it is for.
type TAKChatDeps struct {
	// Heard lists the mesh nodes this Bridge's radio heard within the last
	// day: the crews this Bridge puts on the map, and so the only nodes a
	// chat may be sent to from here.
	Heard func() []uint32
	// LocalNode is this Bridge's own radio.
	LocalNode func() (uint32, bool)
	// SendDM queues text to one node through the delivery ledger.
	SendDM func(node uint32, text string) error
	// Broadcast queues text to the whole mesh through the delivery ledger.
	Broadcast func(text string) error
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

	mu   sync.Mutex
	seen map[string]time.Time
}

// takChatSeenFor is how long a chat's id is remembered against a second
// delivery of the same chat (a QoS 1 redelivery, a client sending again).
const takChatSeenFor = 10 * time.Minute

// NewTAKChat returns a TAKChat with deps.
func NewTAKChat(deps TAKChatDeps) *TAKChat {
	return &TAKChat{deps: deps, now: time.Now, seen: map[string]time.Time{}}
}

// Deliver takes one GeoChat addressed to crews of this Bridge and queues it to
// each of them: a direct message to a node it hears, a broadcast when the chat
// is to this Bridge itself. A chat to the room, to nobody this Bridge reaches,
// with no words, with no sender to answer, or one seen already, goes nowhere.
// It answers how many sends it queued.
func (c *TAKChat) Deliver(ev *CotEvent) int {
	text := ChatText(ev)
	from := ChatSender(ev)
	to, _ := ChatAddressees(ev)
	if text == "" || len(to) == 0 || (from.UID == "" && from.Callsign == "") {
		return 0
	}
	if _, crew := NodeFromTAKUID(from.UID); crew || from.UID == c.ownUID() {
		return 0 // a crew's own chat, or this Bridge's: never back to the air
	}
	if c.seenBefore(chatMessageID(ev)) {
		log.Debug().Str("chat", chatMessageID(ev)).Msg("tak chat: seen already, not sent again")
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
				continue
			}
			if err := c.deps.Broadcast(line); err != nil {
				log.Warn().Err(err).Str("from", from.Callsign).Msg("tak chat: broadcast not queued")
				continue
			}
			log.Info().Str("from", from.Callsign).Msg("tak chat: to this Bridge, queued to the whole mesh")
			n++
			continue
		}
		node, ok := c.resolve(p, heard)
		if !ok {
			// The Hub lists a crew for a day after its last export; this
			// Bridge sends only to a node its radio heard in that time. Say
			// so, or a dispatcher's message vanishes without a trace.
			log.Info().Str("from", from.Callsign).Str("to", p.UID).Str("callsign", p.Callsign).
				Msg("tak chat: to a crew this Bridge has not heard for a day, not sent")
			continue
		}
		if done[node] {
			continue
		}
		done[node] = true
		if c.deps.SendDM == nil {
			continue
		}
		if err := c.deps.SendDM(node, line); err != nil {
			log.Warn().Err(err).Str("to", fmt.Sprintf("!%08x", node)).Msg("tak chat: direct message not queued")
			continue
		}
		if c.deps.Threads != nil {
			if err := c.deps.Threads.OpenTAKChatThread(node, from.UID, from.Callsign, c.now()); err != nil {
				log.Warn().Err(err).Msg("tak chat: the answer's way back was not recorded")
			}
		}
		log.Info().Str("from", from.Callsign).Str("to", fmt.Sprintf("!%08x", node)).
			Msg("tak chat: queued as a direct message")
		n++
	}
	return n
}

// Reply sends a text node wrote to this Bridge's own node back to the one who
// last wrote to node from TAK or the Hub, within TAKChatThreadLife. It answers
// false when nobody did, or no TAK gateway runs to send it.
func (c *TAKChat) Reply(node uint32, text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || c.deps.Threads == nil || c.deps.Gateway == nil {
		return false
	}
	peerUID, peerCallsign, ok, err := c.deps.Threads.TAKChatThread(node, c.now().Add(-TAKChatThreadLife))
	if err != nil {
		log.Warn().Err(err).Msg("tak chat: the way back could not be read")
		return false
	}
	if !ok {
		return false
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
	if err := c.deps.Threads.OpenTAKChatThread(node, peerUID, peerCallsign, c.now()); err != nil {
		log.Warn().Err(err).Msg("tak chat: the thread was not kept open")
	}
	log.Info().Str("from", fmt.Sprintf("!%08x", node)).Str("to", peerCallsign).
		Msg("tak chat: a crew's answer sent back")
	return true
}

// resolve finds the node a peer names among the nodes heard: by uid, or by a
// callsign exactly one heard node matches. A callsign carries only 16 bits of
// the node id, so two matches resolve to neither.
func (c *TAKChat) resolve(p ChatPeer, heard map[uint32]bool) (uint32, bool) {
	if node, ok := NodeFromTAKUID(p.UID); ok {
		return node, heard[node]
	}
	if p.Callsign == "" {
		return 0, false
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
	return found, matches == 1
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
