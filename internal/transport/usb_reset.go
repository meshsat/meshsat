package transport

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/unix"
)

// USBResetSerialDevice re-enumerates the USB device behind a serial port
// (USBDEVFS_RESET on /dev/bus/usb/BBB/DDD). Exported for the OOB
// management RESET command; the device supervisor and gateway manager
// recover the transport afterwards exactly as for a hot-swap. Works with
// /sys mounted read-only, unlike sysfs unbind/rebind. [MESHSAT-756]
func USBResetSerialDevice(who, portPath string) bool {
	return usbResetSerialDevice(who, portPath)
}

// USBResetSysfsID re-enumerates a USB device identified by its sysfs id
// (for example "1-1.3", as reported by the spectrum monitor for the
// RTL-SDR), which has no tty to resolve through. [MESHSAT-756]
func USBResetSysfsID(who, sysfsID string) bool {
	id := strings.TrimSpace(sysfsID)
	if id == "" || strings.ContainsAny(id, "/ \t") {
		log.Warn().Str("subsys", who).Str("id", sysfsID).Msg("usb reset: invalid sysfs id")
		return false
	}
	base := "/sys/bus/usb/devices/" + id
	busNum, err := readIntFile(base + "/busnum")
	if err != nil {
		log.Warn().Str("subsys", who).Err(err).Msg("usb reset: busnum")
		return false
	}
	devNum, err := readIntFile(base + "/devnum")
	if err != nil {
		log.Warn().Str("subsys", who).Err(err).Msg("usb reset: devnum")
		return false
	}
	usbDevPath := fmt.Sprintf("/dev/bus/usb/%03d/%03d", busNum, devNum)
	fd, err := unix.Open(usbDevPath, unix.O_WRONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		log.Warn().Str("subsys", who).Err(err).Str("path", usbDevPath).Msg("usb reset: can't open USB device")
		return false
	}
	defer unix.Close(fd)
	const usbdevfsReset = 0x5514 // _IO('U', 20)
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), usbdevfsReset, 0); errno != 0 {
		log.Warn().Str("subsys", who).Int("errno", int(errno)).Str("path", usbDevPath).Msg("usb reset ioctl failed")
		return false
	}
	log.Info().Str("subsys", who).Str("id", id).Str("usb", usbDevPath).Msg("usb device reset completed")
	return true
}

func readIntFile(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// PortByRole returns the serial port currently claimed for a device role,
// or "" when none is claimed. Used to resolve the port for a hard reset.
func (r *DeviceRegistry) PortByRole(role DeviceRole) string {
	if r == nil {
		return ""
	}
	for _, e := range r.ListAll() {
		if e != nil && e.Role == role && e.DevPath != "" {
			return e.DevPath
		}
	}
	return ""
}
