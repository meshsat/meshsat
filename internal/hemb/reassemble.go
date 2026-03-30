package hemb

import (
	"errors"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ReassemblyBuffer collects RLNC-coded symbols from multiple bearers and
// attempts Gaussian elimination when K independent symbols are received.
// Any K of N symbols from ANY bearer combination reconstruct the payload.
type ReassemblyBuffer struct {
	mu        sync.Mutex
	streams   map[uint8]*streamState
	deliverFn func([]byte)
	eventCh   chan<- Event
	maxAge    time.Duration
}

type streamState struct {
	streamID    uint8
	generations map[uint16]*generationState
	createdAt   time.Time
}

type symbolArrival struct {
	symbol     CodedSymbol
	bearerIdx  uint8
	receivedAt time.Time
}

type generationState struct {
	genID      uint16
	k          int
	symSize    int
	symbols    []CodedSymbol
	arrivals   []symbolArrival
	bearerSeen map[uint8]bool
	firstSeen  time.Time
	decoded    bool
}

// DefaultMaxAge is the reassembly timeout when no bearer latency info is available.
const DefaultMaxAge = 5 * time.Minute

// NewReassemblyBuffer creates a reassembly buffer.
func NewReassemblyBuffer(deliverFn func([]byte), eventCh chan<- Event) *ReassemblyBuffer {
	return &ReassemblyBuffer{
		streams:   make(map[uint8]*streamState),
		deliverFn: deliverFn,
		eventCh:   eventCh,
		maxAge:    DefaultMaxAge,
	}
}

// SetMaxAge updates the reassembly timeout. Thread-safe.
func (rb *ReassemblyBuffer) SetMaxAge(d time.Duration) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.maxAge = d
}

// MaxAgeFromBearers computes an adaptive reassembly timeout from the bearer set.
// Uses 3× the slowest bearer's latency, clamped to [10s, 10min].
// For mixed bearer sets (e.g. LoRa 1s + SBD 30s), this yields 90s instead of
// the fixed 5min default — tight enough to detect failures, loose enough for
// satellite round-trips.
func MaxAgeFromBearers(bearers []BearerProfile) time.Duration {
	if len(bearers) == 0 {
		return DefaultMaxAge
	}
	maxLatency := 0
	for _, b := range bearers {
		if b.LatencyMs > maxLatency {
			maxLatency = b.LatencyMs
		}
	}
	if maxLatency == 0 {
		return DefaultMaxAge
	}
	d := time.Duration(maxLatency) * 3 * time.Millisecond
	if d < 10*time.Second {
		d = 10 * time.Second
	}
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}

// AddSymbol processes an inbound coded symbol. Returns the reassembled payload
// when a generation is successfully decoded, nil otherwise.
func (rb *ReassemblyBuffer) AddSymbol(streamID uint8, bearerIndex uint8, sym CodedSymbol) ([]byte, error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	// Get or create stream state.
	stream, ok := rb.streams[streamID]
	if !ok {
		stream = &streamState{
			streamID:    streamID,
			generations: make(map[uint16]*generationState),
			createdAt:   time.Now(),
		}
		rb.streams[streamID] = stream
	}

	// Get or create generation state.
	gen, ok := stream.generations[sym.GenID]
	if !ok {
		gen = &generationState{
			genID:      sym.GenID,
			k:          sym.K,
			symSize:    len(sym.Data),
			bearerSeen: make(map[uint8]bool),
			firstSeen:  time.Now(),
		}
		stream.generations[sym.GenID] = gen
	}

	if gen.decoded {
		log.Debug().Uint8("stream", streamID).Uint16("gen", gen.genID).Msg("hemb: AddSymbol skip — generation already decoded")
		return nil, nil
	}

	// Add symbol with arrival metadata.
	now := time.Now()
	gen.symbols = append(gen.symbols, sym)
	gen.arrivals = append(gen.arrivals, symbolArrival{
		symbol:     sym,
		bearerIdx:  bearerIndex,
		receivedAt: now,
	})
	gen.bearerSeen[bearerIndex] = true
	Global.RecordSymbolReceived()

	// Attempt decode if we have enough symbols.
	if len(gen.symbols) >= gen.k {
		log.Debug().Uint8("stream", streamID).Uint16("gen", gen.genID).
			Int("symbols", len(gen.symbols)).Int("k", gen.k).
			Msg("hemb: attempting decode")
		return rb.tryDecode(streamID, gen)
	}

	log.Debug().Uint8("stream", streamID).Uint16("gen", gen.genID).
		Int("symbols", len(gen.symbols)).Int("k", gen.k).
		Msg("hemb: not enough symbols yet")
	return nil, nil
}

