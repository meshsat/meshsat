package hemb

import (
	"sync"
)

// EventBus manages fan-out of HeMB events to multiple SSE subscribers
// with a ring buffer for replay on reconnect.
type EventBus struct {
	mu          sync.RWMutex
	subscribers []chan Event
	ring        []Event
	ringSize    int
	ringIdx     int
	ringFull    bool
}

// NewEventBus creates an event bus with a ring buffer of the given size.
func NewEventBus(ringSize int) *EventBus {
	if ringSize <= 0 {
		ringSize = 200
	}
	return &EventBus{
		ring:     make([]Event, ringSize),
		ringSize: ringSize,
	}
}

// Subscribe creates a new subscriber channel. Returns the channel and an
// unsubscribe function. The channel has a 64-element buffer; slow subscribers
// get events dropped (never block the publisher).
func (eb *EventBus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
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

// Publish sends an event to all subscribers and records it in the ring buffer.
// Non-blocking: drops events to slow subscribers.
func (eb *EventBus) Publish(evt Event) {
	// Record in ring buffer.
	eb.mu.Lock()
	eb.ring[eb.ringIdx] = evt
	eb.ringIdx = (eb.ringIdx + 1) % eb.ringSize
	if eb.ringIdx == 0 {
		eb.ringFull = true
	}
	// Fan-out to subscribers.
	for _, ch := range eb.subscribers {
		select {
		case ch <- evt:
		default:
			// Drop — never block on slow subscribers.
		}
	}
	eb.mu.Unlock()
}

// Recent returns the most recent events from the ring buffer, up to limit.
// Events are returned in chronological order (oldest first).
func (eb *EventBus) Recent(limit int) []Event {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	var count int
	if eb.ringFull {
		count = eb.ringSize
	} else {
		count = eb.ringIdx
	}
	if count == 0 {
		return nil
	}
	if limit > 0 && limit < count {
		count = limit
	}

	result := make([]Event, 0, count)
	start := eb.ringIdx - count
	if start < 0 {
		start += eb.ringSize
	}
	for i := 0; i < count; i++ {
		idx := (start + i) % eb.ringSize
		result = append(result, eb.ring[idx])
	}
	return result
}

// RecentByType returns recent events filtered by type.
func (eb *EventBus) RecentByType(eventType EventType, limit int) []Event {
	all := eb.Recent(0) // get all
	var filtered []Event
	for _, evt := range all {
		if evt.Type == eventType {
			filtered = append(filtered, evt)
		}
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	return filtered
}

// SubscribeFiltered creates a subscriber that only receives events matching
// the given types. Returns the channel and an unsubscribe function.
func (eb *EventBus) SubscribeFiltered(types ...EventType) (<-chan Event, func()) {
	match := make(map[EventType]struct{}, len(types))
	for _, t := range types {
		match[t] = struct{}{}
	}

	out := make(chan Event, 64)
	// Internal unfiltered subscription feeds into the filter goroutine.
	raw, unsub := eb.Subscribe()

	done := make(chan struct{})
	go func() {
		defer close(out)
		for {
			select {
			case evt, ok := <-raw:
				if !ok {
					return
				}
				if _, ok := match[evt.Type]; ok {
					select {
					case out <- evt:
					default:
					}
				}
			case <-done:
				return
			}
		}
	}()

	return out, func() {
		close(done)
		unsub()
	}
}

// SubscriberCount returns the number of active subscribers.
func (eb *EventBus) SubscriberCount() int {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	return len(eb.subscribers)
}

// Channel returns a send-only channel suitable for passing to hemb.Options.EventCh.
// Events sent to this channel are automatically published to all subscribers.
func (eb *EventBus) Channel() chan<- Event {
	ch := make(chan Event, 256)
	go func() {
		for evt := range ch {
			eb.Publish(evt)
		}
	}()
	return ch
}

// GlobalEventBus is the default HeMB event bus used by the bridge.
// Created once, shared between Bonder instances and SSE handlers.
var GlobalEventBus = NewEventBus(200)
