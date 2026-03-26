package routing

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
)

// DefaultSegmentSize is the default segment size for resource transfers.
// Chosen to fit within LoRa MTU (~230 bytes) minus Reticulum header overhead.
const DefaultSegmentSize = 180

// ResourceTransferConfig configures the resource transfer manager.
type ResourceTransferConfig struct {
	SegmentSize     int           // Payload per segment (default: 180)
	TransferTimeout time.Duration // Max time for a complete transfer
	RetryInterval   time.Duration // Interval between request retries
	MaxRetries      int           // Max request retry count
}

// DefaultResourceTransferConfig returns sensible defaults.
func DefaultResourceTransferConfig() ResourceTransferConfig {
	return ResourceTransferConfig{
		SegmentSize:     DefaultSegmentSize,
		TransferTimeout: 5 * time.Minute,
		RetryInterval:   10 * time.Second,
		MaxRetries:      5,
	}
}

// ResourceSendFunc sends a resource protocol packet on a specific interface.
type ResourceSendFunc func(ifaceID string, packet []byte) error

// outboundTransfer tracks a resource we're sending.
type outboundTransfer struct {
	hash     [reticulum.FullHashLen]byte
	data     []byte
	segSize  int
	segCount int
	created  time.Time
}

// inboundTransfer tracks a resource we're receiving.
type inboundTransfer struct {
	hash      [reticulum.FullHashLen]byte
	totalSize uint32
	segSize   uint16
	segCount  uint16
	segments  map[uint16][]byte // index → data
	bitmap    []byte            // tracks which segments we still need
	created   time.Time
	resultCh  chan []byte // delivers the complete resource
	iface     string      // interface the advertisement came from
}

// ResourceReceiveFunc is called when a resource transfer completes successfully.
// hash is the hex-encoded SHA-256 hash, data is the complete resource, iface is the source.
type ResourceReceiveFunc func(hash string, data []byte, iface string)

// ResourceTransfer manages chunked reliable delivery of data over Reticulum links.
type ResourceTransfer struct {
	config    ResourceTransferConfig
	sendFn    ResourceSendFunc
	onReceive ResourceReceiveFunc // optional: called on successful receipt

	mu       sync.Mutex
	outbound map[[reticulum.FullHashLen]byte]*outboundTransfer
	inbound  map[[reticulum.FullHashLen]byte]*inboundTransfer
}

// NewResourceTransfer creates a new resource transfer manager.
func NewResourceTransfer(config ResourceTransferConfig, sendFn ResourceSendFunc) *ResourceTransfer {
	if config.SegmentSize <= 0 {
		config.SegmentSize = DefaultSegmentSize
	}
	return &ResourceTransfer{
		config:   config,
		sendFn:   sendFn,
		outbound: make(map[[reticulum.FullHashLen]byte]*outboundTransfer),
		inbound:  make(map[[reticulum.FullHashLen]byte]*inboundTransfer),
	}
}

// Offer advertises a resource for transfer on the specified interface.
// Returns the resource hash. The remote peer will send a ResourceRequest
// for the segments it needs.
func (rt *ResourceTransfer) Offer(ctx context.Context, data []byte, ifaceID string) ([reticulum.FullHashLen]byte, error) {
	if len(data) > reticulum.MaxResourceSize {
		return [reticulum.FullHashLen]byte{}, fmt.Errorf("resource too large: %d bytes (max %d)", len(data), reticulum.MaxResourceSize)
	}

	hash := sha256.Sum256(data)
	segCount := reticulum.ComputeSegmentCount(len(data), rt.config.SegmentSize)

	// Store outbound transfer
	ot := &outboundTransfer{
		hash:     hash,
		data:     data,
		segSize:  rt.config.SegmentSize,
		segCount: segCount,
		created:  time.Now(),
	}

	rt.mu.Lock()
	rt.outbound[hash] = ot
	rt.mu.Unlock()

	// Send advertisement
	adv := &reticulum.ResourceAdvertisement{
		ResourceHash: hash,
		TotalSize:    uint32(len(data)),
		SegmentSize:  uint16(rt.config.SegmentSize),
		SegmentCount: uint16(segCount),
	}

	hdr := reticulum.Header{
		HeaderType: reticulum.HeaderType1,
		PacketType: reticulum.PacketData,
		DestType:   reticulum.DestPlain,
		Context:    reticulum.ContextResourceAdv,
		Data:       reticulum.MarshalResourceAdv(adv),
	}

	if err := rt.sendFn(ifaceID, hdr.Marshal()); err != nil {
		return hash, fmt.Errorf("send resource adv: %w", err)
	}

	log.Info().
		Str("hash", fmt.Sprintf("%x", hash[:8])).
		Int("size", len(data)).
		Int("segments", segCount).
		Str("iface", ifaceID).
		Msg("resource: offered")

	return hash, nil
}

