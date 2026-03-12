package routing

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/rs/zerolog/log"
)

// Keepalive constants derived from the Reticulum spec.
const (
	// KeepaliveBitsPerSec is the bandwidth consumed per link for keepalive.
	// 0.45 bps means 100 links on a 1200 bps channel = 3.75% capacity.
	KeepaliveBitsPerSec = 0.45

	// KeepaliveInterval is the time between keepalive packets.
	// At 0.45 bps with a 1-byte (8-bit) keepalive: interval = 8/0.45 ≈ 17.8s
	KeepaliveInterval = 18 * time.Second

	// KeepaliveTimeout is how long without activity before a link is considered dead.
	// ~3 missed keepalives.
	KeepaliveTimeout = 60 * time.Second

	// KeepalivePacketSize is the size of a keepalive packet.
	// link_id(32) + random(1) = 33 bytes. Minimal but prevents replay.
	KeepalivePacketLen = 33

	PacketKeepalive byte = 0x14
)

// KeepalivePacket is a minimal heartbeat sent to keep a link alive.
type KeepalivePacket struct {
	LinkID [LinkIDLen]byte
	Random byte // 1 byte of randomness to prevent dedup from swallowing keepalives
}

// MarshalKeepalive serializes a keepalive packet.
func (kp *KeepalivePacket) Marshal() []byte {
	buf := make([]byte, 1+KeepalivePacketLen)
	buf[0] = PacketKeepalive
	copy(buf[1:], kp.LinkID[:])
	buf[1+LinkIDLen] = kp.Random
	return buf
}

// UnmarshalKeepalive parses a keepalive packet.
func UnmarshalKeepalive(data []byte) (*KeepalivePacket, error) {
	if len(data) < 1+KeepalivePacketLen {
		return nil, errTooShort
	}
	if data[0] != PacketKeepalive {
		return nil, errWrongType
	}
	kp := &KeepalivePacket{}
	copy(kp.LinkID[:], data[1:1+LinkIDLen])
	kp.Random = data[1+LinkIDLen]
	return kp, nil
}

var (
	errTooShort  = errNew("packet too short")
	errWrongType = errNew("wrong packet type")
)

func errNew(s string) error { return &constError{s} }

type constError struct{ s string }

func (e *constError) Error() string { return e.s }

// SendCallback is called to transmit a keepalive packet on the network.
type SendCallback func(linkID [LinkIDLen]byte, data []byte)

// LinkKeepalive manages periodic keepalive sending and timeout detection
// for all active links.
type LinkKeepalive struct {
	linkMgr  *LinkManager
	sendFn   SendCallback
	interval time.Duration
	timeout  time.Duration
}

// NewLinkKeepalive creates a keepalive manager.
func NewLinkKeepalive(linkMgr *LinkManager, sendFn SendCallback) *LinkKeepalive {
	return &LinkKeepalive{
		linkMgr:  linkMgr,
		sendFn:   sendFn,
		interval: KeepaliveInterval,
		timeout:  KeepaliveTimeout,
	}
}

// Start launches the keepalive loop. It periodically sends keepalives for all
// active links and closes links that have timed out.
func (lk *LinkKeepalive) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(lk.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				lk.tick()
			}
		}
	}()
}

func (lk *LinkKeepalive) tick() {
	links := lk.linkMgr.ActiveLinks()
	now := time.Now()

	for _, link := range links {
		// Check timeout
		if now.Sub(link.LastActivity) > lk.timeout {
			log.Info().Str("link_id", hashHex32(link.ID)).
				Dur("inactive", now.Sub(link.LastActivity)).
				Msg("link keepalive timeout, closing")
			lk.linkMgr.CloseLink(link.ID)
			continue
		}

		// Send keepalive
		kp := &KeepalivePacket{LinkID: link.ID}
		randBuf := make([]byte, 1)
		rand.Read(randBuf)
		kp.Random = randBuf[0]

		if lk.sendFn != nil {
			lk.sendFn(link.ID, kp.Marshal())
		}
	}
}

// HandleKeepalive processes an incoming keepalive and updates link activity.
func (lk *LinkKeepalive) HandleKeepalive(data []byte) error {
	kp, err := UnmarshalKeepalive(data)
	if err != nil {
		return err
	}

	link := lk.linkMgr.GetLink(kp.LinkID)
	if link == nil {
		return errNew("unknown link")
	}

	link.LastActivity = time.Now()
	return nil
}

// BandwidthPerLink returns the keepalive bandwidth consumption per link in bps.
func BandwidthPerLink() float64 {
	return KeepaliveBitsPerSec
}

// MaxLinksForBandwidth returns the maximum number of concurrent links that
// fit within the given bandwidth budget (bps) at the given capacity percentage.
func MaxLinksForBandwidth(bandwidthBps int, capacityPct float64) int {
	if bandwidthBps <= 0 || capacityPct <= 0 {
		return 0
	}
	budget := float64(bandwidthBps) * capacityPct / 100.0
	return int(budget / KeepaliveBitsPerSec)
}
