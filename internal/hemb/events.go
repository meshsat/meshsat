// Package hemb implements Heterogeneous Media Bonding — a transport-agnostic
// bearer bonding protocol that simultaneously distributes RLNC-coded symbols
// across N heterogeneous physical bearers, allowing any K of N symbols from
// any bearer combination to reconstruct the original payload.
//
// This package has ZERO imports from internal/routing or internal/reticulum.
// The dependency arrow is one-way: engine/dispatcher → hemb → gateway send functions.
package hemb

import (
	"encoding/json"
	"time"
)

// EventType identifies a HeMB observability event.
type EventType string

const (
	EventSymbolSent        EventType = "HEMB_SYMBOL_SENT"
	EventSymbolReceived    EventType = "HEMB_SYMBOL_RECEIVED"
	EventGenerationDecoded EventType = "HEMB_GENERATION_DECODED"
	EventGenerationFailed  EventType = "HEMB_GENERATION_FAILED"
	EventBearerDegraded    EventType = "HEMB_BEARER_DEGRADED"
	EventBearerRecovered   EventType = "HEMB_BEARER_RECOVERED"
	EventStreamOpened      EventType = "HEMB_STREAM_OPENED"
	EventStreamClosed      EventType = "HEMB_STREAM_CLOSED"
	EventBondStats         EventType = "HEMB_BOND_STATS"
)

// Event is a single HeMB observability event, ready for SSE dispatch.
type Event struct {
	Type      EventType       `json:"type"`
	Timestamp time.Time       `json:"ts"`
	Payload   json.RawMessage `json:"payload"`
}

// ── Shared sub-types ─────────────────────────────────────────────────────────

// BearerRef identifies a bearer across event payloads.
type BearerRef struct {
	BearerIndex uint8  `json:"bearer_idx"`
	BearerType  string `json:"bearer_type"`
}

// SymbolRef identifies a specific symbol within a stream/generation.
type SymbolRef struct {
	StreamID     uint8  `json:"stream_id"`
	GenerationID uint16 `json:"gen_id"`
	SymbolIndex  int    `json:"sym_idx"`
}

