package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/hubreporter"
	"meshsat/internal/types"
)

// The SOS as MeshSat Android sends it (SosController, MESHSAT-1249): every
// route is a leg that waits while its link is down and goes out when the
// link is back, and keeps trying until the SOS is cancelled. It replaces a
// burst of three direct sends 30 s apart, in which a modem or a node that
// was not connected at that instant was skipped for good, the 9704 was
// never used (the burst looked for gateways of type "iridium" only), and
// the Hub heard of the SOS only through its frame. [MESHSAT-1446]
//
// The legs this Bridge carries:
//   - mesh: the text, broadcast on the mesh (class sos: as given, past the
//     routing rules);
//   - satellite: the SOS frame the Hub raises an alarm from (class
//     hub_uplink), on the modem this Bridge has, queued once there is one;
//   - hub: the Hub told over the internet (HubReporter.PublishSOSNow) once
//     its link is up, with the id the Hub files the frame under, so the Hub
//     pages once whichever arrives first.
//
// SMS to the emergency contacts is the apps' own send (POST
// /api/messages/send, plain), as on Android, where the contacts live.

const (
	sosRouteMesh      = "mesh"
	sosRouteSatellite = "satellite"
	sosRouteHub       = "hub"

	// sosRunKey is where the SOS is kept (system_config).
	sosRunKey = "sos_run"
	// sosMeshInterface is the mesh link the mesh leg waits on.
	sosMeshInterface = "mesh_0"
	// sosFrameDevice is the device an SOS frame from the Bridge itself
	// names: the Hub puts the bridge's own id on the alert for it
	// (meshsat-hub uplink.go, MESHSAT-1294).
	sosFrameDevice = "bridge"
	// sosFrameMessageMax is the most the SOS frame carries of the words.
	sosFrameMessageMax = 64

	// sosCancelDefaultText is the cancellation without the caller's words:
	// MeshSat Android's cancelText for a person without a name.
	sosCancelDefaultText = "Alarm cancelled: A MeshSat user is safe and needs no help now."
)

// sosFollowEvery is how often the SOS looks for a modem for its satellite
// leg and for the Hub's link for its Hub leg (a variable for the tests).
var sosFollowEvery = 5 * time.Second

// sosSatelliteInterfaces are the satellite links an SOS frame may go on,
// newest modem first, as the Hub uplink picks them.
var sosSatelliteInterfaces = []string{"iridium_imt_0", "iridium_0"}

// sosAlarmWords are the words the Hub raises an alarm for, anywhere in a
// text and in any case (meshsat-hub internal/sos/detector.go).
var sosAlarmWords = []string{"SOS", "MAYDAY", "EMERGENCY"}

func sosAlarmWord(text string) bool {
	upper := strings.ToUpper(text)
	for _, w := range sosAlarmWords {
		if strings.Contains(upper, w) {
			return true
		}
	}
	return false
}

// sosPosition is where the person is, as the caller says.
type sosPosition struct {
	Lat, Lon float64
}

// sosLeg is one route the Bridge carries for an SOS, or its cancellation.
type sosLeg struct {
	Route      string `json:"route"`
	Interface  string `json:"interface,omitempty"`
	MsgRef     string `json:"msg_ref,omitempty"`
	DeliveryID int64  `json:"delivery_id,omitempty"`
	SentAt     string `json:"sent_at,omitempty"` // the Hub leg: when the Hub took it
	Error      string `json:"error,omitempty"`   // why it could not be queued
}

// sosRun is an SOS as the Bridge keeps it (sosRunKey).
type sosRun struct {
	ID          int64    `json:"id"` // the start in Unix milliseconds; the legs' msg_ref is "sos-<id>-<route>"
	StartedAt   string   `json:"started_at"`
	Text        string   `json:"text"`
	Trigger     string   `json:"trigger"`
	Lat         float64  `json:"lat,omitempty"`
	Lon         float64  `json:"lon,omitempty"`
	HasPosition bool     `json:"has_position,omitempty"`
	Named       bool     `json:"named,omitempty"`  // the caller named its routes
	Routes      []string `json:"routes,omitempty"` // the routes it named
	Planned     bool     `json:"planned,omitempty"`
	Bearer      string   `json:"bearer,omitempty"` // "sbd" or "imt": the satellite the Hub's alert id names
	Legs        []sosLeg `json:"legs,omitempty"`
	Skipped     []string `json:"skipped,omitempty"`
	HubSMSDone  bool     `json:"hub_sms_done,omitempty"`

	CancelledAt     string   `json:"cancelled_at,omitempty"`
	CancelText      string   `json:"cancel_text,omitempty"`
	CancelLegs      []sosLeg `json:"cancel_legs,omitempty"`
	HubCancelSentAt string   `json:"hub_cancel_sent_at,omitempty"`
}

