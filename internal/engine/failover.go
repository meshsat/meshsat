package engine

import (
	"strings"
	"sync"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// FailoverResolver resolves failover group IDs to the best available interface.
type FailoverResolver struct {
	db       *database.DB
	ifaceMgr *InterfaceManager

	// gate is the one decision about which links are not tried now (a
	// jammed band, a deaf receiver): main sets the same gate the delivery
	// intents read, so a group and an intent never disagree about a link.
	// Resolve and ResolveExcluding both read it; nil = none. gateMu guards
	// it, since main sets it after the dispatchers have started.
	// [MESHSAT-857, MESHSAT-1650]
	gateMu sync.RWMutex
	gate   RouteGate

	// gwOnline reports a member whose gateway is connected even though the
	// interface manager never binds it to a device. The APRS gateway (and
	// the other gateway-backed channels) show "unbound" there for their
	// whole life, which made a group [aprs_0, cellular_0] resolve to SMS
	// while APRS was perfectly healthy (found live 7 Sep 2026). [MESHSAT-857]
	gwOnline func(ifaceID string) bool
}

// SetGatewayOnline installs the gateway-connected check used beside the
// interface manager's state. [MESHSAT-857]
func (fr *FailoverResolver) SetGatewayOnline(fn func(ifaceID string) bool) {
	fr.gwOnline = fn
}

// online reports whether a member can carry traffic now: bound and online
// in the interface manager, or served by a connected gateway. Two channel
// types read otherwise [MESHSAT-1678]:
//   - cell: the interface manager takes cell_0 as device-less, online
//     whenever it is on (the v60 seed switches it on), but it carries
//     nothing unless the cell gateway runs; it reads its gateway alone, so a
//     kit with no cell configured has no phantom free link.
//   - sms: Reticulum's packet interface over the cellular modem (main.go
//     wraps cellular_0's transport), with no device of its own to bind; it
//     reads as its modem's link does (smsCarrier), so the two names of one
//     modem never disagree.
func (fr *FailoverResolver) online(st *InterfaceStatus) bool {
	switch st.ChannelType {
	case "cell":
		return fr.gatewayUp(st.ID)
	case "sms":
		return fr.carrierUp(smsCarrier(st.ID))
	}
	return st.State == StateOnline || fr.gatewayUp(st.ID)
}

// gatewayUp reports a connected gateway serving the link.
func (fr *FailoverResolver) gatewayUp(ifaceID string) bool {
	return fr.gwOnline != nil && fr.gwOnline(ifaceID)
}

// carrierUp reads the link that carries another (an sms link's cellular
// link) by its own gateway and state, never followed further.
func (fr *FailoverResolver) carrierUp(ifaceID string) bool {
	if fr.gatewayUp(ifaceID) {
		return true
	}
	if fr.ifaceMgr == nil {
		return false
	}
	st, err := fr.ifaceMgr.GetStatus(ifaceID)
	return err == nil && st.State == StateOnline
}

// smsCarrier names the cellular link whose modem an sms link rides: sms_N
// rides cellular_N, and an sms link with another name the first modem's.
func smsCarrier(smsID string) string {
	if n, ok := strings.CutPrefix(smsID, "sms_"); ok && n != "" {
		return "cellular_" + n
	}
	return "cellular_0"
}

// Online reports a link online as the failover groups see it (its interface
// state, or a connected gateway behind it, read as online reads it) and
// whether that is known: an interface the manager doesn't have, with no
// gateway connected, is unknown. The hop set reads it for known absence
// (R46-V4).
func (fr *FailoverResolver) Online(ifaceID string) (online, known bool) {
	if fr.gatewayUp(ifaceID) {
		return true, true
	}
	if fr.ifaceMgr == nil {
		return false, false
	}
	st, err := fr.ifaceMgr.GetStatus(ifaceID)
	if err != nil {
		return false, false
	}
	return fr.online(st), true
}

// NewFailoverResolver creates a resolver.
func NewFailoverResolver(db *database.DB, ifaceMgr *InterfaceManager) *FailoverResolver {
	return &FailoverResolver{db: db, ifaceMgr: ifaceMgr}
}

// SetRouteGate sets the gate that decides which members Resolve and
// ResolveExcluding skip: a member whose receiver is deaf (the APRS receive
// watchdog, the device health ladder), so a booth relay configured "APRS
// first, SMS when APRS is down" really moves to SMS while the APRS
// interface still reads online (MESHSAT-857), and a member whose band is
// jammed (MESHSAT-1650).
func (fr *FailoverResolver) SetRouteGate(g RouteGate) {
	fr.gateMu.Lock()
	fr.gate = g
	fr.gateMu.Unlock()
}

// excluded asks the gate about a member.
func (fr *FailoverResolver) excluded(ifaceID string) (bool, string) {
	fr.gateMu.RLock()
	g := fr.gate
	fr.gateMu.RUnlock()
	if g == nil {
		return false, ""
	}
	return g.RouteExcluded(ifaceID)
}

// Resolve takes a target ID which may be an interface ID or failover group ID.
// Returns the resolved interface ID. If it's a plain interface, returns it as-is.
// If it's a failover group, returns the highest-priority online member.
// Returns empty string if no member is available.
func (fr *FailoverResolver) Resolve(targetID string) string {
	// First check if it's a failover group
	group, err := fr.db.GetFailoverGroup(targetID)
	if err != nil {
		// Not a failover group — return as-is (it's a direct interface ID)
		return targetID
	}

	members, err := fr.db.GetFailoverMembers(group.ID)
	if err != nil || len(members) == 0 {
		log.Warn().Str("group", group.ID).Msg("failover: no members in group")
		return ""
	}

	// Members are ordered by priority ASC (lowest = highest priority).
	// A member whose receiver is deaf counts as unavailable even while its
	// interface state is online: an APRS chain that transmits but cannot
	// hear the peer would swallow every relay in silence. [MESHSAT-857]
	for _, m := range members {
		status, err := fr.ifaceMgr.GetStatus(m.InterfaceID)
		if err != nil {
			continue
		}
		if fr.online(status) {
			if ex, why := fr.excluded(m.InterfaceID); ex {
				log.Warn().Str("group", group.ID).Str("member", m.InterfaceID).Str("why", why).
					Int("priority", m.Priority).Msg("failover: member online but excluded, skipping")
				continue
			}
			log.Debug().Str("group", group.ID).Str("resolved", m.InterfaceID).
				Int("priority", m.Priority).Msg("failover: resolved to online member")
			return m.InterfaceID
		}
	}

	// No online member — fall back to first enabled member (deliveries will be held).
	// The same when every online member is excluded by the gate: a group never
	// answers nothing while a member is enabled, so the deliveries wait on it
	// rather than being dropped (a deliberate exception to the gate, kept in
	// slice (b)). [MESHSAT-1650]
	for _, m := range members {
		status, err := fr.ifaceMgr.GetStatus(m.InterfaceID)
		if err != nil {
			continue
		}
		if status.Enabled {
			log.Warn().Str("group", group.ID).Str("fallback", m.InterfaceID).
				Msg("failover: no online member, using first enabled (deliveries will be held)")
			return m.InterfaceID
		}
	}

	log.Warn().Str("group", group.ID).Msg("failover: no available member")
	return ""
}

// ResolveExcluding returns the first member of a failover group, in priority
// order, that is not exclude and can carry traffic now: online and not deaf. It
// moves a message off a member that just failed to deliver it, so unlike
// Resolve it never falls back to a member that would only hold the delivery. A
// target that is not a failover group resolves to "". [MESHSAT-1021]
func (fr *FailoverResolver) ResolveExcluding(targetID, exclude string) string {
	group, err := fr.db.GetFailoverGroup(targetID)
	if err != nil {
		return ""
	}
	members, err := fr.db.GetFailoverMembers(group.ID)
	if err != nil {
		return ""
	}
	for _, m := range members {
		if m.InterfaceID == exclude {
			continue
		}
		status, err := fr.ifaceMgr.GetStatus(m.InterfaceID)
		if err != nil {
			continue
		}
		if !fr.online(status) {
			continue
		}
		if ex, _ := fr.excluded(m.InterfaceID); ex {
			continue
		}
		return m.InterfaceID
	}
	return ""
}
