package engine

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestInterleaveDeinterleaveRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  int
		depth int
	}{
		{"9 bytes depth 3", 9, 3},
		{"12 bytes depth 4", 12, 4},
		{"100 bytes depth 8", 100, 8},
		{"100 bytes depth 16", 100, 16},
		{"255 bytes depth 5", 255, 5}, // not evenly divisible
		{"1000 bytes depth 8", 1000, 8},
		{"7 bytes depth 3", 7, 3}, // ragged last column
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := make([]byte, tc.size)
			for i := range data {
				data[i] = byte(i % 256)
			}

			interleaved := fecInterleave(data, tc.depth)
			if len(interleaved) != len(data) {
				t.Fatalf("interleave changed length: %d -> %d", len(data), len(interleaved))
			}

			deinterleaved := fecDeinterleave(interleaved, tc.depth)
			if !bytes.Equal(deinterleaved, data) {
				t.Fatalf("round-trip mismatch (first 20 bytes): got %v, want %v",
					deinterleaved[:min(20, len(deinterleaved))],
					data[:min(20, len(data))])
			}
		})
	}
}

func TestInterleaveActuallyRearranges(t *testing.T) {
	// [a0,a1,a2, b0,b1,b2, c0,c1,c2] with depth=3
	// Expected: [a0,b0,c0, a1,b1,c1, a2,b2,c2]
	data := []byte{10, 11, 12, 20, 21, 22, 30, 31, 32}
	expected := []byte{10, 20, 30, 11, 21, 31, 12, 22, 32}

	interleaved := fecInterleave(data, 3)
	if !bytes.Equal(interleaved, expected) {
		t.Fatalf("got %v, want %v", interleaved, expected)
	}
}

func TestInterleaveSpreadsBurstErrors(t *testing.T) {
	// Key property: consecutive bytes in interleaved output map to
	// different positions in original data (spread across shards).
	data := make([]byte, 48)
	for i := range data {
		data[i] = byte(i)
	}

	depth := 8
	interleaved := fecInterleave(data, depth)

	// Corrupt a burst of 4 consecutive bytes in the interleaved data.
	for i := 0; i < 4; i++ {
		interleaved[i] = 0xFF
	}

	// De-interleave: the corrupted bytes should be spread apart.
	result := fecDeinterleave(interleaved, depth)

	// Count corrupted bytes per 6-byte shard (48/8=6 bytes per shard).
	shardSize := len(data) / depth
	for s := 0; s < depth; s++ {
		corrupted := 0
		for j := 0; j < shardSize; j++ {
			idx := s*shardSize + j
			if result[idx] != data[idx] {
				corrupted++
			}
		}
		// Each shard should have at most 1 corrupted byte (burst spread).
		if corrupted > 1 {
			t.Errorf("shard %d has %d corrupted bytes (expected <= 1)", s, corrupted)
		}
	}
}

func TestInterleaveNoopCases(t *testing.T) {
	data := []byte{1, 2, 3, 4, 5}

	// Depth 0 — no-op.
	result := fecInterleave(data, 0)
	if !bytes.Equal(result, data) {
		t.Fatal("depth 0 should be no-op")
	}

	// Depth 1 — no-op.
	result = fecInterleave(data, 1)
	if !bytes.Equal(result, data) {
		t.Fatal("depth 1 should be no-op")
	}

	// Depth >= len — no-op.
	result = fecInterleave(data, len(data))
	if !bytes.Equal(result, data) {
		t.Fatal("depth >= len should be no-op")
	}
}

func TestInterleaveRandomData(t *testing.T) {
	for _, size := range []int{17, 64, 230, 340, 1024} {
		data := make([]byte, size)
		rand.Read(data)

		for _, depth := range []int{2, 4, 8, 16} {
			interleaved := fecInterleave(data, depth)
			result := fecDeinterleave(interleaved, depth)
			if !bytes.Equal(result, data) {
				t.Fatalf("size=%d depth=%d: round-trip mismatch", size, depth)
			}
		}
	}
}

func BenchmarkInterleave(b *testing.B) {
	data := make([]byte, 300)
	rand.Read(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fecInterleave(data, 8)
	}
}

func BenchmarkDeinterleave(b *testing.B) {
	data := make([]byte, 300)
	rand.Read(data)
	interleaved := fecInterleave(data, 8)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fecDeinterleave(interleaved, 8)
	}
}
