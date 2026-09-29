package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/ipv4"
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

	// takMulticastTTL keeps SA on the link it is sent on.
	takMulticastTTL = 1
)

// takMulticastRescan is how often the interfaces are looked at again, so a
// Wi-Fi that comes back or a waydroid0 that appears is joined without a
// restart.
var takMulticastRescan = 30 * time.Second

// errTAKMulticastDown: SA multicast is on, but no interface is joined (or the
// socket is closed). Nothing was sent.
var errTAKMulticastDown = errors.New("tak multicast: no interface joined")

// TAKMulticast sends CoT as TAK SA multicast, the way ATAK peers see each other
// on a LAN: 239.2.3.1:6969, TAK protocol version 1 mesh framing, TTL 1 so it
// never leaves the link. It is what MeshSat Android calls "ATAK Broadcast" on
// a Bridge: ATAK or iTAK on the same Wi-Fi, and ATAK in Waydroid on the same
// phone (waydroid0), see the Bridge.
//
// It joins the group on the one interface named in multicast_iface or, with
// none named, on every interface that is up, multicast-capable, not loopback
// and has an IPv4 address, and sends each event out of every joined
// interface. It only sends: SA from the LAN is not read.
type TAKMulticast struct {
	iface string       // multicast_iface; "" = every eligible interface
	dst   *net.UDPAddr // the SA group; a test puts a unicast address here, which joins nothing

	mu      sync.Mutex
	conn    *net.UDPConn
	pc      *ipv4.PacketConn
	joined  map[string]net.Interface // by name
	joinErr map[string]string        // the last join failure per interface, logged once

	sent atomic.Int64

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewTAKMulticast creates the SA multicast sender for one interface, or for
// every eligible one when ifaceName is empty.
func NewTAKMulticast(ifaceName string) *TAKMulticast {
	return &TAKMulticast{
		iface:   ifaceName,
		dst:     &net.UDPAddr{IP: net.ParseIP(takMulticastAddr).To4(), Port: takMulticastPort},
		joined:  make(map[string]net.Interface),
		joinErr: make(map[string]string),
	}
}

// Start opens the socket and joins the interfaces. An interface that cannot be
// joined is not an error: it is tried again every rescan, and until one is
// joined Joined reports false.
func (m *TAKMulticast) Start(ctx context.Context) error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		return fmt.Errorf("tak multicast: socket: %w", err)
	}
	pc := ipv4.NewPacketConn(conn)
	if err := pc.SetMulticastTTL(takMulticastTTL); err != nil {
		conn.Close()
		return fmt.Errorf("tak multicast: set TTL: %w", err)
	}
	m.mu.Lock()
	m.conn, m.pc = conn, pc
	m.mu.Unlock()

	ctx, m.cancel = context.WithCancel(ctx)
	if m.dst.IP.IsMulticast() {
		m.rescan()
		m.wg.Add(1)
		go m.rescanLoop(ctx)
	}

	log.Info().
		Str("addr", m.dst.String()).
		Str("iface", m.iface).
		Strs("joined", m.joinedNames()).
		Int("ttl", takMulticastTTL).
		Msg("tak multicast started")
	return nil
}

// Stop closes the socket, which leaves every group.
func (m *TAKMulticast) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	m.mu.Lock()
	if m.conn != nil {
		m.conn.Close()
	}
	m.conn, m.pc = nil, nil
	m.joined = make(map[string]net.Interface)
	m.mu.Unlock()
	log.Info().Msg("tak multicast stopped")
}

// Joined reports whether SA can go out: an interface is joined, or the
// destination is a unicast address (a test's).
func (m *TAKMulticast) Joined() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pc == nil {
		return false
	}
	if !m.dst.IP.IsMulticast() {
		return true
	}
	return len(m.joined) > 0
}

// Sent is the number of events that left on at least one interface.
func (m *TAKMulticast) Sent() int64 {
	return m.sent.Load()
}

