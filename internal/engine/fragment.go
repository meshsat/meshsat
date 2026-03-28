package engine

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Bundle Fragmentation for DTN
//
// Splits payloads that exceed an interface's MTU into numbered fragments that
// can be independently routed and reassembled at the destination. Each fragment
// carries a self-describing header so the receiver can reconstruct the original
// payload without out-of-band state.
//
// Bundle header (25 bytes):
//   [0x01 version][16B bundle_id UUID][2B frag_index LE][2B frag_total LE][4B total_size LE]

const (
	// BundleVersion is the wire format version for bundle fragments.
	BundleVersion byte = 0x01

	// BundleHeaderLen is the fixed size of the bundle fragment header.
	BundleHeaderLen = 25

	// MaxFragments is the maximum number of fragments per bundle (uint16 max).
	MaxFragments = 65535
)

// BundleHeader describes a single fragment of a larger bundle.
type BundleHeader struct {
	Version   byte     // Wire format version (BundleVersion)
	BundleID  [16]byte // Random UUID shared across all fragments of this bundle
	FragIndex uint16   // Zero-based index of this fragment
	FragTotal uint16   // Total number of fragments in the bundle
	TotalSize uint32   // Total size of the original (unfragmented) payload
}

// MarshalBundleHeader serializes a BundleHeader to its 25-byte wire format.
func MarshalBundleHeader(h *BundleHeader) []byte {
	buf := make([]byte, BundleHeaderLen)
	buf[0] = h.Version
	copy(buf[1:17], h.BundleID[:])
	binary.LittleEndian.PutUint16(buf[17:19], h.FragIndex)
	binary.LittleEndian.PutUint16(buf[19:21], h.FragTotal)
	binary.LittleEndian.PutUint32(buf[21:25], h.TotalSize)
	return buf
}

// UnmarshalBundleHeader deserializes a BundleHeader from wire data.
func UnmarshalBundleHeader(data []byte) (*BundleHeader, error) {
	if len(data) < BundleHeaderLen {
		return nil, fmt.Errorf("bundle header too short: %d bytes (minimum %d)", len(data), BundleHeaderLen)
	}
	if data[0] != BundleVersion {
		return nil, fmt.Errorf("unsupported bundle version: 0x%02x (expected 0x%02x)", data[0], BundleVersion)
	}

	h := &BundleHeader{
		Version: data[0],
	}
	copy(h.BundleID[:], data[1:17])
	h.FragIndex = binary.LittleEndian.Uint16(data[17:19])
	h.FragTotal = binary.LittleEndian.Uint16(data[19:21])
	h.TotalSize = binary.LittleEndian.Uint32(data[21:25])

	if h.FragTotal == 0 {
		return nil, fmt.Errorf("bundle header has zero frag_total")
	}
	if h.FragIndex >= h.FragTotal {
		return nil, fmt.Errorf("bundle frag_index %d >= frag_total %d", h.FragIndex, h.FragTotal)
	}
	return h, nil
}

// IsBundleFragment checks if a byte slice starts with a valid bundle header prefix.
func IsBundleFragment(data []byte) bool {
	return len(data) >= BundleHeaderLen && data[0] == BundleVersion
}

// Fragment splits a payload into MTU-sized fragments, each prefixed with a
// BundleHeader. The mtu parameter is the maximum total size of each fragment
// including the header. Returns the bundle ID and the list of fragment byte slices.
func Fragment(payload []byte, mtu int) (bundleID [16]byte, fragments [][]byte, err error) {
	if mtu <= BundleHeaderLen {
		return bundleID, nil, fmt.Errorf("MTU %d too small (must be > %d header bytes)", mtu, BundleHeaderLen)
	}

	// If payload fits in a single fragment, still wrap it
	maxPayloadPerFrag := mtu - BundleHeaderLen
	totalSize := uint32(len(payload))

	fragCount := (len(payload) + maxPayloadPerFrag - 1) / maxPayloadPerFrag
	if fragCount == 0 {
		fragCount = 1 // empty payload gets one fragment
	}
	if fragCount > MaxFragments {
		return bundleID, nil, fmt.Errorf("payload requires %d fragments (max %d)", fragCount, MaxFragments)
	}

	// Generate bundle ID
	if _, err := rand.Read(bundleID[:]); err != nil {
		return bundleID, nil, fmt.Errorf("generate bundle ID: %w", err)
	}
	// Set version 4 (random) UUID bits
	bundleID[6] = (bundleID[6] & 0x0f) | 0x40
	bundleID[8] = (bundleID[8] & 0x3f) | 0x80

	fragments = make([][]byte, fragCount)
	for i := 0; i < fragCount; i++ {
		start := i * maxPayloadPerFrag
		end := start + maxPayloadPerFrag
		if end > len(payload) {
			end = len(payload)
		}

		fragPayload := payload[start:end]
		hdr := &BundleHeader{
			Version:   BundleVersion,
			BundleID:  bundleID,
			FragIndex: uint16(i),
			FragTotal: uint16(fragCount),
			TotalSize: totalSize,
		}

		hdrBytes := MarshalBundleHeader(hdr)
		frag := make([]byte, len(hdrBytes)+len(fragPayload))
		copy(frag, hdrBytes)
		copy(frag[BundleHeaderLen:], fragPayload)
		fragments[i] = frag
	}

	return bundleID, fragments, nil
}