func (r sosRun) copy() sosRun {
	r.Routes = append([]string(nil), r.Routes...)
	r.Legs = append([]sosLeg(nil), r.Legs...)
	r.Skipped = append([]string(nil), r.Skipped...)
	r.CancelLegs = append([]sosLeg(nil), r.CancelLegs...)
	return r
}

func (r *sosRun) leg(route string) *sosLeg {
	for i := range r.Legs {
		if r.Legs[i].Route == route {
			return &r.Legs[i]
		}
	}
	return nil
}

func (r *sosRun) named(route string) bool {
	for _, n := range r.Routes {
		if n == route {
			return true
		}
	}
	return false
}

func (r *sosRun) ref(route string) string {
	return "sos-" + strconv.FormatInt(r.ID, 10) + "-" + route
}

// parseSOSRef reads a leg's msg_ref: the SOS it belongs to and whether it is
// a cancellation ("sos-<id>-cancel:<route>").
func parseSOSRef(ref string) (id int64, cancel, ok bool) {
	rest, found := strings.CutPrefix(ref, "sos-")
	if !found {
		return 0, false, false
	}
	num, route, found := strings.Cut(rest, "-")
	if !found {
		return 0, false, false
	}
	id, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return 0, false, false
	}
	return id, strings.HasPrefix(route, "cancel:"), true
}

// sosLoad reads the SOS kept in the database, once: one that was on when
// the Bridge stopped is on again (RestoreSOS picks up its legs).
func (s *Server) sosLoad() {
	s.sos.load.Do(func() {
		if s.db == nil {
			return
		}
		raw, err := s.db.GetSystemConfig(sosRunKey)
		if err != nil || raw == "" {
			return
		}
		var run sosRun
		if err := json.Unmarshal([]byte(raw), &run); err != nil || run.ID == 0 {
			log.Warn().Err(err).Msg("SOS: the SOS kept in the database does not read, ignoring it")
			return
		}
		s.sos.mu.Lock()
		defer s.sos.mu.Unlock()
		if s.sos.run != nil {
			return
		}
		s.sos.run = &run
		s.sos.active = run.CancelledAt == ""
		s.sos.text = run.Text
		s.sos.trigger = run.Trigger
		if t, err := time.Parse(time.RFC3339, run.StartedAt); err == nil {
			s.sos.startAt = t
		}
	})
}

// sosMarshalLocked takes a numbered snapshot of the SOS for the database
// (sosWrite). The caller holds s.sos.mu.
func (s *Server) sosMarshalLocked() ([]byte, uint64) {
	if s.db == nil || s.sos.run == nil {
		return nil, 0
	}
	raw, err := json.Marshal(s.sos.run)
	if err != nil {
		return nil, 0
	}
	s.sos.saveSeq++
	return raw, s.sos.saveSeq
}

// sosWrite keeps a snapshot of the SOS in the database, outside s.sos.mu,
// unless a newer one is already written.
func (s *Server) sosWrite(raw []byte, seq uint64) {
	if raw == nil || s.db == nil {
		return
	}
	s.sos.saveMu.Lock()
	defer s.sos.saveMu.Unlock()
	if seq <= s.sos.savedSeq {
		return
	}
	if err := s.db.SetSystemConfig(sosRunKey, string(raw)); err != nil {
		log.Error().Err(err).Msg("SOS: could not keep the SOS in the database; a restart would lose it")
		return
	}
	s.sos.savedSeq = seq
}

