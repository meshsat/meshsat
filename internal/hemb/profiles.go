package hemb

import "math"

// Per-bearer RLNC redundancy factors. These determine how many extra coded
// symbols are generated for each bearer to tolerate its expected loss rate.
// Paid bearers are capped at minimal redundancy to control cost.
var defaultRedundancy = map[string]float64{
	"mesh":        1.30, // LoRa: bursty fading, 10-40% loss
	"iridium_sbd": 1.00, // SBD: reliable but expensive
	"iridium_imt": 1.00, // IMT: reliable, large MTU
	"astrocast":   1.40, // LEO uplink: high loss, constrained MTU
	"cellular":    1.10, // SMS: moderate reliability
	"sms":         1.10, // alias
	"zigbee":      1.30, // short-range ISM: interference
	"aprs":        1.30, // AX.25: very lossy shared channel
	"ipougrs":     2.00, // GSM ring: extreme loss (~30%), micro-MTU
	"tcp":         1.00, // reliable transport
	"mqtt":        1.00, // reliable transport
	"webhook":     1.00, // reliable transport
}

// SelectRedundancy computes the RLNC redundancy factor for a bearer set.
// Cost is a PRIMARY INPUT — paid bearers get minimal redundancy.
// Returns R >= 1.0 where N = ceil(K * R).
func SelectRedundancy(bearers []BearerProfile, priority int) float64 {
	if len(bearers) == 0 {
		return 1.0
	}

	// Weighted average loss rate across bearers.
	var totalWeight, weightedLoss float64
	for _, b := range bearers {
		w := float64(b.MTU) // weight by capacity contribution
		totalWeight += w
		weightedLoss += w * b.LossRate
	}
	avgLoss := weightedLoss / totalWeight

	// Base redundancy from average loss.
	var baseR float64
	switch {
	case avgLoss < 0.05:
		baseR = 1.10
	case avgLoss < 0.15:
		baseR = 1.25
	case avgLoss < 0.30:
		baseR = 1.40
	default:
		baseR = 1.60
	}

	// Priority boost: P0 (critical) gets more redundancy.
	switch priority {
	case 0:
		baseR *= 1.30
	case 1:
		baseR *= 1.10
	}

	// Cost dampening: if most bearers are paid, reduce R to save money.
	paidCount := 0
	for _, b := range bearers {
		if !b.IsFree() {
			paidCount++
		}
	}
	if paidFrac := float64(paidCount) / float64(len(bearers)); paidFrac > 0.5 {
		baseR = math.Max(baseR*0.85, 1.05)
	}

	return math.Min(baseR, 2.0) // cap at 2x
}

// BearerRedundancy returns the per-bearer RLNC redundancy factor.
// Paid bearers are capped at 1 repair symbol equivalent (minimal overhead).
func BearerRedundancy(b *BearerProfile) float64 {
	if r, ok := defaultRedundancy[b.ChannelType]; ok {
		if !b.IsFree() {
			// Paid bearers: cap redundancy — generate at most 1 extra symbol.
			return math.Min(r, 1.10)
		}
		return r
	}
	if b.IsFree() {
		return 1.30
	}
	return 1.05
}

// RepairSymbols calculates how many repair (extra) symbols to add for a bearer,
// given its base allocation of sourceCount symbols.
func RepairSymbols(b *BearerProfile, sourceCount int) int {
	if sourceCount == 0 {
		return 0
	}
	repair := int(math.Ceil(float64(sourceCount) * b.LossRate * 1.5))
	if !b.IsFree() {
		// Paid bearers: cap at 1 repair symbol to minimize cost.
		if repair > 1 {
			repair = 1
		}
	}
	return repair
}
