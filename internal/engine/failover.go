package engine

import (
	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
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
