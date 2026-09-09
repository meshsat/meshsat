package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// DeadManSwitch triggers an SOS callback if no user activity is detected
// within the configured timeout. It checks every 60 seconds whether the
// elapsed time since the last Touch() exceeds the timeout threshold.
type DeadManSwitch struct {
	db         *database.DB
	lastActive atomic.Int64
	enabled    atomic.Bool
	triggered  atomic.Bool
	cancel     context.CancelFunc

	// mu guards the two fields that are not atomics. Both are written by
	// setters on an API request and read by check() on the ticker goroutine,
	// which is a data race the race detector will find the moment a test
	// exercises Start() rather than calling check() directly.
	mu          sync.RWMutex
	timeout     time.Duration
	sosCallback func(lat, lon float64, lastSeen time.Time)
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
//
// Nothing called this outside the tests until MESHSAT-996: the switch armed,
// counted down, logged "triggered" and sent nothing, while the test suite
// stayed green because every test injects its own callback.
func (d *DeadManSwitch) SetSOSCallback(fn func(lat, lon float64, lastSeen time.Time)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sosCallback = fn
}

// IsEnabled returns whether the dead man's switch is enabled.
func (d *DeadManSwitch) IsEnabled() bool {
	return d.enabled.Load()
}

// GetTimeout returns the current timeout duration.
func (d *DeadManSwitch) GetTimeout() time.Duration {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.timeout
}

// SetTimeout updates the timeout duration.
func (d *DeadManSwitch) SetTimeout(t time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
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

	d.mu.RLock()
	timeout, cb := d.timeout, d.sosCallback
	d.mu.RUnlock()

	lastActive := d.lastActive.Load()
	elapsed := time.Now().Unix() - lastActive
	if elapsed <= int64(timeout.Seconds()) {
		return
	}

	d.triggered.Store(true)
	lastSeen := time.Unix(lastActive, 0)
	log.Warn().Time("last_active", lastSeen).Msg("dead man's switch triggered")

	// The position the callback is handed is a fallback only. GetLatestGPSPosition
	// reads the newest row in `positions`, which is shared with mesh peers and
	// carries no node_id filter, so it can hand back a neighbour's coordinates as
	// though they were ours. The SOS path prefers its own GPS reader and only
	// falls back to this. [MESHSAT-996]
	var lat, lon float64
	pos, err := d.db.GetLatestGPSPosition()
	if err == nil && pos != nil {
		lat = pos.Lat
		lon = pos.Lon
	}

	if cb == nil {
		log.Error().Msg("dead man's switch fired with no SOS callback wired: nothing was sent")
		return
	}
	cb(lat, lon, lastSeen)
}
