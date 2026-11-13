package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/engine"
)

// recordingSOS stands in for the API server: it records the SOS the switch
// raises and the switch handed to the API.
type recordingSOS struct {
	mu       sync.Mutex
	calls    int
	trigger  string
	lat, lon float64
	sw       *engine.DeadManSwitch
}

func (r *recordingSOS) TriggerSOSWithFallback(trigger string, lat, lon float64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.trigger, r.lat, r.lon = trigger, lat, lon
	return true
}

func (r *recordingSOS) SetDeadManSwitch(d *engine.DeadManSwitch) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sw = d
}

func (r *recordingSOS) seen() (int, string, float64, float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.trigger, r.lat, r.lon
}

type recordingTAK struct{ alarm chan struct{} }

func (k *recordingTAK) SendOwnDeadman(float64, float64, time.Time) error {
	select {
	case k.alarm <- struct{}{}:
	default:
	}
	return nil
}

// 4. The dead man's switch runs in the shipped binary: SIDN fonds project
// 261042, deliverable D2 [MESHSAT-1136]; items 1-3 are in
// internal/api/sos_safety_chain_test.go. main.go builds the switch with
// wireDeadMan, which hands it to the API and starts it; when its timeout
// passes it raises the SOS with the last known position, once, and the TAK
// alarm. Before MESHSAT-996 the switch was built only in app.go, which the
// binary never runs, so it armed, counted down and sent nothing.
func TestSafetyChain4_TheShippedBinaryArmsTheDeadMansSwitch(t *testing.T) {
	t.Run("main.go wires it", func(t *testing.T) {
		src, err := os.ReadFile("main.go")
		if err != nil {
			t.Fatal(err)
		}
		code := string(src)
		if !strings.Contains(code, "wireDeadMan(ctx, db, srv,") {
			t.Fatal("main.go no longer calls wireDeadMan(ctx, db, srv, ...): the shipped binary has no dead man's switch")
		}
		if strings.Contains(code, "engine.NewDeadManSwitch(") {
			t.Fatal("main.go builds a dead man's switch outside wireDeadMan: wire it through wireDeadMan so this test covers it")
		}
	})

	t.Run("it fires the SOS with its last known position, once", func(t *testing.T) {
		db, err := database.New(filepath.Join(t.TempDir(), "bridge.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err := db.InsertPosition(&database.Position{NodeID: "!a11ce001", Latitude: 52.1702, Longitude: 4.4811}); err != nil {
			t.Fatal(err)
		}
		sos := &recordingSOS{}
		tak := &recordingTAK{alarm: make(chan struct{}, 1)}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		sw := wireDeadMan(ctx, db, sos, func() deadManTAK { return tak }, 4*time.Hour)
		t.Cleanup(sw.Stop)
		sos.mu.Lock()
		handed := sos.sw
		sos.mu.Unlock()
		if handed != sw {
			t.Fatal("the switch was not handed to the API that arms it")
		}

		// Off, as shipped: nothing fires.
		sw.CheckNow()
		if n, _, _, _ := sos.seen(); n != 0 {
			t.Fatalf("a switch that is off fired %d times", n)
		}
		// On, inside its timeout: nothing fires.
		sw.SetEnabled(true)
		sw.CheckNow()
		if n, _, _, _ := sos.seen(); n != 0 {
			t.Fatalf("the switch fired %d times inside its timeout", n)
		}
		// The timeout passes.
		sw.SetTimeout(0)
		time.Sleep(1100 * time.Millisecond)
		sw.CheckNow()
		n, trigger, lat, lon := sos.seen()
		if n != 1 || trigger != "deadman" || lat != 52.1702 || lon != 4.4811 {
			t.Fatalf("fired %d times, trigger %q at %v, %v; want once, deadman, at 52.1702, 4.4811", n, trigger, lat, lon)
		}
		select {
		case <-tak.alarm:
		case <-time.After(2 * time.Second):
			t.Fatal("the TAK alarm was not sent")
		}
		// It fires once until someone is active again.
		sw.CheckNow()
		if n, _, _, _ := sos.seen(); n != 1 {
			t.Fatalf("fired %d times, want once", n)
		}
		if !sw.IsTriggered() {
			t.Fatal("the switch does not say it fired")
		}
	})
}
