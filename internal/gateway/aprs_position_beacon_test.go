package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"meshsat/internal/selfpos"
)

// AprsBeaconTest's constants.
func TestPositionBeaconConstants(t *testing.T) {
	if positionBeaconSlowDefault != 600*time.Second || positionBeaconFastRate != 90*time.Second || positionBeaconMinInterval != 60*time.Second {
		t.Fatalf("rates %s / %s / %s, want 600s / 90s / 60s", positionBeaconSlowDefault, positionBeaconFastRate, positionBeaconMinInterval)
	}
	if positionBeaconSpeedMPS != 2.0 || positionBeaconCornerDeg != 30.0 {
		t.Fatalf("thresholds %v m/s, %v°", positionBeaconSpeedMPS, positionBeaconCornerDeg)
	}
	if positionBeaconCheck != 10*time.Second {
		t.Fatalf("check period %s, want 10s", positionBeaconCheck)
	}
	// The default position_beacon_min gives the default slow rate, and the
	// beacon is off unless asked for (AprsBeaconTest: starts disabled).
	def := DefaultAPRSConfig()
	if def.PositionBeacon || positionBeaconSlowRate(def.PositionBeaconMin) != positionBeaconSlowDefault {
		t.Fatalf("defaults: position_beacon %v, slow rate %s", def.PositionBeacon, positionBeaconSlowRate(def.PositionBeaconMin))
	}
}

func TestPositionBeaconInterval(t *testing.T) {
	for minutes, want := range map[int]time.Duration{0: 60 * time.Second, 1: 60 * time.Second, 10: 600 * time.Second, 30: 30 * time.Minute, -5: 60 * time.Second} {
		if got := positionBeaconSlowRate(minutes); got != want {
			t.Errorf("position_beacon_min %d: %s, want %s", minutes, got, want)
		}
	}
}

func TestPositionBeaconFastAndCornerPeg(t *testing.T) {
	t0 := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	standing := selfpos.Fix{Latitude: 52.3676, Longitude: 4.9041}
	moving := func(course float64) selfpos.Fix {
		return selfpos.Fix{Latitude: 52.3676, Longitude: 4.9041, SpeedMPS: 5, CourseDeg: course}
	}

	b := positionBeacon{slow: positionBeaconSlowRate(10)}
	if !b.due(at(10), standing) {
		t.Fatal("the first look with a position must beacon")
	}
	b.lastBeacon = at(10)
	if b.due(at(10+599), standing) || !b.due(at(10+600), standing) {
		t.Fatal("standing: the slow rate is 600 s")
	}

	// Moving (above 2 m/s): the fast rate, 90 s.
	b = positionBeacon{slow: positionBeaconSlowRate(10), lastBeacon: at(0), lastHeading: 90}
	if b.due(at(89), moving(90)) || !b.due(at(90), moving(90)) {
		t.Fatal("moving: the fast rate is 90 s")
	}
	// 2 m/s exactly is not moving.
	b = positionBeacon{slow: positionBeaconSlowRate(10), lastBeacon: at(0)}
	if b.due(at(90), selfpos.Fix{Latitude: 52, Longitude: 4, SpeedMPS: 2.0}) {
		t.Fatal("2.0 m/s counted as moving")
	}

	// Corner peg: a turn of 30° or more once 60 s have passed.
	b = positionBeacon{slow: positionBeaconSlowRate(10), lastBeacon: at(0), lastHeading: 90}
	if b.due(at(59), moving(130)) {
		t.Fatal("a corner inside 60 s beaconed")
	}
	b = positionBeacon{slow: positionBeaconSlowRate(10), lastBeacon: at(0), lastHeading: 90}
	if !b.due(at(60), moving(120)) {
		t.Fatal("a 30° turn after 60 s did not beacon")
	}
	b = positionBeacon{slow: positionBeaconSlowRate(10), lastBeacon: at(0), lastHeading: 90}
	if b.due(at(60), moving(119)) {
		t.Fatal("a 29° turn beaconed before the fast rate")
	}
	// Across north: 350° to 20° is a 30° turn.
	b = positionBeacon{slow: positionBeaconSlowRate(10), lastBeacon: at(0), lastHeading: 350}
	if !b.due(at(61), moving(20)) {
		t.Fatal("a 30° turn across north did not beacon")
	}
	// Standing still, a heading change is not a corner.
	b = positionBeacon{slow: positionBeaconSlowRate(10), lastBeacon: at(0), lastHeading: 0}
	if b.due(at(61), selfpos.Fix{Latitude: 52, Longitude: 4, CourseDeg: 180}) {
		t.Fatal("a standing heading change beaconed")
	}
	// The last heading follows every look, as Android's follows every fix.
	b = positionBeacon{slow: positionBeaconSlowRate(10), lastBeacon: at(0), lastHeading: 90}
	b.due(at(30), moving(110))
	if b.due(at(61), moving(125)) {
		t.Fatal("a turn split over two looks (20° then 15°) pegged")
	}
}

// Android's buildComment.
func TestPositionBeaconComment(t *testing.T) {
	for _, c := range []struct {
		speed, alt float64
		want       string
	}{
		{6.944, 15.4, "25km/h alt=15m MeshSat"},
		{0, 0, "MeshSat"},
		{0.2, 0, "MeshSat"},                  // 0.72 km/h is not above 1
		{0, -3, "MeshSat"},                   // no altitude at or below 0
		{0.417, 0.7, "2km/h alt=0m MeshSat"}, // 1.5012 km/h rounds to 2
		{1.25, 0, "5km/h MeshSat"},           // 4.5 km/h rounds half up, as Kotlin's %.0f
	} {
		if got := positionBeaconComment(c.speed, c.alt); got != c.want {
			t.Errorf("comment(%v m/s, %v m) = %q, want %q", c.speed, c.alt, got, c.want)
		}
	}
}

