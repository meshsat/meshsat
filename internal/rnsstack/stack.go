// Package rnsstack assembles the bridge's upstream-compatible Reticulum node
// (internal/rns) on top of the interface registry, the processor and the
// database, so that cmd/meshsat/main.go and the interop harness build the
// same thing. [MESHSAT-1348]
package rnsstack

import (
	"context"
	"encoding/hex"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/reticulum"
	"meshsat/internal/rns"
	"meshsat/internal/routing"
)

// Config is what Build needs.
type Config struct {
	Identity *routing.Identity
	Registry *routing.InterfaceRegistry
	DB       *database.DB
	// AnnounceInterval for the bridge destination; 0 disables the announcer.
	AnnounceInterval time.Duration
	AcceptLinks      bool
	IFACNetname      string
	IFACNetkey       string
	PathTTL          time.Duration
	// OnAnnounce receives every accepted remote announce (legacy tables).
	OnAnnounce func(ann *reticulum.Announce, raw []byte, iface string)
	// OnPacket receives decrypted single-packet data for meshsat.bridge.
	OnPacket func(plain []byte, pkt *rns.Packet)
}

// Stack is the built node plus the bridge destination.
type Stack struct {
	Node   *rns.Node
	Bridge *rns.Destination
	cfg    Config
}

// registryTx adapts the interface registry to rns.Transmitter.
type registryTx struct{ reg *routing.InterfaceRegistry }

func (t *registryTx) Transmit(ifaceID string, raw []byte) error { return t.reg.Send(ifaceID, raw) }

func (t *registryTx) Floodable() []string {
	ifs := t.reg.Floodable()
	out := make([]string, 0, len(ifs))
	for _, i := range ifs {
		out = append(out, i.ID())
	}
	return out
}

func (t *registryTx) HWMTU(ifaceID string) int { return t.reg.GetMTU(ifaceID) }

// Bitrate maps an interface id to the bit rate the announce cap works with.
// TCP, MQTT and the like are unlimited (0); the radios use the same table
// as the time-sync limiter.
func (t *registryTx) Bitrate(ifaceID string) int {
	typ := ifaceID
	if i := strings.LastIndex(ifaceID, "_"); i > 0 {
		typ = ifaceID[:i]
	}
	switch typ {
	case "iridium", "iridium_imt", "cellular", "sms":
		return 0 // never flooded anyway
	}
	if bw, ok := routing.DefaultBandwidths[typ]; ok {
		return bw
	}
	return 0
}

// Build creates and starts the node. The bridge destination announces
// "meshsat.bridge" with the same identity and dest hash as before, so the
// Hub and the peer kit keep their addresses.
func Build(ctx context.Context, cfg Config) (*Stack, error) {
	node, err := rns.New(rns.Config{
		Identity:           cfg.Identity.ReticulumIdentity(),
		ForwardHeader1From: map[string]bool{"mesh_0": true},
		PathTTL:            cfg.PathTTL,
	}, &registryTx{reg: cfg.Registry})
	if err != nil {
		return nil, err
	}
	st := &Stack{Node: node, cfg: cfg}
	st.Bridge = node.AddDestination(&rns.Destination{
		Name:        cfg.Identity.AppName(),
		AcceptLinks: cfg.AcceptLinks,
		ProveAll:    true,
		OnPacket: func(plain []byte, pkt *rns.Packet) {
			log.Info().Int("bytes", len(plain)).Str("iface", pkt.Iface).Msg("rns: packet for meshsat.bridge")
			if cfg.OnPacket != nil {
				cfg.OnPacket(plain, pkt)
			}
		},
		OnLink: func(l *rns.Link) {
			log.Info().Str("link", hex.EncodeToString(l.ID[:])).Str("iface", l.Iface).Msg("rns: peer link to meshsat.bridge active")
		},
		OnLinkPacket: func(l *rns.Link, plain []byte, pkt *rns.Packet) {
			if cfg.OnPacket != nil {
				cfg.OnPacket(plain, pkt)
			}
		},
	})
	if cfg.IFACNetname != "" || cfg.IFACNetkey != "" {
		f, err := reticulum.NewIFAC(cfg.IFACNetname, cfg.IFACNetkey, reticulum.DefaultIFACSize)
		if err != nil {
			return nil, err
		}
		node.SetIFAC("tcp_0", f)
		log.Info().Msg("rns: interface access codes enabled on tcp_0")
	}
	node.OnAnnounce = func(ann *reticulum.Announce, pkt *rns.Packet) {
		if cfg.OnAnnounce != nil {
			cfg.OnAnnounce(ann, pkt.Raw, pkt.Iface)
		}
	}
	if cfg.DB != nil {
		st.loadPaths()
		node.OnPathChanged = func(dest [rns.HashLen]byte, e *rns.PathEntry) { go st.persistPath(e) }
	}
	node.Start(ctx)
	if cfg.AnnounceInterval > 0 {
		go st.announcer(ctx)
	}
	tid := node.IdentityHash()
	log.Info().Str("transport_id", hex.EncodeToString(tid[:])).
		Str("dest_hash", cfg.Identity.DestHashHex()).Bool("accept_links", cfg.AcceptLinks).
		Msg("rns: upstream-compatible Reticulum node started")
	return st, nil
}

