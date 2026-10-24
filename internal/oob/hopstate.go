package oob

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/hopwire"
)

// Media hopping, part two: two Bridges tell each other their hop state over
// STATUS-NET (internal/hopwire is the form). A request may carry the
// sender's form as its argument; the reply appends the replier's form to
// the body. Both are shown, never acted on: a peer's report about its own
// links is not evidence about ours (MEDIA_HOP_DESIGN 4.3). [MESHSAT-1650]

// HopShareEvery bounds how often one peer is told our state unasked.
const HopShareEvery = 10 * time.Minute

// hopPartsTTL is how long the first chunks of a STATUS-NET reply wait for
// the rest; hopPartsMax bounds how many replies wait at once and
// hopPartsPerPeer how many of them one peer may hold, so one peer never
// starves another. hopAskedTTL is how long a STATUS-NET request of ours
// waits for its reply (past the request's own TTL, a satellite MT included);
// hopAskedPerPeer bounds the requests remembered per peer.
const (
	hopPartsTTL     = 10 * time.Minute
	hopPartsMax     = 32
	hopPartsPerPeer = 4
	hopAskedTTL     = time.Hour
	hopAskedPerPeer = 16
)

type hopPartsKey struct {
	peer    uint16
	counter uint16 // the request's counter, low 16 bits, as the reply names it
}

type hopParts struct {
	first  time.Time
	gen    uint64
	total  byte
	chunks map[byte]string
}

// hopAskedMark is one STATUS-NET request of ours: when it went, and in which
// media hopping interval.
type hopAskedMark struct {
	at  time.Time
	gen uint64
}

// hopSession reports the media hopping interval now and whether media
// hopping is on or watching. Nil means off: then nothing of the exchange
// is sent, kept or read.
func (s *Service) hopSession() (uint64, bool) {
	if s.d.HopSession == nil {
		return 0, false
	}
	return s.d.HopSession()
}

func (s *Service) hopOn() bool {
	_, on := s.hopSession()
	return on
}

// ResetHop drops every STATUS-NET request awaiting its reply and every reply
// being put back together: media hopping runs it when its switch goes off,
// so nothing gathered in one interval is read in the next (R55-B1). When
// each peer was last told our state stays, so turning the switch off and on
// again tells no peer more often than HopShareEvery.
func (s *Service) ResetHop() {
	s.hopMu.Lock()
	s.hopParts, s.hopAsked = nil, nil
	s.hopMu.Unlock()
}

// freeBearer reports whether the hop state may go on a bearer: a free one.
func (s *Service) freeBearer(id string) bool { return s.d.FreeBearer != nil && s.d.FreeBearer(id) }

// execStatusNet answers STATUS-NET: the network status, then our hop state
// when media hopping is not off and the reply goes on a free bearer. An
// argument is the requester's hop state; anything else is refused, and an
// empty argument is the plain STATUS-NET it always was.
func (s *Service) execStatusNet(ctx context.Context, o Origin, args []byte) Result {
	if len(args) > 0 {
		r, err := hopwire.Parse(string(args))
		if err != nil {
			return Result{Code: RCBadArgs}
		}
		if gen, on := s.hopSession(); s.d.HopPeer != nil && on {
			s.d.HopPeer(gen, o.PeerID, o.Alias, o.Bearer, "request", r)
		}
	}
	return Result{Code: RCOK, Body: s.withHopState(s.statusNetBody(ctx), o.Bearer)}
}

// withHopState appends our hop state to a STATUS-NET body, within what the
// reply can carry on the bearer (MaxReplyChunks chunks), so the form is
// never cut by the chunking: it is cut to fit, and says so, or left out.
// Only on a free bearer: a reply over SMS, satellite or cellular is the
// STATUS-NET it always was, byte for byte, and costs no more.
func (s *Service) withHopState(body, bearer string) string {
	if s.d.HopState == nil || !s.hopOn() || !s.freeBearer(bearer) {
		return body
	}
	room := (BearerBudget(bearer)-ReplyHeaderLen)*MaxReplyChunks - len(body) - 1
	if room < hopwire.MinLen {
		return body
	}
	form := s.d.HopState(room)
	if form == "" {
		return body
	}
	return body + " " + form
}

