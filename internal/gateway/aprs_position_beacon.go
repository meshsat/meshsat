package gateway

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/selfpos"
)

// The APRS position beacon: the device's own position as an APRS position
// report, sent to APRS-IS only. It follows MeshSat Android's AprsBeacon
// (smart beaconing): the slow rate while standing, 90 s while moving, and at
// once on a turn. Android also beacons over RF in KISS mode, from a switch
// its KISS screen hides; the Bridge never does (spec Q2). The kits' status
// beacon (beacon_secs) is a different thing and is untouched. [MESHSAT-1421]

// Smart-beaconing constants, Android's AprsBeacon companion object.
const (
	positionBeaconSlowDefault = 600 * time.Second // DEFAULT_SLOW_RATE_SEC: 10 min standing
	positionBeaconFastRate    = 90 * time.Second  // DEFAULT_FAST_RATE_SEC: moving
	positionBeaconMinInterval = 60 * time.Second  // MIN_BEACON_INTERVAL_SEC: APRS courtesy
	positionBeaconSpeedMPS    = 2.0               // SPEED_THRESHOLD_MPS: about 7 km/h is moving
	positionBeaconCornerDeg   = 30.0              // HEADING_CHANGE_DEG: the corner peg
)

// positionBeaconCheck is how often the beacon looks at the position,
// Android's 10 s loop. A package var so tests can shorten it.
var positionBeaconCheck = 10 * time.Second

// positionBeaconSlowRate is the standing interval for position_beacon_min:
// max(min*60, 60) s, as Android's service sets slowRateSec.
func positionBeaconSlowRate(minutes int) time.Duration {
	d := time.Duration(minutes) * time.Minute
	if d < positionBeaconMinInterval {
		d = positionBeaconMinInterval
	}
	return d
}

// positionBeacon is the smart-beaconing state of one gateway.
type positionBeacon struct {
	slow        time.Duration
	lastBeacon  time.Time
	lastHeading float64
}

// due reports whether fix goes out as a beacon at now. It folds Android's
// two paths into the one periodic look: the corner peg of onLocationUpdate
// (moving, a beacon sent before, the course 30° or more off the last one
// seen, 60 s since the last beacon) and the timer of checkAndBeacon (the
// fast rate while moving, the slow rate standing, never under 60 s; the
// first beacon at the first look that has a position).
func (b *positionBeacon) due(now time.Time, fix selfpos.Fix) bool {
	moving := fix.SpeedMPS > positionBeaconSpeedMPS
	corner := false
	if moving && !b.lastBeacon.IsZero() {
		delta := math.Abs(fix.CourseDeg - b.lastHeading)
		if delta > 180 {
			delta = 360 - delta
		}
		corner = delta >= positionBeaconCornerDeg && now.Sub(b.lastBeacon) >= positionBeaconMinInterval
	}
	b.lastHeading = fix.CourseDeg
	if corner || b.lastBeacon.IsZero() {
		return true
	}
	interval := b.slow
	if interval <= 0 {
		interval = positionBeaconSlowDefault
	}
	if moving {
		interval = positionBeaconFastRate
	}
	if interval < positionBeaconMinInterval {
		interval = positionBeaconMinInterval
	}
	return now.Sub(b.lastBeacon) >= interval
}

// positionBeaconComment is Android's buildComment: the speed in km/h when
// above 1 km/h, the altitude in whole metres when above 0, then "MeshSat",
// joined by spaces. The speed rounds half up, as Kotlin's "%.0f" does.
func positionBeaconComment(speedMPS, altitudeM float64) string {
	var parts []string
	if kmh := speedMPS * 3.6; kmh > 1.0 {
		parts = append(parts, fmt.Sprintf("%dkm/h", int64(math.Floor(kmh+0.5))))
	}
	if altitudeM > 0 {
		parts = append(parts, fmt.Sprintf("alt=%dm", int64(altitudeM)))
	}
	return strings.Join(append(parts, "MeshSat"), " ")
}

// positionBeaconInfo is the beacon's info field, Android's position report:
// symbol table '/', symbol '-'.
func positionBeaconInfo(fix selfpos.Fix) string {
	return string(EncodeAPRSPosition(fix.Latitude, fix.Longitude, '/', '-', positionBeaconComment(fix.SpeedMPS, fix.AltitudeM)))
}

// SetSelfPosition installs the resolver of the device's own position, for
// the APRS-IS filter centre and the position beacon. It is read at every use,
// so it may be set while the gateway runs. [MESHSAT-1421]
func (g *APRSGateway) SetSelfPosition(fn func() (selfpos.Fix, bool)) {
	g.selfPosMu.Lock()
	g.selfPos = fn
	g.selfPosMu.Unlock()
}

func (g *APRSGateway) selfPosition() (selfpos.Fix, bool) {
	g.selfPosMu.RLock()
	fn := g.selfPos
	g.selfPosMu.RUnlock()
	if fn == nil {
		return selfpos.Fix{}, false
	}
	return fn()
}

// positionBeaconWorker beacons the device's own position to APRS-IS until
// ctx ends. Only startIS runs it: in mode kiss nothing starts it, whatever
// position_beacon says. No position, as on Android without a location, means
// no beacon, and 0,0 is never sent. A beacon the link refuses (not connected,
// receive only) is tried again at the next look.
func (g *APRSGateway) positionBeaconWorker(ctx context.Context) {
	defer g.wg.Done()
	b := positionBeacon{slow: positionBeaconSlowRate(g.config.PositionBeaconMin)}
	tick := time.NewTicker(positionBeaconCheck)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		fix, ok := g.selfPosition()
		if !ok || fix.Latitude == 0 && fix.Longitude == 0 {
			continue
		}
		now := time.Now()
		if !b.due(now, fix) {
			continue
		}
		if err := g.aprsISTransmit(positionBeaconInfo(fix)); err != nil {
			log.Debug().Err(err).Msg("aprs-is: position beacon not sent")
			continue
		}
		b.lastBeacon = now
		g.positionBeacons.Add(1)
		g.lastPositionBeaconAt.Store(now.UnixNano())
		log.Info().Float64("lat", fix.Latitude).Float64("lon", fix.Longitude).
			Float64("speed_mps", fix.SpeedMPS).Msg("aprs-is: position beacon sent")
	}
}
