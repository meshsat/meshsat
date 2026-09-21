package hubreporter

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
)

// MTPoller makes sure messages waiting at Iridium for this kit come down.
//
// A RockBLOCK 9704 has no mailbox check (Ground Control's library offers
// none): the network pushes a queued MT when the modem is in a session, and
// under a sky that buildings hide most of the time that in practice means
// the kit's own MO. On 21 Sep 2026 a kit-to-kit relay and two Hub pings
// waited at Iridium for over an hour while the kit flickered between 0 and
// 4 bars, and came down in the same second a short MO from that kit opened
// a session. So when the kit has been quiet on the satellite for a while,
// the poller sends one small MO: a health summary for the Hub, which the Hub
// takes off before its relay, so nothing reaches the other kit.
type MTPoller struct {
	cfg      MTPollConfig
	lastSent time.Time
}

// MTPollConfig wires the poller to the kit.
type MTPollConfig struct {
	// Interval is the longest the satellite may stay quiet before a poll
	// goes out. 0 disables the poller.
	Interval time.Duration
	// Connected reports whether the satellite modem is up.
	Connected func() bool
	// LastActivity is the last successful MO or MT on the modem.
	LastActivity func() time.Time
	// Pending counts deliveries waiting on the satellite channel. Any of
	// them opens a session when it goes, so no poll is needed.
	Pending func() int
	// Frame builds the poll frame and Send queues it.
	Frame func() []byte
	Send  func(frame []byte) error
	Now   func() time.Time
}

// NewMTPoller builds a poller.
func NewMTPoller(cfg MTPollConfig) *MTPoller {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &MTPoller{cfg: cfg}
}

// Tick sends one poll if one is due and reports why it did or did not.
func (p *MTPoller) Tick() (sent bool, reason string) {
	now := p.cfg.Now()
	switch {
	case p.cfg.Interval <= 0:
		return false, "disabled"
	case p.cfg.Connected != nil && !p.cfg.Connected():
		return false, "modem not connected"
	case p.cfg.Pending != nil && p.cfg.Pending() > 0:
		return false, "a send is waiting and will open the session"
	}
	if last := p.cfg.LastActivity; last != nil {
		if t := last(); !t.IsZero() && now.Sub(t) < p.cfg.Interval {
			return false, "the modem was in a session recently"
		}
	}
	if !p.lastSent.IsZero() && now.Sub(p.lastSent) < p.cfg.Interval {
		return false, "a poll went out recently"
	}
	frame := p.cfg.Frame()
	if err := p.cfg.Send(frame); err != nil {
		log.Warn().Err(err).Msg("imt: MT poll not queued")
		return false, "send failed"
	}
	p.lastSent = now
	log.Info().Int("bytes", len(frame)).Dur("interval", p.cfg.Interval).
		Msg("imt: MT poll queued, a short MO to fetch anything waiting at Iridium")
	return true, "sent"
}

// Run checks once a minute until ctx ends.
func (p *MTPoller) Run(ctx context.Context) {
	if p.cfg.Interval <= 0 {
		return
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Tick()
		}
	}
}