// hopReplyChunk puts a peer's STATUS-NET reply back together and, once
// every chunk is in, hands its hop state on. A reply missing a chunk is
// dropped after hopPartsTTL; a lost chunk costs the update, nothing else.
//
// Only a reply to a STATUS-NET request we sent that peer in this media
// hopping interval is read, once; and nothing at all while media hopping is
// off (what was kept went with the switch, ResetHop).
func (s *Service) hopReplyChunk(peer *database.OOBPeer, ifaceID string, ra ReplyArgs) {
	if s.d.HopPeer == nil || ra.Total == 0 || ra.Seq == 0 || ra.Seq > ra.Total {
		return
	}
	gen, on := s.hopSession()
	if !on {
		return
	}
	now := s.now()
	key := hopPartsKey{peer: peer.PeerID, counter: ra.ReqCounterLo}
	s.hopMu.Lock()
	if s.hopParts == nil {
		s.hopParts = map[hopPartsKey]*hopParts{}
	}
	for k, p := range s.hopParts {
		if now.Sub(p.first) > hopPartsTTL {
			delete(s.hopParts, k)
		}
	}
	if mark, ok := s.hopAsked[key]; !ok || mark.gen != gen || now.Sub(mark.at) > hopAskedTTL {
		s.hopMu.Unlock()
		return
	}
	p := s.hopParts[key]
	if p != nil && p.gen != gen {
		delete(s.hopParts, key)
		p = nil
	}
	if p == nil || p.total != ra.Total {
		if len(s.hopParts) >= hopPartsMax || s.openPartsLocked(peer.PeerID) >= hopPartsPerPeer {
			s.hopMu.Unlock()
			return
		}
		p = &hopParts{first: now, gen: gen, total: ra.Total, chunks: map[byte]string{}}
		s.hopParts[key] = p
	}
	p.chunks[ra.Seq] = string(ra.Body)
	if len(p.chunks) < int(p.total) {
		s.hopMu.Unlock()
		return
	}
	delete(s.hopParts, key)
	delete(s.hopAsked, key)
	body := ""
	for i := byte(1); i <= p.total; i++ {
		body += p.chunks[i]
	}
	s.hopMu.Unlock()
	form, ok := hopwire.Find(body)
	if !ok {
		return
	}
	r, err := hopwire.Parse(form)
	if err != nil {
		log.Debug().Err(err).Uint16("peer", peer.PeerID).Msg("oob: STATUS-NET reply carries no readable hop state")
		return
	}
	s.d.HopPeer(gen, peer.PeerID, peer.Alias, ifaceID, "reply", r)
}

// openPartsLocked counts the replies one peer has waiting. Caller holds hopMu.
func (s *Service) openPartsLocked(peer uint16) int {
	n := 0
	for k := range s.hopParts {
		if k.peer == peer {
			n++
		}
	}
	return n
}

// noteAsked remembers a STATUS-NET request we sent a peer, so its reply's
// hop state is read (by the request's counter, low 16 bits, as the reply
// names it) in the same media hopping interval. Only while media hopping
// is on or watching; a peer's oldest request is forgotten past
// hopAskedPerPeer.
func (s *Service) noteAsked(peer uint16, counter uint32) {
	gen, on := s.hopSession()
	if !on {
		return
	}
	now := s.now()
	s.hopMu.Lock()
	defer s.hopMu.Unlock()
	if s.hopAsked == nil {
		s.hopAsked = map[hopPartsKey]hopAskedMark{}
	}
	var oldest hopPartsKey
	var oldestAt time.Time
	n := 0
	for k, mark := range s.hopAsked {
		if mark.gen != gen || now.Sub(mark.at) > hopAskedTTL {
			delete(s.hopAsked, k)
			continue
		}
		if k.peer != peer {
			continue
		}
		n++
		if oldestAt.IsZero() || mark.at.Before(oldestAt) {
			oldest, oldestAt = k, mark.at
		}
	}
	if n >= hopAskedPerPeer {
		delete(s.hopAsked, oldest)
	}
	s.hopAsked[hopPartsKey{peer: peer, counter: uint16(counter)}] = hopAskedMark{at: now, gen: gen}
}

// HopShare is one peer ShareHopState told our state, or found no way to.
type HopShare struct {
	PeerID  uint16 `json:"peer_id"`
	Alias   string `json:"alias"`
	Bearer  string `json:"bearer,omitempty"`
	Sent    bool   `json:"sent"`
	Skipped string `json:"skipped,omitempty"` // why nothing went: "recent", "no_free_bearer", "send_error"
}

