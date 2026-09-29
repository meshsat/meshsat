package selfpos

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestFixValidate(t *testing.T) {
	ok := Fix{Latitude: 52.3676, Longitude: 4.9041, AltitudeM: 15.4, SpeedMPS: 6.944, CourseDeg: 90, AccuracyM: 8}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a good fix refused: %v", err)
	}
	for _, c := range []struct {
		name string
		fix  Fix
		want string
	}{
		{"latitude high", Fix{Latitude: 90.1, Longitude: 4}, "latitude"},
		{"latitude low", Fix{Latitude: -91, Longitude: 4}, "latitude"},
		{"latitude NaN", Fix{Latitude: math.NaN(), Longitude: 4}, "latitude"},
		{"longitude high", Fix{Latitude: 52, Longitude: 180.5}, "longitude"},
		{"longitude inf", Fix{Latitude: 52, Longitude: math.Inf(-1)}, "longitude"},
		{"null island", Fix{}, "0,0"},
		{"geoclue unknown altitude", Fix{Latitude: 52, Longitude: 4, AltitudeM: -math.MaxFloat64}, "altitude_m"},
		{"geoclue unknown speed", Fix{Latitude: 52, Longitude: 4, SpeedMPS: -1}, "speed_mps"},
		{"geoclue unknown heading", Fix{Latitude: 52, Longitude: 4, CourseDeg: -1}, "course_deg"},
		{"course past 360", Fix{Latitude: 52, Longitude: 4, CourseDeg: 361}, "course_deg"},
		{"negative accuracy", Fix{Latitude: 52, Longitude: 4, AccuracyM: -3}, "accuracy_m"},
	} {
		err := c.fix.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error naming %q", c.name, err, c.want)
		}
	}
}

func TestStoreStampsAndRefuses(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	s := &Store{now: func() time.Time { return now }}
	if _, ok := s.Get(); ok {
		t.Fatal("an empty store has a fix")
	}
	if err := s.Set(Fix{Latitude: 91, Longitude: 4}); err == nil {
		t.Fatal("out of range accepted")
	}
	if _, ok := s.Get(); ok {
		t.Fatal("a refused fix was kept")
	}
	if err := s.Set(Fix{Latitude: 52.3676, Longitude: 4.9041}); err != nil {
		t.Fatal(err)
	}
	f, ok := s.Get()
	if !ok || !f.At.Equal(now) || f.Latitude != 52.3676 {
		t.Fatalf("stored %+v ok=%v", f, ok)
	}
}

// The app's fix while it is fresh, then the fallback, then nothing.
func TestResolveOrder(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	s := &Store{now: func() time.Time { return now }}
	node := Fix{Latitude: 51.5, Longitude: -0.1, At: now}
	fallback := func() (Fix, bool) { return node, true }
	none := func() (Fix, bool) { return Fix{}, false }

	if _, ok := Resolve(s, none, MaxAge); ok {
		t.Fatal("resolved with no source at all")
	}
	if _, ok := Resolve(nil, nil, MaxAge); ok {
		t.Fatal("nil sources resolved")
	}
	if f, ok := Resolve(s, fallback, MaxAge); !ok || f.Latitude != 51.5 {
		t.Fatalf("no app fix: got %+v ok=%v, want the fallback", f, ok)
	}
	if err := s.Set(Fix{Latitude: 52.3676, Longitude: 4.9041}); err != nil {
		t.Fatal(err)
	}
	if f, ok := Resolve(s, fallback, MaxAge); !ok || f.Latitude != 52.3676 {
		t.Fatalf("fresh app fix: got %+v ok=%v", f, ok)
	}
	now = now.Add(MaxAge - time.Second)
	if f, _ := Resolve(s, fallback, MaxAge); f.Latitude != 52.3676 {
		t.Fatalf("app fix just under 10 min dropped: %+v", f)
	}
	now = now.Add(time.Second)
	if f, ok := Resolve(s, fallback, MaxAge); !ok || f.Latitude != 51.5 {
		t.Fatalf("stale app fix: got %+v ok=%v, want the fallback", f, ok)
	}
	if _, ok := Resolve(s, none, MaxAge); ok {
		t.Fatal("a stale app fix with no fallback still resolved")
	}
	// A fallback at 0,0 (a node without a position) is no position.
	zero := func() (Fix, bool) { return Fix{At: now}, true }
	if _, ok := Resolve(s, zero, MaxAge); ok {
		t.Fatal("a 0,0 fallback resolved")
	}
}
