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
// the rest; hopPartsMax bounds how many replies wait at once.
const (
	hopPartsTTL = 10 * time.Minute
	hopPartsMax = 32
)

type hopPartsKey struct {
	peer    uint16
	counter uint16 // the request's counter, low 16 bits, as the reply names it
}

type hopParts struct {
	first  time.Time
	total  byte
	chunks map[byte]string
}

// execStatusNet answers STATUS-NET: the network status, then our hop state
// when media hopping is not off. An argument is the requester's hop state;
// anything else is refused, and an empty argument is the plain STATUS-NET
// it always was.
func (s *Service) execStatusNet(ctx context.Context, o Origin, args []byte) Result {
	if len(args) > 0 {
		r, err := hopwire.Parse(string(args))
		if err != nil {
			return Result{Code: RCBadArgs}
		}
		if s.d.HopPeer != nil {
			s.d.HopPeer(o.PeerID, o.Alias, o.Bearer, "request", r)
		}
	}
	return Result{Code: RCOK, Body: s.withHopState(s.statusNetBody(ctx), o.Bearer)}
}

// withHopState appends our hop state to a STATUS-NET body, within what the
// reply can carry on the bearer (MaxReplyChunks chunks), so the form is
// never cut by the chunking: it is cut to fit, and says so, or left out.
func (s *Service) withHopState(body, bearer string) string {
	if s.d.HopState == nil {
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
func (s *Service) hopReplyChunk(peer *database.OOBPeer, ifaceID string, ra ReplyArgs) {
	if s.d.HopPeer == nil || ra.Total == 0 || ra.Seq == 0 || ra.Seq > ra.Total {
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
	p := s.hopParts[key]
	if p == nil || p.total != ra.Total {
		if len(s.hopParts) >= hopPartsMax {
			s.hopMu.Unlock()
			return
		}
		p = &hopParts{first: now, total: ra.Total, chunks: map[byte]string{}}
		s.hopParts[key] = p
	}
	p.chunks[ra.Seq] = string(ra.Body)
	if len(p.chunks) < int(p.total) {
		s.hopMu.Unlock()
		return
	}
	delete(s.hopParts, key)
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
	s.d.HopPeer(peer.PeerID, peer.Alias, ifaceID, "reply", r)
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
// of our links goes out of use or back into use. [MESHSAT-1650]
func (s *Service) ShareHopState(ctx context.Context) []HopShare {
	if !s.enabled() || s.d.DB == nil || s.d.HopState == nil || s.d.FreeBearer == nil {
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
		now := s.now()
		s.hopMu.Lock()
		last, seen := s.hopShared[p.PeerID]
		s.hopMu.Unlock()
		if seen && now.Sub(last) < HopShareEvery {
			sh.Skipped = "recent"
			out = append(out, sh)
			continue
		}
		via := s.freeBearerTo(p, up)
		if via == "" {
			sh.Skipped = "no_free_bearer"
			out = append(out, sh)
			continue
		}
		sh.Bearer = via
		form := s.d.HopState(BearerBudget(via) - ExpiryLen)
		if form == "" {
			sh.Skipped = "no_free_bearer"
			out = append(out, sh)
			continue
		}
		if _, err := s.Send(ctx, SendRequest{PeerID: p.PeerID, Via: via, Cmd: CmdStatusNet, Args: []byte(form)}); err != nil {
			sh.Skipped = "send_error"
			out = append(out, sh)
			continue
		}
		s.hopMu.Lock()
		if s.hopShared == nil {
			s.hopShared = map[uint16]time.Time{}
		}
		s.hopShared[p.PeerID] = now
		s.hopMu.Unlock()
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