func (rb *ReassemblyBuffer) tryDecode(streamID uint8, gen *generationState) ([]byte, error) {
	start := time.Now()
	decoded, err := TryDecode(gen.symbols, gen.k)
	decodeUs := time.Since(start).Microseconds()

	if err != nil {
		if errors.Is(err, ErrNotDecodable) {
			// Rank deficient — wait for more symbols.
			return nil, nil
		}
		latencyMs := time.Since(gen.firstSeen).Milliseconds()
		Global.RecordGenerationFailed()
		emit(rb.eventCh, EventGenerationFailed, GenerationFailedPayload{
			StreamID:     streamID,
			GenerationID: gen.genID,
			K:            gen.k,
			Received:     len(gen.symbols),
			Reason:       "decode_error",
			TimeoutMs:    latencyMs,
		})
		return nil, err
	}

	gen.decoded = true
	latencyMs := time.Since(gen.firstSeen).Milliseconds()
	Global.RecordGenerationDecoded()
	Global.RecordDecodeLatency(latencyMs)

	// Build bearer contribution list with actual per-bearer symbol counts.
	contribMap := make(map[uint8]int)
	for _, arr := range gen.arrivals {
		contribMap[arr.bearerIdx]++
	}
	// Fallback to bearerSeen if arrivals not tracked.
	if len(contribMap) == 0 {
		for bidx := range gen.bearerSeen {
			contribMap[bidx] = 1
		}
	}
	var contributions []BearerContribution
	for bidx, count := range contribMap {
		contributions = append(contributions, BearerContribution{
			BearerRef:   BearerRef{BearerIndex: bidx},
			SymbolCount: count,
		})
	}

	// Reassemble payload from decoded segments.
	var payload []byte
	for _, seg := range decoded {
		payload = append(payload, seg...)
	}

	log.Debug().
		Uint8("stream", streamID).
		Uint16("gen", gen.genID).
		Int("K", gen.k).
		Int("symbols_rcvd", len(gen.symbols)).
		Int("bearers", len(contributions)).
		Int64("decode_us", decodeUs).
		Int64("latency_ms", latencyMs).
		Int("bytes", len(payload)).
		Msg("hemb: DECODED generation")

	emit(rb.eventCh, EventGenerationDecoded, GenerationDecodedPayload{
		StreamID:     streamID,
		GenerationID: gen.genID,
		K:            gen.k,
		N:            len(gen.symbols),
		Received:     len(gen.symbols),
		DecodeTimeUs: decodeUs,
		LatencyMs:    latencyMs,
		PayloadBytes: len(payload),
		Bearers:      contributions,
	})

	if rb.deliverFn != nil {
		rb.deliverFn(payload)
	}

	return payload, nil
}

// PendingCount returns the number of streams with incomplete generations.
func (rb *ReassemblyBuffer) PendingCount() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	count := 0
	for _, s := range rb.streams {
		for _, g := range s.generations {
			if !g.decoded {
				count++
			}
		}
	}
	return count
}

// StreamInfo describes an active reassembly stream.
type StreamInfo struct {
	StreamID    uint8     `json:"stream_id"`
	CreatedAt   time.Time `json:"created_at"`
	Generations int       `json:"generations"`
	Decoded     int       `json:"decoded"`
	Pending     int       `json:"pending"`
}

// GenerationInfo describes a generation within a stream.
type GenerationInfo struct {
	GenID     uint16  `json:"gen_id"`
	K         int     `json:"k"`
	Received  int     `json:"received"`
	Decoded   bool    `json:"decoded"`
	Bearers   []uint8 `json:"bearers"`
	FirstSeen string  `json:"first_seen"`
	LatencyMs int64   `json:"latency_ms,omitempty"`
}

// ActiveStreams returns info about all active reassembly streams.
func (rb *ReassemblyBuffer) ActiveStreams() []StreamInfo {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	out := make([]StreamInfo, 0, len(rb.streams))
	for _, s := range rb.streams {
		decoded, pending := 0, 0
		for _, g := range s.generations {
			if g.decoded {
				decoded++
			} else {
				pending++
			}
		}
		out = append(out, StreamInfo{
			StreamID:    s.streamID,
			CreatedAt:   s.createdAt,
			Generations: len(s.generations),
			Decoded:     decoded,
			Pending:     pending,
		})
	}
	return out
}

// StreamDetail returns per-generation info for a specific stream.
func (rb *ReassemblyBuffer) StreamDetail(streamID uint8) ([]GenerationInfo, bool) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	s, ok := rb.streams[streamID]
	if !ok {
		return nil, false
	}
	now := time.Now()
	out := make([]GenerationInfo, 0, len(s.generations))
	for _, g := range s.generations {
		bearers := make([]uint8, 0, len(g.bearerSeen))
		for b := range g.bearerSeen {
			bearers = append(bearers, b)
		}
		gi := GenerationInfo{
			GenID:     g.genID,
			K:         g.k,
			Received:  len(g.symbols),
			Decoded:   g.decoded,
			Bearers:   bearers,
			FirstSeen: g.firstSeen.Format(time.RFC3339),
		}
		if g.decoded {
			gi.LatencyMs = now.Sub(g.firstSeen).Milliseconds()
		}
		out = append(out, gi)
	}
	return out, true
}

