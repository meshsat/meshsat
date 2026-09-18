package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/oob"
)

// rtlSDRReset is the RTL-SDR's level-3 heal, shared by the device-health
// ladder, an OOB `RESET rtl_sdr 3` and the Hub's mgmt_reset. [MESHSAT-1222]
//
// It stops all scanning first so nothing holds or re-opens the dongle,
// power-cycles its hub port through the host agent, and then checks what the
// agent cannot: that the dongle left the bus and came back. Scanning resumes
// only after the dongle has settled. There is deliberately no USB-reset
// fallback: on the RTL-SDR Blog V4 a USB reset of a stalled dongle is what
// left parallax's dongle unable to enumerate on 17 Sep 2026, through four
// later port cuts and an xHCI rebind.
type rtlSDRReset struct {
	// mu serialises heals: the watchdog and an OOB RESET can arrive
	// together, and the second must not resume scanning under the first
	// one's cut.
	mu         *sync.Mutex
	suspend    func(context.Context)
	resume     func()
	powerCycle func(ctx context.Context) bool
	present    func() bool

	gone   time.Duration // how long the scheduled cut gets to take the dongle off the bus
	back   time.Duration // how long re-enumeration may take
	settle time.Duration // quiet time before the reader opens it again
	poll   time.Duration
}

func newRTLSDRReset(suspend func(context.Context), resume func(), powerCycle func(context.Context) bool, present func() bool) rtlSDRReset {
	return rtlSDRReset{
		mu:         &sync.Mutex{},
		suspend:    suspend,
		resume:     resume,
		powerCycle: powerCycle,
		present:    present,
		gone:       8 * time.Second,
		back:       30 * time.Second,
		settle:     3 * time.Second,
		poll:       250 * time.Millisecond,
	}
}

var (
	errRTLSDRNoSwitchablePort = errors.New("rtl-sdr: no switchable hub port to power-cycle; a USB reset is not used, it wedges the Blog V4")
	errRTLSDRNeverLeft        = errors.New("rtl-sdr: the port power cycle was scheduled but the dongle never left the bus")
	errRTLSDRNotBack          = errors.New("rtl-sdr: the dongle did not re-enumerate after the port power cycle")
)

func (r rtlSDRReset) run(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.suspend(ctx)
	defer r.resume()

	wasPresent := r.present()
	if !r.powerCycle(ctx) {
		return errRTLSDRNoSwitchablePort
	}
	// The agent schedules the cut in a detached unit and returns at once.
	if wasPresent && !waitUntil(ctx, r.gone, r.poll, func() bool { return !r.present() }) {
		return errRTLSDRNeverLeft
	}
	if !waitUntil(ctx, r.back, r.poll, r.present) {
		return errRTLSDRNotBack
	}
	log.Info().Dur("settle", r.settle).Msg("rtl-sdr: back on the bus after the port power cycle")
	t := time.NewTimer(r.settle)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
	}
	return nil
}

// waitUntil polls cond until it holds or d runs out.
func waitUntil(ctx context.Context, d, poll time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		t := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return false
		case <-t.C:
		}
	}
}

// rtlSDRLocationKey holds the hub port the RTL-SDR was last seen on.
const rtlSDRLocationKey = "rtl_sdr_usb_location"

// newRTLSDRLocation persists the RTL-SDR's last hub port in system_config,
// writing only when it changes. [MESHSAT-1222]
func newRTLSDRLocation(db *database.DB) rtlSDRLocation {
	if db == nil {
		return rtlSDRLocation{}
	}
	var mu sync.Mutex
	last := ""
	return rtlSDRLocation{
		get: func() string {
			v, err := db.GetSystemConfig(rtlSDRLocationKey)
			if err != nil {
				return ""
			}
			return v
		},
		set: func(p string) {
			mu.Lock()
			defer mu.Unlock()
			if p == last {
				return
			}
			if err := db.SetSystemConfig(rtlSDRLocationKey, p); err != nil {
				log.Warn().Err(err).Str("location", p).Msg("rtl-sdr: could not remember the hub port")
				return
			}
			last = p
		},
	}
}

// usbLastLocation is the hub port a device role was last seen on: the OOB
// service's probe for any role, and for the RTL-SDR also the port persisted
// across restarts.
func usbLastLocation(svc *oob.Service, db *database.DB, dev string) string {
	if svc != nil {
		if l := svc.LastUSBLocation(dev); l != "" {
			return l
		}
	}
	if dev == "rtl_sdr" {
		if loc := newRTLSDRLocation(db); loc.get != nil {
			return loc.get()
		}
	}
	return ""
}
