package routing

// AutoInterface: zero-configuration peer discovery on a LAN over IPv6
// link-local multicast, then one UDP datagram per Reticulum packet to
// each peer (RNS/Interfaces/AutoInterface.py). This is how Sideband,
// CrossTalk and rnsd find each other on a Haven / HaLow mesh, so a kit on
// the same Ethernet segment joins with nothing configured. [MESHSAT-1350]
//
// Protocol: every ANNOUNCE_INTERVAL a node multicasts its discovery token,
// SHA-256(group_id || its link-local address text), to
// ff12:0:<six 16-bit words from SHA-256(group_id)[2..13]>:29716 on each
// adopted interface, and unicasts the same token to every known peer's
// port 29717 every 5.2 s (reverse peering). A token that verifies adds or
// refreshes a peer; peers time out after 22 s. Data goes to
// [peer%ifindex]:42671. A datagram seen on several adopted interfaces is
// delivered once (48-entry, 0.75 s dedup).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/ipv6"
)

// AutoInterface constants (AutoInterface.py).
const (
	AutoHWMTU            = 1196
	AutoDiscoveryPort    = 29716
	AutoDataPort         = 42671
	AutoDefaultGroup     = "reticulum"
	autoPeeringTimeout   = 22 * time.Second
	autoAnnounceInterval = 1600 * time.Millisecond
	autoPeerJobInterval  = 4 * time.Second
	autoReversePeering   = 5200 * time.Millisecond
	autoMcastEchoTimeout = 6500 * time.Millisecond
	autoDedupLen         = 48
	autoDedupTTL         = 750 * time.Millisecond
)

// AutoInterfaceConfig configures the interface. Devices lists the network
// interfaces to adopt (empty = every interface with a link-local IPv6
// address except lo and the ones in Ignore).
type AutoInterfaceConfig struct {
	Name    string   `json:"name"`
	GroupID string   `json:"group_id,omitempty"`
	Devices []string `json:"devices,omitempty"`
	Ignore  []string `json:"ignore,omitempty"`
}

// DescopeLinkLocal normalises a link-local address as upstream does:
// strip %scope and collapse "fe80:<zeros>::" to "fe80::".
func DescopeLinkLocal(addr string) string {
	addr = strings.Split(addr, "%")[0]
	return autoScopeRe.ReplaceAllString(addr, "fe80::")
}

var autoScopeRe = regexp.MustCompile(`fe80:[0-9a-f]*::`)

// AutoDiscoveryToken is SHA-256(group_id || link-local address text).
func AutoDiscoveryToken(groupID, linkLocal string) [32]byte {
	return sha256.Sum256(append([]byte(groupID), []byte(linkLocal)...))
}

// AutoMulticastGroup derives the discovery group address for a group id.
func AutoMulticastGroup(groupID string) net.IP {
	g := sha256.Sum256([]byte(groupID))
	words := make([]string, 0, 7)
	words = append(words, "0")
	for i := 2; i < 14; i += 2 {
		words = append(words, fmt.Sprintf("%x", int(g[i+1])+int(g[i])<<8))
	}
	return net.ParseIP("ff12:" + strings.Join(words, ":"))
}

type autoPeer struct {
	addr      net.IP
	ifname    string
	ifindex   int
	lastHeard time.Time
	lastOut   time.Time
}

type autoAdopted struct {
	name      string
	index     int
	linkLocal net.IP
	text      string // descoped address text used in tokens
	mcastEcho time.Time
	disc      *net.UDPConn
	uni       *net.UDPConn
	data      *net.UDPConn // bound to the link-local address, port 42671, like upstream
}

// AutoInterface is the bridge's AutoInterface.
type AutoInterface struct {
	cfg      AutoInterfaceConfig
	callback func(packet []byte)
	group    net.IP

	mu       sync.Mutex
	adopted  map[string]*autoAdopted
	peers    map[string]*autoPeer // key: descoped address text
	online   bool
	stopped  bool
	stopCh   chan struct{}
	dedup    []autoDedupEntry
	rxb, txb uint64
}