// BearerContribution records how many symbols a bearer contributed to a decode.
type BearerContribution struct {
	BearerRef
	SymbolCount int     `json:"symbol_count"`
	FirstMs     int64   `json:"first_ms"`
	LastMs      int64   `json:"last_ms"`
	Cost        float64 `json:"cost"`
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// emit sends an event to the channel without blocking. If the channel is nil
// or full, the event is silently dropped. Observability must never block the
// data path.
func emit(ch chan<- Event, t EventType, payload any) {
	if ch == nil {
		return
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	select {
	case ch <- Event{Type: t, Timestamp: time.Now(), Payload: b}:
	default:
		// Drop — never block on observability.
	}
}

// ── Event payloads ───────────────────────────────────────────────────────────

// SymbolSentPayload is emitted by Bonder.Send() for each coded symbol dispatched.
type SymbolSentPayload struct {
	SymbolRef
	BearerRef
	PayloadBytes int     `json:"payload_bytes"`
	IsRepair     bool    `json:"is_repair"`
	CostEstimate float64 `json:"cost_est"`
}

// SymbolReceivedPayload is emitted by ReassemblyBuffer.AddSymbol() for each
// inbound coded symbol.
type SymbolReceivedPayload struct {
	SymbolRef
	BearerRef
	PayloadBytes int   `json:"payload_bytes"`
	LatencyMs    int64 `json:"latency_ms"` // since generation first symbol
	Received     int   `json:"received"`   // total symbols for this generation
	Required     int   `json:"required"`   // K symbols needed
}

// GenerationDecodedPayload is emitted when GaussianEliminate succeeds.
type GenerationDecodedPayload struct {
	StreamID     uint8                `json:"stream_id"`
	GenerationID uint16               `json:"gen_id"`
	K            int                  `json:"k"`
	N            int                  `json:"n"`
	Received     int                  `json:"received"`
	DecodeTimeUs int64                `json:"decode_time_us"`
	LatencyMs    int64                `json:"latency_ms"` // first symbol to decode
	PayloadBytes int                  `json:"payload_bytes"`
	Bearers      []BearerContribution `json:"bearers"`
	CostTotal    float64              `json:"cost_total"`
}

// GenerationFailedPayload is emitted when a generation times out or is
// rank-deficient with no more symbols expected.
type GenerationFailedPayload struct {
	StreamID     uint8                `json:"stream_id"`
	GenerationID uint16               `json:"gen_id"`
	K            int                  `json:"k"`
	Received     int                  `json:"received"`
	Reason       string               `json:"reason"` // "timeout", "rank_deficient"
	TimeoutMs    int64                `json:"timeout_ms"`
	Bearers      []BearerContribution `json:"bearers"`
	CostWasted   float64              `json:"cost_wasted"`
}

// BearerDegradedPayload is emitted when a bearer's health score drops
// below a threshold during active bonding.
type BearerDegradedPayload struct {
	BearerRef
	HealthScore int     `json:"health_score"`
	PrevScore   int     `json:"prev_score"`
	LossRate    float64 `json:"loss_rate"`
	LatencyMs   int     `json:"latency_ms"`
	Reason      string  `json:"reason"` // "signal_loss", "high_loss_rate", "timeout"
}

// BearerRecoveredPayload is emitted when a previously degraded bearer
// recovers above the health threshold.
type BearerRecoveredPayload struct {
	BearerRef
	HealthScore int   `json:"health_score"`
	PrevScore   int   `json:"prev_score"`
	DowntimeMs  int64 `json:"downtime_ms"`
}

// StreamOpenedPayload is emitted when Bonder.Send() creates a new bonded stream.
type StreamOpenedPayload struct {
	StreamID     uint8   `json:"stream_id"`
	BearerCount  int     `json:"bearer_count"`
	PayloadBytes int     `json:"payload_bytes"`
	Generations  int     `json:"generations"`
	K            int     `json:"k"`
	N            int     `json:"n"`
	CostBudget   float64 `json:"cost_budget"`
	Priority     int     `json:"priority"`
}

// StreamClosedPayload is emitted when all generations of a stream are
// decoded or failed.
type StreamClosedPayload struct {
	StreamID           uint8   `json:"stream_id"`
	Verdict            string  `json:"verdict"` // "decoded", "partial", "failed"
	GenerationsTotal   int     `json:"gens_total"`
	GenerationsDecoded int     `json:"gens_decoded"`
	GenerationsFailed  int     `json:"gens_failed"`
	DurationMs         int64   `json:"duration_ms"`
	CostTotal          float64 `json:"cost_total"`
	BytesDelivered     int     `json:"bytes_delivered"`
}

// BondStatsPayload is emitted periodically (every 5 seconds) as an
// aggregate snapshot of bonding activity.
type BondStatsPayload struct {
	ActiveStreams      int                    `json:"active_streams"`
	SymbolsSent        int64                  `json:"symbols_sent"`
	SymbolsReceived    int64                  `json:"symbols_received"`
	GenerationsDecoded int64                  `json:"gens_decoded"`
	GenerationsFailed  int64                  `json:"gens_failed"`
	BytesFree          int64                  `json:"bytes_free"`
	BytesPaid          int64                  `json:"bytes_paid"`
	CostTotal          float64                `json:"cost_total"`
	PendingGenerations int                    `json:"pending_gens"`
	BearerHealth       []BearerHealthSnapshot `json:"bearer_health"`
}

// BearerHealthSnapshot is a point-in-time view of a single bearer's state.
type BearerHealthSnapshot struct {
	BearerRef
	HealthScore int     `json:"health_score"`
	Online      bool    `json:"online"`
	SymbolsSent int64   `json:"symbols_sent"`
	SymbolsRecv int64   `json:"symbols_recv"`
	LossRate    float64 `json:"loss_rate"`
}
