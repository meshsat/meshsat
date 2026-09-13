package oob

import (
	"strings"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// learnMeshAddress follows a peer's radio to a new node number. A kit radio can
// come back from a reboot with a different number, and outbound frames go to
// the stored number, so without this they go nowhere while the peer's own
// frames keep arriving. Only an authenticated, fresh frame reaches this point.
// Only mesh bearers are learned (their addresses change; SMS numbers and
// callsigns do not), and only when a mesh address was already stored for the
// peer, so a frame typed on a handheld cannot seed an address the peer never
// had. [MESHSAT-1102]
func (s *Service) learnMeshAddress(peer *database.OOBPeer, ifaceID, fromAddr string) {
	if s.d.DB == nil || peer == nil || !strings.HasPrefix(ifaceID, "mesh_") || !isMeshNodeAddr(fromAddr) {
		return
	}
	stored := resolveAddress(peer, ifaceID)
	if stored == "" || strings.EqualFold(stored, fromAddr) {
		return
	}
	learned := strings.ToLower(fromAddr)
	changed, err := s.d.DB.SetOOBPeerAddress(peer.PeerID, ifaceID, stored, learned)
	if err != nil {
		log.Warn().Err(err).Uint16("peer", peer.PeerID).Msg("oob: learn mesh address")
		return
	}
	if !changed {
		// Someone changed the row since this frame's snapshot; their write wins.
		return
	}
	detail := stored + " -> " + learned
	log.Warn().Uint16("peer", peer.PeerID).Str("alias", peer.Alias).Str("bearer", ifaceID).
		Str("old", stored).Str("new", learned).Msg("oob: peer's radio has a new node number, following it")
	_, _ = s.d.DB.InsertOOBLog(&database.OOBLogEntry{
		PeerID: peer.PeerID, Direction: "in", Kind: "address_learn", Bearer: ifaceID, FromAddr: fromAddr,
		Result: "ok", Detail: detail,
	})
	if s.d.Audit != nil {
		dir := "in"
		s.d.Audit("oob_address_learn", &ifaceID, &dir, nil, detail)
	}
}

// isMeshNodeAddr reports a Meshtastic node address of the form !xxxxxxxx.
func isMeshNodeAddr(addr string) bool {
	if len(addr) != 9 || addr[0] != '!' {
		return false
	}
	for _, c := range addr[1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