type autoDedupEntry struct {
	hash [32]byte
	t    time.Time
}

// NewAutoInterface creates the interface.
func NewAutoInterface(cfg AutoInterfaceConfig, callback func(packet []byte)) *AutoInterface {
	if cfg.GroupID == "" {
		cfg.GroupID = AutoDefaultGroup
	}
	return &AutoInterface{cfg: cfg, callback: callback, group: AutoMulticastGroup(cfg.GroupID),
		adopted: make(map[string]*autoAdopted), peers: make(map[string]*autoPeer), stopCh: make(chan struct{})}
}

func (a *AutoInterface) wanted(name string) bool {
	if name == "lo" || name == "lo0" {
		return false
	}
	for _, ig := range a.cfg.Ignore {
		if ig == name {
			return false
		}
	}
	if len(a.cfg.Devices) == 0 {
		return true
	}
	for _, d := range a.cfg.Devices {
		if d == name {
			return true
		}
	}
	return false
}

func linkLocalOf(ifc net.Interface) net.IP {
	addrs, err := ifc.Addrs()
	if err != nil {
		return nil
	}
	for _, addr := range addrs {
		ipn, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		if ipn.IP.To4() == nil && ipn.IP.IsLinkLocalUnicast() {
			return ipn.IP
		}
	}
	return nil
}

// Start adopts interfaces, binds sockets and starts announcing.
func (a *AutoInterface) Start(ctx context.Context) error {
	ifcs, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, ifc := range ifcs {
		if !a.wanted(ifc.Name) || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		ll := linkLocalOf(ifc)
		if ll == nil {
			continue
		}
		if err := a.adopt(ifc, ll); err != nil {
			log.Warn().Err(err).Str("iface", a.cfg.Name).Str("dev", ifc.Name).Msg("auto iface: could not adopt device")
		}
	}
	if len(a.adopted) == 0 {
		return fmt.Errorf("auto: no usable network interface with a link-local IPv6 address")
	}
	a.mu.Lock()
	a.online = true
	a.mu.Unlock()
	go a.jobs(ctx)
	names := make([]string, 0, len(a.adopted))
	for n := range a.adopted {
		names = append(names, n)
	}
	log.Info().Str("iface", a.cfg.Name).Strs("devices", names).Str("group", a.group.String()).Msg("auto iface: online")
	return nil
}

func (a *AutoInterface) adopt(ifc net.Interface, ll net.IP) error {
	ad := &autoAdopted{name: ifc.Name, index: ifc.Index, linkLocal: ll, text: DescopeLinkLocal(ll.String()), mcastEcho: time.Now()}
	// Multicast discovery listener, bound to the group on this device.
	disc, err := net.ListenUDP("udp6", &net.UDPAddr{IP: a.group, Port: AutoDiscoveryPort, Zone: ifc.Name})
	if err != nil {
		return fmt.Errorf("discovery socket: %w", err)
	}
	pc := ipv6.NewPacketConn(disc)
	if err := pc.JoinGroup(&ifc, &net.UDPAddr{IP: a.group}); err != nil {
		disc.Close()
		return fmt.Errorf("join group: %w", err)
	}
	// Unicast discovery listener on our link-local address, port +1.
	uni, err := net.ListenUDP("udp6", &net.UDPAddr{IP: ll, Port: AutoDiscoveryPort + 1, Zone: ifc.Name})
	if err != nil {
		disc.Close()
		return fmt.Errorf("unicast discovery socket: %w", err)
	}
	// Data socket on the link-local address itself, so every datagram we
	// send leaves with the address our discovery token was derived from.
	data, err := net.ListenUDP("udp6", &net.UDPAddr{IP: ll, Port: AutoDataPort, Zone: ifc.Name})
	if err != nil {
		disc.Close()
		uni.Close()
		return fmt.Errorf("data socket: %w", err)
	}
	ad.disc, ad.uni, ad.data = disc, uni, data
	a.mu.Lock()
	a.adopted[ifc.Name] = ad
	a.mu.Unlock()
	go a.discoveryLoop(ad, disc)
	go a.discoveryLoop(ad, uni)
	go a.dataLoop(data)
	return nil
}

