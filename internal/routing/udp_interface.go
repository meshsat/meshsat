package routing

// UDPInterface: one raw Reticulum packet per datagram, broadcast to a
// forward address, received on a bind address (RNS/Interfaces/UDPInterface.py).
// Data Slayer's Haven nodes run rnsd with exactly this: listen 0.0.0.0:4242,
// forward 10.41.255.255:4242 over the batman-adv bridge. [MESHSAT-1350]

import (
	"context"
	"fmt"
	"net"
	"sync"
	"syscall"

	"github.com/rs/zerolog/log"
)

// UDPInterfaceConfig configures a UDP interface. Device derives the bind and
// forward addresses from that network interface's IPv4 broadcast address;
// otherwise ListenAddr and ForwardAddr are used as given.
type UDPInterfaceConfig struct {
	Name        string `json:"name"`
	Device      string `json:"device,omitempty"`
	ListenAddr  string `json:"listen_addr,omitempty"`  // host:port, default 0.0.0.0:4242
	ForwardAddr string `json:"forward_addr,omitempty"` // host:port, default <broadcast>:4242
}

// UDPHWMTU is the upstream hardware MTU for UDP interfaces.
const UDPHWMTU = 1064

// UDPInterface sends and receives Reticulum packets as UDP datagrams.
type UDPInterface struct {
	cfg      UDPInterfaceConfig
	callback func(packet []byte)

	mu       sync.Mutex
	conn     *net.UDPConn
	forward  *net.UDPAddr
	online   bool
	stopped  bool
	rxb, txb uint64
}

// NewUDPInterface creates the interface.
func NewUDPInterface(cfg UDPInterfaceConfig, callback func(packet []byte)) *UDPInterface {
	return &UDPInterface{cfg: cfg, callback: callback}
}

// broadcastForDevice returns the IPv4 broadcast address of a network device.
func broadcastForDevice(name string) (net.IP, error) {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil {
			continue
		}
		ip := ipn.IP.To4()
		mask := ipn.Mask
		if len(mask) == 16 {
			mask = mask[12:]
		}
		bc := make(net.IP, 4)
		for i := 0; i < 4; i++ {
			bc[i] = ip[i] | ^mask[i]
		}
		return bc, nil
	}
	return nil, fmt.Errorf("udp: %s has no IPv4 address", name)
}

// Start binds the socket and starts receiving.
func (u *UDPInterface) Start(ctx context.Context) error {
	listen := u.cfg.ListenAddr
	forward := u.cfg.ForwardAddr
	if u.cfg.Device != "" {
		bc, err := broadcastForDevice(u.cfg.Device)
		if err != nil {
			return err
		}
		if listen == "" {
			listen = net.JoinHostPort(bc.String(), "4242")
		}
		if forward == "" {
			forward = net.JoinHostPort(bc.String(), "4242")
		}
	}
	if listen == "" {
		listen = "0.0.0.0:4242"
	}
	if forward == "" {
		return fmt.Errorf("udp: forward address or device required")
	}
	laddr, err := net.ResolveUDPAddr("udp4", listen)
	if err != nil {
		return err
	}
	faddr, err := net.ResolveUDPAddr("udp4", forward)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp4", laddr)
	if err != nil {
		return err
	}
	if raw, err := conn.SyscallConn(); err == nil {
		_ = raw.Control(func(fd uintptr) {
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		})
	}
	u.mu.Lock()
	u.conn = conn
	u.forward = faddr
	u.online = true
	u.mu.Unlock()
	go u.readLoop(ctx, conn)
	log.Info().Str("iface", u.cfg.Name).Str("listen", listen).Str("forward", forward).Msg("udp iface: online")
	return nil
}

func (u *UDPInterface) readLoop(ctx context.Context, conn *net.UDPConn) {
	buf := make([]byte, 65535)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			u.mu.Lock()
			u.online = false
			u.mu.Unlock()
			return
		}
		if n == 0 {
			continue
		}
		u.mu.Lock()
		u.rxb += uint64(n)
		u.mu.Unlock()
		if u.callback != nil {
			u.callback(append([]byte(nil), buf[:n]...))
		}
	}
}

// Send broadcasts one packet.
func (u *UDPInterface) Send(ctx context.Context, packet []byte) error {
	u.mu.Lock()
	conn, faddr, online := u.conn, u.forward, u.online
	u.mu.Unlock()
	if !online || conn == nil {
		return fmt.Errorf("udp %s: offline", u.cfg.Name)
	}
	_, err := conn.WriteToUDP(packet, faddr)
	if err == nil {
		u.mu.Lock()
		u.txb += uint64(len(packet))
		u.mu.Unlock()
	}
	return err
}

// IsOnline reports whether the socket is bound.
func (u *UDPInterface) IsOnline() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.online
}

// Stop closes the socket.
func (u *UDPInterface) Stop() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.stopped {
		return
	}
	u.stopped = true
	u.online = false
	if u.conn != nil {
		u.conn.Close()
	}
}

// Counters returns bytes received and sent.
func (u *UDPInterface) Counters() (rx, tx uint64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.rxb, u.txb
}
