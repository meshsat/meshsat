package engine

import (
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
