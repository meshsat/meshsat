package gateway

import (
	"sync"
	"time"
)

// TakCotEventRecord is a CoT event captured for the event stream.
type TakCotEventRecord struct {
	Timestamp time.Time `json:"timestamp"`
	Direction string    `json:"direction"` // "inbound" or "outbound"
	UID       string    `json:"uid"`
	Type      string    `json:"type"`
	Callsign  string    `json:"callsign"`
	Lat       float64   `json:"lat"`
	Lon       float64   `json:"lon"`
	How       string    `json:"how"`
	Stale     string    `json:"stale"`
	Detail    string    `json:"detail,omitempty"` // condensed detail text
}

// TakEventBus manages fan-out of CoT events to SSE subscribers with a ring buffer.
type TakEventBus struct {
	mu          sync.RWMutex
	subscribers []chan TakCotEventRecord
	ring        []TakCotEventRecord
	ringSize    int
	ringIdx     int
	ringFull    bool
}

// NewTakEventBus creates an event bus with ring buffer replay.
func NewTakEventBus(ringSize int) *TakEventBus {
	if ringSize <= 0 {
		ringSize = 200
	}
	return &TakEventBus{
		ring:     make([]TakCotEventRecord, ringSize),
		ringSize: ringSize,
	}
}

// Subscribe returns a buffered channel and an unsubscribe function.
func (eb *TakEventBus) Subscribe() (<-chan TakCotEventRecord, func()) {
	ch := make(chan TakCotEventRecord, 64)
	eb.mu.Lock()
	eb.subscribers = append(eb.subscribers, ch)
	eb.mu.Unlock()

	unsub := func() {
		eb.mu.Lock()
		defer eb.mu.Unlock()
		for i, s := range eb.subscribers {
			if s == ch {
				eb.subscribers = append(eb.subscribers[:i], eb.subscribers[i+1:]...)
				close(ch)
				return
			}
		}
	}
	return ch, unsub
}

// Publish sends a CoT event to all subscribers and records in ring buffer.
func (eb *TakEventBus) Publish(evt TakCotEventRecord) {
	eb.mu.Lock()
	eb.ring[eb.ringIdx] = evt
	eb.ringIdx = (eb.ringIdx + 1) % eb.ringSize
	if eb.ringIdx == 0 {
		eb.ringFull = true
	}
	subs := make([]chan TakCotEventRecord, len(eb.subscribers))
	copy(subs, eb.subscribers)
	eb.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- evt:
		default: // drop for slow subscribers
		}
	}
}

// Recent returns the last N events from the ring buffer (oldest first).
func (eb *TakEventBus) Recent(n int) []TakCotEventRecord {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	total := eb.ringIdx
	if eb.ringFull {
		total = eb.ringSize
	}
	if n > total {
		n = total
	}

	result := make([]TakCotEventRecord, 0, n)
	start := eb.ringIdx - n
	if start < 0 {
		if eb.ringFull {
			start += eb.ringSize
		} else {
			start = 0
		}
	}

	for i := 0; i < n; i++ {
		idx := (start + i) % eb.ringSize
		result = append(result, eb.ring[idx])
	}
	return result
}

// CotEventToRecord converts a parsed CotEvent to a stream record.
func CotEventToRecord(ev *CotEvent, direction string) TakCotEventRecord {
	rec := TakCotEventRecord{
		Timestamp: time.Now(),
		Direction: direction,
		UID:       ev.UID,
		Type:      ev.Type,
		How:       ev.How,
		Stale:     ev.Stale,
	}
	rec.Lat = ev.Point.Lat
	rec.Lon = ev.Point.Lon
	if ev.Detail != nil {
		if ev.Detail.Contact != nil {
			rec.Callsign = ev.Detail.Contact.Callsign
		}
		if ev.Detail.Remarks != nil && ev.Detail.Remarks.Text != "" {
			rec.Detail = ev.Detail.Remarks.Text
			if len(rec.Detail) > 120 {
				rec.Detail = rec.Detail[:120] + "..."
			}
		}
	}
	return rec
}

// GlobalTakEventBus is the singleton TAK CoT event bus.
var GlobalTakEventBus = NewTakEventBus(200)
