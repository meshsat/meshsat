package hemb

// Bearer construction shared between boot-time TUN adapter init and
// runtime rebind (MESHSAT-647 auto-wire after P2P connect, future
// per-peer bond reshapes). The one place that decides:
//   * HealthScore for each bearer (default 100 — 0 was a bug that
//     made the bonder filter ALL bearers as offline → multi-path
//     send path never ran)
//   * SendFn routing: Reticulum-native interfaces (tcp_0, ble_peer_*,
//     mqtt_rns_0) go through the routing registry; gateway-backed
//     (mesh, aprs, iridium, cellular, zigbee, sms) go through the
//     dispatcher's HeMB frame forwarder.

import (
	"context"
	"fmt"
	"strings"
)

// ReticulumSender is satisfied by routing.InterfaceRegistry. Kept as
// an interface to avoid an import cycle (hemb → routing → hemb).
type ReticulumSender interface {
	Send(ifaceID string, packet []byte) error
}

// HeMBFrameForwarder is satisfied by *engine.Dispatcher's
// ForwardHeMBFrame method. Same reason.
type HeMBFrameForwarder interface {
	ForwardHeMBFrame(ifaceID string, data []byte) error
}

// BuildBearers translates bond_members + live interfaces into
// BearerProfile entries ready for NewBonder or TUNAdapter.Rebind.
// memberIfaceIDs is the ordered list of interface_id strings
// (typically from db.GetBondMembers(groupID) — keep the priority
// ordering).
func BuildBearers(memberIfaceIDs []string, forwarder HeMBFrameForwarder, rns ReticulumSender) []BearerProfile {
	bearers := make([]BearerProfile, 0, len(memberIfaceIDs))
	for i, ifaceID := range memberIfaceIDs {
		ifaceID := ifaceID // capture
		sendFn := chooseBearerSendFn(ifaceID, forwarder, rns)
		bearers = append(bearers, BearerProfile{
			Index:       uint8(i),
			InterfaceID: ifaceID,
			ChannelType: ifaceID,
			MTU:         237,
			HeaderMode:  HeaderModeCompact,
			// HealthScore > 0 is the gate bonder.sendMulti uses.
			// Defaulting to 100 means "healthy until proven
			// otherwise"; a future wiring of HealthScorer should
			// drive real telemetry-backed updates via Rebind().
			HealthScore: 100,
			SendFn:      sendFn,
		})
	}
	return bearers
}

// chooseBearerSendFn picks the right path for a given iface. Reticulum
// native ifaces have no gateway registration; gateway-backed ifaces
// do. Previously everything went through the dispatcher's forwarder
// which failed for tcp_0 / ble_peer_* with "no gateway for interface".
func chooseBearerSendFn(ifaceID string, forwarder HeMBFrameForwarder, rns ReticulumSender) func(context.Context, []byte) error {
	if isReticulumNativeIface(ifaceID) {
		return func(_ context.Context, data []byte) error {
			if rns == nil {
				return fmt.Errorf("hemb: no reticulum sender for %s", ifaceID)
			}
			return rns.Send(ifaceID, data)
		}
	}
	return func(_ context.Context, data []byte) error {
		if forwarder == nil {
			return fmt.Errorf("hemb: no gateway forwarder for %s", ifaceID)
		}
		return forwarder.ForwardHeMBFrame(ifaceID, data)
	}
}

// isReticulumNativeIface returns true for interface IDs that are
// registered in the Reticulum InterfaceRegistry but don't have a
// transport-layer Gateway (tcp_0, MQTT Reticulum, BLE peer links).
func isReticulumNativeIface(id string) bool {
	switch {
	case strings.HasPrefix(id, "tcp_"):
		return true
	case strings.HasPrefix(id, "ble_"):
		// Covers ble_0 (GATT peripheral) + ble_peer_N (GATT client
		// links created by MESHSAT-633 BLEPeerManager).
		return true
	case strings.HasPrefix(id, "mqtt_rns_"):
		return true
	}
	return false
}
