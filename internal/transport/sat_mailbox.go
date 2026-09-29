package transport

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// The mailbox check a person asks for ("Check Mailbox" on Home and the
// Satellite page), as MeshSat Android runs it (IridiumSpp.checkMailbox,
// bt/IridiumSpp.kt at v2.19.4): one billed SBDIX, a message already in the
// MT buffer read first for free, the MO buffer emptied before the session,
// and no session within SBDIXHold of one that ended with MO status 32 or 36
// or lost its link. Before this the Bridge ran its ring-alert path on the
// HTTP request's context, which could skip the session ("no reason") and
// then record a successful one, and kept no outcome.

// Kinds of MailboxResult.
const (
	MailboxNotConnected  = "not_connected"
	MailboxHeld          = "held"
	MailboxSessionFailed = "session_failed"
	MailboxNoAnswer      = "no_answer"
	MailboxLinkLost      = "link_lost"
	MailboxChecked       = "checked"
)

// SBDIXHold is how long no mailbox check a person asks for opens a session
// after one that ended with MO status 32 (no network service) or 36 (wait 3
// minutes since the last registration), or whose link failed under it: the
// ISU registers at most once every 3 minutes (Android's SBDIX_HOLD_MS).
const SBDIXHold = 180 * time.Second

// MOStatusUnknown is the mo_status of a result no session answered: none
// ran, or its answer never came (link_lost). Negative, so it never collides
// with an +SBDIX code (Android's MO_LINK_LOST).
const MOStatusUnknown = -1

// MailboxResult is the outcome of a mailbox check a person asked for, as
// GET /api/iridium/mailbox and the "mailbox" event report it.
type MailboxResult struct {
	// Kind is not_connected, held, session_failed, no_answer, link_lost or checked.
	Kind string `json:"kind"`
	// Seconds until a session may run again, rounded up (held); else 0.
	Seconds int `json:"seconds"`
	// MOStatus is the session's +SBDIX MO status (0-4 sent, 32 no network,
	// ...); MOStatusUnknown when no session answered.
	MOStatus int `json:"mo_status"`
	// Received is how many messages the check handed over, the free read of
	// the MT buffer included (checked).
	Received int `json:"received"`
	// StillQueued is how many more wait at the gateway (checked).
	StillQueued int `json:"still_queued"`
}

// MailboxCheckOutcome is what a transport's CheckMailboxNow returns.
type MailboxCheckOutcome struct {
	Result MailboxResult
	// Messages are the MT payloads the check fetched, the free read first.
	// The caller hands them over whatever Result says.
	Messages [][]byte
	// SessionAnswered is true when an SBDIX ran and the modem answered it
	// with an MO status: only such a session goes into the GSS history.
	SessionAnswered bool
}

// MailboxNowChecker is a satellite transport that runs the mailbox check a
// person asks for itself, under its own session and serial locks. The ctx
// is the gateway's own, never an HTTP request's: the check has to outlive
// the request that started it (the rate-limit wait before an SBDIX
// honours it).
type MailboxNowChecker interface {
	CheckMailboxNow(ctx context.Context) MailboxCheckOutcome
}

// holdSBDIXUntil moves the hold to until when that is later. Caller holds t.mu.
func (t *DirectSatTransport) holdSBDIXUntil(until time.Time) {
	if until.After(t.sbdixHeldUntil) {
		t.sbdixHeldUntil = until
	}
}

// clearMOLocked empties the MO buffer (AT+SBDD0). True when the modem did
// not refuse: it answers 0 (cleared) or 1 (error), then OK. Caller holds
// t.sessionMu and t.mu.
func (t *DirectSatTransport) clearMOLocked() bool {
	if !t.connected || t.file == nil {
		return false
	}
	resp, err := sendAT(t.file, "AT+SBDD0", iridiumReadTimeout)
	if err != nil || strings.Contains(resp, "ERROR") {
		return false
	}
	for _, line := range strings.Split(resp, "\n") {
		if strings.TrimSpace(line) == "1" {
			return false
		}
	}
	return true
}