// HandleAdvertisement processes an incoming resource advertisement.
// Returns a channel that will receive the complete resource data when
// all segments arrive, or nil if the transfer could not be started.
func (rt *ResourceTransfer) HandleAdvertisement(data []byte, sourceIface string) <-chan []byte {
	adv, err := reticulum.UnmarshalResourceAdv(data)
	if err != nil {
		log.Debug().Err(err).Msg("resource: failed to parse advertisement")
		return nil
	}

	if adv.TotalSize > reticulum.MaxResourceSize {
		log.Warn().Uint32("size", adv.TotalSize).Msg("resource: advertisement too large, ignoring")
		return nil
	}

	resultCh := make(chan []byte, 1)

	it := &inboundTransfer{
		hash:      adv.ResourceHash,
		totalSize: adv.TotalSize,
		segSize:   adv.SegmentSize,
		segCount:  adv.SegmentCount,
		segments:  make(map[uint16][]byte),
		bitmap:    reticulum.NewBitmap(int(adv.SegmentCount)),
		created:   time.Now(),
		resultCh:  resultCh,
		iface:     sourceIface,
	}

	rt.mu.Lock()
	rt.inbound[adv.ResourceHash] = it
	rt.mu.Unlock()

	log.Info().
		Str("hash", fmt.Sprintf("%x", adv.ResourceHash[:8])).
		Uint32("size", adv.TotalSize).
		Uint16("segments", adv.SegmentCount).
		Msg("resource: advertisement received, requesting segments")

	// Send request for all segments
	rt.sendRequest(it)

	return resultCh
}

// HandleRequest processes an incoming resource request (we are the sender).
func (rt *ResourceTransfer) HandleRequest(data []byte, sourceIface string) {
	req, err := reticulum.UnmarshalResourceReq(data)
	if err != nil {
		log.Debug().Err(err).Msg("resource: failed to parse request")
		return
	}

	rt.mu.Lock()
	ot, exists := rt.outbound[req.ResourceHash]
	rt.mu.Unlock()

	if !exists {
		log.Debug().Msg("resource: request for unknown resource")
		return
	}

	log.Debug().
		Str("hash", fmt.Sprintf("%x", req.ResourceHash[:8])).
		Msg("resource: sending requested segments")

	// Send requested segments
	for i := 0; i < ot.segCount; i++ {
		if !reticulum.BitmapGet(req.Bitmap, i) {
			continue
		}

		start := i * ot.segSize
		end := start + ot.segSize
		if end > len(ot.data) {
			end = len(ot.data)
		}

		seg := &reticulum.ResourceSegment{
			ResourceHash: ot.hash,
			SegmentIndex: uint16(i),
			Data:         ot.data[start:end],
		}

		hdr := reticulum.Header{
			HeaderType: reticulum.HeaderType1,
			PacketType: reticulum.PacketData,
			DestType:   reticulum.DestPlain,
			Context:    reticulum.ContextResource,
			Data:       reticulum.MarshalResourceSegment(seg),
		}

		if err := rt.sendFn(sourceIface, hdr.Marshal()); err != nil {
			log.Debug().Err(err).Int("segment", i).Msg("resource: segment send failed")
			return
		}
	}
}

