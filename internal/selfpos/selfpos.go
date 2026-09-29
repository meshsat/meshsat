// Package selfpos keeps the position of the device the Bridge runs on: the
// fix the app on the same phone reports through PUT /api/position/self, and
// the order in which the Bridge falls back to other sources when that fix is
// missing or old. The APRS-IS position beacon and the APRS-IS filter centre
// read it, and the TAK gateway's own position report. [MESHSAT-1421]
//
// The package imports nothing of the Bridge, so the gateway, the API and
// main can all share it.
package selfpos

import (
	"errors"
	"math"
	"sync"
	"time"
)

// MaxAge is how long the app's fix stands as the device's own position.
// After that the fallback (the local node's position, then the GPS reader)
// takes over, so a phone whose app was closed does not beacon a place it
// left long ago.
const MaxAge = 10 * time.Minute

// Fix is one position of this device. Latitude and longitude are WGS84
// degrees; AltitudeM is metres above sea level, SpeedMPS metres per second,
// CourseDeg degrees clockwise from true north, AccuracyM the horizontal
// accuracy in metres. At is when the Bridge received the fix.
type Fix struct {
	Latitude  float64   `json:"latitude"`
	Longitude float64   `json:"longitude"`
	AltitudeM float64   `json:"altitude_m"`
	SpeedMPS  float64   `json:"speed_mps"`
	CourseDeg float64   `json:"course_deg"`
	AccuracyM float64   `json:"accuracy_m"`
	At        time.Time `json:"at"`
}

// Validate reports the first value out of range. A value the source does
// not know must be left out (it reads 0), never sent as a sentinel such as
// geoclue's -1 for an unknown speed or heading.
func (f Fix) Validate() error {
	switch {
	case !finite(f.Latitude) || f.Latitude < -90 || f.Latitude > 90:
		return errors.New("latitude must be -90 to 90")
	case !finite(f.Longitude) || f.Longitude < -180 || f.Longitude > 180:
		return errors.New("longitude must be -180 to 180")
	case f.Latitude == 0 && f.Longitude == 0:
		return errors.New("0,0 is not a position")
	case !finite(f.AltitudeM) || f.AltitudeM < -12000 || f.AltitudeM > 100000:
		return errors.New("altitude_m must be -12000 to 100000 (leave it out when unknown)")
	case !finite(f.SpeedMPS) || f.SpeedMPS < 0 || f.SpeedMPS > 1000:
		return errors.New("speed_mps must be 0 to 1000 (leave it out when unknown)")
	case !finite(f.CourseDeg) || f.CourseDeg < 0 || f.CourseDeg > 360:
		return errors.New("course_deg must be 0 to 360 (leave it out when unknown)")
	case !finite(f.AccuracyM) || f.AccuracyM < 0:
		return errors.New("accuracy_m must be 0 or more (leave it out when unknown)")
	}
	return nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Store holds the latest fix the app reported. The zero value is ready to
// use; it is safe for concurrent use.
type Store struct {
	mu  sync.RWMutex
	fix Fix
	ok  bool
	// now is the clock; nil means time.Now. Tests set it.
	now func() time.Time
}

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Set validates f and keeps it as the app's latest fix. A zero At is
// stamped with the time of arrival.
func (s *Store) Set(f Fix) error {
	if err := f.Validate(); err != nil {
		return err
	}
	if f.At.IsZero() {
		f.At = s.clock()
	}
	s.mu.Lock()
	s.fix, s.ok = f, true
	s.mu.Unlock()
	return nil
}

// Get returns the app's latest fix, however old, and whether there is one.
func (s *Store) Get() (Fix, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fix, s.ok
}

// Resolve returns the device's own position: the app's fix while it is
// younger than maxAge, else what nodeFallback finds (main.go: the local
// node's position from the mesh node table, then the GPS reader), else
// nothing. A fallback fix at 0,0 or out of range counts as none. Either
// argument may be nil.
func Resolve(store *Store, nodeFallback func() (Fix, bool), maxAge time.Duration) (Fix, bool) {
	if store != nil {
		if f, ok := store.Get(); ok && store.clock().Sub(f.At) < maxAge {
			return f, true
		}
	}
	if nodeFallback != nil {
		if f, ok := nodeFallback(); ok && f.Validate() == nil {
			return f, true
		}
	}
	return Fix{}, false
}
