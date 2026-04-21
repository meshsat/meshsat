package hemb

// Bearer construction shared between boot-time TUN adapter init and
// runtime rebind (MESHSAT-647 auto-wire after P2P connect, future
// per-peer bond reshapes). The one place that decides:
//   * HealthScore for each bearer (default 100 — 0 was a bug that
//     made the bonder filter ALL bearers as offline → multi-path
//     send path never ran)
//   * SendFn routing: all bearers go through the Reticulum
//     InterfaceRegistry first (which handles mesh_0 / ax25_0 /
//     tcp_0 / iridium_* / ble_* / mqtt_rns_0 / sms_0 / zigbee_0
//     via the same PRIVATE_APP / raw-packet path that produces
//     frames our peer recognises as HeMB on receive). The gateway
//     forwarder is kept as a fallback only for interfaces not
//     registered in ifaceReg — in practice none of the current set.
//
// Why not gateway.Forward? It wraps the frame in a MeshMessage and
// lets each gateway's worker re-encode the payload (APRS text frame,
// etc.), which destroys the HeMB magic bytes. ifaceReg.Send passes
// raw bytes straight to the wire-level send func (PRIVATE_APP for
// mesh, AX.25 UI-frame info for ax25, HDLC for tcp), preserving the
// HeMB header so the receive side's `hemb.IsHeMBFrame` check fires.

import (
	"context"
	"fmt"
)

// ReticulumSender is satisfied by routing.InterfaceRegistry. Kept as
// an interface to avoid an import cycle (hemb → routing → hemb).
type ReticulumSender interface {
	Send(ifaceID string, packet []byte) error
}

// ReticulumMTULookup is an optional extension — if the rns sender
// also exposes per-interface MTU we use it to size each bearer to
// its real wire limit (mesh_0=230, ax25_0=256, tcp_0=65535, …).
// Without it we fall back to defaultBearerMTU, which is the floor
// that fits every transport we currently wire up.
type ReticulumMTULookup interface {
	GetMTU(ifaceID string) int
}

// HeMBFrameForwarder is satisfied by *engine.Dispatcher's
// ForwardHeMBFrame method. Same reason.
type HeMBFrameForwarder interface {
	ForwardHeMBFrame(ifaceID string, data []byte) error
}

// defaultBearerMTU is the conservative floor: mesh_0's PRIVATE_APP
// payload limit is 230 bytes, and the RNS ReticulumInterface.Send
// rejects anything over its declared MTU. Going over this would
// silently drop HeMB symbols on the narrowest bearer. Per-iface
// lookup via ReticulumMTULookup overrides this when available.
const defaultBearerMTU = 230

// BuildBearers translates bond_members + live interfaces into
// BearerProfile entries ready for NewBonder or TUNAdapter.Rebind.
// memberIfaceIDs is the ordered list of interface_id strings
// (typically from db.GetBondMembers(groupID) — keep the priority
// ordering).
func BuildBearers(memberIfaceIDs []string, forwarder HeMBFrameForwarder, rns ReticulumSender) []BearerProfile {
	var mtuLookup ReticulumMTULookup
	if l, ok := rns.(ReticulumMTULookup); ok {
		mtuLookup = l
	}
	bearers := make([]BearerProfile, 0, len(memberIfaceIDs))
	for i, ifaceID := range memberIfaceIDs {
		ifaceID := ifaceID // capture
		sendFn := chooseBearerSendFn(ifaceID, forwarder, rns)
		mtu := defaultBearerMTU
		if mtuLookup != nil {
			if m := mtuLookup.GetMTU(ifaceID); m > 0 {
				mtu = m
			}
		}
		bearers = append(bearers, BearerProfile{
			Index:       uint8(i),
			InterfaceID: ifaceID,
			ChannelType: ifaceID,
			MTU:         mtu,
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

// chooseBearerSendFn prefers the Reticulum InterfaceRegistry for every
// bearer and only falls back to the gateway forwarder if the iface
// isn't registered there. Previously the split was prefix-based (only
// tcp_/ble_/mqtt_rns_ went through RNS), which silently dropped HeMB
// frames on mesh_0 / ax25_0 because their `gateway.Forward` paths
// re-encoded the MeshMessage payload and destroyed the HeMB header.
func chooseBearerSendFn(ifaceID string, forwarder HeMBFrameForwarder, rns ReticulumSender) func(context.Context, []byte) error {
	return func(_ context.Context, data []byte) error {
		if rns != nil {
			if err := rns.Send(ifaceID, data); err == nil {
				return nil
			} else if !isUnknownInterfaceErr(err) {
				return err
			}
		}
		if forwarder != nil {
			return forwarder.ForwardHeMBFrame(ifaceID, data)
		}
		return fmt.Errorf("hemb: no sender for %s", ifaceID)
	}
}

// isUnknownInterfaceErr returns true if rns.Send reported the iface
// isn't registered — the only case where we want to fall through to
// the gateway forwarder. Any other error (offline, write failure,
// MTU exceeded) is real and should propagate.
func isUnknownInterfaceErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return len(msg) >= 18 && msg[:18] == "unknown interface:"
}

// isReticulumNativeIface is retained for tests that assert the legacy
// prefix classification. Runtime send-path no longer uses it.
func isReticulumNativeIface(id string) bool {
	switch {
	case len(id) >= 4 && id[:4] == "tcp_":
		return true
	case len(id) >= 4 && id[:4] == "ble_":
		return true
	case len(id) >= 9 && id[:9] == "mqtt_rns_":
		return true
	}
	return false
}
