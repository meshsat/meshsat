package hemb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// TUNDevice is an abstraction over a TUN file descriptor.
// Production: wraps /dev/net/tun via ioctl.
// Tests: wraps os.Pipe for CI without CAP_NET_ADMIN.
type TUNDevice interface {
	Read(buf []byte) (int, error)
	Write(buf []byte) (int, error)
	Close() error
	Name() string
}

// TUNConfig configures the TUN adapter.
type TUNConfig struct {
	Name    string // interface name, default "hemb0"
	MTU     int    // 0 = compute from bearer set
	EventCh chan<- Event
}

// TUNStats reports TUN adapter counters.
type TUNStats struct {
	PacketsSent    int64 `json:"packets_sent"`
	PacketsRecv    int64 `json:"packets_recv"`
	PacketsDropped int64 `json:"packets_dropped"`
	BytesSent      int64 `json:"bytes_sent"`
	BytesRecv      int64 `json:"bytes_recv"`
}

// TUNAdapter bridges a TUN device to a HeMB Bonder.
// IP packets read from the TUN fd are sent via Bonder.Send().
// Reassembled payloads are written back to the TUN fd via DeliverFn.
type TUNAdapter struct {
	dev     TUNDevice
	bonder  Bonder
	mtu     int
	eventCh chan<- Event

	stats struct {
		packetsSent    atomic.Int64
		packetsRecv    atomic.Int64
		packetsDropped atomic.Int64
		bytesSent      atomic.Int64
		bytesRecv      atomic.Int64
	}

	closeOnce sync.Once
	closed    chan struct{}
}

// NewTUNAdapter creates a TUN adapter and a wired Bonder.
// The returned Bonder has DeliverFn pre-wired to write reassembled payloads
// to the TUN device. The caller must call Start() to begin the read loop.
func NewTUNAdapter(dev TUNDevice, bearers []BearerProfile, cfg TUNConfig) (*TUNAdapter, Bonder) {
	mtu := cfg.MTU
	if mtu == 0 {
		mtu = ComputeTUNMTU(bearers)
	}

	t := &TUNAdapter{
		dev:     dev,
		mtu:     mtu,
		eventCh: cfg.EventCh,
		closed:  make(chan struct{}),
	}

	// Create bonder with DeliverFn wired to TUN write.
	bonder := NewBonder(Options{
		Bearers:   bearers,
		DeliverFn: t.deliverToTUN,
		EventCh:   cfg.EventCh,
	})
	t.bonder = bonder

	return t, bonder
}

// Start begins the read loop, reading IP packets from the TUN device and
// sending them through the HeMB bonder. Blocks until ctx is cancelled or
// the TUN device is closed.
func (t *TUNAdapter) Start(ctx context.Context) error {
	buf := make([]byte, t.mtu+64) // safety margin

	// Close the device when context is cancelled to unblock Read().
	go func() {
		select {
		case <-ctx.Done():
			t.Close()
		case <-t.closed:
		}
	}()

	for {
		n, err := t.dev.Read(buf)
		if err != nil {
			select {
			case <-t.closed:
				return nil
			default:
			}
			if errors.Is(err, os.ErrClosed) || errors.Is(err, io.EOF) {
				return nil
			}
			t.stats.packetsDropped.Add(1)
			continue
		}
		if n == 0 {
			continue
		}

		// Copy packet — buf is reused on next iteration.
		packet := make([]byte, n)
		copy(packet, buf[:n])

		t.stats.packetsSent.Add(1)
		t.stats.bytesSent.Add(int64(n))
		emit(t.eventCh, EventTUNPacketSent, TUNPacketPayload{
			Size:      n,
			Direction: "tx",
		})

		if err := t.bonder.Send(ctx, packet); err != nil {
			t.stats.packetsDropped.Add(1)
			emit(t.eventCh, EventTUNPacketDropped, TUNPacketPayload{
				Size:      n,
				Direction: "tx",
				Error:     err.Error(),
			})
		}
	}
}

