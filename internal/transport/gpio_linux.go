package transport

// GPIO helpers via Linux sysfs — pure Go, no CGO.
// Used for Iridium 9603N sleep/wake pin control.

import (
	"fmt"
	"os"
	"strconv"
)

// gpioExport makes a GPIO pin available via sysfs.
func gpioExport(pin int) error {
	// Skip if already exported
	if _, err := os.Stat(fmt.Sprintf("/sys/class/gpio/gpio%d", pin)); err == nil {
		return nil
	}
	return os.WriteFile("/sys/class/gpio/export", []byte(strconv.Itoa(pin)), 0200)
}

// gpioUnexport releases a GPIO pin from sysfs.
func gpioUnexport(pin int) error {
	return os.WriteFile("/sys/class/gpio/unexport", []byte(strconv.Itoa(pin)), 0200)
}

// gpioSetDirection sets a GPIO pin direction ("in" or "out").
func gpioSetDirection(pin int, dir string) error {
	path := fmt.Sprintf("/sys/class/gpio/gpio%d/direction", pin)
	return os.WriteFile(path, []byte(dir), 0200)
}

// gpioWrite writes a value (0 or 1) to a GPIO pin.
func gpioWrite(pin int, value int) error {
	path := fmt.Sprintf("/sys/class/gpio/gpio%d/value", pin)
	return os.WriteFile(path, []byte(strconv.Itoa(value)), 0200)
}

// gpioSetup exports a pin and sets it as output with an initial value.
func gpioSetup(pin, initialValue int) error {
	if err := gpioExport(pin); err != nil {
		return fmt.Errorf("export gpio %d: %w", pin, err)
	}
	if err := gpioSetDirection(pin, "out"); err != nil {
		return fmt.Errorf("set direction gpio %d: %w", pin, err)
	}
	return gpioWrite(pin, initialValue)
}