// GenerationInspection provides detailed RLNC matrix data for debugging.
type GenerationInspection struct {
	StreamID          uint8                 `json:"stream_id"`
	GenID             uint16                `json:"gen_id"`
	K                 int                   `json:"k"`
	N                 int                   `json:"n"`
	Rank              int                   `json:"rank"`
	Decoded           bool                  `json:"decoded"`
	DecodeStatus      string                `json:"decode_status"` // "decoded", "pending", "rank_deficient"
	CoefficientMatrix [][]byte              `json:"coefficient_matrix"`
	Symbols           []SymbolDetail        `json:"symbols"`
	BearerTimeline    []BearerTimelineEntry `json:"bearer_timeline"`
	Cost              float64               `json:"cost"`
	GaussSteps        []GaussStep           `json:"gauss_steps"`
}

// SymbolDetail describes a single received symbol for inspection.
type SymbolDetail struct {
	Index         int    `json:"index"`
	BearerIndex   uint8  `json:"bearer_idx"`
	ReceivedAt    string `json:"received_at"`
	IsIndependent bool   `json:"is_independent"`
	OffsetMs      int64  `json:"offset_ms"` // ms since first symbol in generation
}

// BearerTimelineEntry records a symbol arrival on a specific bearer.
type BearerTimelineEntry struct {
	SymbolIndex int    `json:"sym_idx"`
	BearerIndex uint8  `json:"bearer_idx"`
	ReceivedAt  string `json:"received_at"`
	OffsetMs    int64  `json:"offset_ms"`
}

// InspectGeneration returns detailed matrix and symbol data for debugging.
func (rb *ReassemblyBuffer) InspectGeneration(streamID uint8, genID uint16) (*GenerationInspection, bool) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	s, ok := rb.streams[streamID]
	if !ok {
		return nil, false
	}
	g, ok := s.generations[genID]
	if !ok {
		return nil, false
	}

	n := len(g.symbols)
	k := g.k

	// Build coefficient matrix from stored symbols.
	matrix := make([][]byte, n)
	for i, sym := range g.symbols {
		row := make([]byte, k)
		copy(row, sym.Coefficients)
		matrix[i] = row
	}

	// Compute rank.
	rank := ComputeRank(matrix, k)

	// Determine which symbols are linearly independent by incremental rank check.
	independent := make([]bool, n)
	for i := 1; i <= n; i++ {
		subRank := ComputeRank(matrix[:i], k)
		prevRank := 0
		if i > 1 {
			prevRank = ComputeRank(matrix[:i-1], k)
		}
		independent[i-1] = subRank > prevRank
	}
	if n > 0 {
		independent[0] = true // first symbol is always independent if non-zero
	}

	// Build symbol details and bearer timeline from arrival metadata.
	symbols := make([]SymbolDetail, n)
	timeline := make([]BearerTimelineEntry, 0, n)
	for i, sym := range g.symbols {
		bearerIdx := uint8(0)
		receivedAt := ""
		var offsetMs int64
		if i < len(g.arrivals) {
			bearerIdx = g.arrivals[i].bearerIdx
			receivedAt = g.arrivals[i].receivedAt.Format(time.RFC3339Nano)
			offsetMs = g.arrivals[i].receivedAt.Sub(g.firstSeen).Milliseconds()
		} else {
			// Fallback for symbols added before arrival tracking.
			for b := range g.bearerSeen {
				bearerIdx = b
				break
			}
		}
		symbols[i] = SymbolDetail{
			Index:         sym.SymbolIndex,
			BearerIndex:   bearerIdx,
			IsIndependent: independent[i],
			ReceivedAt:    receivedAt,
			OffsetMs:      offsetMs,
		}
		timeline = append(timeline, BearerTimelineEntry{
			SymbolIndex: sym.SymbolIndex,
			BearerIndex: bearerIdx,
			ReceivedAt:  receivedAt,
			OffsetMs:    offsetMs,
		})
	}

	// Compute Gaussian elimination steps for animation.
	gaussSteps, _ := GaussianEliminationSteps(matrix, k)

	status := "pending"
	if g.decoded {
		status = "decoded"
	} else if rank < k && n >= k {
		status = "rank_deficient"
	}

	return &GenerationInspection{
		StreamID:          streamID,
		GenID:             genID,
		K:                 k,
		N:                 n,
		Rank:              rank,
		Decoded:           g.decoded,
		DecodeStatus:      status,
		CoefficientMatrix: matrix,
		Symbols:           symbols,
		BearerTimeline:    timeline,
		GaussSteps:        gaussSteps,
	}, true
}

// Reap removes streams older than maxAge.
func (rb *ReassemblyBuffer) Reap() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	removed := 0
	now := time.Now()
	for id, s := range rb.streams {
		if now.Sub(s.createdAt) > rb.maxAge {
			// Emit failure events for incomplete generations.
			for _, g := range s.generations {
				if !g.decoded {
					Global.RecordGenerationFailed()
					emit(rb.eventCh, EventGenerationFailed, GenerationFailedPayload{
						StreamID:     id,
						GenerationID: g.genID,
						K:            g.k,
						Received:     len(g.symbols),
						Reason:       "timeout",
						TimeoutMs:    now.Sub(g.firstSeen).Milliseconds(),
					})
				}
			}
			delete(rb.streams, id)
			removed++
		}
	}
	return removed
}
