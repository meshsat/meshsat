package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/transport"
)

// The mailbox check a person asks for (POST /api/iridium/mailbox/check), as
// MeshSat Android runs it in its service (GatewayService.startMailboxCheck
// and MailboxCheck at v2.19.4): one check at a time, run to the end on the
// gateway's own context whoever asked, its outcome kept for every page that
// shows it and announced as a "mailbox" event.

// ErrMailboxCheckRunning is returned while a mailbox check runs.
var ErrMailboxCheckRunning = errors.New("a mailbox check is already running")

// ErrNoIridiumGateway is returned when no Iridium gateway runs to check the
// mailbox with. The kept outcome becomes not_connected, as on Android.
var ErrNoIridiumGateway = errors.New("iridium gateway not running")

// EventDataEmitFunc emits an SSE event whose data field carries a JSON
// object, for the events a client reads as state rather than as a line of
// text (the mailbox check). Optional: without it the manager falls back to
// EventEmitFunc and the event has no data.
type EventDataEmitFunc func(eventType, message string, data json.RawMessage)

// MailboxCheckState is the mailbox check a person asks for: whether one
// runs, and the outcome of the last one. GET /api/iridium/mailbox answers
// it, and every "mailbox" event with data carries it.
type MailboxCheckState struct {
	Running bool `json:"running"`
	// Result is the last check's outcome; null before the first check and
	// while one runs (as Android's MailboxCheck).
	Result *transport.MailboxResult `json:"result"`
	// FinishedAt is when that check ended (RFC3339, UTC); null whenever
	// Result is.
	FinishedAt *string `json:"finished_at"`
}

// mailboxCheck is the manager's copy of that state.
type mailboxCheck struct {
	mu         sync.Mutex
	running    bool
	result     *transport.MailboxResult
	finishedAt time.Time
}

func (c *mailboxCheck) stateLocked() MailboxCheckState {
	s := MailboxCheckState{Running: c.running}
	if c.result != nil {
		r := *c.result
		s.Result = &r
	}
	if !c.finishedAt.IsZero() {
		at := c.finishedAt.UTC().Format(time.RFC3339)
		s.FinishedAt = &at
	}
	return s
}

func (c *mailboxCheck) finishLocked(res transport.MailboxResult) MailboxCheckState {
	c.running = false
	c.result = &res
	c.finishedAt = time.Now()
	return c.stateLocked()
}

// mailboxNowGateway is a gateway that runs the check itself.
type mailboxNowGateway interface {
	CheckMailboxNow() transport.MailboxResult
}

// SetEventDataEmitFunc sets the callback for SSE events that carry data.
func (m *Manager) SetEventDataEmitFunc(fn EventDataEmitFunc) {
	m.onEventData = fn
}

// GetMailboxCheck returns whether a mailbox check runs and the last outcome.
func (m *Manager) GetMailboxCheck() MailboxCheckState {
	m.mailbox.mu.Lock()
	defer m.mailbox.mu.Unlock()
	return m.mailbox.stateLocked()
}

// StartMailboxCheck starts the mailbox check a person asked for and returns
// at once; the check runs to the end on the gateway's own context and its
// outcome is read from GetMailboxCheck. It runs on the SBD (9603) gateway
// when one runs, else on the IMT (9704) gateway. ErrMailboxCheckRunning
// while one runs; ErrNoIridiumGateway, with the outcome not_connected, when
// neither runs. A hold after a failed session is an outcome (held), not an
// error.
func (m *Manager) StartMailboxCheck() error {
	m.mailbox.mu.Lock()
	if m.mailbox.running {
		m.mailbox.mu.Unlock()
		return ErrMailboxCheckRunning
	}
	gw := m.mailboxGateway()
	if gw == nil {
		state := m.mailbox.finishLocked(transport.MailboxResult{
			Kind: transport.MailboxNotConnected, MOStatus: transport.MOStatusUnknown,
		})
		m.mailbox.mu.Unlock()
		m.emitMailbox(state)
		return ErrNoIridiumGateway
	}
	// As Android's MailboxCheck(running = true): no result while one runs.
	m.mailbox.running = true
	m.mailbox.result = nil
	m.mailbox.finishedAt = time.Time{}
	state := m.mailbox.stateLocked()
	m.mailbox.mu.Unlock()
	m.emitMailbox(state)

	go func() {
		res := gw.CheckMailboxNow()
		m.mailbox.mu.Lock()
		state := m.mailbox.finishLocked(res)
		m.mailbox.mu.Unlock()
		log.Info().Str("kind", res.Kind).Int("seconds", res.Seconds).Int("mo_status", res.MOStatus).
			Int("received", res.Received).Int("still_queued", res.StillQueued).Msg("iridium: mailbox check finished")
		m.emitMailbox(state)
	}()
	return nil
}

// mailboxGateway is the gateway a mailbox check runs on: an SBD (9603)
// gateway first, an IMT (9704) one when no SBD gateway runs, the lowest
// instance id of each kind, so the choice never depends on map order.
func (m *Manager) mailboxGateway() mailboxNowGateway {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.running))
	for id := range m.running {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var imt *IMTGateway
	for _, id := range ids {
		switch gw := m.running[id].(type) {
		case *SBDGateway:
			if gw != nil {
				return gw
			}
		case *IMTGateway:
			if gw != nil && imt == nil {
				imt = gw
			}
		}
	}
	if imt != nil {
		return imt
	}
	return nil
}