// deliverToTUN writes a reassembled payload from the bonder to the TUN device.
// Called from the reassembly goroutine via Options.DeliverFn.
func (t *TUNAdapter) deliverToTUN(payload []byte) {
	t.stats.packetsRecv.Add(1)
	t.stats.bytesRecv.Add(int64(len(payload)))
	emit(t.eventCh, EventTUNPacketRecv, TUNPacketPayload{
		Size:      len(payload),
		Direction: "rx",
	})

	if _, err := t.dev.Write(payload); err != nil {
		t.stats.packetsDropped.Add(1)
		emit(t.eventCh, EventTUNPacketDropped, TUNPacketPayload{
			Size:      len(payload),
			Direction: "rx",
			Error:     err.Error(),
		})
	}
}

// Close shuts down the read loop and closes the TUN device.
func (t *TUNAdapter) Close() error {
	var err error
	t.closeOnce.Do(func() {
		close(t.closed)
		err = t.dev.Close()
	})
	return err
}

// Stats returns current TUN adapter counters.
func (t *TUNAdapter) Stats() TUNStats {
	return TUNStats{
		PacketsSent:    t.stats.packetsSent.Load(),
		PacketsRecv:    t.stats.packetsRecv.Load(),
		PacketsDropped: t.stats.packetsDropped.Load(),
		BytesSent:      t.stats.bytesSent.Load(),
		BytesRecv:      t.stats.bytesRecv.Load(),
	}
}

// Bonder returns the underlying HeMB bonder instance.
func (t *TUNAdapter) Bonder() Bonder {
	return t.bonder
}

// --- TUN device creation (requires CAP_NET_ADMIN) ---

// tunDevice wraps a raw TUN file descriptor.
type tunDevice struct {
	fd   int
	file *os.File
	name string
}

func (d *tunDevice) Read(buf []byte) (int, error)  { return d.file.Read(buf) }
func (d *tunDevice) Write(buf []byte) (int, error) { return d.file.Write(buf) }
func (d *tunDevice) Close() error                  { return d.file.Close() }
func (d *tunDevice) Name() string                  { return d.name }

// OpenTUN creates a TUN device via /dev/net/tun ioctl.
// Requires CAP_NET_ADMIN. The interface must be brought up and assigned an
// IP address by the caller (e.g., ip addr add 10.99.0.1/30 dev hemb0 && ip link set hemb0 up).
func OpenTUN(name string, mtu int) (TUNDevice, error) {
	if name == "" {
		name = "hemb0"
	}

	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("hemb: open /dev/net/tun: %w", err)
	}

	ifr, err := unix.NewIfreq(name)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("hemb: new ifreq %q: %w", name, err)
	}
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("hemb: TUNSETIFF %q: %w", name, err)
	}

	// Read back the actual interface name (kernel may append a number).
	actualName := ifr.Name()

	// Set MTU if specified.
	if mtu > 0 {
		if err := setTUNMTU(actualName, mtu); err != nil {
			unix.Close(fd)
			return nil, err
		}
	}

	file := os.NewFile(uintptr(fd), "/dev/net/tun")

	return &tunDevice{fd: fd, file: file, name: actualName}, nil
}

// setTUNMTU sets the MTU on a named interface via SIOCSIFMTU ioctl.
func setTUNMTU(name string, mtu int) error {
	ctlFd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("hemb: control socket: %w", err)
	}
	defer unix.Close(ctlFd)

	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("hemb: new ifreq for MTU: %w", err)
	}
	ifr.SetUint32(uint32(mtu))
	if err := unix.IoctlIfreq(ctlFd, unix.SIOCSIFMTU, ifr); err != nil {
		return fmt.Errorf("hemb: SIOCSIFMTU %q %d: %w", name, mtu, err)
	}
	return nil
}

// ComputeTUNMTU calculates the maximum IP payload the TUN can accept,
// derived from the minimum effective HeMB capacity across all bearers.
// Returns at least 68 (RFC 791 minimum IP reassembly buffer).
func ComputeTUNMTU(bearers []BearerProfile) int {
	if len(bearers) == 0 {
		return 1500 // default Ethernet MTU
	}

	minPayload := int(^uint(0) >> 1) // max int
	for _, b := range bearers {
		overhead := HeaderOverhead(b.HeaderMode)
		// K coefficient bytes (1 per source segment, minimum 1)
		coeffOverhead := 1
		effective := b.MTU - overhead - coeffOverhead
		if effective < minPayload {
			minPayload = effective
		}
	}

	// Floor at RFC 791 minimum.
	if minPayload < 68 {
		minPayload = 68
	}
	return minPayload
}