func (st *Stack) announcer(ctx context.Context) {
	announce := func() {
		if err := st.Node.Announce(st.Bridge, "", false); err != nil {
			log.Warn().Err(err).Msg("rns: announce failed")
		}
	}
	// Give the interfaces a moment to come up, then announce and repeat.
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}
	announce()
	t := time.NewTicker(st.cfg.AnnounceInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			announce()
		}
	}
}

func (st *Stack) persistPath(e *rns.PathEntry) {
	if e.Announce == nil {
		return
	}
	var blobs []byte
	for _, b := range e.RandomBlobs {
		blobs = append(blobs, b...)
	}
	err := st.cfg.DB.UpsertRNSPath(database.RNSPath{
		DestHash:    hex.EncodeToString(e.DestHash[:]),
		NextHop:     hex.EncodeToString(e.NextHop[:]),
		Hops:        e.Hops,
		Iface:       e.Iface,
		ExpiresAt:   e.Expires,
		RandomBlobs: blobs,
		AnnounceRaw: e.Announce.MarshalPacket(),
	})
	if err != nil {
		log.Warn().Err(err).Msg("rns: persist path failed")
	}
}

func (st *Stack) loadPaths() {
	rows, err := st.cfg.DB.GetRNSPaths()
	if err != nil {
		log.Warn().Err(err).Msg("rns: load paths failed")
		return
	}
	n := 0
	for _, r := range rows {
		ann, err := reticulum.UnmarshalAnnouncePacket(r.AnnounceRaw)
		if err != nil || ann.Verify() != nil {
			continue
		}
		dest, err1 := hex.DecodeString(r.DestHash)
		nh, err2 := hex.DecodeString(r.NextHop)
		if err1 != nil || err2 != nil || len(dest) != rns.HashLen || len(nh) != rns.HashLen {
			continue
		}
		e := &rns.PathEntry{Hops: r.Hops, Expires: r.ExpiresAt, Iface: r.Iface, Announce: ann, Timestamp: time.Now()}
		copy(e.DestHash[:], dest)
		copy(e.NextHop[:], nh)
		for i := 0; i+10 <= len(r.RandomBlobs); i += 10 {
			e.RandomBlobs = append(e.RandomBlobs, append([]byte(nil), r.RandomBlobs[i:i+10]...))
		}
		st.Node.Paths().Put(e)
		_ = st.Node.Remember(e.DestHash, ann.PublicKey, ann.AppData)
		n++
	}
	if n > 0 {
		log.Info().Int("paths", n).Msg("rns: paths loaded from database")
	}
}
