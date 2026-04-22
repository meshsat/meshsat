package transport

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// fakeGPIOLine is an in-memory GPIOLine used to verify that the sleep/wake
// path in DirectSatTransport drives the line as expected without touching
// /dev/gpiochip*.
type fakeGPIOLine struct {
	value    int32 // last written value (atomic so a watcher could read mid-test)
	writes   int32 // count of SetValue calls
	closed   int32 // 1 once Close has been called
	failNext bool  // make the next SetValue return an error
}

func (f *fakeGPIOLine) Value() (int, error) {
	if atomic.LoadInt32(&f.closed) == 1 {
		return 0, errors.New("closed")
	}
	return int(atomic.LoadInt32(&f.value)), nil
}

func (f *fakeGPIOLine) SetValue(v int) error {
	if atomic.LoadInt32(&f.closed) == 1 {
		return errors.New("closed")
	}
	if f.failNext {
		f.failNext = false
		return errors.New("induced write failure")
	}
	atomic.StoreInt32(&f.value, int32(v))
	atomic.AddInt32(&f.writes, 1)
	return nil
}

func (f *fakeGPIOLine) Close() error {
	atomic.StoreInt32(&f.closed, 1)
	return nil
}

// withFakeOpenOutput swaps the package-level OpenOutput for the duration of a
// test, returning the fake line that calls will receive and a restore func.
func withFakeOpenOutput(t *testing.T) (*fakeGPIOLine, func()) {
	t.Helper()
	fake := &fakeGPIOLine{}
	original := OpenOutput
	OpenOutput = func(offset int, initial int, consumer string) (GPIOLine, error) {
		atomic.StoreInt32(&fake.value, int32(initial))
		return fake, nil
	}
	return fake, func() { OpenOutput = original }
}

// TestSleepWakeDrivesGPIOLine verifies the sleep/wake API drives the line
// HIGH on Sleep, LOW on Wake, and tolerates an idempotent second call.
func TestSleepWakeDrivesGPIOLine(t *testing.T) {
	fake, restore := withFakeOpenOutput(t)
	defer restore()

	tr := NewDirectSatTransport("auto")
	tr.SetSleepPin(24)

	// Simulate what connectLocked does: claim the line, mark awake.
	line, err := OpenOutput(tr.sleepPin, 0, "test")
	if err != nil {
		t.Fatalf("OpenOutput: %v", err)
	}
	tr.sleepLine = line
	tr.awake = true
	// Backdate lastWakeTime so sleepLocked doesn't sit in its 2s on-time
	// guard for the duration of the test.
	tr.lastWakeTime = tr.lastWakeTime.Add(-10 * 1e9) // -10s

	if err := tr.Sleep(context.Background()); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	if got, _ := fake.Value(); got != 1 {
		t.Errorf("after Sleep: line value = %d, want 1 (HIGH)", got)
	}
	if tr.awake {
		t.Errorf("after Sleep: transport.awake = true, want false")
	}

	// Idempotent second Sleep call must not double-write.
	writesAfterFirst := atomic.LoadInt32(&fake.writes)
	if err := tr.Sleep(context.Background()); err != nil {
		t.Fatalf("Sleep idempotent: %v", err)
	}
	if got := atomic.LoadInt32(&fake.writes); got != writesAfterFirst {
		t.Errorf("second Sleep wrote line again (writes %d → %d)", writesAfterFirst, got)
	}

	if err := tr.Wake(context.Background()); err != nil {
		t.Fatalf("Wake: %v", err)
	}
	if got, _ := fake.Value(); got != 0 {
		t.Errorf("after Wake: line value = %d, want 0 (LOW)", got)
	}
	if !tr.awake {
		t.Errorf("after Wake: transport.awake = false, want true")
	}
}

// TestSleepWithoutPinReturnsError confirms Sleep is a no-op when no pin is
// configured (the production path logs a warning rather than panicking).
func TestSleepWithoutPinReturnsError(t *testing.T) {
	tr := NewDirectSatTransport("auto") // no SetSleepPin
	if err := tr.Sleep(context.Background()); err == nil {
		t.Errorf("Sleep on transport with no sleep pin: want error, got nil")
	}
}

// TestWakeWithoutPinIsNoop — Wake returning nil when no pin configured is the
// production contract (see wakeLocked).
func TestWakeWithoutPinIsNoop(t *testing.T) {
	tr := NewDirectSatTransport("auto")
	if err := tr.Wake(context.Background()); err != nil {
		t.Errorf("Wake on transport with no sleep pin: want nil, got %v", err)
	}
}

// TestGPIOChipNameRespectsEnv verifies MESHSAT_GPIO_CHIP overrides the Pi 5
// default — important for Pi 4 hosts where the BCM block is gpiochip0.
func TestGPIOChipNameRespectsEnv(t *testing.T) {
	t.Setenv("MESHSAT_GPIO_CHIP", "gpiochip0")
	if got := gpioChipName(); got != "gpiochip0" {
		t.Errorf("gpioChipName() with env override = %q, want %q", got, "gpiochip0")
	}
	t.Setenv("MESHSAT_GPIO_CHIP", "")
	if got := gpioChipName(); got != defaultGPIOChip {
		t.Errorf("gpioChipName() default = %q, want %q", got, defaultGPIOChip)
	}
}