// SendEvent sends ev as TAK SA multicast out of every joined interface. It
// fails only when it left on none.
func (m *TAKMulticast) SendEvent(ev CotEvent) error {
	frame, err := takMulticastFrame(ev)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pc == nil {
		return errTAKMulticastDown
	}
	if err := m.conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		log.Debug().Err(err).Msg("tak multicast: set write deadline")
	}
	if !m.dst.IP.IsMulticast() {
		if _, err := m.pc.WriteTo(frame, nil, m.dst); err != nil {
			return fmt.Errorf("tak multicast: write: %w", err)
		}
		m.sent.Add(1)
		return nil
	}
	if len(m.joined) == 0 {
		return errTAKMulticastDown
	}

	var errs []error
	out := 0
	for _, name := range sortedIfaceNames(m.joined) {
		ifi := m.joined[name]
		if err := m.pc.SetMulticastInterface(&ifi); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if _, err := m.pc.WriteTo(frame, nil, m.dst); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		out++
	}
	if out == 0 {
		return fmt.Errorf("tak multicast: write: %w", errors.Join(errs...))
	}
	if len(errs) > 0 {
		log.Debug().Err(errors.Join(errs...)).Int("sent_on", out).Msg("tak multicast: not sent on every interface")
	}
	m.sent.Add(1)
	return nil
}

// takMulticastFrame is ev as TAK protocol version 1 mesh SA: 0xBF 0x01 0xBF
// and the TakMessage protobuf.
func takMulticastFrame(ev CotEvent) ([]byte, error) {
	takMsg, err := CotEventToProto(ev)
	if err != nil {
		return nil, fmt.Errorf("tak multicast: convert to proto: %w", err)
	}
	payload, err := proto.Marshal(takMsg)
	if err != nil {
		return nil, fmt.Errorf("tak multicast: marshal: %w", err)
	}
	frame := make([]byte, 0, 3+len(payload))
	frame = append(frame, takMulticastMagic0, takMulticastVer, takMulticastMagic1)
	return append(frame, payload...), nil
}

// takMulticastIfaces picks the interfaces SA multicast joins: the named one
// when it exists and is up, otherwise every interface that is up,
// multicast-capable, not loopback and has an IPv4 address (an IPv4 multicast
// needs one to leave from).
func takMulticastIfaces(all []net.Interface, name string, hasIPv4 func(net.Interface) bool) []net.Interface {
	var out []net.Interface
	for _, ifi := range all {
		if ifi.Flags&net.FlagUp == 0 {
			continue
		}
		if name != "" {
			if ifi.Name == name {
				out = append(out, ifi)
			}
			continue
		}
		if ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if hasIPv4 != nil && !hasIPv4(ifi) {
			continue
		}
		out = append(out, ifi)
	}
	return out
}

// ifaceHasIPv4 reports whether the interface has an IPv4 address.
func ifaceHasIPv4(ifi net.Interface) bool {
	addrs, err := ifi.Addrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
			return true
		}
	}
	return false
}

// rescan joins the group on interfaces that became eligible and forgets the
// ones that went away or came back under a new index.
func (m *TAKMulticast) rescan() {
	all, err := net.Interfaces()
	if err != nil {
		log.Warn().Err(err).Msg("tak multicast: list interfaces")
		return
	}
	want := make(map[string]net.Interface)
	for _, ifi := range takMulticastIfaces(all, m.iface, ifaceHasIPv4) {
		want[ifi.Name] = ifi
	}
	group := &net.UDPAddr{IP: m.dst.IP}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pc == nil {
		return
	}
	for name, old := range m.joined {
		if cur, ok := want[name]; ok && cur.Index == old.Index {
			continue
		}
		_ = m.pc.LeaveGroup(&old, group) // the interface may be gone already
		delete(m.joined, name)
		log.Info().Str("iface", name).Msg("tak multicast: interface gone, left the SA group")
	}
	for _, name := range sortedIfaceNames(want) {
		if _, ok := m.joined[name]; ok {
			continue
		}
		ifi := want[name]
		if err := m.pc.JoinGroup(&ifi, group); err != nil {
			if m.joinErr[name] != err.Error() {
				m.joinErr[name] = err.Error()
				log.Warn().Err(err).Str("iface", name).Msg("tak multicast: cannot join the SA group, trying again later")
			}
			continue
		}
		delete(m.joinErr, name)
		m.joined[name] = ifi
		log.Info().Str("iface", name).Str("group", m.dst.String()).Msg("tak multicast: joined the SA group")
	}
	if m.iface != "" && len(m.joined) == 0 && len(want) == 0 {
		const absent = "absent or down"
		if m.joinErr[m.iface] != absent {
			m.joinErr[m.iface] = absent
			log.Warn().Str("iface", m.iface).Msg("tak multicast: the interface is absent or down, trying again later")
		}
	}
}

func (m *TAKMulticast) rescanLoop(ctx context.Context) {
	defer m.wg.Done()
	t := time.NewTicker(takMulticastRescan)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.rescan()
		}
	}
}

func (m *TAKMulticast) joinedNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return sortedIfaceNames(m.joined)
}

func sortedIfaceNames(set map[string]net.Interface) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
