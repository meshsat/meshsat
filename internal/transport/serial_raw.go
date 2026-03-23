package transport

// rawSerialPort provides serial I/O for the RockBLOCK 9704 (FTDI FT234XD),
// working around four compounding failures on ARM Linux:
//
//  1. USB URB -EPIPE death: the ftdi_sio generic_read_bulk_callback permanently
//     stops resubmitting read URBs on ARM host controller errors. The only
//     recovery is closing and reopening the serial port.
//
//  2. FTDI 16ms latency timer: creates bursty USB traffic that ARM EHCI
//     controllers handle poorly. Reduced to 1ms via sysfs.
//
//  3. USB autosuspend: can strand the FTDI chip after idle periods.
//     Disabled via sysfs on connect.
//
//  4. Go's edge-triggered epoll (EPOLLET): incompatible with tty poll
//     semantics. Avoided by using raw fd + FIONREAD polling instead of
//     os.File/SetReadDeadline.
//
// Read strategy: ioctl(FIONREAD) polling with 1ms sleep intervals, which
// directly queries the tty input buffer without depending on any kernel
// notification mechanism. When URBs die, FIONREAD returns 0 and the
// watchdog (in DirectIMTTransport) cycles the port.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/unix"
)

// rawSerialPort wraps a raw file descriptor for serial I/O with FIONREAD
// polling. Implements the jsprPort interface used by jsprConn.
type rawSerialPort struct {
	fd       int
	path     string // device path for sysfs lookups
	timeout  time.Duration
	lastRead time.Time // tracks last successful read for watchdog
}

// openRawSerial opens a serial port with FTDI-specific workarounds applied.
func openRawSerial(path string, baud int) (*rawSerialPort, error) {
	baudConst, ok := baudRateMap[baud]
	if !ok {
		return nil, fmt.Errorf("unsupported baud rate: %d", baud)
	}

	// Open with O_NONBLOCK + O_NOCTTY + O_SYNC (matches C library).
	// O_NONBLOCK prevents blocking on modem control signals during open
	// and ensures reads return EAGAIN instead of blocking.
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_SYNC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// Configure termios using TCGETS2/TCSETS2 for proper baud rate support.
	// Standard TCGETS/TCSETS only handles baud rates up to B38400 in the CBAUD
	// field. Baud rates above 38400 (like 230400) require TCGETS2/TCSETS2 which
	// properly handles the extended baud rate range via Ispeed/Ospeed fields.
	// Using TCGETS/TCSETS with B230400 silently fails on ARM64 — the baud rate
	// stays at the kernel default (9600), causing the JSPR modem to not respond.
	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS2)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("get termios2 %s: %w", path, err)
	}

	// Set baud rate via CBAUD + Ispeed/Ospeed (TCSETS2 supports all rates)
	termios.Cflag &^= unix.CBAUD
	termios.Cflag |= baudConst
	termios.Ispeed = baudConst
	termios.Ospeed = baudConst

	termios.Cflag &^= unix.CSIZE
	termios.Cflag |= unix.CS8
	termios.Cflag &^= unix.PARENB
	termios.Cflag &^= unix.CSTOPB
	termios.Cflag |= unix.CLOCAL | unix.CREAD

	termios.Iflag &^= unix.IXON | unix.IXOFF | unix.IXANY | unix.ICRNL
	termios.Lflag &^= unix.ICANON | unix.ECHO | unix.ECHOE | unix.ISIG

	if err := unix.IoctlSetTermios(fd, unix.TCSETS2, termios); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("set termios2 %s: %w", path, err)
	}

	unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIOFLUSH)

	p := &rawSerialPort{
		fd:       fd,
		path:     path,
		timeout:  500 * time.Millisecond,
		lastRead: time.Now(),
	}

	// Apply FTDI-specific workarounds via sysfs
	p.tuneFTDI()

	return p, nil
}

// tuneFTDI applies FTDI-specific sysfs workarounds:
//   - Latency timer 16ms → 1ms (reduces bursty USB traffic that kills ARM host controllers)
//   - Disable USB autosuspend (prevents FTDI chip from entering unrecoverable suspend state)
func (p *rawSerialPort) tuneFTDI() {
	devName := filepath.Base(p.path) // e.g. "ttyUSB0"

	// 1. Set latency timer to 1ms
	latencyPath := fmt.Sprintf("/sys/bus/usb-serial/devices/%s/latency_timer", devName)
	if err := os.WriteFile(latencyPath, []byte("1"), 0644); err != nil {
		log.Debug().Err(err).Str("path", latencyPath).Msg("ftdi: failed to set latency_timer (non-fatal)")
	} else {
		log.Info().Str("device", devName).Msg("ftdi: latency_timer set to 1ms")
	}

	// 2. Disable USB autosuspend — walk sysfs to find the USB device
	usbDevPath := findUSBDeviceSysfs(devName)
	if usbDevPath != "" {
		controlPath := filepath.Join(usbDevPath, "power", "control")
		if err := os.WriteFile(controlPath, []byte("on"), 0644); err != nil {
			log.Debug().Err(err).Str("path", controlPath).Msg("ftdi: failed to disable autosuspend (non-fatal)")
		} else {
			log.Info().Str("device", devName).Msg("ftdi: USB autosuspend disabled")
		}
	}
}

