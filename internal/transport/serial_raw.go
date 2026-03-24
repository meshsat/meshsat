package transport

// rawSerialPort provides serial I/O for the RockBLOCK 9704 (FTDI FT234XD).
//
// Uses TCGETS2/TCSETS2 for baud rates above 38400 (standard TCSETS silently
// fails on ARM64). O_NONBLOCK + select() + read() for I/O — matching the
// official RockBLOCK 9704 C library exactly. Termios is set ONCE on open
// and never modified (repeated TCSETS2 ioctl calls disrupt the tty driver).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/unix"
)

type rawSerialPort struct {
	fd       int
	path     string
	timeout  time.Duration
	lastRead time.Time
}

func openRawSerial(path string, baud int) (*rawSerialPort, error) {
	baudConst, ok := baudRateMap[baud]
	if !ok {
		return nil, fmt.Errorf("unsupported baud rate: %d", baud)
	}

	// Match C library: O_RDWR | O_NOCTTY | O_SYNC | O_NONBLOCK
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_SYNC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// TCGETS2/TCSETS2 for correct baud rate on ARM64
	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS2)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("get termios2 %s: %w", path, err)
	}

	termios.Cflag &^= unix.CBAUD
	termios.Cflag |= baudConst
	termios.Ispeed = baudConst
	termios.Ospeed = baudConst
	termios.Cflag &^= unix.CSIZE
	termios.Cflag |= unix.CS8
	termios.Cflag &^= unix.PARENB | unix.CSTOPB
	termios.Cflag |= unix.CLOCAL | unix.CREAD
	termios.Iflag &^= unix.IXON | unix.IXOFF | unix.IXANY | unix.ICRNL
	termios.Lflag &^= unix.ICANON | unix.ECHO | unix.ECHOE | unix.ISIG
	// C library does not set VMIN/VTIME — leave at kernel defaults

	if err := unix.IoctlSetTermios(fd, unix.TCSETS2, termios); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("set termios2 %s: %w", path, err)
	}

	// Verify baud
	if v, err := unix.IoctlGetTermios(fd, unix.TCGETS2); err == nil {
		log.Info().Uint32("requested", baudConst).Uint32("actual_cbaud", v.Cflag&unix.CBAUD).
			Uint32("ispeed", v.Ispeed).Uint32("ospeed", v.Ospeed).Msg("ftdi: baud rate after TCSETS2")
	}

	unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIOFLUSH)

	p := &rawSerialPort{fd: fd, path: path, timeout: 500 * time.Millisecond, lastRead: time.Now()}
	p.tuneFTDI()
	return p, nil
}

func (p *rawSerialPort) tuneFTDI() {
	devName := filepath.Base(p.path)
	latencyPath := fmt.Sprintf("/sys/bus/usb-serial/devices/%s/latency_timer", devName)
	if err := os.WriteFile(latencyPath, []byte("1"), 0644); err != nil {
		log.Debug().Err(err).Str("path", latencyPath).Msg("ftdi: failed to set latency_timer")
	} else {
		log.Info().Str("device", devName).Msg("ftdi: latency_timer set to 1ms")
	}
	if usbDev := findUSBDeviceSysfs(devName); usbDev != "" {
		if err := os.WriteFile(filepath.Join(usbDev, "power", "control"), []byte("on"), 0644); err != nil {
			log.Debug().Err(err).Msg("ftdi: failed to disable autosuspend")
		}
	}
}

func findUSBDeviceSysfs(devName string) string {
	sysPath := fmt.Sprintf("/sys/class/tty/%s/device", devName)
	resolved, err := filepath.EvalSymlinks(sysPath)
	if err != nil {
		return ""
	}
	current := resolved
	for i := 0; i < 6; i++ {
		current = filepath.Dir(current)
		if _, err := os.ReadFile(filepath.Join(current, "idVendor")); err == nil {
			return current
		}
	}
	return ""
}

// Read uses select() + read() — matching the C library's readLinux() exactly.
// select() waits for data with timeout, then read() fetches it.
// Both select() and read() are real syscalls that keep the tty driver active.
func (p *rawSerialPort) Read(buf []byte) (int, error) {
	timeoutUs := p.timeout.Microseconds()
	tv := unix.Timeval{
		Sec:  timeoutUs / 1_000_000,
		Usec: timeoutUs % 1_000_000,
	}
	var fds unix.FdSet
	fds.Set(p.fd)

	n, err := unix.Select(p.fd+1, &fds, nil, nil, &tv)
	if err != nil {
		if err == unix.EINTR {
			return 0, nil
		}
		return 0, fmt.Errorf("select: %w", err)
	}
	if n == 0 {
		return 0, nil // timeout
	}

	nr, err := unix.Read(p.fd, buf)
	if err != nil {
		if err == unix.EAGAIN {
			return 0, nil
		}
		return 0, fmt.Errorf("read: %w", err)
	}
	if nr > 0 {
		p.lastRead = time.Now()
	}
	return nr, nil
}

// Write with EAGAIN retry — matches C library's writeLinux().
func (p *rawSerialPort) Write(data []byte) (int, error) {
	sent := 0
	for sent < len(data) {
		n, err := unix.Write(p.fd, data[sent:])
		if err != nil {
			if err == unix.EAGAIN {
				continue
			}
			return sent, fmt.Errorf("write: %w", err)
		}
		sent += n
	}
	return sent, nil
}

// SetReadTimeout — no-op. Termios is set once and never changed.
func (p *rawSerialPort) SetReadTimeout(d time.Duration) error {
	p.timeout = d
	return nil
}

func (p *rawSerialPort) Close() error        { return unix.Close(p.fd) }
func (p *rawSerialPort) LastRead() time.Time { return p.lastRead }
func (p *rawSerialPort) Path() string        { return p.path }

func readLatencyTimer(devName string) string {
	data, err := os.ReadFile(fmt.Sprintf("/sys/bus/usb-serial/devices/%s/latency_timer", devName))
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}

var baudRateMap = map[int]uint32{
	9600: unix.B9600, 19200: unix.B19200, 38400: unix.B38400,
	57600: unix.B57600, 115200: unix.B115200, 230400: unix.B230400,
	460800: unix.B460800, 921600: unix.B921600,
}
