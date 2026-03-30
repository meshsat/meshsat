package hemb

import (
	"sort"
	"sync"
	"sync/atomic"
)

// GlobalStats aggregates metrics across ALL bonder instances (ephemeral + persistent).
// The dispatcher creates ephemeral bonders per delivery — their stats would otherwise
// be lost when the bonder is garbage collected. This singleton accumulates globally.
type GlobalStats struct {
	symbolsSent        atomic.Int64
	symbolsReceived    atomic.Int64
	generationsDecoded atomic.Int64
	generationsFailed  atomic.Int64
	bytesFree          atomic.Int64
	bytesPaid          atomic.Int64
	costMicro          atomic.Int64 // cost * 1e6
	payloadsSent       atomic.Int64
	payloadsDecoded    atomic.Int64

	latencyMu       sync.Mutex
	decodeLatencies []int64 // ms, ring buffer
	latencyIdx      int
}

const latencyRingSize = 10000 // keep last 10k decode latencies

// Global is the singleton stats aggregator.
var Global = &GlobalStats{}

func (g *GlobalStats) RecordSymbolSent(bytes int, isFree bool, costPerMsg float64) {
	g.symbolsSent.Add(1)
	if isFree {
		g.bytesFree.Add(int64(bytes))
	} else {
		g.bytesPaid.Add(int64(bytes))
		g.costMicro.Add(int64(costPerMsg * 1e6))
	}
}

func (g *GlobalStats) RecordSymbolReceived() { g.symbolsReceived.Add(1) }
func (g *GlobalStats) RecordGenerationDecoded() {
	g.generationsDecoded.Add(1)
	g.payloadsDecoded.Add(1)
}
func (g *GlobalStats) RecordGenerationFailed() { g.generationsFailed.Add(1) }
func (g *GlobalStats) RecordPayloadSent()      { g.payloadsSent.Add(1) }

// RecordDecodeLatency records a generation decode latency in ms.
func (g *GlobalStats) RecordDecodeLatency(ms int64) {
	g.latencyMu.Lock()
	defer g.latencyMu.Unlock()
	if g.decodeLatencies == nil {
		g.decodeLatencies = make([]int64, 0, latencyRingSize)
	}
	if len(g.decodeLatencies) < latencyRingSize {
		g.decodeLatencies = append(g.decodeLatencies, ms)
	} else {
		g.decodeLatencies[g.latencyIdx%latencyRingSize] = ms
	}
	g.latencyIdx++
}

// DecodeLatencyPercentiles returns P50 and P95 decode latencies in ms.
// Returns (0, 0) if no data.
func (g *GlobalStats) DecodeLatencyPercentiles() (p50, p95 int64) {
	g.latencyMu.Lock()
	n := len(g.decodeLatencies)
	if n == 0 {
		g.latencyMu.Unlock()
		return 0, 0
	}
	cp := make([]int64, n)
	copy(cp, g.decodeLatencies)
	g.latencyMu.Unlock()

	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	p50 = cp[n*50/100]
	p95 = cp[n*95/100]
	return p50, p95
}

// Snapshot returns a point-in-time copy of all counters.
func (g *GlobalStats) Snapshot() BondStats {
	p50, p95 := g.DecodeLatencyPercentiles()
	return BondStats{
		SymbolsSent:        g.symbolsSent.Load(),
		SymbolsReceived:    g.symbolsReceived.Load(),
		GenerationsDecoded: g.generationsDecoded.Load(),
		GenerationsFailed:  g.generationsFailed.Load(),
		BytesFree:          g.bytesFree.Load(),
		BytesPaid:          g.bytesPaid.Load(),
		CostIncurred:       float64(g.costMicro.Load()) / 1e6,
		DecodeLatencyP50:   p50,
		DecodeLatencyP95:   p95,
	}
}