// ShareHopState tells each enabled peer our hop state in a STATUS-NET
// request, whose reply brings the peer's state back: on a free bearer that
// is up (never SMS, satellite or cellular), at most once per HopShareEvery
// per peer, and only while media hopping is on or watching (our form is ""
// otherwise). Each request sent is audited. The engine calls it when one
// of our links goes out of use or back into use, as output of the interval
// (FollowHopSet): once the switch is off our form is "" and no further peer
// is told, the send under way gets the interval's cancelled context, and the
// switch waits for that send and its audit (R55-B2). [MESHSAT-1650]
func (s *Service) ShareHopState(ctx context.Context) []HopShare {
	if !s.enabled() || s.d.DB == nil || s.d.HopState == nil || s.d.FreeBearer == nil || !s.hopOn() {
		return nil
	}
	if s.d.HopState(MaxArgs) == "" {
		return nil
	}
	peers, err := s.d.DB.ListOOBPeers()
	if err != nil {
		return nil
	}
	var up map[string]bool
	if s.d.BearersUp != nil {
		up = s.d.BearersUp()
	}
	var out []HopShare
	for i := range peers {
		p := &peers[i]
		if !p.Enabled {
			continue
		}
		sh := HopShare{PeerID: p.PeerID, Alias: p.Alias}
		// The slot is taken before the send and given back if nothing went,
		// so two shares at once (one per link change) tell a peer once.
		now := s.now()
		s.hopMu.Lock()
		last, seen := s.hopShared[p.PeerID]
		if seen && now.Sub(last) < HopShareEvery {
			s.hopMu.Unlock()
			sh.Skipped = "recent"
			out = append(out, sh)
			continue
		}
		if s.hopShared == nil {
			s.hopShared = map[uint16]time.Time{}
		}
		s.hopShared[p.PeerID] = now
		s.hopMu.Unlock()
		giveBack := func() {
			s.hopMu.Lock()
			if s.hopShared[p.PeerID].Equal(now) {
				if seen {
					s.hopShared[p.PeerID] = last
				} else {
					delete(s.hopShared, p.PeerID)
				}
			}
			s.hopMu.Unlock()
		}
		via := s.freeBearerTo(p, up)
		if via == "" {
			giveBack()
			sh.Skipped = "no_free_bearer"
			out = append(out, sh)
			continue
		}
		sh.Bearer = via
		form := s.d.HopState(BearerBudget(via) - ExpiryLen)
		if form == "" {
			giveBack()
			sh.Skipped = "no_free_bearer"
			out = append(out, sh)
			continue
		}
		if _, err := s.Send(ctx, SendRequest{PeerID: p.PeerID, Via: via, Cmd: CmdStatusNet, Args: []byte(form)}); err != nil {
			giveBack()
			sh.Skipped = "send_error"
			out = append(out, sh)
			continue
		}
		sh.Sent = true
		out = append(out, sh)
		if s.d.Audit != nil {
			detail, _ := json.Marshal(map[string]any{"peer_id": p.PeerID, "alias": p.Alias, "bearer": via, "hop_state": form})
			bearer, dir := via, "egress"
			s.d.Audit("oob_hop_share", &bearer, &dir, nil, string(detail))
		}
	}
	return out
}

// freeBearerTo picks the bearer our state goes to a peer on: the first, in
// interface-id order, of the peer's addressed bearers that is free and up.
func (s *Service) freeBearerTo(p *database.OOBPeer, up map[string]bool) string {
	var addrs map[string]string
	if err := json.Unmarshal([]byte(p.Addresses), &addrs); err != nil {
		return ""
	}
	ids := make([]string, 0, len(addrs))
	for id, a := range addrs {
		if a != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if s.d.FreeBearer(id) && up[id] {
			return id
		}
	}
	return ""
}

// HopSet is media hopping's switch as the hop exchange follows it
// (engine.HopSetManager).
type HopSet interface {
	OnOff(f func())
	SetOnChange(f func(gen uint64))
	Go(gen uint64, f func(ctx context.Context))
	Hold() (release func(), ok bool)
}

// hopHold holds the switch's interval while a STATUS-NET is answered, so a
// reply that carries our form is queued before the switch to off returns
// (R55-B2). Without a switch followed, or with it off, nothing is held.
func (s *Service) hopHold() func() {
	s.hopMu.Lock()
	h := s.hopSet
	s.hopMu.Unlock()
	if h == nil {
		return func() {}
	}
	release, _ := h.Hold()
	return release
}

// FollowHopSet makes the hop exchange follow media hopping's switch: what it
// keeps goes when the switch goes off; a change of our links tells the peers
// our state as output of the interval it happened in, and a STATUS-NET
// reply carrying our form is too, and the switch to off cancels and waits
// for both (R55-B1, R55-B2). A share ends with
// parent too, and after a minute at most.
func (s *Service) FollowHopSet(parent context.Context, h HopSet) {
	s.hopMu.Lock()
	s.hopSet = h
	s.hopMu.Unlock()
	h.OnOff(s.ResetHop)
	h.SetOnChange(func(gen uint64) {
		h.Go(gen, func(ctx context.Context) {
			sctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			defer context.AfterFunc(parent, cancel)()
			s.ShareHopState(sctx)
		})
	})
}
