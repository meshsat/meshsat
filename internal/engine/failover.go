package engine

import (
	"context"
	"sort"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/hemb"
	"meshsat/internal/reticulum"
)

// FailoverResolver resolves failover group IDs to the best available interface.
type FailoverResolver struct {
	db       *database.DB
	ifaceMgr *InterfaceManager
}

// NewFailoverResolver creates a resolver.
func NewFailoverResolver(db *database.DB, ifaceMgr *InterfaceManager) *FailoverResolver {
	return &FailoverResolver{db: db, ifaceMgr: ifaceMgr}
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

	// Members are ordered by priority ASC (lowest = highest priority)
	for _, m := range members {
		status, err := fr.ifaceMgr.GetStatus(m.InterfaceID)
		if err != nil {
			continue
		}
		if status.State == StateOnline {
			log.Debug().Str("group", group.ID).Str("resolved", m.InterfaceID).
				Int("priority", m.Priority).Msg("failover: resolved to online member")
			return m.InterfaceID
		}
	}

	// No online member — fall back to first enabled member (deliveries will be held)
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

// SelectBearers returns all online members of a bond group as HeMB BearerProfiles.
// Returns nil if the ID is not a bond group.
func (fr *FailoverResolver) SelectBearers(groupID string, sendFnProvider func(ifaceID string) func(ctx context.Context, data []byte) error) []hemb.BearerProfile {
	members, err := fr.db.GetBondMembers(groupID)
	if err != nil || len(members) == 0 {
		return nil
	}

	var bearers []hemb.BearerProfile
	for i, m := range members {
		status, err := fr.ifaceMgr.GetStatus(m.InterfaceID)
		if err != nil || status.State != StateOnline {
			continue
		}

		channelType := m.InterfaceID
		if idx := len(m.InterfaceID) - 1; idx > 0 {
			// Strip trailing _N to get channel type (e.g. "mesh_0" -> "mesh")
			for j := idx; j >= 0; j-- {
				if m.InterfaceID[j] == '_' {
					channelType = m.InterfaceID[:j]
					break
				}
			}
		}

		cost := reticulum.InterfaceCost(reticulum.InterfaceType(channelType))

		headerMode := hemb.HeaderModeCompact
		if channelType == "ipougrs" {
			headerMode = hemb.HeaderModeImplicit
		} else if channelType == "tcp" || channelType == "mqtt" || channelType == "webhook" {
			headerMode = hemb.HeaderModeExtended
		}

		// Default MTU from channel type.
		mtu := 237 // mesh default
		switch channelType {
		case "mesh":
			mtu = 237
		case "iridium":
			mtu = 340
		case "iridium_imt":
			mtu = 102400
		case "astrocast":
			mtu = 160
		case "cellular", "sms":
			mtu = 160
		case "zigbee":
			mtu = 100
		case "aprs":
			mtu = 256
		case "tcp", "mqtt", "webhook":
			mtu = 65535
		case "ipougrs":
			mtu = 1
		}

		sendFn := sendFnProvider(m.InterfaceID)
		if sendFn == nil {
			continue
		}

		bearers = append(bearers, hemb.BearerProfile{
			Index:        uint8(i),
			InterfaceID:  m.InterfaceID,
			ChannelType:  channelType,
			MTU:          mtu,
			CostPerMsg:   cost,
			LossRate:     0.10, // default; will be refined by health scorer
			LatencyMs:    250,  // default
			HealthScore:  80,   // default
			SendFn:       sendFn,
			RelayCapable: channelType != "ipougrs",
			HeaderMode:   headerMode,
		})
	}

	// Sort: free first (by MTU DESC), then paid (by cost ASC).
	sort.Slice(bearers, func(i, j int) bool {
		fi, fj := bearers[i].IsFree(), bearers[j].IsFree()
		if fi != fj {
			return fi // free before paid
		}
		if fi {
			return bearers[i].MTU > bearers[j].MTU // bigger free bearers first
		}
		return bearers[i].CostPerMsg < bearers[j].CostPerMsg // cheaper paid first
	})

	return bearers
}
