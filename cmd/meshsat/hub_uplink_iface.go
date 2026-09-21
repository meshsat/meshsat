package main

import "time"

// hubUplinkSatInterfaces are the satellite interfaces a Hub uplink frame may
// leave on, newest modem first.
var hubUplinkSatInterfaces = []string{"iridium_imt_0", "iridium_0"}

// hubUplinkSatInterface picks the satellite interface for a Hub uplink frame
// and says whether it is worth using right now: connected, and it has moved
// traffic in the last 30 minutes (indoors it has not, and a frame queued on it
// would never leave, so "auto" falls to SMS). The interface is returned even
// when it is not usable, so a forced "satellite" policy queues onto the modem
// the kit really has. With no satellite gateway at all it names the first
// candidate and the delivery fails loudly in the ledger. [MESHSAT-963]
func hubUplinkSatInterface(status func(id string) (connected bool, lastActivity time.Time, exists bool), now time.Time) (string, bool) {
	first := ""
	for _, id := range hubUplinkSatInterfaces {
		connected, last, exists := status(id)
		if !exists {
			continue
		}
		if first == "" {
			first = id
		}
		if connected && !last.IsZero() && now.Sub(last) < 30*time.Minute {
			return id, true
		}
	}
	if first == "" {
		first = hubUplinkSatInterfaces[0]
	}
	return first, false
}
