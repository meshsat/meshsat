package engine

import (
	"context"
	"os"
	"sort"
	"strconv"
	"sync"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/hemb"
	"meshsat/internal/reticulum"
)

// meshBearerMTU returns the mesh-bearer MTU for HeMB bond symbols.
// Defaults to 100 B — the empirically-verified SF7-LongFast ceiling
// below which Meshtastic reliably delivers PRIVATE_APP frames on
// the tesseract + parallax field kits. Operators on different
// LoRa presets (SHORT_FAST, MEDIUM_*, LONG_SLOW) or different
// antennas can override with `MESHSAT_MESH_MTU` in the range
// [1, 237]. Values outside the range fall back to 100. [MESHSAT-672]
func meshBearerMTU() int {
	if v := os.Getenv("MESHSAT_MESH_MTU"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 237 {
			return n
		}
		log.Warn().Str("MESHSAT_MESH_MTU", v).
			Msg("hemb: invalid MESHSAT_MESH_MTU — expected integer in 1..237, using default 100")
	}
	return 100
}

// FailoverResolver resolves failover group IDs to the best available interface.
type FailoverResolver struct {
	db       *database.DB
	ifaceMgr *InterfaceManager

	faultMu    sync.RWMutex
	faultedIDs map[string]bool // interface IDs with injected faults
}

// NewFailoverResolver creates a resolver.
func NewFailoverResolver(db *database.DB, ifaceMgr *InterfaceManager) *FailoverResolver {
	return &FailoverResolver{db: db, ifaceMgr: ifaceMgr, faultedIDs: make(map[string]bool)}
}

// InjectFault marks an interface as faulted. SelectBearers will skip it.
func (fr *FailoverResolver) InjectFault(ifaceID string) {
	fr.faultMu.Lock()
	defer fr.faultMu.Unlock()
	fr.faultedIDs[ifaceID] = true
	log.Warn().Str("interface", ifaceID).Msg("fault-inject: bearer faulted")
}

// ClearFault removes a fault injection from an interface.
func (fr *FailoverResolver) ClearFault(ifaceID string) {
	fr.faultMu.Lock()
	defer fr.faultMu.Unlock()
	delete(fr.faultedIDs, ifaceID)
	log.Info().Str("interface", ifaceID).Msg("fault-inject: bearer restored")
}

// FaultedInterfaces returns the set of currently faulted interface IDs.
func (fr *FailoverResolver) FaultedInterfaces() []string {
	fr.faultMu.RLock()
	defer fr.faultMu.RUnlock()
	out := make([]string, 0, len(fr.faultedIDs))
	for id := range fr.faultedIDs {
		out = append(out, id)
	}
	return out
}

// isFaulted returns true if the given interface has an injected fault.
func (fr *FailoverResolver) isFaulted(ifaceID string) bool {
	fr.faultMu.RLock()
	defer fr.faultMu.RUnlock()
	return fr.faultedIDs[ifaceID]
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
		// Skip bearers with injected faults (field testing).
		if fr.isFaulted(m.InterfaceID) {
			continue
		}
		// Check InterfaceManager state if available, but don't skip if the
		// interface isn't registered (mesh transport, Reticulum interfaces).
		// The sendFnProvider is the authoritative check — if it returns a
		// valid send function, the bearer is usable.
		if fr.ifaceMgr != nil {
			if status, err := fr.ifaceMgr.GetStatus(m.InterfaceID); err == nil {
				if status.State == StateError {
					continue // definitively broken or disabled — skip
				}
			}
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
		//
		// The mesh value must stay under Meshtastic's effective on-air
		// payload limit. SF7 LongFast nominally allows ~230–237 B of
		// Data.payload, but the radio firmware silently drops larger
		// packets depending on the preset, region, and antenna setup
		// — we've measured a 100 B practical ceiling on the field
		// kits. Operators with different radio configurations can
		// tune via `MESHSAT_MESH_MTU` env var (1-237); the default
		// stays conservative so out-of-the-box bonded sends work.
		// [MESHSAT-672]
		mtu := 100
		switch channelType {
		case "mesh":
			mtu = meshBearerMTU()
		case "iridium":
			mtu = 340
		case "iridium_imt":
			mtu = 102400
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

	// Log the resolved bearer set so the HeMB allocator's input is
	// visible when operators are diagnosing "why didn't X bearer
	// carry any symbol?" questions. [MESHSAT-672]
	if len(bearers) > 0 {
		ids := make([]string, 0, len(bearers))
		mtus := make([]int, 0, len(bearers))
		for _, br := range bearers {
			ids = append(ids, br.InterfaceID)
			mtus = append(mtus, br.MTU)
		}
		log.Debug().Str("group", groupID).
			Strs("bearers", ids).
			Ints("mtus", mtus).
			Int("count", len(bearers)).
			Msg("hemb: SelectBearers resolved bearer set")
	}

	return bearers
}