// HandleSegment processes an incoming resource segment (we are the receiver).
func (rt *ResourceTransfer) HandleSegment(data []byte, sourceIface string) {
	seg, err := reticulum.UnmarshalResourceSegment(data)
	if err != nil {
		log.Debug().Err(err).Msg("resource: failed to parse segment")
		return
	}

	rt.mu.Lock()
	it, exists := rt.inbound[seg.ResourceHash]
	if !exists {
		rt.mu.Unlock()
		return
	}

	it.segments[seg.SegmentIndex] = seg.Data
	reticulum.BitmapClear(it.bitmap, int(seg.SegmentIndex))

	allReceived := reticulum.BitmapAllClear(it.bitmap)
	rt.mu.Unlock()

	if !allReceived {
		log.Debug().
			Str("hash", fmt.Sprintf("%x", seg.ResourceHash[:8])).
			Uint16("segment", seg.SegmentIndex).
			Int("have", len(it.segments)).
			Uint16("total", it.segCount).
			Msg("resource: segment received")
		return
	}

	// All segments received — reassemble
	result := make([]byte, 0, it.totalSize)
	for i := uint16(0); i < it.segCount; i++ {
		segData, ok := it.segments[i]
		if !ok {
			log.Error().Uint16("segment", i).Msg("resource: missing segment during reassembly")
			return
		}
		result = append(result, segData...)
	}

	// Verify hash
	gotHash := sha256.Sum256(result)
	if gotHash != it.hash {
		log.Error().
			Str("expected", fmt.Sprintf("%x", it.hash[:8])).
			Str("got", fmt.Sprintf("%x", gotHash[:8])).
			Msg("resource: hash mismatch after reassembly")
		return
	}

	hashHex := fmt.Sprintf("%x", it.hash)
	log.Info().
		Str("hash", hashHex[:16]).
		Int("size", len(result)).
		Msg("resource: transfer complete")

	// Notify receive callback if set
	if rt.onReceive != nil {
		rt.onReceive(hashHex, result, sourceIface)
	}

	// Send proof
	prf := &reticulum.ResourceProof{ResourceHash: it.hash}
	hdr := reticulum.Header{
		HeaderType: reticulum.HeaderType1,
		PacketType: reticulum.PacketData,
		DestType:   reticulum.DestPlain,
		Context:    reticulum.ContextResourcePRF,
		Data:       reticulum.MarshalResourceProof(prf),
	}
	rt.sendFn(sourceIface, hdr.Marshal())

	// Deliver result
	select {
	case it.resultCh <- result:
	default:
	}

	// Clean up
	rt.mu.Lock()
	delete(rt.inbound, it.hash)
	rt.mu.Unlock()
}

// HandleProof processes an incoming resource proof (transfer confirmed).
func (rt *ResourceTransfer) HandleProof(data []byte) {
	prf, err := reticulum.UnmarshalResourceProof(data)
	if err != nil {
		return
	}

	rt.mu.Lock()
	if _, exists := rt.outbound[prf.ResourceHash]; exists {
		delete(rt.outbound, prf.ResourceHash)
		log.Info().
			Str("hash", fmt.Sprintf("%x", prf.ResourceHash[:8])).
			Msg("resource: transfer confirmed by receiver")
	}
	rt.mu.Unlock()
}

// sendRequest sends a resource request for all missing segments.
func (rt *ResourceTransfer) sendRequest(it *inboundTransfer) {
	req := &reticulum.ResourceRequest{
		ResourceHash: it.hash,
		Bitmap:       it.bitmap,
	}

	hdr := reticulum.Header{
		HeaderType: reticulum.HeaderType1,
		PacketType: reticulum.PacketData,
		DestType:   reticulum.DestPlain,
		Context:    reticulum.ContextResourceReq,
		Data:       reticulum.MarshalResourceReq(req),
	}

	if err := rt.sendFn(it.iface, hdr.Marshal()); err != nil {
		log.Debug().Err(err).Msg("resource: request send failed")
	}
}

// StartPruner launches a background goroutine to clean up stale transfers.
func (rt *ResourceTransfer) StartPruner(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rt.prune()
			}
		}
	}()
}

func (rt *ResourceTransfer) prune() {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	now := time.Now()
	timeout := rt.config.TransferTimeout

	for hash, ot := range rt.outbound {
		if now.Sub(ot.created) > timeout {
			delete(rt.outbound, hash)
		}
	}
	for hash, it := range rt.inbound {
		if now.Sub(it.created) > timeout {
			delete(rt.inbound, hash)
		}
	}
}

// SetOnReceive sets the callback invoked when a resource is fully received.
func (rt *ResourceTransfer) SetOnReceive(fn ResourceReceiveFunc) {
	rt.onReceive = fn
}

// Stats returns transfer statistics.
func (rt *ResourceTransfer) Stats() (outbound, inbound int) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return len(rt.outbound), len(rt.inbound)
}
