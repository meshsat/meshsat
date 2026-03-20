package routing

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
)

// Keepalive constants — re-exported from reticulum.
const (
	KeepaliveBitsPerSec = reticulum.KeepaliveBitsPerSec
	KeepaliveInterval   = reticulum.KeepaliveInterval
	KeepaliveTimeout    = reticulum.KeepaliveTimeout
	KeepalivePacketLen  = reticulum.KeepalivePacketLen
)

// Type alias for wire format type — delegate to reticulum package.
type KeepalivePacket = reticulum.KeepalivePacket

// Wire format functions — delegate to reticulum package.
var UnmarshalKeepalive = reticulum.UnmarshalKeepalive

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
		return reticulum.ErrWrongType // unknown link
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
	return reticulum.MaxLinksForBandwidth(bandwidthBps, capacityPct)
}