// sosUpdate changes the SOS with id under the lock and keeps it; false when
// that SOS is no longer the Bridge's.
func (s *Server) sosUpdate(id int64, fn func(run *sosRun)) bool {
	s.sos.mu.Lock()
	if s.sos.run == nil || s.sos.run.ID != id {
		s.sos.mu.Unlock()
		return false
	}
	fn(s.sos.run)
	raw, seq := s.sosMarshalLocked()
	s.sos.mu.Unlock()
	s.sosWrite(raw, seq)
	return true
}

// sosSnapshot is a copy of the SOS with id and whether it is on; ok false
// when it is no longer the Bridge's.
func (s *Server) sosSnapshot(id int64) (run sosRun, active, ok bool) {
	s.sos.mu.Lock()
	defer s.sos.mu.Unlock()
	if s.sos.run == nil || s.sos.run.ID != id {
		return sosRun{}, false, false
	}
	return s.sos.run.copy(), s.sos.active, true
}

// RestoreSOS picks up the SOS kept in the database after a restart: one
// that is on goes on looking for its satellite modem and the Hub's link;
// one that was cancelled goes on telling the Hub, if it still has to. The
// legs already queued are in the queue, which survives a restart by itself.
// It runs once, and not for an SOS started in this process (its own worker
// carries it). [MESHSAT-1446]
func (s *Server) RestoreSOS() {
	s.sosLoad()
	s.sos.mu.Lock()
	run := s.sos.run
	if run == nil || s.sos.restored || s.sos.cancelFn != nil {
		s.sos.mu.Unlock()
		return
	}
	s.sos.restored = true
	id, active := run.ID, s.sos.active
	hubCancel := !active && run.CancelledAt != "" && run.HubCancelSentAt == "" && s.sosHubHad(run)
	var ctx context.Context
	if active {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(context.Background())
		s.sos.cancelFn = cancel
	}
	s.sos.mu.Unlock()
	if active {
		log.Warn().Int64("sos", id).Msg("SOS: the SOS is still on after a restart, carrying on")
		go s.sosWorker(ctx, id)
	}
	if hubCancel {
		go s.sosHubCancelLoop(id)
	}
}

// sosHubHad reports whether the Hub was told of the SOS over the internet.
func (s *Server) sosHubHad(run *sosRun) bool {
	leg := run.leg(sosRouteHub)
	return leg != nil && leg.SentAt != ""
}

// sosWorker carries the SOS with id: the routes worked out, the mesh leg
// queued and the SMS to the Hub's number sent, each once (a restart that
// came in between does the rest), then, until both are done or the SOS is
// cancelled, the satellite leg queued once a modem is there and the Hub told
// once its link is up.
func (s *Server) sosWorker(ctx context.Context, id int64) {
	s.sosPlan(id)
	s.sosQueueMesh(id)
	s.sosHubSMS(id)
	for {
		if ctx.Err() != nil {
			return
		}
		satDone := s.sosTrySatellite(ctx, id)
		hubDone := s.sosTryHub(ctx, id)
		if satDone && hubDone {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(sosFollowEvery):
		}
	}
}

// sosPlan works out the routes of the SOS with id: every route set up, or
// the routes the caller named that this Bridge can carry.
func (s *Server) sosPlan(id int64) {
	run, _, ok := s.sosSnapshot(id)
	if !ok || run.Planned {
		return
	}
	var legs []sosLeg
	var skipped []string

	// Mesh
	mesh, why := s.sosMeshSetUp()
	switch {
	case mesh && (!run.Named || run.named(sosRouteMesh)):
		legs = append(legs, sosLeg{Route: sosRouteMesh, Interface: sosMeshInterface})
	case !mesh && (!run.Named || run.named(sosRouteMesh)):
		skipped = append(skipped, "Mesh: "+why)
	}

	// Satellite
	link := s.sosSatelliteLink()
	bearer := sosBearer(link)
	setUp := link != "" || s.sosSatelliteBound()
	switch {
	case run.named(sosRouteSatellite), !run.Named && setUp:
		if s.uplinkBridgeID() == "" {
			skipped = append(skipped, "Satellite: this Bridge has no bridge id to name itself by to the Hub.")
		} else {
			legs = append(legs, sosLeg{Route: sosRouteSatellite})
		}
	case !run.Named:
		skipped = append(skipped, "Satellite: no satellite modem has been connected to this Bridge.")
	}

	// Hub
	hub := s.hubReporter != nil
	switch {
	case hub && (!run.Named || run.named(sosRouteHub)):
		legs = append(legs, sosLeg{Route: sosRouteHub})
	case !hub && (!run.Named || run.named(sosRouteHub)):
		skipped = append(skipped, "Hub: not set up on this Bridge.")
	}

	s.sosUpdate(id, func(r *sosRun) {
		r.Legs, r.Skipped, r.Bearer, r.Planned = legs, skipped, bearer, true
	})
}

