package hemb

import (
	"context"
	"crypto/rand"
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// bonder implements the HeMB bonding layer.
type bonder struct {
	opts       Options
	reassembly *ReassemblyBuffer
	stats      bondStatsCounters
	streamSeq  atomic.Uint32
	mu         sync.Mutex
}

type bondStatsCounters struct {
	symbolsSent        atomic.Int64
	symbolsReceived    atomic.Int64
	generationsDecoded atomic.Int64
	generationsFailed  atomic.Int64
	bytesFree          atomic.Int64
	bytesPaid          atomic.Int64
	costIncurred       atomic.Int64 // stored as cost * 1e6 (micro-dollars)
}

// NewBonder creates a HeMB bonder with the given options.
func NewBonder(opts Options) Bonder {
	b := &bonder{opts: opts}
	if len(opts.Bearers) > 1 {
		b.reassembly = NewReassemblyBuffer(opts.DeliverFn, opts.EventCh)
	}
	return b
}

// Send splits payload, encodes with RLNC, and distributes across all bearers.
// N=1: direct passthrough with zero overhead (no header, no RLNC coding).
// N>1: RLNC-coded symbols distributed cost-weighted across bearers.
func (b *bonder) Send(ctx context.Context, payload []byte) error {
	bearers := b.opts.Bearers
	if len(bearers) == 0 {
		return errors.New("hemb: no bearers configured")
	}

	if len(bearers) == 1 {
		return b.sendN1(ctx, payload, &bearers[0])
	}

	return b.sendMulti(ctx, payload)
}

// sendN1 is the zero-overhead passthrough for single-bearer mode.
func (b *bonder) sendN1(ctx context.Context, payload []byte, bearer *BearerProfile) error {
	emit(b.opts.EventCh, EventSymbolSent, SymbolSentPayload{
		BearerRef:    BearerRef{BearerIndex: bearer.Index, BearerType: bearer.ChannelType},
		PayloadBytes: len(payload),
		CostEstimate: bearer.CostPerMsg,
	})
	if bearer.IsFree() {
		b.stats.bytesFree.Add(int64(len(payload)))
	} else {
		b.stats.bytesPaid.Add(int64(len(payload)))
		b.stats.costIncurred.Add(int64(bearer.CostPerMsg * 1e6))
	}
	b.stats.symbolsSent.Add(1)
	Global.RecordSymbolSent(len(payload), bearer.IsFree(), bearer.CostPerMsg)
	Global.RecordPayloadSent()
	return bearer.SendFn(ctx, payload)
}

// bearerAlloc tracks how many symbols are allocated to a bearer.
type bearerAlloc struct {
	bearer *BearerProfile
	source int // source symbols assigned
	repair int // repair symbols assigned
	total  int // source + repair
}

// sendMulti implements the N>1 RLNC-coded multi-bearer send path.
func (b *bonder) sendMulti(ctx context.Context, payload []byte) error {
	bearers := b.opts.Bearers

	// Filter to healthy bearers.
	var online []BearerProfile
	for i := range bearers {
		if bearers[i].HealthScore > 0 {
			online = append(online, bearers[i])
		}
	}
	if len(online) == 0 {
		return errors.New("hemb: no healthy bearers available")
	}
	if len(online) == 1 {
		return b.sendN1(ctx, payload, &online[0])
	}

	streamID := uint8(b.streamSeq.Add(1) & 0x0F)

	// Step 1: compute initial symbol size = min bearer capacity with K=1 estimate.
	// Each bearer frame = header + K coefficient bytes + symbol data.
	// We iterate: estimate K from rough symSize, then refine.
	minMTU := math.MaxInt
	for i := range online {
		m := online[i].MTU - HeaderOverhead(online[i].HeaderMode)
		if m < minMTU {
			minMTU = m
		}
	}
	if minMTU <= 2 {
		return errors.New("hemb: bearer MTU too small for HeMB framing")
	}

	// Step 2: estimate K from payload size and available data capacity.
	// Start with generous estimate: assume K=1 coeff byte overhead.
	roughSymSize := minMTU - 1 // header already subtracted, minus 1 coeff byte
	if roughSymSize <= 0 {
		roughSymSize = 1
	}
	k := (len(payload) + roughSymSize - 1) / roughSymSize
	if k > 255 {
		k = 255
	}
	if k == 0 {
		k = 1
	}

	// Step 3: compute exact symSize given K (each frame carries K coeff bytes).
	symSize := minMTU - k // data bytes available after K coefficient bytes
	if symSize <= 0 {
		// K too large for smallest bearer — reduce K.
		k = minMTU / 2 // leave half for data
		if k == 0 {
			k = 1
		}
		symSize = minMTU - k
	}
	if symSize <= 0 {
		return errors.New("hemb: payload too large for bearer MTU set")
	}

	// Step 4: recalculate K from exact symSize.
	k = (len(payload) + symSize - 1) / symSize
	if k > 255 {
		k = 255
		symSize = (len(payload) + k - 1) / k
	}
	if k == 0 {
		k = 1
	}

	// Step 3: segment payload into K chunks.
	segments := make([][]byte, k)
	for i := 0; i < k; i++ {
		start := i * symSize
		end := start + symSize
		if end > len(payload) {
			end = len(payload)
		}
		segments[i] = make([]byte, symSize)
		copy(segments[i], payload[start:end])
	}

	// Step 4: allocate symbols to bearers — free first, then paid by cost ASC.
	allocs := b.allocateSymbols(online, k)

	// Step 5: compute total N.
	totalN := 0
	for _, a := range allocs {
		totalN += a.total
	}

	emit(b.opts.EventCh, EventStreamOpened, StreamOpenedPayload{
		StreamID:     streamID,
		BearerCount:  len(online),
		PayloadBytes: len(payload),
		Generations:  1,
		K:            k,
		N:            totalN,
	})

	// Step 6: RLNC encode.
	symbols, err := EncodeGeneration(0, segments, totalN, rand.Reader)
	if err != nil {
		return err
	}

	// Step 7: distribute symbols across bearers and send.
	si := 0
	var sendErr error
	for _, alloc := range allocs {
		for j := 0; j < alloc.total; j++ {
			sym := symbols[si]
			si++
			isRepair := j >= alloc.source
			frame := marshalSymbolFrame(alloc.bearer, streamID, sym, totalN)

			emit(b.opts.EventCh, EventSymbolSent, SymbolSentPayload{
				SymbolRef:    SymbolRef{StreamID: streamID, GenerationID: 0, SymbolIndex: sym.SymbolIndex},
				BearerRef:    BearerRef{BearerIndex: alloc.bearer.Index, BearerType: alloc.bearer.ChannelType},
				PayloadBytes: len(frame),
				IsRepair:     isRepair,
				CostEstimate: alloc.bearer.CostPerMsg,
			})

			if alloc.bearer.IsFree() {
				b.stats.bytesFree.Add(int64(len(frame)))
			} else {
				b.stats.bytesPaid.Add(int64(len(frame)))
				b.stats.costIncurred.Add(int64(alloc.bearer.CostPerMsg * 1e6))
			}
			b.stats.symbolsSent.Add(1)
			Global.RecordSymbolSent(len(frame), alloc.bearer.IsFree(), alloc.bearer.CostPerMsg)

			if err := alloc.bearer.SendFn(ctx, frame); err != nil {
				sendErr = err // record but continue sending to other bearers
			}
		}
	}

	Global.RecordPayloadSent()
	return sendErr
}

// allocateSymbols distributes K source symbols across bearers using
// cost-weighted free-first allocation. Paid bearers get minimal repair.
func (b *bonder) allocateSymbols(bearers []BearerProfile, k int) []bearerAlloc {
	// Separate free and paid bearers.
	var free, paid []BearerProfile
	for i := range bearers {
		if bearers[i].IsFree() {
			free = append(free, bearers[i])
		} else {
			paid = append(paid, bearers[i])
		}
	}

	// Sort free by bandwidth DESC (give more to faster bearers).
	sort.Slice(free, func(i, j int) bool {
		return free[i].MTU > free[j].MTU
	})
	// Sort paid by cost ASC (cheapest first).
	sort.Slice(paid, func(i, j int) bool {
		return paid[i].CostPerMsg < paid[j].CostPerMsg
	})

	allocMap := make(map[uint8]*bearerAlloc)
	for i := range bearers {
		allocMap[bearers[i].Index] = &bearerAlloc{bearer: &bearers[i]}
	}

	// Phase 1: fill free bearers with source symbols.
	remaining := k
	for _, fb := range free {
		if remaining == 0 {
			break
		}
		give := remaining // give as much as possible to each free bearer
		allocMap[fb.Index].source = give
		remaining -= give
		if remaining <= 0 {
			remaining = 0
			break
		}
	}

	// Phase 2: waterfall to paid if free capacity insufficient.
	for _, pb := range paid {
		if remaining == 0 {
			break
		}
		give := remaining
		allocMap[pb.Index].source = give
		remaining -= give
	}

	// Phase 3: add per-bearer repair symbols.
	var result []bearerAlloc
	for i := range bearers {
		a := allocMap[bearers[i].Index]
		if a.source == 0 && a.bearer.IsFree() {
			// Free bearer with no source symbols still gets repair symbols
			// for redundancy across bearer diversity.
			a.repair = RepairSymbols(a.bearer, k)
		} else {
			a.repair = RepairSymbols(a.bearer, a.source)
		}
		a.total = a.source + a.repair
		if a.total > 0 {
			result = append(result, *a)
		}
	}

	return result
}

// ReceiveSymbol processes one inbound coded symbol from a bearer.
// For N=1, the raw payload is delivered directly.
// For N>1, the symbol is parsed and added to the reassembly buffer.
func (b *bonder) ReceiveSymbol(bearerIndex uint8, data []byte) ([]byte, error) {
	if len(b.opts.Bearers) == 1 {
		b.stats.symbolsReceived.Add(1)
		emit(b.opts.EventCh, EventSymbolReceived, SymbolReceivedPayload{
			BearerRef:    BearerRef{BearerIndex: bearerIndex, BearerType: b.opts.Bearers[0].ChannelType},
			PayloadBytes: len(data),
			Received:     1,
			Required:     1,
		})
		if b.opts.DeliverFn != nil {
			b.opts.DeliverFn(data)
		}
		return data, nil
	}

	// N>1: parse frame and add to reassembly.
	sym, streamID, _, err := parseSymbolFromFrame(data)
	if err != nil {
		return nil, err
	}

	b.stats.symbolsReceived.Add(1)
	emit(b.opts.EventCh, EventSymbolReceived, SymbolReceivedPayload{
		SymbolRef:    SymbolRef{StreamID: streamID, GenerationID: sym.GenID, SymbolIndex: sym.SymbolIndex},
		BearerRef:    BearerRef{BearerIndex: bearerIndex},
		PayloadBytes: len(data),
	})

	return b.reassembly.AddSymbol(streamID, bearerIndex, sym)
}

// StartStatsEmitter runs a background goroutine that emits EventBondStats
// at the given interval. Stops when ctx is cancelled.
func (b *bonder) StartStatsEmitter(ctx context.Context, interval time.Duration, ch chan<- Event) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				st := b.Stats()
				emit(ch, EventBondStats, BondStatsPayload{
					ActiveStreams:      st.ActiveStreams,
					SymbolsSent:        st.SymbolsSent,
					SymbolsReceived:    st.SymbolsReceived,
					GenerationsDecoded: st.GenerationsDecoded,
					GenerationsFailed:  st.GenerationsFailed,
					BytesFree:          st.BytesFree,
					BytesPaid:          st.BytesPaid,
					CostTotal:          st.CostIncurred,
				})
			}
		}
	}()
}

