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
	"meshsat/internal/hubmsg"
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
// Under its own identity (HeMB-NG C10, MESHSAT-1584), while the Hub's
// hub/caps says sos_v2: the run keeps its sos_id (a UUIDv7 of its start), the
// frames are 0x07 (start) and 0x08 (cancel), the hub leg is the v2 event on
// sos/event, repeated until the Hub's sos/ack (the Hub has it then, not at
// the broker's PUBACK), and the cancel goes until its ack, or for 24 h, on
// each bearer that carried the start. The Hub pages once per run whichever
// bearers carry it, and a cancel there is final. The contract is the Hub's
// docs/bridge-hub-protocol.md "SOS identity (C10)".
//
// SMS to the emergency contacts is the apps' own send (POST
// /api/messages/send, plain), as on Android, where the contacts live.

const (
	sosRouteMesh      = "mesh"
	sosRouteSatellite = "satellite"
	sosRouteHub       = "hub"
	// sosRouteHubSMS is the SMS to the Hub's number (sosHubSMS); a leg of the
	// cancellation only.
	sosRouteHubSMS = "hubsms"

	// sosFrameV2 marks a leg that carried the SOS under its own identity (C10).
	sosFrameV2 = "v2"
	// sosHubCapsKey is where the last hub/caps the Hub sent is kept
	// (system_config): the Bridge may have to send by satellite with no Hub
	// session at all. [MESHSAT-1584]
	sosHubCapsKey = "hub_caps"
	// sosFrameSMSMax is the most an SOS frame under its own identity may be:
	// base64 in one SMS of 160 characters (the cellular link cuts a longer
	// text at 160, which would break the frame).
	sosFrameSMSMax = 120

	// sosRunKey is where the SOS is kept (system_config).
	sosRunKey = "sos_run"
	// sosPrevRunsKey keeps the earlier runs whose cancellation is still owed (C10-R4): all of
	// them, however many SOS follow; a run leaves only when its cancel is acked or its 24 h
	// are over (C10-R7).
	sosPrevRunsKey = "sos_prev_runs"
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

// The C10 repeats (variables for the tests): the hub leg's start and its
// cancellation are published again until the Hub's ack, from sosFollowEvery
// doubling to sosRepeatMax; the cancellation is followed for sosCancelFor at
// most; while the Hub's session is up, the cancel frames wait sosCancelAckGrace
// for the Hub's ack before they spend a satellite credit or an SMS.
var (
	sosRepeatMax      = time.Minute
	sosCancelFor      = 24 * time.Hour
	sosCancelAckGrace = 30 * time.Second
	sosPubackWait     = 5 * time.Second
)

// sosHub is what the SOS needs of the Hub session: the HubReporter, or a
// test's.
type sosHub interface {
	IsConnected() bool
	PublishSOSNow(hubreporter.SOSAlert, hubreporter.DeviceSOS) error
	PublishSOSEvent(hubreporter.SOSEvent, time.Duration) error
}

// sosHubLink is the Hub session the SOS uses; nil without one.
func (s *Server) sosHubLink() sosHub {
	if s.sos.hub != nil {
		return s.sos.hub
	}
	if s.hubReporter == nil {
		return nil
	}
	return s.hubReporter
}

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
	SentAt     string `json:"sent_at,omitempty"` // the Hub leg: when the Hub took it (under C10: its ack)
	Error      string `json:"error,omitempty"`   // why it could not be queued
	// PublishedAt is when the Hub leg's C10 start first went to the broker;
	// the Hub has it only at its ack (SentAt).
	PublishedAt string `json:"published_at,omitempty"`
	// Frame is "v2" when the leg carried the SOS under its own identity (C10).
	Frame string `json:"frame,omitempty"`
	// OldWay: the Hub leg's start went the old way (PublishSOSNow), so its
	// cancellation goes the old way too.
	OldWay bool `json:"old_way,omitempty"`
	// HubState is the run's state in the Hub's last ack ("active", "cancelled").
	HubState string `json:"hub_state,omitempty"`
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
	// SOSID is the run's own identity (C10): a UUIDv7 of its start, or, for a
	// run kept from before C10, the old-format v5 (sosEnsureID).
	SOSID string `json:"sos_id,omitempty"`
	// HubSMSFrame is "v2" when the SMS to the Hub's number carried the C10 start.
	HubSMSFrame string `json:"hub_sms_frame,omitempty"`

	CancelledAt string   `json:"cancelled_at,omitempty"`
	CancelText  string   `json:"cancel_text,omitempty"`
	CancelLegs  []sosLeg `json:"cancel_legs,omitempty"`
	// HubCancelSentAt: the Hub took the cancellation the old way.
	HubCancelSentAt string `json:"hub_cancel_sent_at,omitempty"`
	// HubCancelV2: a bearer carried the C10 start, so the C10 cancel is owed;
	// published first at HubCancelPublishedAt, acked by the Hub at
	// HubCancelAckedAt. FrameCancelsDone: the cancel frames were queued on the
	// bearers that carried the start (or none was owed).
	HubCancelV2          bool   `json:"hub_cancel_v2,omitempty"`
	HubCancelPublishedAt string `json:"hub_cancel_published_at,omitempty"`
	HubCancelAckedAt     string `json:"hub_cancel_acked_at,omitempty"`
	FrameCancelsDone     bool   `json:"frame_cancels_done,omitempty"`
	// HubCancelGaveUp: 24 h passed without the cancel's ack; it is no longer followed.
	HubCancelGaveUp bool `json:"hub_cancel_gave_up,omitempty"`
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
		var prev []*sosRun
		if raw, err := s.db.GetSystemConfig(sosPrevRunsKey); err == nil && raw != "" {
			if err := json.Unmarshal([]byte(raw), &prev); err != nil {
				log.Warn().Err(err).Msg("SOS: the earlier runs kept in the database do not read, ignoring them")
				prev = nil
			}
		}
		s.sos.mu.Lock()
		if s.sos.prev == nil {
			s.sos.prev = prev
		}
		s.sos.mu.Unlock()
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

// sosSaved is a snapshot of the SOS and of the earlier runs still owed a cancellation.
type sosSaved struct{ run, prev []byte }

// sosMarshalLocked takes a numbered snapshot of the SOS for the database
// (sosWrite). The caller holds s.sos.mu.
func (s *Server) sosMarshalLocked() (sosSaved, uint64) {
	if s.db == nil || s.sos.run == nil {
		return sosSaved{}, 0
	}
	raw, err := json.Marshal(s.sos.run)
	if err != nil {
		return sosSaved{}, 0
	}
	prev := s.sos.prev
	if prev == nil {
		prev = []*sosRun{}
	}
	rawPrev, err := json.Marshal(prev)
	if err != nil {
		return sosSaved{}, 0
	}
	s.sos.saveSeq++
	return sosSaved{run: raw, prev: rawPrev}, s.sos.saveSeq
}

// sosWrite keeps a snapshot of the SOS in the database, outside s.sos.mu,
// unless a newer one is already written.
func (s *Server) sosWrite(saved sosSaved, seq uint64) {
	if saved.run == nil || s.db == nil {
		return
	}
	s.sos.saveMu.Lock()
	defer s.sos.saveMu.Unlock()
	if seq <= s.sos.savedSeq {
		return
	}
	if err := s.db.SetSystemConfig(sosPrevRunsKey, string(saved.prev)); err != nil {
		log.Error().Err(err).Msg("SOS: could not keep the earlier runs' owed cancellations; a restart would lose them")
		return
	}
	if err := s.db.SetSystemConfig(sosRunKey, string(saved.run)); err != nil {
		log.Error().Err(err).Msg("SOS: could not keep the SOS in the database; a restart would lose it")
		return
	}
	s.sos.savedSeq = seq
}

// sosFindLocked is the run with id: the SOS on now or the last, or an earlier run still owed
// its cancellation; nil when the Bridge no longer keeps it. The caller holds s.sos.mu.
func (s *Server) sosFindLocked(id int64) *sosRun {
	if s.sos.run != nil && s.sos.run.ID == id {
		return s.sos.run
	}
	for _, p := range s.sos.prev {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// sosKeepOwedLocked keeps the run about to be replaced when its cancellation is still owed
// (a new SOS is no ack of the older one's cancel). No count limit: an owed cancel is dropped
// only by its ack or its 24 h (sosForget), never to make room (C10-R7). The caller holds
// s.sos.mu.
func (s *Server) sosKeepOwedLocked() {
	old := s.sos.run
	if old == nil || s.sos.active || old.CancelledAt == "" || !sosCancelOwed(old) {
		return
	}
	s.sos.prev = append(s.sos.prev, old)
	log.Warn().Int64("sos", old.ID).Str("sos_id", old.SOSID).Int("owed", len(s.sos.prev)).
		Msg("SOS: a new SOS starts; the last one's cancellation is still followed")
}

// sosForget drops an earlier run whose cancellation is no longer owed.
func (s *Server) sosForget(id int64) {
	s.sos.mu.Lock()
	kept := s.sos.prev[:0]
	dropped := false
	for _, p := range s.sos.prev {
		if p.ID == id && !sosCancelOwed(p) {
			dropped = true
			continue
		}
		kept = append(kept, p)
	}
	s.sos.prev = kept
	if !dropped {
		s.sos.mu.Unlock()
		return
	}
	raw, seq := s.sosMarshalLocked()
	s.sos.mu.Unlock()
	s.sosWrite(raw, seq)
}

// sosUpdate changes the run with id under the lock and keeps it; false when
// the Bridge no longer keeps that run.
func (s *Server) sosUpdate(id int64, fn func(run *sosRun)) bool {
	s.sos.mu.Lock()
	r := s.sosFindLocked(id)
	if r == nil {
		s.sos.mu.Unlock()
		return false
	}
	fn(r)
	raw, seq := s.sosMarshalLocked()
	s.sos.mu.Unlock()
	s.sosWrite(raw, seq)
	return true
}

// sosSnapshot is a copy of the run with id and whether it is on (an earlier
// run never is); ok false when the Bridge no longer keeps it.
func (s *Server) sosSnapshot(id int64) (run sosRun, active, ok bool) {
	s.sos.mu.Lock()
	defer s.sos.mu.Unlock()
	r := s.sosFindLocked(id)
	if r == nil {
		return sosRun{}, false, false
	}
	return r.copy(), s.sos.active && r == s.sos.run, true
}

// RestoreSOS picks up the SOS kept in the database after a restart: one
// that is on goes on looking for its satellite modem and the Hub's link;
// one that was cancelled goes on telling the Hub, if it still has to. The
// legs already queued are in the queue, which survives a restart by itself.
// It runs once, and not for an SOS started in this process (its own worker
// carries it). [MESHSAT-1446]
func (s *Server) RestoreSOS() {
	s.sosLoad()
	s.sosEnsureID()
	s.sos.mu.Lock()
	// The earlier runs still owed a cancellation are followed again (C10-R4), once.
	if !s.sos.prevRestored {
		s.sos.prevRestored = true
		for _, p := range s.sos.prev {
			if sosCancelOwed(p) {
				go s.sosHubCancelLoop(p.ID)
			}
		}
	}
	run := s.sos.run
	if run == nil || s.sos.restored || s.sos.cancelFn != nil {
		s.sos.mu.Unlock()
		return
	}
	s.sos.restored = true
	id, active := run.ID, s.sos.active
	hubCancel := !active && run.CancelledAt != "" && sosCancelOwed(run)
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

// sosHubHadOldWay reports whether the Hub took the SOS over the internet the
// old way (PublishSOSNow), so its cancellation goes the old way too; a run
// kept from before C10 says so by a sent_at alone.
func sosHubHadOldWay(run *sosRun) bool {
	leg := run.leg(sosRouteHub)
	return leg != nil && (leg.OldWay || (leg.SentAt != "" && leg.Frame == ""))
}

// sosCancelOwed reports whether the Hub is still owed the cancellation of a
// cancelled run: the old way, or under C10 until its ack (the follow loop
// ends it at sosCancelFor).
func sosCancelOwed(run *sosRun) bool {
	return (run.HubCancelSentAt == "" && sosHubHadOldWay(run)) || sosV2CancelOwed(run)
}

// sosV2CancelOwed reports whether the C10 cancel is still owed: not acked, not given up.
func sosV2CancelOwed(run *sosRun) bool {
	return run.HubCancelV2 && run.HubCancelAckedAt == "" && !run.HubCancelGaveUp
}

// sosEnsureID gives the SOS kept its sos_id when it has none: a run started
// before this Bridge spoke C10 takes the old-format id, UUIDv5 of
// "<bridge id>:<start ms>", so it is the same on every bearer and after a
// restart. It needs the bridge id; without one the run stays without (and
// goes out the old way). [MESHSAT-1584]
func (s *Server) sosEnsureID() {
	bridge := s.uplinkBridgeID()
	s.sos.mu.Lock()
	run := s.sos.run
	if run == nil || run.SOSID != "" || bridge == "" {
		s.sos.mu.Unlock()
		return
	}
	run.SOSID = hubmsg.LegacySOSID(bridge, run.ID)
	raw, seq := s.sosMarshalLocked()
	s.sos.mu.Unlock()
	s.sosWrite(raw, seq)
}

// HubCapsReceived keeps what the Hub says it understands (hub/caps, retained),
// in memory and in the database. [MESHSAT-1584]
func (s *Server) HubCapsReceived(c hubreporter.HubCaps) {
	s.sos.mu.Lock()
	changed := !s.sos.capsLoaded || s.sos.caps.Has(hubreporter.CapSOSV2) != c.Has(hubreporter.CapSOSV2)
	s.sos.caps, s.sos.capsLoaded = c, true
	s.sos.mu.Unlock()
	if s.db != nil {
		raw, _ := json.Marshal(c)
		if err := s.db.SetSystemConfig(sosHubCapsKey, string(raw)); err != nil {
			log.Warn().Err(err).Msg("hub: could not keep the Hub's caps; a restart without a Hub session goes the old way")
		}
	}
	if changed {
		log.Info().Bool("sos_v2", c.Has(hubreporter.CapSOSV2)).Strs("caps", c.Caps).Msg("hub: the Hub's caps")
	}
}

// sosV2 reports whether the last hub/caps the Hub sent says sos_v2.
func (s *Server) sosV2() bool {
	s.sos.mu.Lock()
	loaded, caps := s.sos.capsLoaded, s.sos.caps
	s.sos.mu.Unlock()
	if !loaded {
		var kept hubreporter.HubCaps
		if s.db != nil {
			if raw, err := s.db.GetSystemConfig(sosHubCapsKey); err == nil && raw != "" {
				_ = json.Unmarshal([]byte(raw), &kept)
			}
		}
		s.sos.mu.Lock()
		if !s.sos.capsLoaded {
			s.sos.caps, s.sos.capsLoaded = kept, true
		}
		caps = s.sos.caps
		s.sos.mu.Unlock()
	}
	return caps.Has(hubreporter.CapSOSV2)
}

// sosUseV2 reports whether the run goes out under its own identity now.
func (s *Server) sosUseV2(run sosRun) bool {
	return run.SOSID != "" && s.sosV2()
}

// sosWorker carries the SOS with id: the routes worked out, the mesh leg
// queued and the SMS to the Hub's number sent, each once (a restart that
// came in between does the rest), then, until both are done or the SOS is
// cancelled, the satellite leg queued once a modem is there and the Hub told
// once its link is up.
func (s *Server) sosWorker(ctx context.Context, id int64) {
	every := sosFollowEvery
	s.sosEnsureID()
	s.sosPlan(id)
	s.sosQueueMesh(id)
	s.sosHubSMS(id)
	for {
		if ctx.Err() != nil {
			return
		}
		satDone := s.sosTrySatellite(ctx, id)
		hubDone := s.sosTryHub(ctx, id, every)
		if satDone && hubDone {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
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
	hub := s.sosHubLink() != nil
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
	return sosFrameMessageMaxBytes(text, hasPosition, sosFrameMessageMax)
}

// sosFrameMessageMaxBytes is sosFrameMessage held to limit bytes.
func sosFrameMessageMaxBytes(text string, hasPosition bool, limit int) string {
	first := text
	if i := strings.Index(text, ". "); i > 0 {
		first = text[:i]
	}
	first = strings.TrimSuffix(strings.TrimSpace(first), ".")
	if !hasPosition && !strings.Contains(strings.ToLower(first), "position unknown") {
		first += ", position unknown"
	}
	for len(first) > max(limit, 0) {
		_, size := utf8.DecodeLastRuneInString(first)
		first = first[:len(first)-size]
	}
	return first
}

// sosFrame is the SOS frame for the Hub: the bridge id the Bridge names
// itself by, where the person is, the words and the SOS's start. Under C10
// (v2 true) it is the 0x07 start with the run's sos_id and start, its words
// cut so the frame fits one SMS as base64 (sosFrameSMSMax); else the 0x02.
func (s *Server) sosFrame(run sosRun) (frame []byte, v2 bool) {
	lat, lon, known := s.sosFramePosition(run)
	started := time.UnixMilli(run.ID).UTC()
	bridge := s.uplinkBridgeID()
	if s.sosUseV2(run) {
		if id, ok := hubmsg.UUIDBytes(run.SOSID); ok {
			fixed := len(hubreporter.EncodeSatSOSStart(bridge, sosFrameDevice, lat, lon, "", started, id, run.ID))
			words := sosFrameMessageMaxBytes(run.Text, known, min(sosFrameMessageMax, sosFrameSMSMax-fixed))
			return hubreporter.EncodeSatSOSStart(bridge, sosFrameDevice, lat, lon, words, started, id, run.ID), true
		}
	}
	return hubreporter.EncodeSatSOS(bridge, sosFrameDevice, lat, lon, sosFrameMessage(run.Text, known), started), false
}

// sosCancelFrame is the 0x08 cancel of the run (C10); nil without its id.
func (s *Server) sosCancelFrame(run sosRun) []byte {
	id, ok := hubmsg.UUIDBytes(run.SOSID)
	if !ok {
		return nil
	}
	at, err := time.Parse(time.RFC3339, run.CancelledAt)
	if err != nil {
		at = s.clockNow()
	}
	return hubreporter.EncodeSatSOSCancel(s.uplinkBridgeID(), id, run.ID, at, hubreporter.SOSCancelOperator)
}

// sosEvent is the run's start, or its cancellation, as the v2 event on
// sos/event (C10). The device is the Bridge itself (no device id).
func (s *Server) sosEvent(run sosRun, cancel bool) hubreporter.SOSEvent {
	ev := hubreporter.SOSEvent{Kind: hubreporter.SOSEventStart, SOSID: run.SOSID, RunStartMs: run.ID}
	if cancel {
		reason := int(hubreporter.SOSCancelOperator)
		ev.Kind, ev.Reason = hubreporter.SOSEventCancel, &reason
		if at, err := time.Parse(time.RFC3339, run.CancelledAt); err == nil {
			ev.Timestamp = at.Unix()
		}
		return ev
	}
	ev.Text = run.Text
	ev.Timestamp = time.UnixMilli(run.ID).Unix()
	if lat, lon, ok := s.sosFramePosition(run); ok {
		ev.Lat, ev.Lon = &lat, &lon
	}
	return ev
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
	frame, v2 := s.sosFrame(run)
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
				if v2 {
					l.Frame = sosFrameV2
				}
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
	log.Warn().Int64("sos", id).Int64("delivery_id", delID).Str("interface", link).Int("bytes", len(frame)).Bool("v2", v2).
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
// link is up; true when the Hub has it, or there is no Hub leg. Under C10 the
// Hub has it at its sos/ack (SOSAckReceived), and the start is published again
// until then (sosTryHubV2).
func (s *Server) sosTryHub(ctx context.Context, id int64, every time.Duration) bool {
	run, active, ok := s.sosSnapshot(id)
	if !ok || !active {
		return true
	}
	leg := run.leg(sosRouteHub)
	if leg == nil || leg.SentAt != "" {
		return true
	}
	hub := s.sosHubLink()
	if hub == nil || !hub.IsConnected() || ctx.Err() != nil {
		return false
	}
	if s.sosUseV2(run) {
		s.sosTryHubV2(hub, id, run, every)
		return false
	}
	alert, event := s.sosHubAlert(run, false)
	if err := hub.PublishSOSNow(alert, event); err != nil {
		log.Warn().Err(err).Int64("sos", id).Msg("SOS: the Hub did not take the SOS yet, trying again")
		return false
	}
	sentAt := s.clockNow().UTC().Format(time.RFC3339)
	s.sosUpdate(id, func(r *sosRun) {
		if l := r.leg(sosRouteHub); l != nil {
			l.SentAt, l.OldWay = sentAt, true
		}
	})
	log.Warn().Int64("sos", id).Str("alert", alert.ID).Msg("SOS: the Hub has the SOS over the internet")
	// Cancelled while it was being published: the Hub is told that too.
	if _, active, ok := s.sosSnapshot(id); ok && !active {
		go s.sosHubCancelLoop(id)
	}
	return true
}

// sosTryHubV2 publishes the run's C10 start on sos/event, at most once per
// backoff step (every doubling to sosRepeatMax), until the Hub's ack.
func (s *Server) sosTryHubV2(hub sosHub, id int64, run sosRun, every time.Duration) {
	now := s.clockNow()
	s.sos.mu.Lock()
	if s.sos.hubStartFor != id {
		s.sos.hubStartFor, s.sos.hubStartNext, s.sos.hubStartStep = id, time.Time{}, every
	}
	if now.Before(s.sos.hubStartNext) {
		s.sos.mu.Unlock()
		return
	}
	s.sos.hubStartNext = now.Add(s.sos.hubStartStep)
	s.sos.hubStartStep = min(2*s.sos.hubStartStep, sosRepeatMax)
	s.sos.mu.Unlock()

	// Kept before the publish: once it is under way the start may reach the Hub whatever the
	// answer, so a cancel from now on owes the Hub the C10 cancel, also after a restart in
	// between (r16 review C10-R5).
	s.sosUpdate(id, func(r *sosRun) {
		if l := r.leg(sosRouteHub); l != nil {
			l.Frame = sosFrameV2
		}
	})
	if err := hub.PublishSOSEvent(s.sosEvent(run, false), sosPubackWait); err != nil {
		log.Warn().Err(err).Int64("sos", id).Msg("SOS: the start did not reach the Hub's broker yet, trying again")
		return
	}
	at := now.UTC().Format(time.RFC3339)
	first := false
	s.sosUpdate(id, func(r *sosRun) {
		if l := r.leg(sosRouteHub); l != nil {
			if l.PublishedAt == "" {
				l.PublishedAt, first = at, true
			}
			l.Frame = sosFrameV2
		}
	})
	if first {
		log.Warn().Int64("sos", id).Str("sos_id", run.SOSID).Msg("SOS: the start is with the Hub's broker; it is published again until the Hub's ack")
	}
	// Cancelled while it was being published: the Hub is owed the cancel.
	if _, active, ok := s.sosSnapshot(id); ok && !active {
		s.sosUpdate(id, func(r *sosRun) { r.HubCancelV2 = true })
		go s.sosHubCancelLoop(id)
	}
}

// SOSAckReceived takes the Hub's ack of a start or a cancel of this Bridge's
// SOS (sos/ack, C10): the Hub has it. A start's ack ends the hub leg; a
// cancel's ends the cancellation's repeats and stops the cancel frames that
// have not gone. An ack for another run changes nothing. [MESHSAT-1584]
func (s *Server) SOSAckReceived(a hubreporter.SOSAck) {
	s.sosLoad()
	s.sos.mu.Lock()
	var id int64
	if s.sos.run != nil && s.sos.run.SOSID == a.SOSID {
		id = s.sos.run.ID
	}
	for _, p := range s.sos.prev {
		if id == 0 && p.SOSID == a.SOSID {
			id = p.ID // an earlier run still owed its cancellation (C10-R4)
		}
	}
	s.sos.mu.Unlock()
	if id == 0 {
		log.Info().Str("sos_id", a.SOSID).Str("kind", a.Kind).Msg("SOS: an ack for an SOS this Bridge no longer follows, ignored")
		return
	}
	at := s.clockNow().UTC().Format(time.RFC3339)
	switch a.Kind {
	case hubreporter.SOSEventStart:
		first := false
		s.sosUpdate(id, func(r *sosRun) {
			if l := r.leg(sosRouteHub); l != nil {
				if l.SentAt == "" {
					l.SentAt, first = at, true
				}
				l.Frame, l.HubState = sosFrameV2, a.State
			}
		})
		if first {
			log.Warn().Int64("sos", id).Str("sos_id", a.SOSID).Str("state", a.State).Int("rev", a.Rev).Msg("SOS: the Hub has the SOS")
		}
		// The Hub has the start of a run cancelled here: it is owed the cancel, even when the
		// cancel was made before anything said the start had left (C10-R5).
		owe := false
		s.sosUpdate(id, func(r *sosRun) {
			if r.CancelledAt != "" && r.SOSID != "" && r.HubCancelAckedAt == "" && !r.HubCancelGaveUp && !r.HubCancelV2 {
				r.HubCancelV2, owe = true, true
			}
		})
		if owe {
			go s.sosHubCancelLoop(id)
		}
	case hubreporter.SOSEventCancel:
		first := false
		s.sosUpdate(id, func(r *sosRun) {
			if r.CancelledAt != "" && r.HubCancelAckedAt == "" {
				r.HubCancelAckedAt, first = at, true
			}
		})
		if first {
			s.sosStopCancelFrames(id)
			log.Warn().Int64("sos", id).Str("sos_id", a.SOSID).Str("state", a.State).Msg("SOS cancelled: the Hub has the cancellation")
			s.sosForget(id)
		}
	}
}

// sosHubSMS sends the SOS frame by SMS to the Hub's number where the Hub
// uplink's bearer choice takes SMS (the satellite being the SOS's own leg),
// under the SOS's reference, so a cancellation stops it too. Once per SOS.
func (s *Server) sosHubSMS(id int64) {
	run, active, ok := s.sosSnapshot(id)
	if !ok || !active || run.HubSMSDone || s.satFallback == nil || s.uplinkBridgeID() == "" {
		return
	}
	frame, v2 := s.sosFrame(run)
	s.sosUpdate(id, func(r *sosRun) {
		r.HubSMSDone = true
		if v2 {
			r.HubSMSFrame = sosFrameV2
		}
	})
	if err := s.satFallback.PublishSOSFrame(frame, run.ref(sosRouteHubSMS)); err != nil {
		log.Warn().Err(err).Int64("sos", id).Msg("SOS: no SMS to the Hub's number")
	}
}

// sosStartWent reports whether a start's delivery went out, or may have: sent,
// delivered, being sent, or given up with its outcome unknown.
func sosStartWent(del *database.MessageDelivery) bool {
	if del == nil {
		return false
	}
	switch del.Status {
	case "sent", "delivered", "sending":
		return true
	case "dead":
		return strings.HasPrefix(del.LastError, database.UnconfirmedPrefix)
	}
	return false
}

// sosStartCarried reports, for each frame bearer, whether it carried the
// run's C10 start: the satellite leg, and the SMS to the Hub's number.
func (s *Server) sosStartCarried(run sosRun) (satellite, hubSMS bool) {
	if s.db == nil {
		return false, false
	}
	if leg := run.leg(sosRouteSatellite); leg != nil && leg.Frame == sosFrameV2 && leg.DeliveryID != 0 {
		del, err := s.db.GetDelivery(leg.DeliveryID)
		satellite = err == nil && sosStartWent(del)
	}
	if run.HubSMSFrame == sosFrameV2 {
		if dels, err := s.db.GetDeliveriesByMessage(run.ref(sosRouteHubSMS)); err == nil {
			for i := range dels {
				hubSMS = hubSMS || sosStartWent(&dels[i])
			}
		}
	}
	return satellite, hubSMS
}

// sosCancelLegs stops what of the SOS with id has not gone out, and queues
// the cancellation on the mesh when the mesh leg went out (or was going out
// when the SOS was cancelled), as MeshSat Android does; the Hub is told if it
// had the SOS. Under C10 the Hub is owed the cancel when any bearer carried
// the start under the run's identity: the follow loop repeats it until the
// Hub's ack and puts the 0x08 frame on each frame bearer that carried it
// (sosHubCancelLoop). A start that went the old way gets no frame: its frame
// stays the Hub's alert, and a second frame costs a credit. It answers the
// cancellation's legs.
func (s *Server) sosCancelLegs(id int64, text string) []sosLeg {
	s.sosEnsureID()
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
		if ids, err := s.db.WaitingDeliveryIDsByRef(run.ref(sosRouteHubSMS)); err == nil {
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
	sat, sms := s.sosStartCarried(run)
	hubLeg := run.leg(sosRouteHub)
	v2 := run.SOSID != "" && (sat || sms || (hubLeg != nil && hubLeg.Frame == sosFrameV2))
	owed := false
	s.sosUpdate(id, func(r *sosRun) {
		r.CancelLegs = cancelLegs
		r.HubCancelV2 = r.HubCancelV2 || v2
		owed = sosCancelOwed(r)
	})
	if owed {
		go s.sosHubCancelLoop(id)
	}
	return cancelLegs
}

// sosQueueCancelFrames puts the run's 0x08 on each frame bearer that carried
// its C10 start (sosStartCarried): the satellite link the start went on, and
// the SMS to the Hub's number (only for a v7 id: the Hub takes no v5 cancel
// by SMS, which anyone could work out from the bridge id and the start).
// Each bearer is owed its frame until a delivery row for it exists: a failed
// enqueue is tried again on the next round, and a row already under the
// frame's reference (a crash between the enqueue and the run's update)
// counts as queued, so no bearer gets two. FrameCancelsDone only once every
// owed bearer has its row (r16 review C10-R3). The queue tries each row until
// it goes; the Hub's ack stops those still waiting.
func (s *Server) sosQueueCancelFrames(id int64) {
	run, active, ok := s.sosSnapshot(id)
	if !ok || active || run.FrameCancelsDone {
		return
	}
	frame := s.sosCancelFrame(run)
	sat, sms := s.sosStartCarried(run)
	owed, queued := 0, 0
	var legs []sosLeg
	if leg := run.leg(sosRouteSatellite); sat {
		owed++
		ref := run.ref("cancel:" + sosRouteSatellite)
		if delID := s.sosRowFor(ref); delID != 0 {
			queued++
			legs = append(legs, sosLeg{Route: sosRouteSatellite, Interface: leg.Interface, MsgRef: ref, DeliveryID: delID, Frame: sosFrameV2})
		} else if frame != nil && s.dispatcher != nil {
			opts := sosLegOptions(database.DeliveryClassHubUplink, ref)
			opts.Payload = frame
			delID, _, err := s.dispatcher.QueueDirectSendTo(leg.Interface, run.CancelText, opts)
			if err != nil {
				log.Error().Err(err).Int64("sos", id).Str("interface", leg.Interface).Msg("SOS cancelled: the cancel frame could not be queued on the satellite; tried again")
			} else {
				queued++
				legs = append(legs, sosLeg{Route: sosRouteSatellite, Interface: leg.Interface, MsgRef: ref, DeliveryID: delID, Frame: sosFrameV2})
				log.Warn().Int64("sos", id).Int64("delivery_id", delID).Str("interface", leg.Interface).Msg("SOS cancelled: the cancel frame is queued on the satellite")
			}
		}
	}
	if sms && hubmsg.ValidV7(run.SOSID) {
		owed++
		ref := run.ref("cancel:" + sosRouteHubSMS)
		if s.sosRowFor(ref) == 0 && frame != nil && s.satFallback != nil {
			if err := s.satFallback.PublishSOSFrame(frame, ref); err != nil {
				log.Warn().Err(err).Int64("sos", id).Msg("SOS cancelled: no cancel frame by SMS to the Hub's number yet; tried again")
			}
		}
		if delID := s.sosRowFor(ref); delID != 0 {
			queued++
			legs = append(legs, sosLeg{Route: sosRouteHubSMS, MsgRef: ref, DeliveryID: delID, Frame: sosFrameV2})
		}
	}
	s.sosUpdate(id, func(r *sosRun) {
		for _, l := range legs {
			known := false
			for _, c := range r.CancelLegs {
				known = known || c.MsgRef == l.MsgRef
			}
			if !known {
				r.CancelLegs = append(r.CancelLegs, l)
			}
		}
		r.FrameCancelsDone = queued == owed
	})
}

// sosRowFor is the first delivery under ref, whatever its state; 0 without one.
func (s *Server) sosRowFor(ref string) int64 {
	if s.db == nil {
		return 0
	}
	dels, err := s.db.GetDeliveriesByMessage(ref)
	if err != nil || len(dels) == 0 {
		return 0
	}
	return dels[0].ID
}

// sosStopCancelFrames stops the run's cancel frames that have not gone: the
// Hub has the cancellation, or the follow loop gave up.
func (s *Server) sosStopCancelFrames(id int64) {
	if s.db == nil {
		return
	}
	run, _, ok := s.sosSnapshot(id)
	if !ok {
		return
	}
	for _, route := range []string{sosRouteSatellite, sosRouteHubSMS} {
		ids, err := s.db.WaitingDeliveryIDsByRef(run.ref("cancel:" + route))
		if err != nil {
			continue
		}
		for _, delID := range ids {
			if err := s.db.CancelDelivery(delID); err == nil {
				log.Info().Int64("sos", id).Str("route", route).Int64("delivery_id", delID).Msg("SOS cancelled: the cancel frame is not needed, stopped")
			}
		}
	}
}

// sosHubCancelLoop tells the Hub the SOS with id is over, while that SOS is
// still the Bridge's last: the old way once its link is up, when the start
// went the old way; under C10, the cancel event published again (backing
// off) until the Hub's ack or for sosCancelFor, and the cancel frames queued
// once the ack has not come within sosCancelAckGrace (at once with no Hub
// session). At the end, the frames still waiting are stopped. One loop per
// SOS; a restart starts it again (RestoreSOS). [MESHSAT-1446, MESHSAT-1584]
func (s *Server) sosHubCancelLoop(id int64) {
	s.sos.mu.Lock()
	if s.sos.cancelLoops[id] {
		s.sos.mu.Unlock()
		return
	}
	if s.sos.cancelLoops == nil {
		s.sos.cancelLoops = map[int64]bool{}
	}
	s.sos.cancelLoops[id] = true
	s.sos.mu.Unlock()
	defer func() {
		s.sos.mu.Lock()
		delete(s.sos.cancelLoops, id)
		s.sos.mu.Unlock()
		s.sosForget(id) // an earlier run whose cancellation is done
	}()

	every, repeatMax, cancelFor, grace := sosFollowEvery, sosRepeatMax, sosCancelFor, sosCancelAckGrace
	step := every
	var next time.Time
	for {
		run, active, ok := s.sosSnapshot(id)
		if !ok || active {
			return
		}
		now := s.clockNow()
		cancelledAt, err := time.Parse(time.RFC3339, run.CancelledAt)
		if err != nil {
			cancelledAt = now
		}
		oldWay := run.HubCancelSentAt == "" && sosHubHadOldWay(&run)
		v2 := sosV2CancelOwed(&run)
		if v2 && now.Sub(cancelledAt) >= cancelFor {
			log.Warn().Int64("sos", id).Str("sos_id", run.SOSID).Msg("SOS cancelled: no ack from the Hub in 24 h; the cancellation is no longer repeated")
			s.sosStopCancelFrames(id)
			s.sosUpdate(id, func(r *sosRun) { r.HubCancelGaveUp = true })
			v2 = false
		}
		if !oldWay && !v2 {
			return
		}
		hub := s.sosHubLink()
		up := hub != nil && hub.IsConnected()
		if oldWay && up {
			alert, event := s.sosHubAlert(run, true)
			err := hub.PublishSOSNow(alert, event)
			if err == nil {
				sentAt := now.UTC().Format(time.RFC3339)
				s.sosUpdate(id, func(r *sosRun) { r.HubCancelSentAt = sentAt })
				log.Warn().Int64("sos", id).Msg("SOS cancelled: the Hub is told")
			} else if !errors.Is(err, hubreporter.ErrNotConnected) {
				log.Warn().Err(err).Int64("sos", id).Msg("SOS cancelled: the Hub did not take the cancellation yet")
			}
		}
		if v2 && up && !now.Before(next) {
			next, step = now.Add(step), min(2*step, repeatMax)
			if err := hub.PublishSOSEvent(s.sosEvent(run, true), sosPubackWait); err == nil {
				at := now.UTC().Format(time.RFC3339)
				s.sosUpdate(id, func(r *sosRun) {
					if r.HubCancelPublishedAt == "" {
						r.HubCancelPublishedAt = at
					}
				})
			} else if !errors.Is(err, hubreporter.ErrNotConnected) {
				log.Warn().Err(err).Int64("sos", id).Msg("SOS cancelled: the cancellation did not reach the Hub's broker yet")
			}
		}
		if v2 && !run.FrameCancelsDone && (!up || now.Sub(cancelledAt) >= grace) {
			s.sosQueueCancelFrames(id)
		}
		time.Sleep(every)
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
		case leg.Route == sosRouteHub && leg.PublishedAt != "":
			st.Status = "sending" // with the Hub's broker, the Hub's ack not yet in
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