// sosMeshSetUp reports whether the mesh can carry an SOS leg, and why not:
// a mesh radio, and the mesh link switched on (a link switched off gets no
// delivery worker, so a leg on it would never go).
func (s *Server) sosMeshSetUp() (bool, string) {
	if s.mesh == nil {
		return false, "no mesh radio on this Bridge."
	}
	if s.dispatcher == nil || s.db == nil {
		return false, "the message queue is not running, so nothing could be queued."
	}
	iface, err := s.db.GetInterface(sosMeshInterface)
	if err != nil || iface == nil {
		return false, "this Bridge has no mesh link."
	}
	if !iface.Enabled {
		return false, "the mesh link is switched off in Links."
	}
	return true, ""
}

// sosSatelliteLink is the satellite link an SOS frame goes on now: the one
// whose modem is connected (the 9704's link first), else the first whose
// gateway runs; "" without a satellite gateway.
func (s *Server) sosSatelliteLink() string {
	if s.gwManager == nil {
		return ""
	}
	first := ""
	for _, id := range sosSatelliteInterfaces {
		gw := s.gwManager.GatewayByInterfaceID(id)
		if gw == nil {
			continue
		}
		if gw.Status().Connected {
			return id
		}
		if first == "" {
			first = id
		}
	}
	return first
}

// sosSatelliteBound reports whether a satellite modem has been bound to a
// switched-on satellite link: set up, though not there now (unplugged, or
// its node out of reach).
func (s *Server) sosSatelliteBound() bool {
	if s.db == nil {
		return false
	}
	for _, id := range sosSatelliteInterfaces {
		if iface, err := s.db.GetInterface(id); err == nil && iface != nil && iface.Enabled && iface.DeviceID != "" {
			return true
		}
	}
	return false
}

// sosBearer is how the Hub names the satellite a frame came by: "imt" for
// the 9704, "sbd" otherwise (Android's, too).
func sosBearer(link string) string {
	if strings.HasPrefix(link, "iridium_imt") {
		return "imt"
	}
	return "sbd"
}

// sosLegOptions is how an SOS leg is queued: precedence Override, priority
// 0 (first in its queue, never evicted, never expired), tried until it goes
// or the SOS is cancelled, under the leg's own reference.
func sosLegOptions(class, ref string) engine.DirectSendOptions {
	return engine.DirectSendOptions{
		Precedence:   string(types.PrecedenceOverride),
		Class:        class,
		Critical:     true,
		RetryForever: true,
		MsgRef:       ref,
	}
}

// sosQueueMesh queues the mesh leg of the SOS with id: its text, broadcast.
func (s *Server) sosQueueMesh(id int64) {
	run, active, ok := s.sosSnapshot(id)
	if !ok || !active {
		return
	}
	leg := run.leg(sosRouteMesh)
	if leg == nil || leg.MsgRef != "" || leg.Error != "" {
		return
	}
	ref := run.ref(sosRouteMesh)
	delID, _, err := s.dispatcher.QueueDirectSendTo(sosMeshInterface, run.Text, sosLegOptions(database.DeliveryClassSOS, ref))
	s.sosUpdate(id, func(r *sosRun) {
		if l := r.leg(sosRouteMesh); l != nil {
			if err != nil {
				l.Error = "Could not be queued: " + err.Error()
			} else {
				l.MsgRef, l.DeliveryID = ref, delID
			}
		}
	})
	if err != nil {
		log.Error().Err(err).Int64("sos", id).Msg("SOS: the mesh leg could not be queued")
		return
	}
	log.Warn().Int64("sos", id).Int64("delivery_id", delID).Msg("SOS: mesh leg queued")
}

