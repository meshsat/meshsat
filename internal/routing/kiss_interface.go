package routing

// KISSInterface: raw Reticulum packets over a KISS TNC, serial or TCP
// (RNS/Interfaces/KISSInterface.py). Unlike ax25_0, nothing is wrapped in
// AX.25: the TNC gets the packet as the frame. Rhizomatica's Mercury HF
// modem (KISS over TCP, port 8100) and any hardware TNC fit here. [MESHSAT-1350]

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/kiss"
	"meshsat/internal/transport"
)

// KISS TNC command bytes (KISSInterface.py).
const (
	kissCmdTXDelay    = 0x01
	kissCmdP          = 0x02
	kissCmdSlotTime   = 0x03
	kissCmdTXTail     = 0x04
	kissCmdFullDuplex = 0x05
	kissCmdReady      = 0x0F
	kissCmdReturn     = 0xFF

	// KISSHWMTU is the upstream hardware MTU.
	KISSHWMTU = 564
)

// KISSInterfaceConfig configures a KISS TNC interface.
type KISSInterfaceConfig struct {
	Name        string `json:"name"`
	Port        string `json:"port"` // /dev/... or tcp://host:port
	Baud        int    `json:"baud,omitempty"`
	PreambleMs  int    `json:"preamble_ms,omitempty"` // TXDELAY, default 350
	TXTailMs    int    `json:"txtail_ms,omitempty"`   // default 20
	Persistence int    `json:"persistence,omitempty"` // default 64
	SlotTimeMs  int    `json:"slottime_ms,omitempty"` // default 20
	FlowControl bool   `json:"flow_control"`
	BeaconData  string `json:"beacon_data,omitempty"`
	BeaconSec   int    `json:"beacon_interval,omitempty"`
}

// KISSInterface drives one TNC.
type KISSInterface struct {
	cfg      KISSInterfaceConfig
	callback func(packet []byte)

	mu       sync.Mutex
	link     io.ReadWriteCloser
	online   bool
	stopped  bool
	stopCh   chan struct{}
	ready    bool
	lockedAt time.Time
	queue    [][]byte
	firstTX  time.Time
	rxb, txb uint64
	lastErr  string
}

// NewKISSInterface creates the interface.
func NewKISSInterface(cfg KISSInterfaceConfig, callback func(packet []byte)) *KISSInterface {
	if cfg.Baud == 0 {
		cfg.Baud = 9600
	}
	if cfg.PreambleMs == 0 {
		cfg.PreambleMs = 350
	}
	if cfg.TXTailMs == 0 {
		cfg.TXTailMs = 20
	}
	if cfg.Persistence == 0 {
		cfg.Persistence = 64
	}
	if cfg.SlotTimeMs == 0 {
		cfg.SlotTimeMs = 20
	}
	return &KISSInterface{cfg: cfg, callback: callback, stopCh: make(chan struct{})}
}

