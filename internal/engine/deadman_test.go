package engine

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestDeadManSwitch_TimeoutTrigger(t *testing.T) {
	db := testDB(t)
	d := NewDeadManSwitch(db, 1*time.Second)
	d.SetEnabled(true)

	triggered := make(chan struct{}, 1)
	d.SetSOSCallback(func(lat, lon float64, lastSeen time.Time) {
		triggered <- struct{}{}
	})

	// Set lastActive far in the past to trigger immediately on check
	d.lastActive.Store(time.Now().Add(-2 * time.Second).Unix())
	d.check()

	select {
	case <-triggered:
		// expected
	default:
		t.Fatal("expected SOS callback to fire")
	}

	if !d.IsTriggered() {
		t.Fatal("expected IsTriggered() to be true after timeout")
	}
}

func TestDeadManSwitch_TouchResets(t *testing.T) {
	db := testDB(t)
	d := NewDeadManSwitch(db, 1*time.Second)
	d.SetEnabled(true)

	callCount := 0
	d.SetSOSCallback(func(lat, lon float64, lastSeen time.Time) {
		callCount++
	})

	// Trigger once
	d.lastActive.Store(time.Now().Add(-2 * time.Second).Unix())
	d.check()
	if callCount != 1 {
		t.Fatalf("expected 1 SOS call, got %d", callCount)
	}

	// Second check should NOT fire again (already triggered)
	d.check()
	if callCount != 1 {
		t.Fatalf("expected still 1 SOS call, got %d", callCount)
	}

	// Touch resets the triggered flag
	d.Touch()
	if d.IsTriggered() {
		t.Fatal("expected IsTriggered() to be false after Touch()")
	}

	// Set far in the past again — should fire a second time
	d.lastActive.Store(time.Now().Add(-2 * time.Second).Unix())
	d.check()
	if callCount != 2 {
		t.Fatalf("expected 2 SOS calls after Touch+timeout, got %d", callCount)
	}
}

func TestDeadManSwitch_DisabledDoesNotTrigger(t *testing.T) {
	db := testDB(t)
	d := NewDeadManSwitch(db, 1*time.Second)
	// Disabled by default

	callCount := 0
	d.SetSOSCallback(func(lat, lon float64, lastSeen time.Time) {
		callCount++
	})

	d.lastActive.Store(time.Now().Add(-2 * time.Second).Unix())
	d.check()
	if callCount != 0 {
		t.Fatalf("expected 0 SOS calls when disabled, got %d", callCount)
	}
}

// The switch fired for months and sent nothing, because SetSOSCallback was only
// ever called from this file. Every test injected its own callback, so the suite
// stayed green while production had none. This one pins the case the suite was
// blind to: an armed switch with no callback must not look like a success.
// [MESHSAT-996]
func TestDeadManSwitch_NoCallbackIsNotSilent(t *testing.T) {
	d := NewDeadManSwitch(testDB(t), time.Second)
	d.SetEnabled(true)
	d.lastActive.Store(time.Now().Add(-2 * time.Second).Unix())

	d.check() // must not panic, and must mark itself triggered

	if !d.IsTriggered() {
		t.Fatal("switch did not mark itself triggered")
	}
}

// Start() spawns a goroutine that reads timeout and sosCallback while the API
// handlers write them. Before MESHSAT-996 both were plain fields and this test
// under -race reported a data race.
func TestDeadManSwitch_SettersAreRaceFree(t *testing.T) {
	d := NewDeadManSwitch(testDB(t), time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); d.SetTimeout(time.Duration(i+1) * time.Minute) }()
		go func() { defer wg.Done(); d.SetSOSCallback(func(_, _ float64, _ time.Time) {}) }()
		go func() { defer wg.Done(); _ = d.GetTimeout(); d.check() }()
	}
	wg.Wait()
}
