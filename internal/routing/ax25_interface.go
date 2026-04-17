package routing

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
)

// AX25 KISS framing constants (same as gateway/aprs_kiss.go)
const (
	ax25FEND  = 0xC0
	ax25FESC  = 0xDB
	ax25TFEND = 0xDC
	ax25TFESC = 0xDD
	ax25Data  = 0x00
)

// AX25InterfaceConfig configures an AX.25/APRS Reticulum interface.
type AX25InterfaceConfig struct {
	Name     string // e.g. "ax25_0"
	KISSAddr string // Direwolf KISS TCP address (e.g. "localhost:8001")
	Callsign string // AX.25 source callsign (e.g. "MESHSAT-1")
	DestCall string // AX.25 destination callsign (default "RTICUL-0")
}

// AX25Interface is a bidirectional Reticulum interface over AX.25 via KISS TNC.
// Reticulum packets are embedded in AX.25 UI (unnumbered information) frames.
// MTU: 256 bytes (standard AX.25 info field limit).
// KISSTXFunc is a function that sends a KISS-encoded frame via a shared connection.
// When set, AX25Interface routes TX through this instead of its own TCP write,
// so all TX is counted at one KISS pipeline node. [MESHSAT-403]
type KISSTXFunc func(payload []byte) error

type AX25Interface struct {
	config   AX25InterfaceConfig
	callback func(packet []byte)

	// Either kissTX (legacy fixed pointer) or kissTXProvider (resolved
	// per-send) supplies the shared KISS connection. The provider form
	// survives APRSGateway recreation by ConfigureInstance — a fixed
	// pointer captured by SetKISSTX would go stale and Send would
	// silently fall back to the AX25Interface's own dead-after-restart
	// conn. [MESHSAT-403, fixed 2026-04-17]
	kissTX         KISSTXFunc
	kissTXProvider func() KISSTXFunc

	mu      sync.Mutex
	conn    net.Conn
	online  bool
	stopCh  chan struct{}
	stopped bool
}

// NewAX25Interface creates a new AX.25 Reticulum interface.
func NewAX25Interface(config AX25InterfaceConfig, callback func(packet []byte)) *AX25Interface {
	if config.DestCall == "" {
		config.DestCall = "RTICUL-0"
	}
	return &AX25Interface{
		config:   config,
		callback: callback,
		stopCh:   make(chan struct{}),
	}
}

// SetKISSTX routes TX through a fixed shared KISS-send function.
// Prefer SetKISSTXProvider when the underlying gateway can be recreated
// (e.g. via Manager.ConfigureInstance) — a fixed pointer captured here
// will outlive its target. [MESHSAT-403]
func (a *AX25Interface) SetKISSTX(fn KISSTXFunc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.kissTX = fn
}

// SetKISSTXProvider stores a function that resolves the current shared
// KISS-send function on every Send. This pattern keeps the AX25Interface
// loosely coupled to the APRSGateway's lifecycle: when the gateway is
// recreated, the provider returns the new instance's KISSSendFrame and
// no rewiring is needed. The provider may return nil (no gateway
// running) in which case Send falls back to the interface's own conn.
func (a *AX25Interface) SetKISSTXProvider(provider func() KISSTXFunc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.kissTXProvider = provider
}

// Start connects to the Direwolf KISS TNC and begins reading frames.
func (a *AX25Interface) Start(ctx context.Context) error {
	if err := a.connect(); err != nil {
		return err
	}

	go a.readLoop(ctx)

	log.Info().Str("iface", a.config.Name).Str("kiss", a.config.KISSAddr).
		Str("callsign", a.config.Callsign).Msg("ax25 reticulum interface started")
	return nil
}

// Send transmits a Reticulum packet as an AX.25 UI frame via KISS.
func (a *AX25Interface) Send(ctx context.Context, packet []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.online || a.conn == nil {
		return fmt.Errorf("ax25 interface %s is offline", a.config.Name)
	}
	if len(packet) > 256 {
		return fmt.Errorf("packet %d bytes exceeds AX.25 MTU 256", len(packet))
	}

	// Build AX.25 UI frame: dest(7) + src(7) + control(1) + PID(1) + info
	ax25Frame := buildAX25UIFrame(a.config.DestCall, a.config.Callsign, packet)

	// Resolve shared TX path: prefer the provider (always-current), fall
	// back to a fixed pointer (legacy), final fallback is our own TCP
	// conn. The provider can return nil if the APRSGateway is between
	// recreations — that's a transient and we just write directly. [MESHSAT-403]
	var sharedTX KISSTXFunc
	if a.kissTXProvider != nil {
		sharedTX = a.kissTXProvider()
	}
	if sharedTX == nil {
		sharedTX = a.kissTX
	}

	var err error
	if sharedTX != nil {
		err = sharedTX(ax25Frame)
	} else {
		kissFrame := kissEncode(ax25Frame)
		if wdErr := a.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); wdErr != nil {
			return wdErr
		}
		_, err = a.conn.Write(kissFrame)
	}
	if err != nil {
		return fmt.Errorf("ax25 send: %w", err)
	}

	log.Debug().Str("iface", a.config.Name).Int("size", len(packet)).
		Msg("ax25 iface: packet sent")
	return nil
}

// Stop disconnects from the TNC.
func (a *AX25Interface) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stopped {
		return
	}
	a.stopped = true
	a.online = false
	if a.conn != nil {
		a.conn.Close()
	}
	close(a.stopCh)
	log.Info().Str("iface", a.config.Name).Msg("ax25 reticulum interface stopped")
}

// IsOnline returns whether the KISS TNC is connected.
func (a *AX25Interface) IsOnline() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.online
}

