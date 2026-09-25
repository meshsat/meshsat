package routing

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/rnode"
	"meshsat/internal/transport"
)

// RNodeInterfaceConfig configures one RNode radio as a Reticulum interface.
// Port is "auto" (the device supervisor assigns it), "usb_serial:<serial>"
// (a specific board by its USB serial number), a device path, "tcp://host[:7633]"
// (RNode over WiFi) or "ble://[name|MAC]" (RNode over Bluetooth LE). [MESHSAT-1349]
type RNodeInterfaceConfig struct {
	Name        string        `json:"name"`
	Port        string        `json:"port"`
	Preset      string        `json:"preset,omitempty"`
	Params      rnode.Params  `json:"params"`
	FlowControl bool          `json:"flow_control"`
	IDCallsign  string        `json:"id_callsign,omitempty"`
	IDInterval  time.Duration `json:"id_interval,omitempty"`
	BLEAdapter  string        `json:"ble_adapter,omitempty"` // hci0 by default
}

// RNodeTCPPort is the port an RNode listens on over WiFi.
const RNodeTCPPort = 7633

// RNodeInterface is the bridge side of one RNode radio.
type RNodeInterface struct {
	cfg      RNodeInterfaceConfig
	callback func(packet []byte)

	mu       sync.Mutex
	drv      *rnode.Driver
	port     string // resolved device path for serial
	online   bool
	stopped  bool
	stopCh   chan struct{}
	wake     chan struct{}
	lastErr  string
	lastOpen time.Time
}

// NewRNodeInterface creates the interface; Start connects and keeps reconnecting.
func NewRNodeInterface(cfg RNodeInterfaceConfig, callback func(packet []byte)) *RNodeInterface {
	if p := rnode.PresetByID(cfg.Preset); p != nil && cfg.Params.Frequency == 0 {
		cfg.Params = p.Params
	}
	return &RNodeInterface{cfg: cfg, callback: callback, stopCh: make(chan struct{}), wake: make(chan struct{}, 1)}
}

// Config returns the configuration.
func (r *RNodeInterface) Config() RNodeInterfaceConfig { return r.cfg }

// Transport kind from the port string.
func (r *RNodeInterface) transport() rnode.Transport {
	switch {
	case strings.HasPrefix(strings.ToLower(r.cfg.Port), "tcp://"):
		return rnode.TCP
	case strings.HasPrefix(strings.ToLower(r.cfg.Port), "ble://"):
		return rnode.BLE
	}
	return rnode.Serial
}

// NeedsSupervisor reports whether the port comes from the device supervisor.
func (r *RNodeInterface) NeedsSupervisor() bool { return r.cfg.Port == "" || r.cfg.Port == "auto" }

// SetPort assigns a serial device (from the supervisor) and connects.
func (r *RNodeInterface) SetPort(port string) {
	r.mu.Lock()
	r.port = port
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// ClearPort drops the serial device (the supervisor lost it).
func (r *RNodeInterface) ClearPort() {
	r.mu.Lock()
	r.port = ""
	drv := r.drv
	r.drv = nil
	r.online = false
	r.mu.Unlock()
	if drv != nil {
		drv.Close()
	}
}

// Port returns the resolved device path or address.
func (r *RNodeInterface) Port() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.port != "" {
		return r.port
	}
	return r.cfg.Port
}

// IsOnline reports whether the radio is configured and up.
func (r *RNodeInterface) IsOnline() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.online && r.drv != nil && r.drv.Online()
}

// Stats returns the radio's reported values (zero when offline).
func (r *RNodeInterface) Stats() rnode.Stats {
	r.mu.Lock()
	drv := r.drv
	r.mu.Unlock()
	if drv == nil {
		return rnode.Stats{}
	}
	return drv.Stats()
}

// LastError returns the last connect or device error.
func (r *RNodeInterface) LastError() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}

// Bitrate returns the on-air bit rate in bits per second, or 0 when offline.
func (r *RNodeInterface) Bitrate() int {
	s := r.Stats()
	return int(s.BitrateBps)
}

// Send transmits one Reticulum packet.
func (r *RNodeInterface) Send(ctx context.Context, packet []byte) error {
	r.mu.Lock()
	drv := r.drv
	online := r.online
	r.mu.Unlock()
	if drv == nil || !online {
		return transport.ErrNotConnected
	}
	return drv.Send(packet)
}

// Start runs the connect/reconnect loop until ctx ends or Stop is called.
func (r *RNodeInterface) Start(ctx context.Context) error {
	if err := r.cfg.Params.Validate(); err != nil {
		return err
	}
	go r.run(ctx)
	return nil
}

// Stop closes the radio and ends the loop.
func (r *RNodeInterface) Stop() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	drv := r.drv
	r.drv = nil
	r.online = false
	close(r.stopCh)
	r.mu.Unlock()
	if drv != nil {
		drv.Close()
	}
}