// sosFramePosition is where the SOS frame says the person is: the caller's
// position, else this Bridge's GPS fix; ok false without either (the frame
// then carries 0, 0 and its words say the position is unknown).
func (s *Server) sosFramePosition(run sosRun) (lat, lon float64, ok bool) {
	if run.HasPosition {
		return run.Lat, run.Lon, true
	}
	if s.gpsReader != nil {
		if st := s.gpsReader.GetStatus(); st.Fix && (st.Lat != 0 || st.Lon != 0) {
			return st.Lat, st.Lon, true
		}
	}
	return 0, 0, false
}

// sosFrameMessage is the words the SOS frame carries (MeshSat Android's
// frameMessage): the text's first sentence ("SOS: <name> needs help"), and
// ", position unknown" when the frame has no position; at most 64 bytes,
// never a character cut in two. The position itself is in the frame.
func sosFrameMessage(text string, hasPosition bool) string {
	first := text
	if i := strings.Index(text, ". "); i > 0 {
		first = text[:i]
	}
	first = strings.TrimSuffix(strings.TrimSpace(first), ".")
	if !hasPosition && !strings.Contains(strings.ToLower(first), "position unknown") {
		first += ", position unknown"
	}
	for len(first) > sosFrameMessageMax {
		_, size := utf8.DecodeLastRuneInString(first)
		first = first[:len(first)-size]
	}
	return first
}

// sosFrame is the SOS frame for the Hub: the bridge id the Bridge names
// itself by, where the person is, the words and the SOS's start.
func (s *Server) sosFrame(run sosRun) []byte {
	lat, lon, known := s.sosFramePosition(run)
	started := time.UnixMilli(run.ID).UTC()
	return hubreporter.EncodeSatSOS(s.uplinkBridgeID(), sosFrameDevice, lat, lon, sosFrameMessage(run.Text, known), started)
}

// sosTrySatellite queues the satellite leg of the SOS with id once a modem
// is there; true when it is queued, or there is none to queue.
func (s *Server) sosTrySatellite(ctx context.Context, id int64) bool {
	run, active, ok := s.sosSnapshot(id)
	if !ok || !active {
		return true
	}
	leg := run.leg(sosRouteSatellite)
	if leg == nil || leg.MsgRef != "" || leg.Error != "" {
		return true
	}
	if s.dispatcher == nil {
		s.sosUpdate(id, func(r *sosRun) {
			if l := r.leg(sosRouteSatellite); l != nil {
				l.Error = "Could not be queued: the message queue is not running."
			}
		})
		return true
	}
	link := s.sosSatelliteLink()
	if link == "" || ctx.Err() != nil {
		return false // no modem yet: the leg waits for one
	}
	ref := run.ref(sosRouteSatellite)
	frame := s.sosFrame(run)
	delID, _, err := s.dispatcher.QueueDirectSendTo(link, run.Text, func() engine.DirectSendOptions {
		opts := sosLegOptions(database.DeliveryClassHubUplink, ref)
		opts.Payload = frame
		return opts
	}())
	s.sosUpdate(id, func(r *sosRun) {
		if l := r.leg(sosRouteSatellite); l != nil {
			if err != nil {
				l.Error = "Could not be queued: " + err.Error()
			} else {
				l.MsgRef, l.DeliveryID, l.Interface = ref, delID, link
			}
		}
		// The Hub files the frame under the satellite it came by: the Hub
		// leg, if it has not gone yet, names the same one.
		if hub := r.leg(sosRouteHub); err == nil && (hub == nil || hub.SentAt == "") {
			r.Bearer = sosBearer(link)
		}
	})
	if err != nil {
		log.Error().Err(err).Int64("sos", id).Str("interface", link).Msg("SOS: the satellite leg could not be queued")
		return true
	}
	log.Warn().Int64("sos", id).Int64("delivery_id", delID).Str("interface", link).Int("bytes", len(frame)).
		Msg("SOS: satellite leg queued, the SOS frame to the Hub")
	return true
}