// CheckMailboxNow runs the mailbox check a person asked for. It opens
// exactly one satellite session, billed (at least one credit even when
// nothing waits), unless the modem is not connected, the hold after a
// failed session runs, the modem does not answer the free status check, or
// a message waiting in the MT buffer cannot be read.
// Holds the session lock for the whole check (see sessionMu), so it queues
// behind a send that has loaded the MO buffer until that send's session has
// ended, and no send loads the buffer until this check's session has ended;
// and mu except where readMTLocked and sbdixLocked let go of it.
func (t *DirectSatTransport) CheckMailboxNow(ctx context.Context) MailboxCheckOutcome {
	t.sessionMu.Lock()
	defer t.sessionMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()

	var out MailboxCheckOutcome
	out.Result.MOStatus = MOStatusUnknown
	if !t.connected || t.file == nil || ctx.Err() != nil {
		out.Result.Kind = MailboxNotConnected
		return out
	}
	if left := time.Until(t.sbdixHeldUntil); left > 0 {
		out.Result.Kind = MailboxHeld
		out.Result.Seconds = int((left + time.Second - 1) / time.Second)
		return out
	}

	// Free: what the modem holds, and what it last heard the gateway holds.
	resp, err := sendAT(t.file, "AT+SBDSX", 5*time.Second)
	if err != nil {
		log.Warn().Err(err).Msg("iridium: mailbox check: SBDSX failed, forcing serial reconnect")
		t.disconnectLocked()
		out.Result.Kind = MailboxNoAnswer
		return out
	}
	status, err := parseSBDSX(resp)
	if err != nil {
		log.Warn().Err(err).Msg("iridium: mailbox check: SBDSX answer unreadable, no session")
		out.Result.Kind = MailboxNoAnswer
		return out
	}
	t.lastReply.Store(time.Now().UnixNano())

	// A message already in the MT buffer is read first, for free. A session
	// empties the MT buffer, so when that read fails no session is opened:
	// the message stays in the modem for the next read (as the ring-alert
	// path, which never opens a session while one waits), where a session
	// would lose it.
	if status.MTFlag {
		data, err := t.readMTLocked()
		if err != nil {
			log.Warn().Err(err).Msg("iridium: mailbox check: reading the waiting MT failed, no session")
			out.Result.Kind = MailboxNoAnswer
			return out
		}
		if len(data) > 0 {
			out.Messages = append(out.Messages, data)
		}
	}

	// The MO buffer is emptied before the session: outgoing messages belong
	// to the delivery queue, which keeps them for its retry, and one sent
	// from here went out again on that retry (Android, open question 16).
	// No send is between its load and its session now: each holds the
	// session lock from one to the other, and this check holds it, so what
	// the buffer holds is left over from a send that has already returned.
	// A buffer the modem would not clear is never sent.
	if !t.clearMOLocked() && status.MOFlag {
		log.Warn().Msg("iridium: mailbox check: the MO buffer could not be cleared, no session")
		out.Result.Kind = MailboxNoAnswer
		return out
	}

	log.Info().Bool("mt_was_waiting", status.MTFlag).Bool("mo_was_waiting", status.MOFlag).
		Int("gateway_waiting", status.MTWaiting).Msg("iridium: mailbox check: opening a session")
	t.lastGSSSync = time.Now()
	res, err := t.sbdixLocked(ctx)
	// Every session leaves the MO buffer empty, as Send and MailboxCheck do.
	if t.connected && t.file != nil {
		sendAT(t.file, "AT+SBDD0", 3*time.Second)
	}
	if err != nil {
		switch {
		case errors.Is(err, errSBDIXLinkLost):
			// The hold is already set, from the session's start.
			out.Result.Kind = MailboxLinkLost
		case errors.Is(err, errSBDIXReadTimeout), errors.Is(err, errSBDIXTooLarge):
			// The session went out; its answer never came.
			out.Result.Kind = MailboxNoAnswer
		case ctx.Err() != nil, errors.Is(err, ErrNotConnected), !t.connected, t.file == nil:
			// No session went out: the gateway stopped, or the link went
			// down (or was reopened) during the rate-limit wait.
			out.Result.Kind = MailboxNotConnected
		default:
			// SBDIX could not be written, or its answer not read.
			out.Result.Kind = MailboxNoAnswer
		}
		log.Warn().Err(err).Str("kind", out.Result.Kind).Msg("iridium: mailbox check: the session did not answer")
		return out
	}

	out.SessionAnswered = true
	out.Result.MOStatus = res.MOStatus
	// A message that came in with the session is read now, before another
	// session overwrites it.
	if res.MTStatus == 1 && t.connected && t.file != nil {
		if data, err := t.readMTLocked(); err != nil {
			log.Warn().Err(err).Msg("iridium: mailbox check: reading the session's MT failed")
		} else if len(data) > 0 {
			out.Messages = append(out.Messages, data)
		}
	}
	if !res.MOSuccess() {
		out.Result.Kind = MailboxSessionFailed
		return out
	}
	out.Result.Kind = MailboxChecked
	out.Result.Received = len(out.Messages)
	out.Result.StillQueued = res.MTQueued
	return out
}
