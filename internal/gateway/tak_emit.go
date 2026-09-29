package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// The TAK gateway's outputs and its own events, after MeshSat Android's
// TakIntegration: one emit for every CoT, each output behind its own switch,
// and the Bridge's own PLI, SOS, dead man and chat under its own identity.
// [MESHSAT-1421]

// An output that is switched on but has no link right now: nothing was tried,
// so emit does not count it as an error and SendCotEvent does not report it.
var (
	errTAKServerDown = errors.New("tak server: not connected")
	errTAKHubDown    = errors.New("tak hub: no Hub session")
	errTAKStopped    = errors.New("tak: gateway stopped")
)

// ErrTAKNoPosition is SendOwnSOS's answer when this Bridge knows no position of
// its own: nothing was sent, as on Android without a location.
var ErrTAKNoPosition = errors.New("tak: no position of this Bridge, nothing sent")

// TAKHub is where MQTT Export (hub_export) goes: the Hub reporter,
// hubreporter.(*HubReporter), which publishes on
// meshsat/<bridge id>/tak/cot/out.
type TAKHub interface {
	IsConnected() bool
	PublishTAKCoT(xml []byte) error
}

// TAKHooks connect the TAK gateway to the rest of the Bridge. Each is
// optional.
type TAKHooks struct {
	// SelfPosition is this Bridge's own position, for its PLI and its SOS.
	// ok false means none is known, and then neither goes out.
	SelfPosition func() (lat, lon, altM float64, ok bool)
	// NodeID is the local mesh node's id as hex without "!" ("aabbccdd"), or
	// "" when there is none. It makes the own uid and callsign.
	NodeID func() string
	// Hub takes MQTT Export. Leave it nil without a Hub reporter; never set
	// it to a nil *hubreporter.HubReporter.
	Hub TAKHub
}

// SetHooks connects the gateway to the Bridge's own position, node id and
// Hub link. It may be called while the gateway runs.
func (g *TAKGateway) SetHooks(h TAKHooks) {
	g.hooks.Store(&h)
}

func (g *TAKGateway) hooksNow() TAKHooks {
	if h := g.hooks.Load(); h != nil {
		return *h
	}
	return TAKHooks{}
}

// OwnTAKCallsign is this Bridge's own callsign, MeshSat Android's
// CotBuilder.callsign: the prefix ("MESHSAT" when empty), a dash and the last
// four characters of the node id in upper case, or the prefix alone when the
// node id is empty. Routed mesh nodes keep their own "PREFIX-%04x".
func OwnTAKCallsign(nodeID, prefix string) string {
	if prefix == "" {
		prefix = "MESHSAT"
	}
	suffix := []rune(nodeID)
	if len(suffix) > 4 {
		suffix = suffix[len(suffix)-4:]
	}
	if len(suffix) == 0 {
		return prefix
	}
	return prefix + "-" + strings.ToUpper(string(suffix))
}

// OwnTAKUID is this Bridge's own CoT uid, Android's "MESHSAT-<device id>".
func OwnTAKUID(nodeID string) string {
	return "MESHSAT-" + nodeID
}

// own is this Bridge's uid and callsign now: the node id can appear after the
// gateway started.
func (g *TAKGateway) own() (uid, callsign string) {
	id := ""
	if f := g.hooksNow().NodeID; f != nil {
		id = f()
	}
	return OwnTAKUID(id), OwnTAKCallsign(id, g.config.CallsignPrefix)
}

// hasOutput reports whether any output is switched on.
func (g *TAKGateway) hasOutput() bool {
	return g.config.Host != "" || g.config.Multicast || g.config.HubExport
}

// emit sends one CoT event to every output the config switches on: the TAK
// server when there is a host, TAK SA multicast when multicast is on, the Hub
// when hub_export is on. The event counts out once when any output took it; an
// output that tried and failed counts an error, one without a link does not.
// The error joins what each output that did not take it said.
func (g *TAKGateway) emit(ev CotEvent) error {
	if !g.running.Load() {
		return errTAKStopped
	}
	var errs []error
	delivered := false
	send := func(out func(CotEvent) error) {
		err := out(ev)
		if err == nil {
			delivered = true
			return
		}
		if !isTAKLinkDown(err) {
			g.errors.Add(1)
		}
		errs = append(errs, err)
	}
	if g.config.Host != "" {
		send(g.sendToServer)
	}
	if g.config.Multicast {
		send(g.sendMulticast)
	}
	if g.config.HubExport {
		send(g.sendToHub)
	}
	if delivered {
		g.msgsOut.Add(1)
		g.lastActive.Store(time.Now().Unix())
		log.Debug().Str("uid", ev.UID).Str("type", ev.Type).Msg("tak: sent CoT event")
		// Publish to CoT event stream for dashboard
		GlobalTakEventBus.Publish(CotEventToRecord(&ev, "outbound"))
	}
	return errors.Join(errs...)
}

// isTAKLinkDown reports whether err says an output had no link.
func isTAKLinkDown(err error) bool {
	return errors.Is(err, errTAKServerDown) || errors.Is(err, errTAKMulticastDown) ||
		errors.Is(err, errTAKHubDown) || errors.Is(err, errTAKStopped)
}

