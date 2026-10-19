package engine

import (
	"encoding/binary"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/gateway"
	"meshsat/internal/hubmsg"
	"meshsat/internal/rules"
)

// The Hub app route on the intent path (hub_msg, HeMB-NG stage 2, contract C4): a contact's
// MQTT address "bridge:<id>" is a free route through the Hub on interface hub_0, carried by
// the adapter main.go sets (internal/hubroute). The broker's PUBACK is accepted, not
// confirmed; the recipient's app storing it (recipient_app_stored, through the Hub) completes
// it as delivered_app. [MESHSAT-1584]

const (
	modeHub           = "hub"
	hubRouteInterface = "hub_0"
)

// hubRouteRef is the Hub app route's adapter, shared by the dispatcher and its workers and
// set once main.go has a Hub session.
type hubRouteRef struct {
	v atomic.Pointer[gateway.IntentAdapter]
}

func (h *hubRouteRef) get() gateway.IntentAdapter {
	if h == nil {
		return nil
	}
	if p := h.v.Load(); p != nil {
		return *p
	}
	return nil
}

// SetHubRoute installs the Hub app route's adapter (nil removes it).
func (d *Dispatcher) SetHubRoute(ad gateway.IntentAdapter) {
	if d.hubRoute == nil {
		d.hubRoute = &hubRouteRef{} // a dispatcher not made by NewDispatcher (tests)
	}
	if ad == nil {
		d.hubRoute.v.Store(nil)
		return
	}
	d.hubRoute.v.Store(&ad)
}

// HubHandoff takes a message the Hub route stored to the ingress rules of hub_0 (A10). The
// work it makes is listed under the stored message (DispatchAccessKeyed), so a hand-off
// tried again after a failed write or a restart makes only what is missing; it returns an
// error while any of that work could not be made, and the route then tries it again. No
// rule matching is a hand-off done. [MESHSAT-1584]
func (d *Dispatcher) HubHandoff(h database.HubHandoff) error {
	res := d.DispatchAccessKeyed(hubRouteInterface, rules.RouteMessage{Text: h.Text, From: h.From, PortNum: 1, Plain: true},
		[]byte(h.Text), database.HubHandoffOrigin(h.MessageID))
	if res.Failed > 0 {
		return fmt.Errorf("%d piece(s) of work not made (%d made now, %d before)", res.Failed, res.Queued, res.Existing)
	}
	return nil
}

// intentCreatedMs is when the intent was made, from its UUIDv7 id.
func intentCreatedMs(in database.DeliveryIntent) int64 {
	if u, err := uuid.Parse(in.ID); err == nil && u.Version() == 7 {
		var b [8]byte
		copy(b[2:], u[0:6])
		return int64(binary.BigEndian.Uint64(b[:])) // #nosec G115 -- 48 bits
	}
	return int64(intentTimestamp(in) * 1000)
}

// messageDeadline is the message's deadline on the wire (mid_expires_at): its creation plus
// its expiry allowance, the same for every operation of the message, so every operation
// carries the same content digest (C1d).
func messageDeadline(in database.DeliveryIntent) time.Time {
	return time.UnixMilli(intentCreatedMs(in) + int64(in.ExpiryAllowS)*1000)
}

// admitHub admits a "bridge:<id>" address for the Hub app route, with its own allowances
// (C4, your Q2 answer), each capped by the intent's lifetime.
func (e *Escalator) admitHub(p *database.RoutePlanEntry, payload []byte) {
	p.AdapterMode = modeHub
	p.CompletionRule = database.OutcomeDeliveredApp
	if hubmsg.BridgeOf(p.Destination) == "" {
		p.Reason = reasonBadAddress
		return
	}
	if e.cfg.HubQueueAllowS > 0 {
		p.QueueAllowS = min(e.cfg.HubQueueAllowS, e.cfg.ExpiryAllowS)
	}
	if e.cfg.HubReceiptAllowS > 0 {
		p.ReceiptAllowS = min(e.cfg.HubReceiptAllowS, e.cfg.ExpiryAllowS)
	}
	p.InterfaceID = hubRouteInterface
	iface, err := e.d.db.GetInterface(hubRouteInterface)
	if err != nil || !iface.Enabled {
		p.Reason = reasonOffline
		return
	}
	ad := e.d.intentAdapter(hubRouteInterface)
	if ad == nil {
		p.Reason = reasonNotEnabled
		return
	}
	wire, chainSHA, err := e.wireBytes(iface.EgressTransforms, payload)
	if err != nil {
		p.Reason = reasonEncoding
		return
	}
	p.EstWireBytes, p.TransformSHA256 = len(wire), chainSHA
	if reason, _ := ad.IntentAdmit(p.Destination, wire, ChainEncrypts(iface.EgressTransforms)); reason != "" {
		p.Reason = reason
		return
	}
	p.Admitted = true
}

// hubFirst moves the admitted Hub app routes before the local order (your Q2 answer: a
// connected, admitted Hub app route plans first by default), keeping the order within each.
func hubFirst(plan []database.RoutePlanEntry) []database.RoutePlanEntry {
	out := make([]database.RoutePlanEntry, 0, len(plan))
	for _, p := range plan {
		if p.Admitted && p.AdapterMode == modeHub {
			out = append(out, p)
		}
	}
	for _, p := range plan {
		if !(p.Admitted && p.AdapterMode == modeHub) {
			out = append(out, p)
		}
	}
	return out
}

// isHubAddress is a contact address the Hub app route carries.
func isHubAddress(a directory.Address) bool {
	return a.Kind == directory.KindMQTT && hubmsg.BridgeOf(a.Value) != ""
}
