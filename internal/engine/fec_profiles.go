package engine

// FEC auto-tuned profiles per channel type. Each profile specifies Reed-Solomon
// parameters and optional byte interleaving optimized for that channel's error
// characteristics.
//
// Profiles are selected via the "profile" param in the FEC transform spec:
//   {"type": "fec", "params": {"profile": "lora"}}
//   {"type": "fec", "params": {"profile": "auto", "channel": "mesh"}}
//
// Manual override is still supported:
//   {"type": "fec", "params": {"data_shards": "4", "parity_shards": "2"}}

// FECProfile defines Reed-Solomon parameters and interleaving for a channel type.
type FECProfile struct {
	DataShards      int  // k — number of data shards
	ParityShards    int  // m — number of parity shards
	Interleave      bool // enable byte interleaving for burst error protection
	InterleaveDepth int  // interleave matrix depth (rows), 0 if disabled
}

// fecProfiles maps channel type identifiers to their tuned FEC profiles.
var fecProfiles = map[string]FECProfile{
	"lora": {
		DataShards:      4,
		ParityShards:    2,    // 33% redundancy — handles LoRa packet loss
		Interleave:      true, // burst error protection for fading
		InterleaveDepth: 8,    // 8-symbol interleave depth
	},
	"mesh": { // alias for lora (Meshtastic radios)
		DataShards:      4,
		ParityShards:    2,
		Interleave:      true,
		InterleaveDepth: 8,
	},
	"sbd": {
		DataShards:   6,
		ParityShards: 2,     // 25% redundancy — SBD is reliable but expensive
		Interleave:   false, // SBD is packet-level, no burst errors
	},
	"iridium": { // alias for sbd
		DataShards:   6,
		ParityShards: 2,
		Interleave:   false,
	},
	"imt": {
		DataShards:   8,
		ParityShards: 2, // 20% redundancy — IMT has large MTU, lower error rate
		Interleave:   false,
	},
	"iridium_imt": { // alias for imt
		DataShards:   8,
		ParityShards: 2,
		Interleave:   false,
	},
	"astrocast": {
		DataShards:      3,
		ParityShards:    2, // 40% redundancy — constrained uplink, maximize success
		Interleave:      true,
		InterleaveDepth: 4,
	},
	"ax25": {
		DataShards:      4,
		ParityShards:    3, // 43% redundancy — AX.25/HF is very lossy
		Interleave:      true,
		InterleaveDepth: 16,
	},
	"tcp": {
		DataShards:   0, // No FEC — TCP has its own error correction
		ParityShards: 0,
	},
	"mqtt": {
		DataShards:   0, // No FEC — MQTT runs over TCP
		ParityShards: 0,
	},
	"webhook": {
		DataShards:   0, // No FEC — HTTP runs over TCP
		ParityShards: 0,
	},
	"cellular": {
		DataShards:   6,
		ParityShards: 2, // 25% — similar to SBD
		Interleave:   false,
	},
	"sms": {
		DataShards:   4,
		ParityShards: 2, // 33% — SMS can lose segments
		Interleave:   false,
	},
	"zigbee": {
		DataShards:      4,
		ParityShards:    2, // 33% — short-range but can have interference
		Interleave:      true,
		InterleaveDepth: 4,
	},
}

// LookupFECProfile returns the FEC profile for a channel type.
// Returns the profile and true if found, or a zero profile and false if not.
func LookupFECProfile(channelType string) (FECProfile, bool) {
	p, ok := fecProfiles[channelType]
	return p, ok
}

// resolveFECParams resolves the FEC parameters from a transform spec's params.
// Priority: explicit data_shards/parity_shards > named profile > defaults.
// Returns dataShards, parityShards, interleave, interleaveDepth.
func resolveFECParams(params map[string]string) (int, int, bool, int) {
	// Check for named profile first.
	profile := params["profile"]

	if profile == "auto" {
		// Auto mode: resolve from channel type param.
		channel := params["channel"]
		if p, ok := fecProfiles[channel]; ok {
			if p.DataShards == 0 {
				// Profile says no FEC for this channel type.
				return 0, 0, false, 0
			}
			return p.DataShards, p.ParityShards, p.Interleave, p.InterleaveDepth
		}
		// Unknown channel, fall through to defaults.
	} else if profile != "" {
		// Named profile (e.g. "lora", "sbd").
		if p, ok := fecProfiles[profile]; ok {
			if p.DataShards == 0 {
				return 0, 0, false, 0
			}
			return p.DataShards, p.ParityShards, p.Interleave, p.InterleaveDepth
		}
		// Unknown profile name, fall through to defaults.
	}

	// Explicit params override everything.
	ds := parseIntParam(params, "data_shards", 0)
	ps := parseIntParam(params, "parity_shards", 0)
	if ds > 0 && ps > 0 {
		il := params["interleave"] == "true"
		ild := parseIntParam(params, "interleave_depth", 8)
		if !il {
			ild = 0
		}
		return ds, ps, il, ild
	}

	// Default fallback (original behavior).
	if ds == 0 {
		ds = 4
	}
	if ps == 0 {
		ps = 2
	}
	return ds, ps, false, 0
}

// AdaptFECProfile adjusts a profile's parity based on a health score (0-100).
// Lower health = more parity shards for extra protection on degraded channels.
func AdaptFECProfile(p FECProfile, healthScore int) FECProfile {
	if p.DataShards == 0 || p.ParityShards == 0 {
		return p // no FEC to adapt
	}
	adapted := p
	switch {
	case healthScore > 80:
		// Healthy — use base profile as-is.
	case healthScore > 50:
		// Degraded — add 1 extra parity shard.
		adapted.ParityShards++
	default:
		// Poor health — add 2 extra parity shards.
		adapted.ParityShards += 2
	}
	// Cap total shards at 255 (Reed-Solomon limit).
	if adapted.DataShards+adapted.ParityShards > 255 {
		adapted.ParityShards = 255 - adapted.DataShards
	}
	return adapted
}