// reassemblyState tracks fragments received for a single bundle.
type reassemblyState struct {
	totalSize uint32
	fragTotal uint16
	fragments map[uint16][]byte // fragIndex -> payload (without header)
	createdAt time.Time
}

// ReassemblyBuffer reassembles fragmented bundles. It is safe for concurrent use.
type ReassemblyBuffer struct {
	mu      sync.Mutex
	bundles map[[16]byte]*reassemblyState
	maxAge  time.Duration // maximum age before reaping incomplete bundles
	maxSize int           // maximum number of in-progress bundles (0 = unlimited)
}

// NewReassemblyBuffer creates a new reassembly buffer.
// maxAge controls how long incomplete bundles are retained before reaping.
// maxBundles limits the number of in-progress bundles (0 for unlimited).
func NewReassemblyBuffer(maxAge time.Duration, maxBundles int) *ReassemblyBuffer {
	return &ReassemblyBuffer{
		bundles: make(map[[16]byte]*reassemblyState),
		maxAge:  maxAge,
		maxSize: maxBundles,
	}
}

// Reassemble processes a single fragment. If all fragments of a bundle have been
// received, it returns the reassembled payload. Otherwise returns nil.
// Returns an error only on structural problems (wrong total_size, etc.).
func (rb *ReassemblyBuffer) Reassemble(data []byte) ([]byte, error) {
	hdr, err := UnmarshalBundleHeader(data)
	if err != nil {
		return nil, err
	}

	fragPayload := data[BundleHeaderLen:]

	rb.mu.Lock()
	defer rb.mu.Unlock()

	state, exists := rb.bundles[hdr.BundleID]
	if !exists {
		// Check capacity
		if rb.maxSize > 0 && len(rb.bundles) >= rb.maxSize {
			return nil, fmt.Errorf("reassembly buffer full (%d bundles)", rb.maxSize)
		}

		state = &reassemblyState{
			totalSize: hdr.TotalSize,
			fragTotal: hdr.FragTotal,
			fragments: make(map[uint16][]byte),
			createdAt: time.Now(),
		}
		rb.bundles[hdr.BundleID] = state
	}

	// Validate consistency
	if hdr.TotalSize != state.totalSize {
		return nil, fmt.Errorf("total_size mismatch for bundle: got %d, expected %d", hdr.TotalSize, state.totalSize)
	}
	if hdr.FragTotal != state.fragTotal {
		return nil, fmt.Errorf("frag_total mismatch for bundle: got %d, expected %d", hdr.FragTotal, state.fragTotal)
	}

	// Store fragment (duplicates silently overwrite)
	payloadCopy := make([]byte, len(fragPayload))
	copy(payloadCopy, fragPayload)
	state.fragments[hdr.FragIndex] = payloadCopy

	// Check if complete
	if uint16(len(state.fragments)) < state.fragTotal {
		return nil, nil // not yet complete
	}

	// Reassemble
	result := make([]byte, 0, state.totalSize)
	for i := uint16(0); i < state.fragTotal; i++ {
		frag, ok := state.fragments[i]
		if !ok {
			// Should not happen since we checked count, but guard against map corruption
			return nil, fmt.Errorf("missing fragment %d during reassembly", i)
		}
		result = append(result, frag...)
	}

	if uint32(len(result)) != state.totalSize {
		delete(rb.bundles, hdr.BundleID)
		return nil, fmt.Errorf("reassembled size %d != expected total_size %d", len(result), state.totalSize)
	}

	// Clean up completed bundle
	delete(rb.bundles, hdr.BundleID)
	return result, nil
}

// Reap removes incomplete bundles older than maxAge. Returns the number of
// bundles reaped.
func (rb *ReassemblyBuffer) Reap() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	now := time.Now()
	reaped := 0
	for id, state := range rb.bundles {
		if now.Sub(state.createdAt) > rb.maxAge {
			log.Debug().
				Hex("bundle_id", id[:]).
				Int("received", len(state.fragments)).
				Uint16("total", state.fragTotal).
				Msg("reaping incomplete bundle")
			delete(rb.bundles, id)
			reaped++
		}
	}
	return reaped
}

// PendingCount returns the number of bundles currently being reassembled.
func (rb *ReassemblyBuffer) PendingCount() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return len(rb.bundles)
}

// PendingBundleInfo returns summary info about a specific in-progress bundle.
// Returns received fragment count and total, or (0, 0) if bundle not found.
func (rb *ReassemblyBuffer) PendingBundleInfo(bundleID [16]byte) (received int, total int) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	state, ok := rb.bundles[bundleID]
	if !ok {
		return 0, 0
	}
	return len(state.fragments), int(state.fragTotal)
}
