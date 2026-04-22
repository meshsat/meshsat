package transport

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/warthog618/go-gpiocdev"
)

// fakeGPIOLine is an in-memory GPIOLine used to verify that the sleep/wake
// path in DirectSatTransport drives the line as expected without touching
// /dev/gpiochip*.
type fakeGPIOLine struct {
	value    int32 // last written value (atomic so a watcher could read mid-test)
	writes   int32 // count of SetValue calls
	closed   int32 // 1 once Close has been called
	failNext bool  // make the next SetValue return an error

	// Ordered event log — each entry is either "set=N" (SetValue) or
	// "rcfg=mode" (Reconfigure). Used by HardPowerCycle tests to
	// assert the input→output-LOW→input sequence.
	hmu     sync.Mutex
	events  []string
	history []int // legacy: SetValue-only history, kept for existing tests
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
	f.hmu.Lock()
	f.history = append(f.history, v)
	f.events = append(f.events, fmt.Sprintf("set=%d", v))
	f.hmu.Unlock()
	return nil
}

// Events returns a copy of the ordered SetValue/Reconfigure log.
func (f *fakeGPIOLine) Events() []string {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	out := make([]string, len(f.events))
	copy(out, f.events)
	return out
}

// History returns a copy of the ordered SetValue log.
func (f *fakeGPIOLine) History() []int {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	out := make([]int, len(f.history))
	copy(out, f.history)
	return out
}

func (f *fakeGPIOLine) Close() error {
	atomic.StoreInt32(&f.closed, 1)
	return nil
}

// Reconfigure records the direction/bias change for tests to inspect.
// Distinguishes "input" (HardPowerCycle's release-to-ON) from
// "output" (the OFF pulse) by peeking at the first option's type.
func (f *fakeGPIOLine) Reconfigure(opts ...gpiocdev.LineConfigOption) error {
	if atomic.LoadInt32(&f.closed) == 1 {
		return errors.New("closed")
	}
	mode := "other"
	if len(opts) > 0 {
		switch opts[0].(type) {
		case gpiocdev.OutputOption:
			mode = "output"
		case gpiocdev.InputOption:
			mode = "input"
		}
	}
	f.hmu.Lock()
	f.events = append(f.events, "rcfg="+mode)
	f.hmu.Unlock()
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

// TestHardPowerCyclePulsesViaReconfigure verifies that HardPowerCycle
// implements open-drain emulation: Reconfigure(output) for the OFF
// pulse, then Reconfigure(input) to release the line so the modem's
// 1 MΩ pull-up takes OnOff back to the ON level. The 5 s post-cycle
// boot wait is aborted by a short test context so connectLocked() is
// never reached. MESHSAT-668 / MESHSAT-669.
func TestHardPowerCyclePulsesViaReconfigure(t *testing.T) {
	fake := &fakeGPIOLine{}
	tr := NewDirectSatTransport("auto")
	tr.SetOnOffPin(24)
	tr.onOffLine = fake

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	err := tr.HardPowerCycle(ctx)
	if err == nil {
		t.Fatal("HardPowerCycle: want ctx-cancelled error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("HardPowerCycle: err = %v, want DeadlineExceeded", err)
	}

	events := fake.Events()
	// Expect: rcfg=output (OFF pulse), rcfg=input (release to ON).
	// We don't care about the 500 ms SetValue in-between (go-gpiocdev
	// applies the initial output value from AsOutput(0) directly).
	if len(events) != 2 || events[0] != "rcfg=output" || events[1] != "rcfg=input" {
		t.Errorf("HardPowerCycle: events = %v, want [rcfg=output rcfg=input]", events)
	}
}

// TestHardPowerCycleWithoutPinReturnsError confirms HardPowerCycle
// rejects calls when the OnOff pin was never configured — the field
// operator must wire MESHSAT_IRIDIUM_ONOFF_PIN before the endpoint is
// usable.
func TestHardPowerCycleWithoutPinReturnsError(t *testing.T) {
	tr := NewDirectSatTransport("auto")
	if err := tr.HardPowerCycle(context.Background()); err == nil {
		t.Error("HardPowerCycle on transport with no OnOff pin: want error, got nil")
	}
}

// TestOnOffLevels verifies the polarity helper returns the expected
// (off, on) pair for both MOSFET-default and direct-wire polarities.
func TestOnOffLevels(t *testing.T) {
	tr := NewDirectSatTransport("auto")
	// Default polarity = MOSFET-buffered (active-low on the Pi GPIO).
	if off, on := tr.onOffLevels(); off != 1 || on != 0 {
		t.Errorf("default polarity levels = (%d,%d), want (1,0)", off, on)
	}
	tr.SetOnOffActiveHigh(true)
	if off, on := tr.onOffLevels(); off != 0 || on != 1 {
		t.Errorf("active-high polarity levels = (%d,%d), want (0,1)", off, on)
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