// findUSBDeviceSysfs walks the sysfs tree from a tty device to find the parent
// USB device directory (containing power/control).
func findUSBDeviceSysfs(devName string) string {
	sysPath := fmt.Sprintf("/sys/class/tty/%s/device", devName)
	resolved, err := filepath.EvalSymlinks(sysPath)
	if err != nil {
		return ""
	}
	// Walk up to find the USB device (has "idVendor" file)
	current := resolved
	for i := 0; i < 6; i++ {
		current = filepath.Dir(current)
		if _, err := os.ReadFile(filepath.Join(current, "idVendor")); err == nil {
			return current
		}
	}
	return ""
}

// Read waits for data using ioctl(FIONREAD) polling, then reads with unix.Read().
// FIONREAD directly queries the tty input buffer, bypassing the kernel notification
// system (select/poll/epoll) which fails on ftdi_sio after URB errors.
// Returns (0, nil) on timeout to match go.bug.st/serial's behavior.
func (p *rawSerialPort) Read(buf []byte) (int, error) {
	deadline := time.Now().Add(p.timeout)
	for time.Now().Before(deadline) {
		avail, err := unix.IoctlGetInt(p.fd, unix.TIOCINQ)
		if err != nil {
			return 0, fmt.Errorf("fionread: %w", err)
		}
		if avail > 0 {
			n, err := unix.Read(p.fd, buf)
			if err != nil {
				if err == unix.EAGAIN {
					continue
				}
				return 0, fmt.Errorf("read: %w", err)
			}
			p.lastRead = time.Now()
			return n, nil
		}
		// 1ms sleep — minimum to avoid starving the tty flip buffer workqueue.
		// Faster polling causes FIONREAD to always return 0.
		time.Sleep(time.Millisecond)
	}
	return 0, nil
}

// DiagnosticCheck probes the fd state without reading data (safe to call
// concurrently with readerLoop). Returns a string describing what FIONREAD
// and select() report.
func (p *rawSerialPort) DiagnosticCheck() string {
	// Check FIONREAD (non-destructive — just queries buffer count)
	avail, fErr := unix.IoctlGetInt(p.fd, unix.TIOCINQ)
	if fErr != nil {
		return fmt.Sprintf("fionread_err=%v", fErr)
	}

	// Check select() readiness with 0 timeout (non-blocking poll)
	tv := unix.Timeval{Sec: 0, Usec: 0}
	var readFds unix.FdSet
	readFds.Set(p.fd)
	ready, sErr := unix.Select(p.fd+1, &readFds, nil, nil, &tv)

	selectState := "not_ready"
	if sErr != nil {
		selectState = fmt.Sprintf("err=%v", sErr)
	} else if ready > 0 {
		selectState = "ready"
	}

	return fmt.Sprintf("fionread=%d select=%s fd=%d", avail, selectState, p.fd)
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

// SetReadTimeout sets the timeout for subsequent Read calls.
func (p *rawSerialPort) SetReadTimeout(d time.Duration) error {
	p.timeout = d
	return nil
}

// Close closes the serial port file descriptor.
func (p *rawSerialPort) Close() error {
	return unix.Close(p.fd)
}

// LastRead returns the time of the last successful read.
// Used by the watchdog to detect stale connections.
func (p *rawSerialPort) LastRead() time.Time {
	return p.lastRead
}

// Peek returns the number of bytes available to read without blocking.
func (p *rawSerialPort) Peek() (int, error) {
	return unix.IoctlGetInt(p.fd, unix.TIOCINQ)
}

// Path returns the serial device path.
func (p *rawSerialPort) Path() string {
	return p.path
}

// ResetUSB forces a USB re-enumeration via sysfs authorized toggle.
// This is the nuclear option when the URB is permanently dead.
func (p *rawSerialPort) ResetUSB() error {
	devName := filepath.Base(p.path)
	usbDevPath := findUSBDeviceSysfs(devName)
	if usbDevPath == "" {
		return fmt.Errorf("cannot find USB device sysfs path for %s", p.path)
	}
	authPath := filepath.Join(usbDevPath, "authorized")

	// Deauthorize (disconnects device)
	if err := os.WriteFile(authPath, []byte("0"), 0644); err != nil {
		return fmt.Errorf("deauthorize USB: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Reauthorize (forces re-enumeration and fresh URB submission)
	if err := os.WriteFile(authPath, []byte("1"), 0644); err != nil {
		return fmt.Errorf("reauthorize USB: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	return nil
}

// deviceSysfsPath returns the sysfs path for a tty device name, resolving symlinks.
func deviceSysfsPath(devName string) string {
	sysPath := fmt.Sprintf("/sys/class/tty/%s/device", devName)
	resolved, err := filepath.EvalSymlinks(sysPath)
	if err != nil {
		return ""
	}
	return resolved
}

// readLatencyTimer reads the current FTDI latency timer value from sysfs.
func readLatencyTimer(devName string) string {
	path := fmt.Sprintf("/sys/bus/usb-serial/devices/%s/latency_timer", devName)
	data, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}

var baudRateMap = map[int]uint32{
	9600:   unix.B9600,
	19200:  unix.B19200,
	38400:  unix.B38400,
	57600:  unix.B57600,
	115200: unix.B115200,
	230400: unix.B230400,
	460800: unix.B460800,
	921600: unix.B921600,
}
