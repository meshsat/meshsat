package transport

// rawSerialPort provides serial I/O for the RockBLOCK 9704 (FTDI FT234XD).
//
// Uses TCGETS2/TCSETS2 for baud rates above 38400 (standard TCSETS silently
// fails on ARM64). Blocking read() with VMIN=0/VTIME for timeouts — the
// read() syscall is required to keep the Linux USB serial driver's tty
// unthrottled and URBs resubmitting. FIONREAD/select/poll polling without
// calling read() breaks the URB submission chain and causes permanent
// read stalls.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/unix"
)

// rawSerialPort wraps a raw file descriptor for serial I/O.
// Uses blocking read() with VMIN/VTIME for timeouts.
type rawSerialPort struct {
	fd       int
	path     string
	timeout  time.Duration
	lastRead time.Time
}

// openRawSerial opens a serial port with proper baud rate via TCSETS2.
func openRawSerial(path string, baud int) (*rawSerialPort, error) {
	baudConst, ok := baudRateMap[baud]
	if !ok {
		return nil, fmt.Errorf("unsupported baud rate: %d", baud)
	}

	// Open WITHOUT O_NONBLOCK — we use blocking read() with VTIME for timeouts.
	// O_NONBLOCK is removed so read() actually enters the tty read path, which
	// keeps the USB serial driver's URBs resubmitting. With O_NONBLOCK + FIONREAD
	// polling, read() is never called and URBs stop being submitted.
	// O_NOCTTY prevents this from becoming the controlling terminal.
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// TCGETS2/TCSETS2 for baud rates above 38400.
	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS2)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("get termios2 %s: %w", path, err)
	}

	// Baud rate
	termios.Cflag &^= unix.CBAUD
	termios.Cflag |= baudConst
	termios.Ispeed = baudConst
	termios.Ospeed = baudConst

	// 8N1
	termios.Cflag &^= unix.CSIZE
	termios.Cflag |= unix.CS8
	termios.Cflag &^= unix.PARENB
	termios.Cflag &^= unix.CSTOPB
	termios.Cflag |= unix.CLOCAL | unix.CREAD

	// Raw mode
	termios.Iflag &^= unix.IXON | unix.IXOFF | unix.IXANY | unix.ICRNL
	termios.Lflag &^= unix.ICANON | unix.ECHO | unix.ECHOE | unix.ISIG

	// VMIN=0, VTIME=5 (500ms timeout in 100ms units).
	// read() returns when data is available OR after VTIME expires.
	// This is the key: the read() syscall exercises the tty read path,
	// which triggers tty_unthrottle() and keeps URBs being resubmitted.
	termios.Cc[unix.VMIN] = 0
	termios.Cc[unix.VTIME] = 5 // 500ms in deciseconds

	if err := unix.IoctlSetTermios(fd, unix.TCSETS2, termios); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("set termios2 %s: %w", path, err)
	}

	// Verify baud rate
	verify, err := unix.IoctlGetTermios(fd, unix.TCGETS2)
	if err == nil {
		actualBaud := verify.Cflag & unix.CBAUD
		log.Info().
			Uint32("requested", baudConst).
			Uint32("actual_cbaud", actualBaud).
			Uint32("actual_ispeed", verify.Ispeed).
			Uint32("actual_ospeed", verify.Ospeed).
			Msg("ftdi: baud rate after TCSETS2")
		if actualBaud != baudConst {
			log.Error().Uint32("requested", baudConst).Uint32("actual", actualBaud).
				Msg("ftdi: BAUD RATE MISMATCH")
		}
	}

	// Flush stale data
	unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIOFLUSH)

	p := &rawSerialPort{
		fd:       fd,
		path:     path,
		timeout:  500 * time.Millisecond,
		lastRead: time.Now(),
	}

	p.tuneFTDI()
	return p, nil
}

// tuneFTDI sets latency timer to 1ms and disables USB autosuspend via sysfs.
func (p *rawSerialPort) tuneFTDI() {
	devName := filepath.Base(p.path)

	latencyPath := fmt.Sprintf("/sys/bus/usb-serial/devices/%s/latency_timer", devName)
	if err := os.WriteFile(latencyPath, []byte("1"), 0644); err != nil {
		log.Debug().Err(err).Str("path", latencyPath).Msg("ftdi: failed to set latency_timer (non-fatal)")
	} else {
		log.Info().Str("device", devName).Msg("ftdi: latency_timer set to 1ms")
	}

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

// Read calls unix.Read() directly — a blocking read with VTIME timeout.
// The read() syscall is REQUIRED to keep the USB serial driver healthy:
// it exercises the tty read path which triggers tty_unthrottle() and URB
// resubmission. Polling with FIONREAD/select/poll without calling read()
// causes the URB chain to break and reads to stall permanently.
//
// Returns (0, nil) on VTIME timeout to match go.bug.st/serial's behavior.
func (p *rawSerialPort) Read(buf []byte) (int, error) {
	n, err := unix.Read(p.fd, buf)
	if err != nil {
		if err == unix.EINTR {
			return 0, nil
		}
		return 0, fmt.Errorf("read: %w", err)
	}
	if n > 0 {
		p.lastRead = time.Now()
	}
	return n, nil // n=0 on VTIME timeout
}

// Write sends data to the serial port. Blocking at 230400 baud is <1ms
// for typical JSPR commands (~30 bytes).
func (p *rawSerialPort) Write(data []byte) (int, error) {
	return unix.Write(p.fd, data)
}

// SetReadTimeout is a no-op — VTIME is set once during openRawSerial and
// never changed. Repeated TCGETS2/TCSETS2 ioctl calls disrupt the USB serial
// driver's tty state, causing reads to stall. Minicom works because it sets
// termios once and never touches it again.
func (p *rawSerialPort) SetReadTimeout(d time.Duration) error {
	p.timeout = d
	return nil
}

// Close closes the serial port.
func (p *rawSerialPort) Close() error {
	return unix.Close(p.fd)
}

// LastRead returns the time of the last successful read.
func (p *rawSerialPort) LastRead() time.Time {
	return p.lastRead
}

// Path returns the serial device path.
func (p *rawSerialPort) Path() string {
	return p.path
}

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