// emitMailbox announces the check's state as a "mailbox" event: on start
// (running true) and when it ends (running false, with the result).
func (m *Manager) emitMailbox(state MailboxCheckState) {
	msg := "Mailbox check started"
	if !state.Running && state.Result != nil {
		msg = describeMailboxResult(*state.Result)
	}
	if m.onEventData != nil {
		data, err := json.Marshal(state)
		if err == nil {
			m.onEventData("mailbox", msg, data)
			return
		}
	}
	if m.onEventEmit != nil {
		m.onEventEmit("mailbox", msg)
	}
}

// describeMailboxResult is the event's line of text, in MeshSat Android's
// words (describeMailboxResult, ui/components/CheckMailboxButton.kt).
func describeMailboxResult(r transport.MailboxResult) string {
	switch r.Kind {
	case transport.MailboxNotConnected:
		return "Mailbox check: the modem is not connected."
	case transport.MailboxHeld:
		return fmt.Sprintf("Mailbox check held after a failed session: try again in %d s.", r.Seconds)
	case transport.MailboxSessionFailed:
		if r.MOStatus == 32 {
			return "Mailbox check: no network, the modem sees no satellite. No credit used."
		}
		return fmt.Sprintf("Mailbox check: the session failed (status %d).", r.MOStatus)
	case transport.MailboxNoAnswer:
		return "Mailbox check: the modem did not answer."
	case transport.MailboxLinkLost:
		return "Mailbox check: the link to the modem dropped during the session; what it fetched is unknown."
	case transport.MailboxChecked:
		var s string
		switch r.Received {
		case 0:
			s = "Mailbox check: no new messages."
		case 1:
			s = "Mailbox check: 1 message received."
		default:
			s = fmt.Sprintf("Mailbox check: %d messages received.", r.Received)
		}
		if r.StillQueued > 0 {
			s += fmt.Sprintf(" %d more waiting.", r.StillQueued)
		}
		return s
	}
	return "Mailbox check finished."
}

// CheckMailboxNow runs the mailbox check a person asked for on the 9603 and
// returns its outcome. It runs on the gateway's own context: the request
// that asked has long been answered. The messages it fetched are handed
// over whatever the outcome, and a session goes into the GSS history only
// when the modem answered it.
func (g *SBDGateway) CheckMailboxNow() transport.MailboxResult {
	ctx := g.runContext()
	fc, ok := g.sat.(transport.MailboxNowChecker)
	if !ok {
		return g.checkMailboxViaTransport(ctx)
	}
	out := fc.CheckMailboxNow(ctx)
	for _, data := range out.Messages {
		g.deliverMT(data)
	}
	if out.SessionAnswered {
		mo := out.Result.MOStatus
		g.recordGSSRegistration((&transport.SatResult{MOStatus: mo}).MOSuccess(), mo)
	}
	switch out.Result.Kind {
	case transport.MailboxNoAnswer, transport.MailboxLinkLost:
		g.errors.Add(1)
	}
	return out.Result
}

// checkMailboxViaTransport is the check for an SBD transport that cannot
// force a session itself (the HAL's): its own mailbox check, which opens the
// session the HAL decides on, then the message that came in.
func (g *IridiumGateway) checkMailboxViaTransport(ctx context.Context) transport.MailboxResult {
	res := transport.MailboxResult{MOStatus: transport.MOStatusUnknown}
	if st, err := g.sat.GetStatus(ctx); err != nil || st == nil || !st.Connected {
		res.Kind = transport.MailboxNotConnected
		return res
	}
	r, err := g.sat.MailboxCheck(ctx)
	if err != nil {
		if errors.Is(err, transport.ErrNotConnected) {
			res.Kind = transport.MailboxNotConnected
		} else {
			res.Kind = transport.MailboxNoAnswer
			g.errors.Add(1)
		}
		return res
	}
	if !r.NoSession {
		res.MOStatus = r.MOStatus
		g.recordGSSRegistration(r.MOSuccess(), r.MOStatus)
	}
	if r.MTStatus == 1 && r.MTLength > 0 {
		if data, err := g.sat.Receive(ctx); err != nil {
			log.Warn().Err(err).Msg("iridium: mailbox check: receive failed")
		} else if len(data) > 0 {
			g.deliverMT(data)
			res.Received = 1
		}
	}
	if !r.NoSession && !r.MOSuccess() {
		res.Kind = transport.MailboxSessionFailed
		return res
	}
	res.Kind = transport.MailboxChecked
	res.StillQueued = r.MTQueued
	return res
}

// CheckMailboxNow on the 9704: the modem fetches its messages by itself
// whenever it sees a satellite, so no session is opened (and none recorded);
// the check hands over the messages the transport holds.
func (g *IMTGateway) CheckMailboxNow() transport.MailboxResult {
	ctx := g.runContext()
	res := transport.MailboxResult{MOStatus: transport.MOStatusUnknown}
	if st, err := g.sat.GetStatus(ctx); err != nil || st == nil || !st.Connected {
		res.Kind = transport.MailboxNotConnected
		return res
	}
	// Moves what the modem announced into the transport's queues.
	if _, err := g.sat.MailboxCheck(ctx); err != nil {
		log.Warn().Err(err).Msg("imt: mailbox check: poll failed")
	}
	res.Kind = transport.MailboxChecked
	res.Received = g.receivePendingMT(ctx)
	return res
}
