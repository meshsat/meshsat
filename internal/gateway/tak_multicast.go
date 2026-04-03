package gateway

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	pb "meshsat/internal/gateway/takproto"

	"google.golang.org/protobuf/proto"
)

const (
	// Default TAK multicast address and port (ATAK mesh SA).
	takMulticastAddr = "239.2.3.1"
	takMulticastPort = 6969

	// Multicast protobuf framing: 0xBF 0x01 0xBF <protobuf>
	takMulticastMagic0 = 0xBF
	takMulticastVer    = 0x01
	takMulticastMagic1 = 0xBF
)

// TAKMulticast handles UDP multicast TAK SA (situational awareness).
type TAKMulticast struct {
	iface   string // network interface name (empty = all)
	conn    *net.UDPConn
	inCh    chan InboundMessage
	running atomic.Bool

	msgsIn  atomic.Int64
	msgsOut atomic.Int64

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewTAKMulticast creates a multicast SA listener/sender.
func NewTAKMulticast(ifaceName string) *TAKMulticast {
	return &TAKMulticast{
		iface: ifaceName,
		inCh:  make(chan InboundMessage, 32),
	}
}

// Start joins the multicast group and begins listening.
func (m *TAKMulticast) Start(ctx context.Context) error {
	ctx, m.cancel = context.WithCancel(ctx)

	addr := &net.UDPAddr{
		IP:   net.ParseIP(takMulticastAddr),
		Port: takMulticastPort,
	}

	var ifi *net.Interface
	if m.iface != "" {
		var err error
		ifi, err = net.InterfaceByName(m.iface)
		if err != nil {
			return fmt.Errorf("tak multicast: interface %q: %w", m.iface, err)
		}
	}

	conn, err := net.ListenMulticastUDP("udp4", ifi, addr)
	if err != nil {
		return fmt.Errorf("tak multicast: listen %s:%d: %w", takMulticastAddr, takMulticastPort, err)
	}
	conn.SetReadBuffer(256 * 1024) //nolint:errcheck
	m.conn = conn
	m.running.Store(true)

	m.wg.Add(1)
	go m.readLoop(ctx)

	log.Info().
		Str("addr", fmt.Sprintf("%s:%d", takMulticastAddr, takMulticastPort)).
		Str("iface", m.iface).
		Msg("tak multicast started")
	return nil
}

// Stop leaves the multicast group.
func (m *TAKMulticast) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	if m.conn != nil {
		m.conn.Close()
	}
	m.wg.Wait()
	m.running.Store(false)
	log.Info().Msg("tak multicast stopped")
}

// SendEvent sends a CotEvent as protobuf multicast.
func (m *TAKMulticast) SendEvent(ev CotEvent) error {
	if m.conn == nil || !m.running.Load() {
		return fmt.Errorf("tak multicast: not running")
	}

	takMsg, err := CotEventToProto(ev)
	if err != nil {
		return fmt.Errorf("tak multicast: convert to proto: %w", err)
	}

	payload, err := proto.Marshal(takMsg)
	if err != nil {
		return fmt.Errorf("tak multicast: marshal: %w", err)
	}

	// Multicast frame: 0xBF 0x01 0xBF <protobuf>
	frame := make([]byte, 0, 3+len(payload))
	frame = append(frame, takMulticastMagic0, takMulticastVer, takMulticastMagic1)
	frame = append(frame, payload...)

	dst := &net.UDPAddr{
		IP:   net.ParseIP(takMulticastAddr),
		Port: takMulticastPort,
	}
	if _, err := m.conn.WriteToUDP(frame, dst); err != nil {
		return fmt.Errorf("tak multicast: write: %w", err)
	}

	m.msgsOut.Add(1)
	return nil
}

// Receive returns the inbound message channel.
func (m *TAKMulticast) Receive() <-chan InboundMessage {
	return m.inCh
}

// Stats returns multicast message counts.
func (m *TAKMulticast) Stats() (in, out int64) {
	return m.msgsIn.Load(), m.msgsOut.Load()
}

func (m *TAKMulticast) readLoop(ctx context.Context) {
	defer m.wg.Done()
	buf := make([]byte, 64*1024)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		m.conn.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
		n, _, err := m.conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Debug().Err(err).Msg("tak multicast: read error")
			continue
		}

		if n < 4 {
			continue
		}

		data := buf[:n]

		// Check framing: 0xBF 0x01 0xBF <protobuf>
		if data[0] == takMulticastMagic0 && data[1] == takMulticastVer && data[2] == takMulticastMagic1 {
			// Protobuf multicast
			msg := &pb.TakMessage{}
			if err := proto.Unmarshal(data[3:], msg); err != nil {
				log.Debug().Err(err).Msg("tak multicast: unmarshal protobuf")
				continue
			}
			ev, err := ProtoToCotEvent(msg)
			if err != nil {
				continue
			}
			// Skip pings
			if ev.Type == "t-x-c-t" || ev.Type == "t-x-c-t-r" {
				continue
			}
			inMsg := CotEventToInboundMessage(ev)
			select {
			case m.inCh <- inMsg:
				m.msgsIn.Add(1)
			default:
			}
		} else if data[0] == '<' || bytes.HasPrefix(data, []byte("<?xml")) {
			// XML multicast (legacy ATAK)
			ev, err := ParseCotEvent(data)
			if err != nil {
				continue
			}
			if ev.Type == "t-x-c-t" || ev.Type == "t-x-c-t-r" {
				continue
			}
			inMsg := CotEventToInboundMessage(ev)
			select {
			case m.inCh <- inMsg:
				m.msgsIn.Add(1)
			default:
			}
		}
	}
}
