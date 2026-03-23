package transport

// rawSerialPort opens a serial port using raw Linux syscalls, bypassing
// go.bug.st/serial. This fixes a bug where go.bug.st/serial's port.Read()
// hangs permanently after port.Write() on FTDI USB-serial chips (specifically
// the FT234XD in the RockBLOCK 9704).
//
// This implementation matches the official RockBLOCK 9704 C library
// (github.com/rock7/RockBLOCK-9704, src/serial_presets/serial_linux/serial_linux.c)
// exactly:
//   - Open with O_RDWR | O_NOCTTY | O_SYNC | O_NONBLOCK
//   - serialPeek: ioctl(FIONREAD) to check available bytes
//   - serialRead: select() with 500ms timeout, then read() byte-by-byte
//   - serialWrite: write() with EAGAIN retry loop

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// rawSerialPort wraps a raw file descriptor for serial I/O, matching the
// official RockBLOCK 9704 C library's Linux serial preset.
// Implements the jsprPort interface used by jsprConn.
type rawSerialPort struct {
	fd      int
	timeout time.Duration
}

// openRawSerial opens a serial port matching the official RockBLOCK 9704
// C library's openPortLinux() + configurePortLinux() exactly.
func openRawSerial(path string, baud int) (*rawSerialPort, error) {
	baudConst, ok := baudRateMap[baud]
	if !ok {
		return nil, fmt.Errorf("unsupported baud rate: %d", baud)
	}

	// Match C library: open(port, O_RDWR | O_NOCTTY | O_SYNC | O_NONBLOCK)
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_SYNC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// Match C library's configurePortLinux() — tcgetattr + modify + tcsetattr
	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("get termios %s: %w", path, err)
	}

	// cfsetispeed/cfsetospeed equivalent
	termios.Cflag &^= unix.CBAUD
	termios.Cflag |= baudConst
	termios.Ispeed = baudConst
	termios.Ospeed = baudConst

	// C library: options.c_cflag &= ~CSIZE; options.c_cflag |= CS8;
	termios.Cflag &^= unix.CSIZE
	termios.Cflag |= unix.CS8

	// C library: options.c_cflag &= ~PARENB; options.c_cflag &= ~CSTOPB;
	termios.Cflag &^= unix.PARENB
	termios.Cflag &^= unix.CSTOPB

	// C library: options.c_cflag |= CLOCAL | CREAD;
	termios.Cflag |= unix.CLOCAL | unix.CREAD

	// C library: options.c_iflag &= ~(IXON | IXOFF | IXANY | ICRNL);
	termios.Iflag &^= unix.IXON | unix.IXOFF | unix.IXANY | unix.ICRNL

	// C library: options.c_lflag &= ~(ICANON | ECHO | ECHOE | ISIG);
	termios.Lflag &^= unix.ICANON | unix.ECHO | unix.ECHOE | unix.ISIG

	// C library does NOT set VMIN/VTIME — leave at kernel defaults.
	// With O_NONBLOCK, reads return immediately with EAGAIN if no data.

	// tcsetattr(fd, TCSANOW, &options)
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, termios); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("set termios %s: %w", path, err)
	}

	// Flush any stale data
	unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIOFLUSH)

	return &rawSerialPort{fd: fd, timeout: 500 * time.Millisecond}, nil
}

// Read waits for data using ioctl(FIONREAD) polling, then reads with unix.Read().
//
// The C library uses select() for read timeouts, but on the FTDI FT234XD
// driver (kernel 6.17), select()/poll()/epoll all stop notifying after the
// initial serial burst — a known issue with the ftdi_sio driver's tty
// wake-up mechanism. ioctl(FIONREAD) bypasses the notification system entirely
// by directly querying the tty input buffer, matching the C library's
// serialPeek() (peekLinux) function which also uses ioctl(FIONREAD).
//
// Returns (0, nil) on timeout to match go.bug.st/serial's behavior.
func (p *rawSerialPort) Read(buf []byte) (int, error) {
	deadline := time.Now().Add(p.timeout)
	for time.Now().Before(deadline) {
		// ioctl(FIONREAD) — same as C library's peekLinux()/serialPeek()
		avail, err := unix.IoctlGetInt(p.fd, unix.TIOCINQ)
		if err != nil {
			return 0, fmt.Errorf("fionread: %w", err)
		}
		if avail > 0 {
			n, err := unix.Read(p.fd, buf)
			if err != nil {
				if err == unix.EAGAIN {
					continue // race: data consumed between peek and read
				}
				return 0, fmt.Errorf("read: %w", err)
			}
			return n, nil
		}
		// No data — sleep 2ms to avoid busy-spinning.
		// At 230400 baud, 2ms ≈ 58 bytes — well within tolerance.
		time.Sleep(2 * time.Millisecond)
	}
	return 0, nil // timeout
}

// Write matches the C library's writeLinux() — write() with EAGAIN retry.
func (p *rawSerialPort) Write(data []byte) (int, error) {
	sent := 0
	for sent < len(data) {
		n, err := unix.Write(p.fd, data[sent:])
		if err != nil {
			if err == unix.EAGAIN {
				continue // retry (matches C library's do-while loop)
			}
			return sent, fmt.Errorf("write: %w", err)
		}
		sent += n
	}
	return sent, nil
}

// SetReadTimeout sets the timeout for subsequent Read calls (select timeout).
func (p *rawSerialPort) SetReadTimeout(d time.Duration) error {
	p.timeout = d
	return nil
}

// Close closes the serial port file descriptor.
func (p *rawSerialPort) Close() error {
	return unix.Close(p.fd)
}

// Peek returns the number of bytes available to read without blocking.
// Matches the C library's peekLinux() — ioctl(fd, FIONREAD, &bytes).
func (p *rawSerialPort) Peek() (int, error) {
	return unix.IoctlGetInt(p.fd, unix.TIOCINQ)
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