func (a *AX25Interface) connect() error {
	conn, err := net.DialTimeout("tcp", a.config.KISSAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("ax25: dial %s: %w", a.config.KISSAddr, err)
	}
	a.mu.Lock()
	a.conn = conn
	a.online = true
	a.mu.Unlock()
	return nil
}

// readLoop reads KISS frames from the TNC and extracts Reticulum packets.
func (a *AX25Interface) readLoop(ctx context.Context) {
	buf := make([]byte, 1024)
	var accumulated []byte

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		default:
		}

		a.mu.Lock()
		conn := a.conn
		a.mu.Unlock()
		if conn == nil {
			return
		}

		conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, err := conn.Read(buf)
		if n > 0 {
			accumulated = append(accumulated, buf[:n]...)
			// Extract KISS frames
			for {
				frame, rest := kissExtractFrame(accumulated)
				if frame == nil {
					break
				}
				accumulated = rest
				a.handleFrame(frame)
			}
		}
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Warn().Err(err).Str("iface", a.config.Name).Msg("ax25 iface: read error")
			a.mu.Lock()
			a.online = false
			a.mu.Unlock()
			return
		}
	}
}

// handleFrame processes a decoded KISS data frame (AX.25 payload).
func (a *AX25Interface) handleFrame(ax25Payload []byte) {
	// AX.25 UI frame: dest(7) + src(7) + control(1) + PID(1) + info
	// Minimum: 16 bytes header + at least 2 bytes info for Reticulum
	if len(ax25Payload) < 18 {
		return
	}

	// Skip to info field: 14 bytes address + 1 control + 1 PID = 16
	info := ax25Payload[16:]
	if len(info) < 2 {
		return
	}

	log.Debug().Str("iface", a.config.Name).Int("size", len(info)).
		Msg("ax25 iface: received Reticulum packet")
	a.callback(info)
}

// buildAX25UIFrame creates an AX.25 UI frame with Reticulum data as the info field.
func buildAX25UIFrame(destCall, srcCall string, info []byte) []byte {
	frame := make([]byte, 0, 16+len(info))

	// Destination address (7 bytes: 6 callsign + 1 SSID)
	frame = append(frame, encodeAX25Addr(destCall, true)...)
	// Source address (7 bytes: 6 callsign + 1 SSID, last address bit set)
	frame = append(frame, encodeAX25Addr(srcCall, false)...)
	// Set last address bit on source SSID byte
	frame[13] |= 0x01

	// Control: UI frame (0x03)
	frame = append(frame, 0x03)
	// PID: No Layer 3 (0xF0) — raw data
	frame = append(frame, 0xF0)
	// Info field: Reticulum packet
	frame = append(frame, info...)

	return frame
}

// encodeAX25Addr encodes a callsign-SSID into 7-byte AX.25 address format.
func encodeAX25Addr(callSSID string, isDest bool) []byte {
	addr := make([]byte, 7)
	call := callSSID
	ssid := byte(0)

	// Parse SSID if present (e.g. "MESHSAT-1")
	for i, c := range callSSID {
		if c == '-' {
			call = callSSID[:i]
			if i+1 < len(callSSID) {
				s := 0
				for _, d := range callSSID[i+1:] {
					s = s*10 + int(d-'0')
				}
				ssid = byte(s & 0x0F)
			}
			break
		}
	}

	// Pad callsign to 6 chars, shift left by 1 (AX.25 encoding)
	for i := 0; i < 6; i++ {
		if i < len(call) {
			addr[i] = call[i] << 1
		} else {
			addr[i] = ' ' << 1
		}
	}

	// SSID byte: 0b0SSID00R (R=reserved, set high bits)
	addr[6] = 0x60 | (ssid << 1)
	if isDest {
		addr[6] |= 0x80 // C bit for destination
	}

	return addr
}

// KISS encoding/decoding (local copies to avoid import cycle with gateway package)

func kissEncode(payload []byte) []byte {
	frame := []byte{ax25FEND, ax25Data}
	for _, b := range payload {
		switch b {
		case ax25FEND:
			frame = append(frame, ax25FESC, ax25TFEND)
		case ax25FESC:
			frame = append(frame, ax25FESC, ax25TFESC)
		default:
			frame = append(frame, b)
		}
	}
	frame = append(frame, ax25FEND)
	return frame
}

func kissExtractFrame(data []byte) (frame []byte, rest []byte) {
	// Find start FEND
	start := -1
	for i, b := range data {
		if b == ax25FEND {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, data
	}

	// Find end FEND
	for i := start + 1; i < len(data); i++ {
		if data[i] == ax25FEND {
			raw := data[start+1 : i]
			rest = data[i+1:]
			if len(raw) < 2 {
				return nil, rest // empty or just command byte
			}
			// Skip command byte
			return kissUnescape(raw[1:]), rest
		}
	}
	return nil, data // incomplete frame
}

func kissUnescape(data []byte) []byte {
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] == ax25FESC && i+1 < len(data) {
			switch data[i+1] {
			case ax25TFEND:
				out = append(out, ax25FEND)
			case ax25TFESC:
				out = append(out, ax25FESC)
			}
			i++
		} else {
			out = append(out, data[i])
		}
	}
	return out
}

// Ensure AX25Interface satisfies the needed usage pattern.
var _ interface {
	Send(ctx context.Context, packet []byte) error
	IsOnline() bool
} = (*AX25Interface)(nil)

// RegisterAX25Interface creates the ReticulumInterface wrapper.
func RegisterAX25Interface(config AX25InterfaceConfig, callback func(packet []byte)) (*AX25Interface, *ReticulumInterface) {
	ax25Iface := NewAX25Interface(config, callback)
	ri := NewReticulumInterface(
		config.Name,
		reticulum.IfaceAPRS,
		256, // AX.25 standard MTU
		ax25Iface.Send,
	)
	return ax25Iface, ri
}