func (r *RNodeInterface) run(ctx context.Context) {
	backoff := 5 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		default:
		}
		drv, err := r.connect(ctx)
		if err != nil {
			r.mu.Lock()
			r.lastErr = err.Error()
			r.mu.Unlock()
			if !errors.Is(err, errNoPortYet) {
				log.Warn().Err(err).Str("iface", r.cfg.Name).Str("port", r.Port()).Msg("rnode iface: connect failed")
			}
			select {
			case <-ctx.Done():
				return
			case <-r.stopCh:
				return
			case <-r.wake:
			case <-time.After(backoff):
			}
			continue
		}
		r.mu.Lock()
		r.drv = drv
		r.online = true
		r.lastErr = ""
		r.lastOpen = time.Now()
		r.mu.Unlock()
		log.Info().Str("iface", r.cfg.Name).Str("port", r.Port()).Msg("rnode iface: online")
		// Wait for a device error, then reopen.
		select {
		case <-ctx.Done():
			drv.Close()
			return
		case <-r.stopCh:
			return
		case err := <-drv.Errors():
			r.mu.Lock()
			r.online = false
			r.drv = nil
			r.lastErr = err.Error()
			r.mu.Unlock()
			log.Warn().Err(err).Str("iface", r.cfg.Name).Msg("rnode iface: device error, reopening")
			drv.Close()
		}
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-time.After(time.Second):
		}
	}
}

var errNoPortYet = errors.New("rnode: waiting for the device supervisor to assign a port")

// openPTY opens a pseudo-terminal non-blocking so the Go poller can
// interrupt reads on Close.
func openPTY(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// resolvePort turns the configured port into something to open.
func (r *RNodeInterface) resolvePort() (string, error) {
	r.mu.Lock()
	assigned := r.port
	r.mu.Unlock()
	p := r.cfg.Port
	switch {
	case p == "" || p == "auto":
		if assigned == "" {
			return "", errNoPortYet
		}
		return assigned, nil
	case strings.HasPrefix(p, "usb_serial:"):
		want := strings.TrimPrefix(p, "usb_serial:")
		for _, glob := range []string{"/dev/ttyACM*", "/dev/ttyUSB*"} {
			matches, _ := filepath.Glob(glob)
			for _, m := range matches {
				if transport.FindUSBSerial(m) == want {
					if owner := transport.PortOwner(m); owner != "" && owner != r.cfg.Name {
						return "", fmt.Errorf("rnode: %s (serial %s) belongs to %s", m, want, owner)
					}
					return m, nil
				}
			}
		}
		return "", fmt.Errorf("rnode: no serial device with USB serial %q", want)
	}
	return p, nil
}

// connect opens the link and runs the RNode configure sequence.
func (r *RNodeInterface) connect(ctx context.Context) (*rnode.Driver, error) {
	var link interface {
		Read([]byte) (int, error)
		Write([]byte) (int, error)
		Close() error
	}
	tr := r.transport()
	switch tr {
	case rnode.TCP:
		addr := strings.TrimPrefix(strings.ToLower(r.cfg.Port), "tcp://")
		addr = r.cfg.Port[len(r.cfg.Port)-len(addr):]
		if _, _, err := net.SplitHostPort(addr); err != nil {
			addr = net.JoinHostPort(addr, fmt.Sprint(RNodeTCPPort))
		}
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			return nil, err
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
			_ = tc.SetKeepAlive(true)
			_ = tc.SetKeepAlivePeriod(2 * time.Second)
		}
		link = conn
	case rnode.BLE:
		l, err := OpenNUSLink(ctx, r.cfg.BLEAdapter, r.cfg.Port)
		if err != nil {
			return nil, err
		}
		link = l
	default:
		path, err := r.resolvePort()
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(path); err != nil {
			return nil, err
		}
		if owner := transport.PortOwner(path); owner != "" && owner != r.cfg.Name && !r.NeedsSupervisor() {
			return nil, fmt.Errorf("rnode: %s belongs to %s", path, owner)
		}
		port, err := transport.OpenKISSSerial(path, 115200)
		if err != nil {
			// A pseudo-terminal (the software RNode used in tests, or a
			// socat bridge) has no modem lines; open it as a plain file.
			if strings.Contains(err.Error(), "Invalid serial port") {
				f, ferr := openPTY(path)
				if ferr != nil {
					return nil, err
				}
				link = f
			} else {
				return nil, err
			}
		} else {
			link = port
		}
		r.mu.Lock()
		r.port = path
		r.mu.Unlock()
	}
	drv := rnode.New(link, rnode.Options{
		Name:        r.cfg.Name,
		Transport:   tr,
		Params:      r.cfg.Params,
		FlowControl: r.cfg.FlowControl,
		IDCallsign:  r.cfg.IDCallsign,
		IDInterval:  r.cfg.IDInterval,
		OnPacket: func(p []byte) {
			if r.callback != nil {
				r.callback(p)
			}
		},
	})
	if err := drv.Open(ctx); err != nil {
		drv.Close()
		return nil, err
	}
	return drv, nil
}
