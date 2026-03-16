package engine

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// DeadManSwitch triggers an SOS callback if no user activity is detected
// within the configured timeout. It checks every 60 seconds whether the
// elapsed time since the last Touch() exceeds the timeout threshold.
type DeadManSwitch struct {
	db          *database.DB
	timeout     time.Duration
	lastActive  atomic.Int64
	enabled     atomic.Bool
	triggered   atomic.Bool
	sosCallback func(lat, lon float64, lastSeen time.Time)
	cancel      context.CancelFunc
}

// NewDeadManSwitch creates a dead man's switch with the given timeout.
func NewDeadManSwitch(db *database.DB, timeout time.Duration) *DeadManSwitch {
	d := &DeadManSwitch{
		db:      db,
		timeout: timeout,
	}
	d.lastActive.Store(time.Now().Unix())
	return d
}

// Start begins the background check loop. It runs every 60 seconds and
// fires the SOS callback if enabled and the timeout has elapsed since
// the last Touch(). The callback is only fired once until Touch() resets it.
func (d *DeadManSwitch) Start(ctx context.Context) {
	ctx, d.cancel = context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.check()
			}
		}
	}()
	log.Info().Dur("timeout", d.timeout).Msg("dead man's switch started")
}

// Stop cancels the background check loop.
func (d *DeadManSwitch) Stop() {
	if d.cancel != nil {
		d.cancel()
	}
}

// Touch resets the activity timer. Should be called on any user activity
// (message sent, button press, etc.). Also clears the triggered flag so
// the SOS can fire again after a subsequent timeout.
func (d *DeadManSwitch) Touch() {
	d.lastActive.Store(time.Now().Unix())
	d.triggered.Store(false)
}

// SetEnabled enables or disables the dead man's switch.
func (d *DeadManSwitch) SetEnabled(enabled bool) {
	d.enabled.Store(enabled)
}

// IsTriggered returns true if the SOS callback has been fired and
// Touch() has not been called since.
func (d *DeadManSwitch) IsTriggered() bool {
	return d.triggered.Load()
}

// SetSOSCallback sets the function to call when the timeout expires.
func (d *DeadManSwitch) SetSOSCallback(fn func(lat, lon float64, lastSeen time.Time)) {
	d.sosCallback = fn
}

// IsEnabled returns whether the dead man's switch is enabled.
func (d *DeadManSwitch) IsEnabled() bool {
	return d.enabled.Load()
}

// GetTimeout returns the current timeout duration.
func (d *DeadManSwitch) GetTimeout() time.Duration {
	return d.timeout
}

// SetTimeout updates the timeout duration.
func (d *DeadManSwitch) SetTimeout(t time.Duration) {
	d.timeout = t
}

// LastActivity returns the unix timestamp of the last Touch().
func (d *DeadManSwitch) LastActivity() int64 {
	return d.lastActive.Load()
}

func (d *DeadManSwitch) check() {
	if !d.enabled.Load() {
		return
	}
	if d.triggered.Load() {
		return
	}

	lastActive := d.lastActive.Load()
	elapsed := time.Now().Unix() - lastActive
	if elapsed <= int64(d.timeout.Seconds()) {
		return
	}

	d.triggered.Store(true)
	lastSeen := time.Unix(lastActive, 0)
	log.Warn().Time("last_active", lastSeen).Msg("dead man's switch triggered")

	// Fetch last known GPS position from the positions table
	var lat, lon float64
	pos, err := d.db.GetLatestGPSPosition()
	if err == nil && pos != nil {
		lat = pos.Lat
		lon = pos.Lon
	}

	if d.sosCallback != nil {
		d.sosCallback(lat, lon, lastSeen)
	}
}