// withoutLinkDown keeps, of an emit error, only the outputs that tried and
// failed: nil when the rest had no link.
func withoutLinkDown(err error) error {
	if err == nil {
		return nil
	}
	parts := []error{err}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		parts = joined.Unwrap()
	}
	var kept []error
	for _, p := range parts {
		if !isTAKLinkDown(p) {
			kept = append(kept, p)
		}
	}
	return errors.Join(kept...)
}

// sendToServer writes ev to the TAK server connection.
func (g *TAKGateway) sendToServer(ev CotEvent) error {
	conn := g.serverConn()
	if conn == nil || !g.connected.Load() {
		return errTAKServerDown
	}
	out, err := g.serverFrame(ev)
	if err != nil {
		return fmt.Errorf("tak server: %w", err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		log.Warn().Err(err).Msg("tak: set write deadline")
	}
	if _, err := conn.Write(out); err != nil {
		g.connected.Store(false)
		return fmt.Errorf("tak server: write: %w", err)
	}
	return nil
}

// serverFrame is ev as the TAK server takes it: TAK protocol v1 (protobuf)
// once negotiated or configured, XML with a newline otherwise.
func (g *TAKGateway) serverFrame(ev CotEvent) ([]byte, error) {
	if g.useProtobuf() {
		takMsg, err := CotEventToProto(ev)
		if err != nil {
			return nil, fmt.Errorf("convert CoT to protobuf: %w", err)
		}
		out, err := MarshalTakProto(takMsg)
		if err != nil {
			return nil, fmt.Errorf("marshal protobuf: %w", err)
		}
		return out, nil
	}
	out, err := MarshalCotEvent(ev)
	if err != nil {
		return nil, fmt.Errorf("marshal CoT XML: %w", err)
	}
	return append(out, '\n'), nil // XML uses newline delimiter
}

// sendMulticast sends ev as TAK SA multicast.
func (g *TAKGateway) sendMulticast(ev CotEvent) error {
	if g.mcast == nil {
		return errTAKMulticastDown
	}
	return g.mcast.SendEvent(ev)
}

// sendToHub publishes ev's XML to the Hub, as Android's MQTT Export does.
func (g *TAKGateway) sendToHub(ev CotEvent) error {
	hub := g.hooksNow().Hub
	if hub == nil || !hub.IsConnected() {
		return errTAKHubDown
	}
	out, err := MarshalCotEvent(ev)
	if err != nil {
		return fmt.Errorf("tak hub: marshal CoT XML: %w", err)
	}
	if err := hub.PublishTAKCoT(out); err != nil {
		return fmt.Errorf("tak hub: %w", err)
	}
	return nil
}

// SendOwnSOS puts this Bridge's SOS on TAK: a CoT emergency at its own
// position with reason as its text, as MeshSat Android's SosController does.
// Without a position nothing is sent and the answer is ErrTAKNoPosition.
func (g *TAKGateway) SendOwnSOS(reason string) error {
	lat, lon, alt, ok := g.selfPosition()
	if !ok {
		return ErrTAKNoPosition
	}
	uid, callsign := g.own()
	return withoutLinkDown(g.emit(BuildSOSEvent(uid, callsign, lat, lon, alt, g.config.CotStaleSec, reason)))
}

// SendOwnDeadman puts the dead man's switch on TAK: an alarm at the position
// the switch last saw, naming the seconds since the last check-in, as
// Android's sendDeadman.
func (g *TAKGateway) SendOwnDeadman(lat, lon float64, lastSeen time.Time) error {
	secs := 0
	if !lastSeen.IsZero() {
		secs = int(time.Since(lastSeen).Seconds())
	}
	uid, callsign := g.own()
	return withoutLinkDown(g.emit(BuildDeadmanEvent(uid, callsign, lat, lon, g.config.CotStaleSec, secs)))
}

// SendOwnChat puts a text this Bridge sent to the mesh on TAK as GeoChat, as
// Android's sendChat does for every text the phone sends to the mesh.
func (g *TAKGateway) SendOwnChat(text string) error {
	uid, callsign := g.own()
	return withoutLinkDown(g.emit(BuildChatEvent(uid, callsign, text, g.config.CotStaleSec)))
}

func (g *TAKGateway) selfPosition() (lat, lon, alt float64, ok bool) {
	f := g.hooksNow().SelfPosition
	if f == nil {
		return 0, 0, 0, false
	}
	return f()
}

// ownPLILoop sends this Bridge's own PLI, which Android sends on every fix, at
// most every coalesce_seconds: at start, then on every tick with a position.
func (g *TAKGateway) ownPLILoop(ctx context.Context) {
	defer g.wg.Done()
	every := time.Duration(g.config.CoalesceSeconds) * time.Second
	if every <= 0 {
		every = 30 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		g.sendOwnPLI()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sendOwnPLI sends one own PLI now, when an output is on and a position is
// known.
func (g *TAKGateway) sendOwnPLI() {
	if !g.hasOutput() {
		return
	}
	lat, lon, alt, ok := g.selfPosition()
	if !ok {
		return
	}
	uid, callsign := g.own()
	if err := withoutLinkDown(g.emit(BuildPositionEvent(uid, callsign, lat, lon, alt, g.config.CotStaleSec))); err != nil {
		log.Debug().Err(err).Msg("tak: own PLI not delivered everywhere")
	}
}
