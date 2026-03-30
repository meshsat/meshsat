package hemb

import "sync/atomic"

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
}

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

// Snapshot returns a point-in-time copy of all counters.
func (g *GlobalStats) Snapshot() BondStats {
	return BondStats{
		SymbolsSent:        g.symbolsSent.Load(),
		SymbolsReceived:    g.symbolsReceived.Load(),
		GenerationsDecoded: g.generationsDecoded.Load(),
		GenerationsFailed:  g.generationsFailed.Load(),
		BytesFree:          g.bytesFree.Load(),
		BytesPaid:          g.bytesPaid.Load(),
		CostIncurred:       float64(g.costMicro.Load()) / 1e6,
	}
}