func (a *AutoInterface) discoveryLoop(ad *autoAdopted, conn *net.UDPConn) {
	buf := make([]byte, 1024)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n < 32 {
			continue
		}
		text := DescopeLinkLocal(from.IP.String())
		expected := AutoDiscoveryToken(a.cfg.GroupID, text)
		var got [32]byte
		copy(got[:], buf[:32])
		if got != expected {
			log.Debug().Str("iface", a.cfg.Name).Str("from", text).Msg("auto iface: peering packet with a wrong token")
			continue
		}
		a.addPeer(from.IP, text, ad)
	}
}

func (a *AutoInterface) addPeer(ip net.IP, text string, ad *autoAdopted) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Our own multicast echoed back: proves the link carries multicast.
	for _, own := range a.adopted {
		if own.text == text {
			own.mcastEcho = time.Now()
			return
		}
	}
	if p, ok := a.peers[text]; ok {
		p.lastHeard = time.Now()
		return
	}
	a.peers[text] = &autoPeer{addr: ip, ifname: ad.name, ifindex: ad.index, lastHeard: time.Now()}
	log.Info().Str("iface", a.cfg.Name).Str("peer", text).Str("dev", ad.name).Msg("auto iface: peer added")
}

func (a *AutoInterface) dataLoop(conn *net.UDPConn) {
	buf := make([]byte, 65535)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		text := DescopeLinkLocal(from.IP.String())
		a.mu.Lock()
		p, known := a.peers[text]
		if known {
			p.lastHeard = time.Now()
		}
		if !known {
			a.mu.Unlock()
			continue
		}
		h := sha256.Sum256(buf[:n])
		now := time.Now()
		dup := false
		kept := a.dedup[:0]
		for _, e := range a.dedup {
			if now.Sub(e.t) <= autoDedupTTL {
				kept = append(kept, e)
				if e.hash == h {
					dup = true
				}
			}
		}
		a.dedup = kept
		if !dup {
			a.dedup = append(a.dedup, autoDedupEntry{hash: h, t: now})
			if len(a.dedup) > autoDedupLen {
				a.dedup = a.dedup[len(a.dedup)-autoDedupLen:]
			}
			a.rxb += uint64(n)
		}
		a.mu.Unlock()
		if !dup && a.callback != nil {
			a.callback(append([]byte(nil), buf[:n]...))
		}
	}
}

// jobs announces, sends reverse peering packets and expires peers.
func (a *AutoInterface) jobs(ctx context.Context) {
	announce := time.NewTicker(autoAnnounceInterval)
	peer := time.NewTicker(autoPeerJobInterval)
	defer announce.Stop()
	defer peer.Stop()
	a.announceAll()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		case <-announce.C:
			a.announceAll()
		case <-peer.C:
			a.peerJobs()
		}
	}
}

func (a *AutoInterface) announceAll() {
	a.mu.Lock()
	ads := make([]*autoAdopted, 0, len(a.adopted))
	for _, ad := range a.adopted {
		ads = append(ads, ad)
	}
	a.mu.Unlock()
	for _, ad := range ads {
		tok := AutoDiscoveryToken(a.cfg.GroupID, ad.text)
		conn, err := net.DialUDP("udp6", &net.UDPAddr{IP: ad.linkLocal, Zone: ad.name}, &net.UDPAddr{IP: a.group, Port: AutoDiscoveryPort, Zone: ad.name})
		if err != nil {
			continue
		}
		_, _ = conn.Write(tok[:])
		conn.Close()
	}
}

