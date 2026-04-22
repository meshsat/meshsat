package transport

// GPIO helpers backed by libgpiod chardev (/dev/gpiochip*) via
// github.com/warthog618/go-gpiocdev — pure Go, no CGO. Works inside the
// bridge's Docker container because /dev is bind-mounted rw; /sys is
// read-only in the standalone compose (docker-compose.standalone.yml).
//
// Pi 5 note: the RP1 GPIO block is exposed as gpiochip4 on Raspberry
// Pi OS / Ubuntu 24.04 kernels. Override with MESHSAT_GPIO_CHIP for
// Pi 4 (gpiochip0) or other boards. Chardev line offsets on Pi 5 map
// directly to BCM pin numbers (unlike sysfs, where gpiochip571 + N is
// required).

import (
	"fmt"
	"os"

	"github.com/warthog618/go-gpiocdev"
)

const defaultGPIOChip = "gpiochip4"

func gpioChipName() string {
	if c := os.Getenv("MESHSAT_GPIO_CHIP"); c != "" {
		return c
	}
	return defaultGPIOChip
}

// GPIOLine is the minimal handle needed by callers (output write, input
// read, release). Kept as an interface so tests can substitute fakes for
// the kernel-backed line without standing up a real /dev/gpiochip device.
type GPIOLine interface {
	Value() (int, error)
	SetValue(v int) error
	Close() error
}

// cdevLine is the production implementation backed by go-gpiocdev.
type cdevLine struct {
	line *gpiocdev.Line
}

func (g *cdevLine) Value() (int, error) {
	if g == nil || g.line == nil {
		return 0, fmt.Errorf("gpio line not open")
	}
	return g.line.Value()
}

func (g *cdevLine) SetValue(v int) error {
	if g == nil || g.line == nil {
		return fmt.Errorf("gpio line not open")
	}
	return g.line.SetValue(v)
}

func (g *cdevLine) Close() error {
	if g == nil || g.line == nil {
		return nil
	}
	err := g.line.Close()
	g.line = nil
	return err
}

// OpenOutput / OpenInput / WatchFallingEdge are package-level vars (not
// plain funcs) so unit tests can swap them for in-memory fakes without
// touching real hardware. Production callers use them as if they were
// functions.

// OpenOutput reserves a line as a push-pull output with the given
// initial value. `consumer` shows up in `gpioinfo` so operators can
// tell what claimed the pin.
var OpenOutput = func(offset int, initial int, consumer string) (GPIOLine, error) {
	l, err := gpiocdev.RequestLine(
		gpioChipName(),
		offset,
		gpiocdev.AsOutput(initial),
		gpiocdev.WithConsumer(consumer),
	)
	if err != nil {
		return nil, fmt.Errorf("open gpio output %s/%d (%s): %w", gpioChipName(), offset, consumer, err)
	}
	return &cdevLine{line: l}, nil
}

// OpenInput reserves a line as an input with the requested bias. Pass
// gpiocdev.WithBiasDisabled to leave biasing to external hardware,
// gpiocdev.WithPullUp for a defensive pull-up, etc. nil bias = whatever
// the kernel last set.
var OpenInput = func(offset int, bias gpiocdev.LineReqOption, consumer string) (GPIOLine, error) {
	opts := []gpiocdev.LineReqOption{
		gpiocdev.AsInput,
		gpiocdev.WithConsumer(consumer),
	}
	if bias != nil {
		opts = append(opts, bias)
	}
	l, err := gpiocdev.RequestLine(gpioChipName(), offset, opts...)
	if err != nil {
		return nil, fmt.Errorf("open gpio input %s/%d (%s): %w", gpioChipName(), offset, consumer, err)
	}
	return &cdevLine{line: l}, nil
}

// WatchFallingEdge reserves a line as an input with an internal
// pull-up and invokes handler from a kernel-driven goroutine on every
// active→inactive (HIGH→LOW) transition. Close the returned handle to
// stop watching. Used for the 9603 RI ring-alert input.
var WatchFallingEdge = func(offset int, consumer string, handler func(gpiocdev.LineEvent)) (GPIOLine, error) {
	l, err := gpiocdev.RequestLine(
		gpioChipName(),
		offset,
		gpiocdev.AsInput,
		gpiocdev.WithPullUp,
		gpiocdev.WithFallingEdge,
		gpiocdev.WithEventHandler(handler),
		gpiocdev.WithConsumer(consumer),
	)
	if err != nil {
		return nil, fmt.Errorf("watch gpio falling edge %s/%d (%s): %w", gpioChipName(), offset, consumer, err)
	}
	return &cdevLine{line: l}, nil
}