func clamp255(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// Start connects and keeps reconnecting.
func (k *KISSInterface) Start(ctx context.Context) error {
	if k.cfg.Port == "" {
		return fmt.Errorf("kiss: port required")
	}
	go k.run(ctx)
	return nil
}

func (k *KISSInterface) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-k.stopCh:
			return
		default:
		}
		if err := k.connect(); err != nil {
			k.mu.Lock()
			k.lastErr = err.Error()
			k.mu.Unlock()
			log.Warn().Err(err).Str("iface", k.cfg.Name).Str("port", k.cfg.Port).Msg("kiss iface: connect failed")
			select {
			case <-ctx.Done():
				return
			case <-k.stopCh:
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		k.readLoop(ctx)
		select {
		case <-ctx.Done():
			return
		case <-k.stopCh:
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (k *KISSInterface) connect() error {
	var link io.ReadWriteCloser
	if strings.HasPrefix(strings.ToLower(k.cfg.Port), "tcp://") {
		addr := k.cfg.Port[len("tcp://"):]
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			return err
		}
		link = conn
	} else {
		port, err := transport.OpenKISSSerial(k.cfg.Port, k.cfg.Baud)
		if err != nil {
			// go.bug.st/serial refuses a pseudo-terminal; tests and software
			// TNCs live on ptys.
			f, perr := openPTY(k.cfg.Port)
			if perr != nil {
				return err
			}
			link = f
		} else {
			link = port
		}
		time.Sleep(2 * time.Second) // as upstream, let the TNC settle
	}
	// TNC parameters: ms/10, then the READY handshake.
	for _, c := range [][]byte{
		kiss.Encode(kissCmdTXDelay, []byte{clamp255(k.cfg.PreambleMs / 10)}),
		kiss.Encode(kissCmdTXTail, []byte{clamp255(k.cfg.TXTailMs / 10)}),
		kiss.Encode(kissCmdP, []byte{clamp255(k.cfg.Persistence)}),
		kiss.Encode(kissCmdSlotTime, []byte{clamp255(k.cfg.SlotTimeMs / 10)}),
		kiss.Encode(kissCmdReady, []byte{0x01}),
	} {
		if _, err := link.Write(c); err != nil {
			link.Close()
			return err
		}
	}
	k.mu.Lock()
	k.link = link
	k.online = true
	k.ready = true
	k.lastErr = ""
	k.mu.Unlock()
	log.Info().Str("iface", k.cfg.Name).Str("port", k.cfg.Port).Msg("kiss iface: online")
	return nil
}

func (k *KISSInterface) readLoop(ctx context.Context) {
	k.mu.Lock()
	link := k.link
	k.mu.Unlock()
	sp := &kiss.Splitter{MaxLen: KISSHWMTU + 1, Timeout: 100 * time.Millisecond}
	buf := make([]byte, 4096)
	for {
		n, err := link.Read(buf)
		now := time.Now()
		if n > 0 {
			for _, f := range sp.Feed(buf[:n], now) {
				switch f.Cmd & 0x0F {
				case kiss.CmdData:
					if len(f.Payload) > 0 {
						k.mu.Lock()
						k.rxb += uint64(len(f.Payload))
						k.mu.Unlock()
						if k.callback != nil {
							k.callback(append([]byte(nil), f.Payload...))
						}
					}
				case kissCmdReady:
					k.processQueue()
				}
			}
		}
		if err != nil {
			k.mu.Lock()
			k.online = false
			k.link = nil
			k.mu.Unlock()
			link.Close()
			log.Warn().Err(err).Str("iface", k.cfg.Name).Msg("kiss iface: link lost")
			return
		}
		// Flow-control unlock after 5 s without READY, and the beacon.
		k.mu.Lock()
		if k.cfg.FlowControl && !k.ready && now.After(k.lockedAt.Add(5*time.Second)) {
			k.mu.Unlock()
			log.Warn().Str("iface", k.cfg.Name).Msg("kiss iface: unlocking flow control after a missed READY")
			k.processQueue()
		} else {
			k.mu.Unlock()
		}
		if k.cfg.BeaconSec > 0 && k.cfg.BeaconData != "" {
			k.mu.Lock()
			due := !k.firstTX.IsZero() && now.After(k.firstTX.Add(time.Duration(k.cfg.BeaconSec)*time.Second))
			if due {
				k.firstTX = time.Time{}
			}
			k.mu.Unlock()
			if due {
				b := []byte(k.cfg.BeaconData)
				for len(b) < 15 {
					b = append(b, 0)
				}
				_ = k.Send(ctx, b)
			}
		}
		if n == 0 {
			time.Sleep(20 * time.Millisecond)
		}
		select {
		case <-ctx.Done():
			link.Close()
			return
		case <-k.stopCh:
			link.Close()
			return
		default:
		}
	}
}

func (k *KISSInterface) processQueue() {
	k.mu.Lock()
	if len(k.queue) == 0 {
		k.ready = true
		k.mu.Unlock()
		return
	}
	next := k.queue[0]
	k.queue = k.queue[1:]
	k.ready = true
	k.mu.Unlock()
	_ = k.Send(context.Background(), next)
}

// Send transmits one packet as a KISS data frame.
func (k *KISSInterface) Send(ctx context.Context, packet []byte) error {
	if len(packet) > KISSHWMTU {
		return fmt.Errorf("kiss %s: packet %d bytes exceeds %d", k.cfg.Name, len(packet), KISSHWMTU)
	}
	k.mu.Lock()
	link := k.link
	if !k.online || link == nil {
		k.mu.Unlock()
		return transport.ErrNotConnected
	}
	if k.cfg.FlowControl && !k.ready {
		k.queue = append(k.queue, append([]byte(nil), packet...))
		k.mu.Unlock()
		return nil
	}
	if k.cfg.FlowControl {
		k.ready = false
		k.lockedAt = time.Now()
	}
	if k.firstTX.IsZero() {
		k.firstTX = time.Now()
	}
	k.txb += uint64(len(packet))
	k.mu.Unlock()
	_, err := link.Write(kiss.Encode(kiss.CmdData, packet))
	return err
}

// IsOnline reports whether the TNC link is up.
func (k *KISSInterface) IsOnline() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.online
}

// LastError returns the last connect error.
func (k *KISSInterface) LastError() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.lastErr
}

// Counters returns bytes received and sent.
func (k *KISSInterface) Counters() (rx, tx uint64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.rxb, k.txb
}

// Stop closes the link and ends the loop.
func (k *KISSInterface) Stop() {
	k.mu.Lock()
	if k.stopped {
		k.mu.Unlock()
		return
	}
	k.stopped = true
	k.online = false
	close(k.stopCh)
	link := k.link
	k.link = nil
	k.mu.Unlock()
	if link != nil {
		link.Close()
	}
}