func TestPositionBeaconLine(t *testing.T) {
	// AprsBeaconTest's encodings.
	if got := string(EncodeAPRSPosition(47.3, -122.5, '/', '-', "MeshSat")); got != "!4718.00N/12230.00W-MeshSat" {
		t.Fatalf("encode 47.3,-122.5: %q", got)
	}
	enc := string(EncodeAPRSPosition(52.3676, 4.9041, '/', '-', "25km/h alt=15m MeshSat"))
	if !strings.Contains(enc, "5222") || !strings.Contains(enc, "00454") || !strings.Contains(enc, "25km/h") {
		t.Fatalf("encode 52.3676,4.9041: %q", enc)
	}
	if got := positionBeaconInfo(selfpos.Fix{Latitude: 52.3676, Longitude: 4.9041}); got != "!5222.06N/00454.25E-MeshSat" {
		t.Fatalf("beacon info %q", got)
	}

	// On the wire, to the loopback fake, at the first look.
	setAPRSISKnobs(t, 50*time.Millisecond)
	positionBeaconCheck = 20 * time.Millisecond
	f := newFakeAPRSIS(t, logrespVerified)
	g := newISGateway(t, f, func(c *APRSConfig) { c.PositionBeacon = true })
	g.SetSelfPosition(func() (selfpos.Fix, bool) { return selfpos.Fix{Latitude: 52.3676, Longitude: 4.9041}, true })
	startGateway(t, g)
	f.waitLine(t, "N0CALL-10>APMSHT,TCPIP*:!5222.06N/00454.25E-MeshSat", 5*time.Second)
	st := g.GetAPRSStatus()
	if st["position_beacons"] != int64(1) || st["last_beacon_at"] == "" {
		t.Fatalf("status after one beacon: position_beacons %v last_beacon_at %v", st["position_beacons"], st["last_beacon_at"])
	}
	// The slow rate holds the next one back.
	time.Sleep(200 * time.Millisecond)
	if n := g.positionBeacons.Load(); n != 1 {
		t.Fatalf("%d beacons within the slow rate", n)
	}
}

// No position, 0,0, or a receive-only login: nothing goes out.
func TestPositionBeaconNeedsAPositionAndAVerifiedLogin(t *testing.T) {
	setAPRSISKnobs(t, 50*time.Millisecond)
	positionBeaconCheck = 20 * time.Millisecond
	for name, c := range map[string]struct {
		logresp string
		fix     func() (selfpos.Fix, bool)
	}{
		"no position":  {logrespVerified, func() (selfpos.Fix, bool) { return selfpos.Fix{}, false }},
		"0,0":          {logrespVerified, func() (selfpos.Fix, bool) { return selfpos.Fix{}, true }},
		"receive only": {logrespUnverified, func() (selfpos.Fix, bool) { return selfpos.Fix{Latitude: 52.3676, Longitude: 4.9041}, true }},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeAPRSIS(t, c.logresp)
			g := newISGateway(t, f, func(cfg *APRSConfig) { cfg.PositionBeacon = true })
			g.SetSelfPosition(c.fix)
			startGateway(t, g)
			waitISState(t, g, APRSStateConnected, 5*time.Second)
			time.Sleep(200 * time.Millisecond)
			if _, lines, _ := f.snapshot(); len(lines) != 0 {
				t.Fatalf("sent %q", lines)
			}
			if n := g.positionBeacons.Load(); n != 0 {
				t.Fatalf("position_beacons %d", n)
			}
		})
	}
}

// Mode kiss with position_beacon on: the beacon never starts, and no frame
// reaches the TNC (spec Q2).
func TestPositionBeaconNeverOnRF(t *testing.T) {
	orig := positionBeaconCheck
	positionBeaconCheck = 20 * time.Millisecond
	t.Cleanup(func() { positionBeaconCheck = orig })
	tnc := newMockKISSTNC(t)
	defer tnc.close()
	host, port := splitHostPort(t, tnc.addr())
	gw := NewAPRSGateway(APRSConfig{
		KISSHost: host, KISSPort: port, Callsign: "N0CALL", SSID: 10, FrequencyMHz: 144.800,
		ExternalDirewolf: true, PositionBeacon: true, PositionBeaconMin: 0,
	}, nil)
	gw.SetSelfPosition(func() (selfpos.Fix, bool) {
		return selfpos.Fix{Latitude: 52.3676, Longitude: 4.9041, SpeedMPS: 10}, true
	})
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()
	time.Sleep(300 * time.Millisecond)
	if frames := tnc.frames(); len(frames) != 0 {
		t.Fatalf("%d frame(s) went to the TNC, want none", len(frames))
	}
	if n := gw.positionBeacons.Load(); n != 0 {
		t.Fatalf("position_beacons %d in mode kiss", n)
	}
	if _, has := gw.GetAPRSStatus()["position_beacons"]; has {
		t.Fatal("mode kiss reports position beacons")
	}
}

// Minutes that round to 60.00 carry into the degrees.
func TestEncodeAPRSPositionMinuteCarry(t *testing.T) {
	for _, c := range []struct {
		lat, lon float64
		want     string
	}{
		{52.999999, 4.9999999, "!5300.00N/00500.00E-x"},
		{-33.9999999, -151.9999999, "!3400.00S/15200.00W-x"},
		{0, 0, "!0000.00N/00000.00E-x"},
		{52.3676, 4.9041, "!5222.06N/00454.25E-x"},
	} {
		if got := string(EncodeAPRSPosition(c.lat, c.lon, '/', '-', "x")); got != c.want {
			t.Errorf("%v,%v: %q, want %q", c.lat, c.lon, got, c.want)
		}
	}
}
