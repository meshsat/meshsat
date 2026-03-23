package transport

// rawSerialPort opens a serial port using raw Linux syscalls and os.File,
// bypassing go.bug.st/serial. This fixes a bug where go.bug.st/serial's
// port.Read() hangs permanently after port.Write() on FTDI USB-serial chips
// (specifically the FT234XD in the RockBLOCK 9704).
//
// Root cause: go.bug.st/serial uses VMIN/VTIME termios settings for read
// timeouts. On FTDI chips, the kernel tty driver's VTIME timer state can
// get corrupted after a Write() call, causing Read() to block indefinitely.
//
// Fix: use Go's runtime poller (epoll) with SetReadDeadline() for timeouts
// instead of VMIN/VTIME. This matches how Python's pyserial works (which
// has no issue with the same hardware).

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// rawSerialPort wraps an os.File for serial I/O with epoll-based timeouts.
// Implements the jsprPort interface used by jsprConn.
type rawSerialPort struct {
	file    *os.File
	timeout time.Duration
}

// openRawSerial opens a serial port using raw syscalls and returns an os.File-backed
// serial port that uses Go's runtime poller (epoll) for read timeouts.
// This avoids the VMIN/VTIME hang bug in go.bug.st/serial on FTDI chips.
func openRawSerial(path string, baud int) (*rawSerialPort, error) {
	baudConst, ok := baudRateMap[baud]
	if !ok {
		return nil, fmt.Errorf("unsupported baud rate: %d", baud)
	}

	// Open with O_NONBLOCK so the open itself doesn't block on modem signals,
	// and so Go's runtime poller can register the fd with epoll.
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// Configure termios for raw serial communication
	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("get termios %s: %w", path, err)
	}

	// Raw mode — no echo, no canonical processing, no signals
	termios.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	termios.Oflag &^= unix.OPOST
	termios.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	termios.Cflag &^= unix.CSIZE | unix.PARENB | unix.CSTOPB | unix.CRTSCTS

	// 8N1, enable receiver, ignore modem control lines
	termios.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL

	// VMIN=0, VTIME=0 — non-blocking at the termios level.
	// Actual timeout control is via Go's epoll-based SetReadDeadline.
	termios.Cc[unix.VMIN] = 0
	termios.Cc[unix.VTIME] = 0

	// Set baud rate via Cflag speed bits and Ispeed/Ospeed fields
	termios.Cflag &^= unix.CBAUD
	termios.Cflag |= baudConst
	termios.Ispeed = baudConst
	termios.Ospeed = baudConst

	if err := unix.IoctlSetTermios(fd, unix.TCSETS, termios); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("set termios %s: %w", path, err)
	}

	// Flush any stale data in the kernel buffers
	unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIOFLUSH)

	// Wrap in os.File — Go's runtime registers the non-blocking fd with epoll,
	// enabling SetReadDeadline/SetWriteDeadline support.
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		unix.Close(fd)
		return nil, fmt.Errorf("os.NewFile failed for %s", path)
	}

	return &rawSerialPort{file: file, timeout: 100 * time.Millisecond}, nil
}

// Read reads from the serial port with the configured timeout.
// Returns (0, nil) on timeout to match go.bug.st/serial's behavior.
func (p *rawSerialPort) Read(buf []byte) (int, error) {
	if p.timeout > 0 {
		p.file.SetReadDeadline(time.Now().Add(p.timeout))
	} else {
		p.file.SetReadDeadline(time.Time{}) // no deadline
	}
	n, err := p.file.Read(buf)
	if err != nil {
		if os.IsTimeout(err) {
			return 0, nil // match go.bug.st/serial: timeout = 0 bytes, no error
		}
		return n, err
	}
	return n, nil
}

// Write writes data to the serial port.
func (p *rawSerialPort) Write(data []byte) (int, error) {
	return p.file.Write(data)
}

// SetReadTimeout sets the timeout for subsequent Read calls.
// The timeout is applied via SetReadDeadline on each Read, not via VMIN/VTIME.
func (p *rawSerialPort) SetReadTimeout(d time.Duration) error {
	p.timeout = d
	return nil
}

// Close closes the serial port.
func (p *rawSerialPort) Close() error {
	return p.file.Close()
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