// sosHubAlert is the SOS, or its cancellation, as the Hub's detector reads
// it (HubReporter.PublishSOSNow).
func (s *Server) sosHubAlert(run sosRun, cancel bool) (hubreporter.SOSAlert, hubreporter.DeviceSOS) {
	bridge := s.uplinkBridgeID()
	started := time.UnixMilli(run.ID).UTC()
	id := fmt.Sprintf("sos-%s-%s-%d", run.Bearer, bridge, started.Unix())
	text, kind := run.Text, "triggered"
	if cancel {
		id, text, kind = id+"-cancelled", run.CancelText, "cancelled"
	}
	now := s.clockNow().UTC()
	alert := hubreporter.SOSAlert{ID: id, IMEI: bridge, Text: text, SOS: !cancel, Channel: "mqtt", Source: "bridge_sos",
		Timestamp: now.Format(time.RFC3339)}
	event := hubreporter.DeviceSOS{DeviceID: bridge, Type: kind, Message: text, Timestamp: now}
	if lat, lon, ok := s.sosFramePosition(run); ok {
		alert.Lat, alert.Lon = &lat, &lon
		event.Lat, event.Lon = lat, lon
	}
	return alert, event
}

// sosTryHub tells the Hub of the SOS with id over the internet once its
// link is up; true when the Hub has it, or there is no Hub leg.
func (s *Server) sosTryHub(ctx context.Context, id int64) bool {
	run, active, ok := s.sosSnapshot(id)
	if !ok || !active {
		return true
	}
	leg := run.leg(sosRouteHub)
	if leg == nil || leg.SentAt != "" {
		return true
	}
	if s.hubReporter == nil || !s.hubReporter.IsConnected() || ctx.Err() != nil {
		return false
	}
	alert, event := s.sosHubAlert(run, false)
	if err := s.hubReporter.PublishSOSNow(alert, event); err != nil {
		log.Warn().Err(err).Int64("sos", id).Msg("SOS: the Hub did not take the SOS yet, trying again")
		return false
	}
	sentAt := s.clockNow().UTC().Format(time.RFC3339)
	s.sosUpdate(id, func(r *sosRun) {
		if l := r.leg(sosRouteHub); l != nil {
			l.SentAt = sentAt
		}
	})
	log.Warn().Int64("sos", id).Str("alert", alert.ID).Msg("SOS: the Hub has the SOS over the internet")
	// Cancelled while it was being published: the Hub is told that too.
	if _, active, ok := s.sosSnapshot(id); ok && !active {
		go s.sosHubCancelLoop(id)
	}
	return true
}

// sosHubSMS sends the SOS frame by SMS to the Hub's number where the Hub
// uplink's bearer choice takes SMS (the satellite being the SOS's own leg),
// under the SOS's reference, so a cancellation stops it too. Once per SOS.
func (s *Server) sosHubSMS(id int64) {
	run, active, ok := s.sosSnapshot(id)
	if !ok || !active || run.HubSMSDone || s.satFallback == nil || s.uplinkBridgeID() == "" {
		return
	}
	s.sosUpdate(id, func(r *sosRun) { r.HubSMSDone = true })
	if err := s.satFallback.PublishSOSFrame(s.sosFrame(run), run.ref("hubsms")); err != nil {
		log.Warn().Err(err).Int64("sos", id).Msg("SOS: no SMS to the Hub's number")
	}
}

// sosCancelLegs stops what of the SOS with id has not gone out, and queues
// the cancellation on the mesh when the mesh leg went out (or was going out
// when the SOS was cancelled), as MeshSat Android does; the Hub is told if it
// had the SOS. The satellite leg gets no cancellation: its frame stays the
// Hub's alert, and a second frame costs a credit. It answers the
// cancellation's legs.
func (s *Server) sosCancelLegs(id int64, text string) []sosLeg {
	run, _, ok := s.sosSnapshot(id)
	if !ok {
		return nil
	}
	if s.db != nil {
		for _, leg := range run.Legs {
			if leg.DeliveryID == 0 {
				continue
			}
			if err := s.db.CancelDelivery(leg.DeliveryID); err == nil {
				log.Warn().Int64("sos", id).Str("route", leg.Route).Int64("delivery_id", leg.DeliveryID).Msg("SOS cancelled: leg stopped before it went out")
			}
		}
		// SMS to the Hub's number, under the SOS's reference
		if ids, err := s.db.WaitingDeliveryIDsByRef(run.ref("hubsms")); err == nil {
			for _, delID := range ids {
				_ = s.db.CancelDelivery(delID)
			}
		}
	}

	var cancelLegs []sosLeg
	if leg := run.leg(sosRouteMesh); leg != nil && leg.DeliveryID != 0 && s.dispatcher != nil && s.db != nil {
		if del, err := s.db.GetDelivery(leg.DeliveryID); err == nil && del != nil &&
			(del.Status == "sent" || del.Status == "delivered" || del.Status == "sending") {
			ref := run.ref("cancel:" + sosRouteMesh)
			delID, _, err := s.dispatcher.QueueDirectSendTo(sosMeshInterface, text, sosLegOptions(database.DeliveryClassSOS, ref))
			if err != nil {
				log.Error().Err(err).Int64("sos", id).Msg("SOS cancelled: the cancellation could not be queued on the mesh")
			} else {
				cancelLegs = append(cancelLegs, sosLeg{Route: sosRouteMesh, Interface: sosMeshInterface, MsgRef: ref, DeliveryID: delID})
			}
		}
	}
	hubHad := false
	s.sosUpdate(id, func(r *sosRun) {
		r.CancelLegs = cancelLegs
		hubHad = s.sosHubHad(r)
	})
	if hubHad {
		go s.sosHubCancelLoop(id)
	}
	return cancelLegs
}

