package engine

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

const (
	// BurstTypeByte is the TLV type marker for burst frames.
	BurstTypeByte = 0x42

	// IridiumMTU is the maximum SBD payload size for Iridium.
	IridiumMTU = 340

	// burstHeaderLen is 1 byte type + 2 bytes total_count.
	burstHeaderLen = 3

	// burstMsgHeaderLen is 2 bytes payload_len per message.
	burstMsgHeaderLen = 2
)

// BurstMessage represents a single message queued for burst transmission.
type BurstMessage struct {
	Payload   []byte
	Priority  int
	QueuedAt  time.Time
	Interface string
}

// BurstQueue queues messages for burst-send during satellite passes.
type BurstQueue struct {
	db      *database.DB
	maxSize int
	maxAge  time.Duration
	mu      sync.Mutex
	pending []BurstMessage
}

// NewBurstQueue creates a burst queue with size and age limits.
func NewBurstQueue(db *database.DB, maxSize int, maxAge time.Duration) *BurstQueue {
	return &BurstQueue{
		db:      db,
		maxSize: maxSize,
		maxAge:  maxAge,
	}
}

// Enqueue adds a message to the burst queue.
func (b *BurstQueue) Enqueue(msg BurstMessage) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(msg.Payload) == 0 {
		return fmt.Errorf("empty payload")
	}
	if len(msg.Payload) > IridiumMTU-burstHeaderLen-burstMsgHeaderLen {
		return fmt.Errorf("payload too large: %d bytes (max %d)", len(msg.Payload), IridiumMTU-burstHeaderLen-burstMsgHeaderLen)
	}

	if msg.QueuedAt.IsZero() {
		msg.QueuedAt = time.Now()
	}
	b.pending = append(b.pending, msg)

	log.Debug().Int("pending", len(b.pending)).Int("priority", msg.Priority).Msg("burst: message enqueued")
	return nil
}

// Flush TLV-packs all pending messages into one or more SBD-sized payloads.
// Returns the first combined payload, the count of messages packed, and any error.
// If total exceeds IridiumMTU, only messages that fit are included (highest priority first).
func (b *BurstQueue) Flush(ctx context.Context) ([]byte, int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.pending) == 0 {
		return nil, 0, nil
	}

	// Sort by priority descending (higher priority first)
	sort.Slice(b.pending, func(i, j int) bool {
		return b.pending[i].Priority > b.pending[j].Priority
	})

	payload, count := packBurst(b.pending, IridiumMTU)

	// Remove packed messages from pending
	if count >= len(b.pending) {
		b.pending = b.pending[:0]
	} else {
		b.pending = b.pending[count:]
	}

	log.Info().Int("packed", count).Int("remaining", len(b.pending)).Int("bytes", len(payload)).Msg("burst: flushed")
	return payload, count, nil
}

// Pending returns the number of queued messages.
func (b *BurstQueue) Pending() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

// GetMaxSize returns the maximum queue size.
func (b *BurstQueue) GetMaxSize() int {
	return b.maxSize
}

// GetMaxAge returns the maximum message age before auto-flush.
func (b *BurstQueue) GetMaxAge() time.Duration {
	return b.maxAge
}

// ShouldFlush returns true if maxAge exceeded or maxSize reached.
func (b *BurstQueue) ShouldFlush() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.pending) == 0 {
		return false
	}
	if len(b.pending) >= b.maxSize {
		return true
	}
	// Check if the oldest message exceeds maxAge
	oldest := b.pending[0].QueuedAt
	for _, m := range b.pending[1:] {
		if m.QueuedAt.Before(oldest) {
			oldest = m.QueuedAt
		}
	}
	return time.Since(oldest) >= b.maxAge
}

// packBurst creates a TLV-framed burst payload from the given messages,
// fitting within the specified MTU.
//
// Format:
//
//	[1B type=0x42] [2B total_count uint16 LE]
//	For each message:
//	  [2B payload_len uint16 LE] [payload bytes]
func packBurst(msgs []BurstMessage, mtu int) ([]byte, int) {
	buf := make([]byte, 0, mtu)
	buf = append(buf, BurstTypeByte)
	buf = append(buf, 0, 0) // placeholder for count

	count := 0
	for _, msg := range msgs {
		needed := burstMsgHeaderLen + len(msg.Payload)
		if len(buf)+needed > mtu {
			break
		}
		lenBuf := make([]byte, 2)
		binary.LittleEndian.PutUint16(lenBuf, uint16(len(msg.Payload)))
		buf = append(buf, lenBuf...)
		buf = append(buf, msg.Payload...)
		count++
	}

	// Write the actual count
	binary.LittleEndian.PutUint16(buf[1:3], uint16(count))
	return buf, count
}

// UnpackBurst decodes a TLV-framed burst payload into individual message payloads.
func UnpackBurst(data []byte) ([][]byte, error) {
	if len(data) < burstHeaderLen {
		return nil, fmt.Errorf("burst data too short: %d bytes", len(data))
	}
	if data[0] != BurstTypeByte {
		return nil, fmt.Errorf("invalid burst type byte: 0x%02x (expected 0x%02x)", data[0], BurstTypeByte)
	}

	count := int(binary.LittleEndian.Uint16(data[1:3]))
	offset := burstHeaderLen
	payloads := make([][]byte, 0, count)

	for i := 0; i < count; i++ {
		if offset+burstMsgHeaderLen > len(data) {
			return nil, fmt.Errorf("truncated burst at message %d: need header at offset %d", i, offset)
		}
		payloadLen := int(binary.LittleEndian.Uint16(data[offset : offset+burstMsgHeaderLen]))
		offset += burstMsgHeaderLen

		if offset+payloadLen > len(data) {
			return nil, fmt.Errorf("truncated burst at message %d: need %d bytes at offset %d", i, payloadLen, offset)
		}
		payload := make([]byte, payloadLen)
		copy(payload, data[offset:offset+payloadLen])
		payloads = append(payloads, payload)
		offset += payloadLen
	}

	return payloads, nil
}
