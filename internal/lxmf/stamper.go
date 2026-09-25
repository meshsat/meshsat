package lxmf

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"math/bits"
	"runtime"
	"sync"
	"sync/atomic"

	"meshsat/internal/reticulum"
)

// WorkblockExpandRounds is LXStamper.WORKBLOCK_EXPAND_ROUNDS.
const WorkblockExpandRounds = 3000

// Workblock is LXStamper.stamp_workblock: 3000 HKDF expansions of the
// message id, each salted with SHA-256(id || msgpack(n)).
func Workblock(messageID [32]byte) []byte {
	out := make([]byte, 0, WorkblockExpandRounds*256)
	for n := 0; n < WorkblockExpandRounds; n++ {
		salt := sha256.Sum256(append(append([]byte(nil), messageID[:]...), Pack(Int(int64(n)))...))
		out = append(out, reticulum.HKDF(256, messageID[:], salt[:], nil)...)
	}
	return out
}

// StampValid is LXStamper.stamp_valid: SHA-256(workblock || stamp) as a
// big-endian integer must not exceed 2^(256-cost).
func StampValid(workblock, stamp []byte, cost int) bool {
	if cost <= 0 || cost >= 256 || len(stamp) == 0 {
		return false
	}
	h := sha256.Sum256(append(append([]byte(nil), workblock...), stamp...))
	return StampValue(workblock, stamp, h) >= cost
}

// StampValue is the number of leading zero bits of SHA-256(workblock||stamp).
func StampValue(workblock, stamp []byte, h [32]byte) int {
	value := 0
	for _, b := range h {
		if b == 0 {
			value += 8
			continue
		}
		value += bits.LeadingZeros8(b)
		break
	}
	return value
}

// stampValueOf recomputes the hash and the value.
func stampValueOf(workblock, stamp []byte) int {
	h := sha256.Sum256(append(append([]byte(nil), workblock...), stamp...))
	return StampValue(workblock, stamp, h)
}

// GenerateStamp searches random 32-byte stamps until one meets cost, using
// every core, or returns nil when ctx ends first (LXStamper.generate_stamp).
func GenerateStamp(ctx context.Context, messageID [32]byte, cost int) ([]byte, int) {
	if cost <= 0 {
		return nil, 0
	}
	wb := Workblock(messageID)
	var found atomic.Bool
	var result []byte
	var mu sync.Mutex
	var wg sync.WaitGroup
	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, len(wb)+StampLen)
			copy(buf, wb)
			stamp := buf[len(wb):]
			for i := 0; !found.Load(); i++ {
				if i%256 == 0 && ctx.Err() != nil {
					return
				}
				rand.Read(stamp)
				h := sha256.Sum256(buf)
				if StampValue(wb, stamp, h) >= cost {
					if found.CompareAndSwap(false, true) {
						mu.Lock()
						result = append([]byte(nil), stamp...)
						mu.Unlock()
					}
					return
				}
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if result == nil {
		return nil, 0
	}
	return result, stampValueOf(wb, result)
}