// Stats returns current bonding metrics.
func (b *bonder) Stats() BondStats {
	s := BondStats{
		SymbolsSent:        b.stats.symbolsSent.Load(),
		SymbolsReceived:    b.stats.symbolsReceived.Load(),
		GenerationsDecoded: b.stats.generationsDecoded.Load(),
		GenerationsFailed:  b.stats.generationsFailed.Load(),
		BytesFree:          b.stats.bytesFree.Load(),
		BytesPaid:          b.stats.bytesPaid.Load(),
		CostIncurred:       float64(b.stats.costIncurred.Load()) / 1e6,
	}
	if b.reassembly != nil {
		s.ActiveStreams = b.reassembly.PendingCount()
	}
	return s
}

// ActiveStreams returns info about active reassembly streams.
func (b *bonder) ActiveStreams() []StreamInfo {
	if b.reassembly == nil {
		return nil
	}
	return b.reassembly.ActiveStreams()
}

// StreamDetail returns per-generation info for a specific stream.
func (b *bonder) StreamDetail(streamID uint8) ([]GenerationInfo, bool) {
	if b.reassembly == nil {
		return nil, false
	}
	return b.reassembly.StreamDetail(streamID)
}

// InspectGeneration returns detailed RLNC matrix data for a specific generation.
func (b *bonder) InspectGeneration(streamID uint8, genID uint16) (*GenerationInspection, bool) {
	if b.reassembly == nil {
		return nil, false
	}
	return b.reassembly.InspectGeneration(streamID, genID)
}