// sosHubCancelLoop tells the Hub the SOS with id is over, once its link is
// up, while that SOS is still the Bridge's last.
func (s *Server) sosHubCancelLoop(id int64) {
	for {
		run, active, ok := s.sosSnapshot(id)
		if !ok || active || run.HubCancelSentAt != "" {
			return
		}
		if s.hubReporter != nil && s.hubReporter.IsConnected() {
			alert, event := s.sosHubAlert(run, true)
			err := s.hubReporter.PublishSOSNow(alert, event)
			if err == nil {
				sentAt := s.clockNow().UTC().Format(time.RFC3339)
				s.sosUpdate(id, func(r *sosRun) { r.HubCancelSentAt = sentAt })
				log.Warn().Int64("sos", id).Msg("SOS cancelled: the Hub is told")
				return
			}
			if !errors.Is(err, hubreporter.ErrNotConnected) {
				log.Warn().Err(err).Int64("sos", id).Msg("SOS cancelled: the Hub did not take the cancellation yet")
			}
		}
		time.Sleep(sosFollowEvery)
	}
}

// sosLegState is a leg as GET /api/sos/status shows it.
type sosLegState struct {
	sosLeg
	Status    string `json:"status"`
	LastError string `json:"last_error,omitempty"`
	AckStatus string `json:"ack_status,omitempty"`
}

// sosLegStates reads where each leg of run stands.
func (s *Server) sosLegStates(run sosRun) []sosLegState {
	out := make([]sosLegState, 0, len(run.Legs))
	for _, leg := range run.Legs {
		st := sosLegState{sosLeg: leg, Status: "waiting"}
		switch {
		case leg.Error != "":
			st.Status = "failed"
		case leg.Route == sosRouteHub && leg.SentAt != "":
			st.Status = "sent"
		case leg.DeliveryID != 0 && s.db != nil:
			if del, err := s.db.GetDelivery(leg.DeliveryID); err == nil && del != nil {
				st.Status, st.LastError = del.Status, del.LastError
				if del.AckStatus != nil {
					st.AckStatus = *del.AckStatus
				}
			}
		}
		out = append(out, st)
	}
	return out
}

// sosMayDeliver is the dispatcher's send gate (engine.SendGate), MeshSat
// Android's SosController.mayDeliver: a leg of an SOS goes out only while
// that SOS is on. A cancellation always goes, and so does anything that is
// not an SOS leg, or a leg of an SOS this Bridge does not know (its record
// lost: better a late SOS than none).
func (s *Server) sosMayDeliver(del database.MessageDelivery) bool {
	id, cancel, ok := parseSOSRef(del.MsgRef)
	if !ok || cancel {
		return true
	}
	s.sosLoad()
	s.sos.mu.Lock()
	defer s.sos.mu.Unlock()
	if s.sos.run == nil {
		return true
	}
	if s.sos.run.ID != id {
		// A leg of an earlier SOS: every SOS before the one kept was
		// cancelled before it could start (one is on at a time).
		return id > s.sos.run.ID
	}
	return s.sos.active
}
