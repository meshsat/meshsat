package hemb

import (
	"context"
	"time"
)

// BearerProfile describes a single physical transport channel for HeMB bonding.
// Cost is a PRIMARY INPUT to the allocation function, not metadata.
// Bearers are NEVER treated as equivalent — MTU, cost, loss rate, and latency
// differ by orders of magnitude across the bearer set.
type BearerProfile struct {
	Index        uint8                                        // position in the bearer set (0-based)
	InterfaceID  string                                       // e.g. "mesh_0", "iridium_0", "sms_0"
	ChannelType  string                                       // e.g. "mesh", "iridium_sbd", "sms", "aprs", "ipougrs"
	MTU          int                                          // effective payload bytes after HeMB header
	CostPerMsg   float64                                      // $0.00 = free, $0.05 = SBD
	LossRate     float64                                      // estimated 0.0-1.0 (0.02 = 2%, 0.40 = 40%)
	LatencyMs    int                                          // median one-way latency in ms
	HealthScore  int                                          // 0-100, from existing health scorer
	SendFn       func(ctx context.Context, data []byte) error // injected send function
	RelayCapable bool                                         // false for IPoUGRS (single-hop only)
	HeaderMode   string                                       // "compact", "extended", "implicit" (IPoUGRS)
}

// IsFree returns true if this bearer has zero per-message cost.
func (b *BearerProfile) IsFree() bool { return b.CostPerMsg == 0 }

// Options configures a Bonder instance.
type Options struct {
	Bearers   []BearerProfile      // available physical bearers
	DeliverFn func(payload []byte) // called when reassembly completes a generation
	EventCh   chan<- Event         // SSE event channel, nil = disabled
}

// Bonder is the exported interface for the HeMB bonding layer.
// Implemented by the unexported bonder struct.
type Bonder interface {
	Send(ctx context.Context, payload []byte) error
	ReceiveSymbol(bearerIndex uint8, data []byte) ([]byte, error)
	Stats() BondStats
	StartStatsEmitter(ctx context.Context, interval time.Duration, ch chan<- Event)
	ActiveStreams() []StreamInfo
	StreamDetail(streamID uint8) ([]GenerationInfo, bool)
	InspectGeneration(streamID uint8, genID uint16) (*GenerationInspection, bool)
}

// BondConfig controls per-send bonding parameters.
type BondConfig struct {
	CostBudget     float64 // max $ to spend (0 = unlimited)
	MinReliability float64 // target delivery probability (0.0-1.0)
	PreferFree     bool    // exhaust free bearers before paid (default true)
}

// BondStats reports aggregate bonding metrics.
type BondStats struct {
	ActiveStreams      int
	SymbolsSent        int64
	SymbolsReceived    int64
	GenerationsDecoded int64
	GenerationsFailed  int64
	BytesFree          int64
	BytesPaid          int64
	CostIncurred       float64
}