func (a *AutoInterface) peerJobs() {
	now := time.Now()
	a.mu.Lock()
	var reverse []*autoPeer
	for k, p := range a.peers {
		if now.Sub(p.lastHeard) > autoPeeringTimeout {
			delete(a.peers, k)
			log.Info().Str("iface", a.cfg.Name).Str("peer", k).Msg("auto iface: peer timed out")
			continue
		}
		if now.Sub(p.lastOut) > autoReversePeering {
			p.lastOut = now
			reverse = append(reverse, p)
		}
	}
	ads := make(map[string]*autoAdopted, len(a.adopted))
	for k, v := range a.adopted {
		ads[k] = v
	}
	a.mu.Unlock()
	for _, p := range reverse {
		ad := ads[p.ifname]
		if ad == nil {
			continue
		}
		tok := AutoDiscoveryToken(a.cfg.GroupID, ad.text)
		conn, err := net.DialUDP("udp6", nil, &net.UDPAddr{IP: p.addr, Port: AutoDiscoveryPort + 1, Zone: ad.name})
		if err != nil {
			continue
		}
		_, _ = conn.Write(tok[:])
		conn.Close()
	}
	// Re-check link-local addresses (a re-plugged cable changes them).
	for name, ad := range ads {
		if ifc, err := net.InterfaceByName(name); err == nil {
			if ll := linkLocalOf(*ifc); ll != nil && !ll.Equal(ad.linkLocal) {
				a.mu.Lock()
				ad.linkLocal = ll
				ad.text = DescopeLinkLocal(ll.String())
				a.mu.Unlock()
				log.Info().Str("iface", a.cfg.Name).Str("dev", name).Str("addr", ad.text).Msg("auto iface: link-local address changed")
			}
		}
	}
}

// Send delivers one packet to every peer.
func (a *AutoInterface) Send(ctx context.Context, packet []byte) error {
	a.mu.Lock()
	peers := make([]*autoPeer, 0, len(a.peers))
	for _, p := range a.peers {
		peers = append(peers, p)
	}
	socks := make(map[string]*net.UDPConn, len(a.adopted))
	for name, ad := range a.adopted {
		socks[name] = ad.data
	}
	online := a.online
	a.mu.Unlock()
	if !online {
		return fmt.Errorf("auto %s: offline", a.cfg.Name)
	}
	if len(peers) == 0 {
		return fmt.Errorf("auto %s: no peers discovered yet", a.cfg.Name)
	}
	var lastErr error
	for _, p := range peers {
		conn := socks[p.ifname]
		if conn == nil {
			continue
		}
		if _, err := conn.WriteToUDP(packet, &net.UDPAddr{IP: p.addr, Port: AutoDataPort, Zone: p.ifname}); err != nil {
			lastErr = err
		} else {
			a.mu.Lock()
			a.txb += uint64(len(packet))
			a.mu.Unlock()
		}
	}
	return lastErr
}

// IsOnline reports whether at least one device is adopted.
func (a *AutoInterface) IsOnline() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.online
}

// PeerCount returns the number of discovered peers.
func (a *AutoInterface) PeerCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.peers)
}

// AutoPeerInfo describes one discovered peer.
type AutoPeerInfo struct {
	Address   string `json:"address"`
	Device    string `json:"device"`
	LastHeard string `json:"last_heard"`
}

// Peers lists discovered peers.
func (a *AutoInterface) Peers() []AutoPeerInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]AutoPeerInfo, 0, len(a.peers))
	for k, p := range a.peers {
		out = append(out, AutoPeerInfo{Address: k, Device: p.ifname, LastHeard: p.lastHeard.UTC().Format(time.RFC3339)})
	}
	return out
}

// Stop closes every socket.
func (a *AutoInterface) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		return
	}
	a.stopped = true
	a.online = false
	close(a.stopCh)
	for _, ad := range a.adopted {
		if ad.disc != nil {
			ad.disc.Close()
		}
		if ad.uni != nil {
			ad.uni.Close()
		}
		if ad.data != nil {
			ad.data.Close()
		}
	}
}

// GroupHex is the multicast group for logs and the API.
func (a *AutoInterface) GroupHex() string { return hex.EncodeToString(a.group) }
