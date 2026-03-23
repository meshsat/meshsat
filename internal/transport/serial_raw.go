package transport

// rawSerialPort opens a serial port using raw Linux syscalls, bypassing
// go.bug.st/serial. This fixes a bug where go.bug.st/serial's port.Read()
// hangs permanently after port.Write() on FTDI USB-serial chips (specifically
// the FT234XD in the RockBLOCK 9704).
//
// Root cause: go.bug.st/serial uses VMIN/VTIME termios settings for read
// timeouts. On FTDI chips, the kernel tty driver's VTIME timer state can
// get corrupted after a Write() call, causing Read() to block indefinitely.
//
// Fix: use poll() syscall for read timeouts and unix.Read()/unix.Write() for
// I/O. This is the same approach Python's pyserial uses (select/poll + read),
// which works correctly with the same hardware.

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// rawSerialPort wraps a raw file descriptor for serial I/O with poll()-based
// timeouts. Implements the jsprPort interface used by jsprConn.
type rawSerialPort struct {
	fd      int
	timeout time.Duration
}

// openRawSerial opens a serial port using raw syscalls and returns a
// poll()-based serial port. This avoids both the VMIN/VTIME hang bug in
// go.bug.st/serial and epoll notification issues with FTDI serial devices.
func openRawSerial(path string, baud int) (*rawSerialPort, error) {
	baudConst, ok := baudRateMap[baud]
	if !ok {
		return nil, fmt.Errorf("unsupported baud rate: %d", baud)
	}

	// Open with O_NOCTTY to avoid becoming the controlling terminal.
	// Do NOT use O_NONBLOCK — we use blocking I/O with poll() for timeouts.
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY, 0)
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

	// VMIN=1, VTIME=0 — blocking reads (return when at least 1 byte available).
	// Actual timeout control is via poll() before each read.
	termios.Cc[unix.VMIN] = 1
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

	return &rawSerialPort{fd: fd, timeout: 100 * time.Millisecond}, nil
}

// Read reads from the serial port with the configured timeout.
// Uses poll() syscall to wait for data, then unix.Read() to fetch it.
// Returns (0, nil) on timeout to match go.bug.st/serial's behavior.
func (p *rawSerialPort) Read(buf []byte) (int, error) {
	if p.timeout > 0 {
		timeoutMs := int(p.timeout / time.Millisecond)
		if timeoutMs < 1 {
			timeoutMs = 1
		}
		fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, timeoutMs)
		if err != nil {
			// EINTR is normal (signal interrupted poll) — treat as timeout
			if err == unix.EINTR {
				return 0, nil
			}
			return 0, fmt.Errorf("poll: %w", err)
		}
		if n == 0 {
			return 0, nil // timeout, no data — matches go.bug.st/serial behavior
		}
		// Check for errors on the fd
		if fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return 0, fmt.Errorf("poll: fd error (revents=0x%x)", fds[0].Revents)
		}
	}

	n, err := unix.Read(p.fd, buf)
	if err != nil {
		return 0, fmt.Errorf("read: %w", err)
	}
	return n, nil
}

// Write writes data to the serial port.
func (p *rawSerialPort) Write(data []byte) (int, error) {
	return unix.Write(p.fd, data)
}

// SetReadTimeout sets the timeout for subsequent Read calls.
// The timeout is applied via poll() before each read.
func (p *rawSerialPort) SetReadTimeout(d time.Duration) error {
	p.timeout = d
	return nil
}

// Close closes the serial port file descriptor.
func (p *rawSerialPort) Close() error {
	return unix.Close(p.fd)
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
