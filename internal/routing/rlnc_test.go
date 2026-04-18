package routing

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"testing"
)

// --- GF(256) field axiom tests ---

func TestGF256_AddCommutativity(t *testing.T) {
	for a := 0; a < 256; a++ {
		for b := 0; b < 256; b++ {
			if gfAdd(byte(a), byte(b)) != gfAdd(byte(b), byte(a)) {
				t.Fatalf("add not commutative: %d + %d", a, b)
			}
		}
	}
}

func TestGF256_MulCommutativity(t *testing.T) {
	for a := 0; a < 256; a++ {
		for b := 0; b < 256; b++ {
			if gfMul(byte(a), byte(b)) != gfMul(byte(b), byte(a)) {
				t.Fatalf("mul not commutative: %d * %d", a, b)
			}
		}
	}
}

func TestGF256_Inverse(t *testing.T) {
	for a := 1; a < 256; a++ {
		inv := gfInv(byte(a))
		product := gfMul(byte(a), inv)
		if product != 1 {
			t.Fatalf("inverse failed: %d * inv(%d)=%d = %d, want 1", a, a, inv, product)
		}
	}
}

func TestGF256_MulZero(t *testing.T) {
	for a := 0; a < 256; a++ {
		if gfMul(byte(a), 0) != 0 {
			t.Fatalf("mul by zero failed for %d", a)
		}
		if gfMul(0, byte(a)) != 0 {
			t.Fatalf("zero mul failed for %d", a)
		}
	}
}

func TestGF256_AddSelfIsZero(t *testing.T) {
	// In GF(2^n), a + a = 0 for all a (since XOR with self is zero).
	for a := 0; a < 256; a++ {
		if gfAdd(byte(a), byte(a)) != 0 {
			t.Fatalf("a + a != 0 for a=%d", a)
		}
	}
}

func TestGF256_MulOne(t *testing.T) {
	for a := 0; a < 256; a++ {
		if gfMul(byte(a), 1) != byte(a) {
			t.Fatalf("mul by 1 failed for %d", a)
		}
	}
}

func TestGF256_MulAssociativity(t *testing.T) {
	// Spot-check associativity: (a*b)*c == a*(b*c)
	vals := []byte{0, 1, 2, 3, 127, 128, 254, 255}
	for _, a := range vals {
		for _, b := range vals {
			for _, c := range vals {
				if gfMul(gfMul(a, b), c) != gfMul(a, gfMul(b, c)) {
					t.Fatalf("associativity failed: (%d*%d)*%d != %d*(%d*%d)", a, b, c, a, b, c)
				}
			}
		}
	}
}

func TestGF256_Distributive(t *testing.T) {
	// a*(b+c) == a*b + a*c
	vals := []byte{0, 1, 2, 3, 42, 127, 200, 255}
	for _, a := range vals {
		for _, b := range vals {
			for _, c := range vals {
				lhs := gfMul(a, gfAdd(b, c))
				rhs := gfAdd(gfMul(a, b), gfMul(a, c))
				if lhs != rhs {
					t.Fatalf("distributive failed: %d*(%d+%d)=%d != %d*%d + %d*%d = %d",
						a, b, c, lhs, a, b, a, c, rhs)
				}
			}
		}
	}
}

// --- Gaussian elimination tests ---

func TestGaussianEliminate_Identity(t *testing.T) {
	k := 4
	payloadLen := 16
	coeffs := NewGFMatrix(k, k)
	payloads := make([][]byte, k)

	for i := 0; i < k; i++ {
		coeffs.Set(i, i, 1) // identity matrix
		payloads[i] = make([]byte, payloadLen)
		rand.Read(payloads[i])
	}

	// Save expected results before GaussianEliminate (which works on copies).
	expected := make([][]byte, k)
	for i := 0; i < k; i++ {
		expected[i] = make([]byte, payloadLen)
		copy(expected[i], payloads[i])
	}

	result, err := GaussianEliminate(coeffs, payloads)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != k {
		t.Fatalf("got %d results, want %d", len(result), k)
	}
	for i := 0; i < k; i++ {
		if !bytes.Equal(result[i], expected[i]) {
			t.Fatalf("payload %d mismatch", i)
		}
	}
}

func TestGaussianEliminate_Random(t *testing.T) {
	k := 5
	payloadLen := 32

	// Generate random original payloads.
	originals := make([][]byte, k)
	for i := 0; i < k; i++ {
		originals[i] = make([]byte, payloadLen)
		rand.Read(originals[i])
	}

	// Generate random K-by-K coefficient matrix and compute coded payloads.
	coeffs := NewGFMatrix(k, k)
	rand.Read(coeffs.Data)

	// Ensure the matrix is not singular by making diagonal non-zero.
	for i := 0; i < k; i++ {
		if coeffs.Get(i, i) == 0 {
			coeffs.Set(i, i, 1)
		}
	}

	codedPayloads := make([][]byte, k)
	for i := 0; i < k; i++ {
		codedPayloads[i] = make([]byte, payloadLen)
		for j := 0; j < k; j++ {
			c := coeffs.Get(i, j)
			if c == 0 {
				continue
			}
			for b := 0; b < payloadLen; b++ {
				codedPayloads[i][b] = gfAdd(codedPayloads[i][b], gfMul(c, originals[j][b]))
			}
		}
	}

	result, err := GaussianEliminate(coeffs, codedPayloads)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 0; i < k; i++ {
		if !bytes.Equal(result[i], originals[i]) {
			t.Fatalf("payload %d mismatch after decoding", i)
		}
	}
}

func TestGaussianEliminate_Overdetermined(t *testing.T) {
	k := 3
	extra := 2
	n := k + extra
	payloadLen := 20

	originals := make([][]byte, k)
	for i := 0; i < k; i++ {
		originals[i] = make([]byte, payloadLen)
		rand.Read(originals[i])
	}

	// Build N rows of random coefficients and their coded payloads.
	coeffs := NewGFMatrix(n, k)
	rand.Read(coeffs.Data)
	for i := 0; i < n; i++ {
		if coeffs.Get(i, i%k) == 0 {
			coeffs.Set(i, i%k, 1)
		}
	}

	codedPayloads := make([][]byte, n)
	for i := 0; i < n; i++ {
		codedPayloads[i] = make([]byte, payloadLen)
		for j := 0; j < k; j++ {
			c := coeffs.Get(i, j)
			if c == 0 {
				continue
			}
			for b := 0; b < payloadLen; b++ {
				codedPayloads[i][b] = gfAdd(codedPayloads[i][b], gfMul(c, originals[j][b]))
			}
		}
	}

	result, err := GaussianEliminate(coeffs, codedPayloads)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 0; i < k; i++ {
		if !bytes.Equal(result[i], originals[i]) {
			t.Fatalf("payload %d mismatch with overdetermined system", i)
		}
	}
}

func TestGaussianEliminate_RankDeficient(t *testing.T) {
	k := 3
	payloadLen := 10

	// Make row 1 = row 0 (linearly dependent).
	coeffs := NewGFMatrix(k, k)
	rand.Read(coeffs.Data[:k]) // fill row 0
	copy(coeffs.Data[k:2*k], coeffs.Data[:k])
	rand.Read(coeffs.Data[2*k:]) // row 2 is independent

	payloads := make([][]byte, k)
	for i := 0; i < k; i++ {
		payloads[i] = make([]byte, payloadLen)
		rand.Read(payloads[i])
	}
	// Make payload 1 = payload 0 to match the duplicate coefficient row.
	copy(payloads[1], payloads[0])

	_, err := GaussianEliminate(coeffs, payloads)
	if err == nil {
		t.Fatal("expected error for rank-deficient matrix, got nil")
	}
}

// --- RLNC coded packet marshal/unmarshal tests ---

func TestRLNCPacket_MarshalRoundtrip(t *testing.T) {
	pkt := &RLNCCodedPacket{
		Version:      RLNCVersion,
		GenerationID: 42,
		K:            4,
		Coefficients: []byte{0x12, 0x34, 0x56, 0x78},
		Payload:      []byte("hello RLNC world"),
	}
	rand.Read(pkt.ResourceHash[:])

	data := MarshalRLNCPacket(pkt)
	pkt2, err := UnmarshalRLNCPacket(data)
	if err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if pkt2.ResourceHash != pkt.ResourceHash {
		t.Fatal("resource hash mismatch")
	}
	if pkt2.Version != pkt.Version {
		t.Fatal("version mismatch")
	}
	if pkt2.GenerationID != pkt.GenerationID {
		t.Fatalf("generation ID mismatch: got %d, want %d", pkt2.GenerationID, pkt.GenerationID)
	}
	if pkt2.K != pkt.K {
		t.Fatalf("K mismatch: got %d, want %d", pkt2.K, pkt.K)
	}
	if !bytes.Equal(pkt2.Coefficients, pkt.Coefficients) {
		t.Fatal("coefficients mismatch")
	}
	if !bytes.Equal(pkt2.Payload, pkt.Payload) {
		t.Fatal("payload mismatch")
	}
}

func TestRLNCPacket_UnmarshalTooShort(t *testing.T) {
	_, err := UnmarshalRLNCPacket([]byte{0x01, 0x02})
	if err == nil {
		t.Fatal("expected error for short packet")
	}
}

func TestRLNCPacket_UnmarshalBadVersion(t *testing.T) {
	pkt := &RLNCCodedPacket{
		Version:      RLNCVersion,
		GenerationID: 1,
		K:            1,
		Coefficients: []byte{0x01},
		Payload:      []byte{0x42},
	}
	data := MarshalRLNCPacket(pkt)
	data[32] = 0xFF // corrupt version byte
	_, err := UnmarshalRLNCPacket(data)
	if err == nil {
		t.Fatal("expected error for bad version")
	}
}

func TestRLNCPacket_UnmarshalZeroK(t *testing.T) {
	pkt := &RLNCCodedPacket{
		Version:      RLNCVersion,
		GenerationID: 1,
		K:            1,
		Coefficients: []byte{0x01},
		Payload:      []byte{0x42},
	}
	data := MarshalRLNCPacket(pkt)
	data[35] = 0 // set K to 0
	_, err := UnmarshalRLNCPacket(data)
	if err == nil {
		t.Fatal("expected error for K=0")
	}
}

// --- RLNC encode/decode generation tests ---

func TestRLNCEncodeGeneration(t *testing.T) {
	k := 4
	segSize := 64
	segments := make([][]byte, k)
	for i := 0; i < k; i++ {
		segments[i] = make([]byte, segSize)
		rand.Read(segments[i])
	}

	hash := sha256.Sum256([]byte("test resource"))
	redundancy := 1.5
	packets := EncodeGeneration(1, hash, segments, redundancy)

	expectedN := 6 // ceil(4 * 1.5) = 6
	if len(packets) != expectedN {
		t.Fatalf("got %d packets, want %d", len(packets), expectedN)
	}

	for i, pkt := range packets {
		if pkt.ResourceHash != hash {
			t.Fatalf("packet %d: wrong hash", i)
		}
		if pkt.Version != RLNCVersion {
			t.Fatalf("packet %d: wrong version", i)
		}
		if pkt.GenerationID != 1 {
			t.Fatalf("packet %d: wrong gen ID", i)
		}
		if pkt.K != byte(k) {
			t.Fatalf("packet %d: K=%d, want %d", i, pkt.K, k)
		}
		if len(pkt.Coefficients) != k {
			t.Fatalf("packet %d: got %d coefficients, want %d", i, len(pkt.Coefficients), k)
		}
		if len(pkt.Payload) != segSize {
			t.Fatalf("packet %d: payload len %d, want %d", i, len(pkt.Payload), segSize)
		}
	}
}

func TestRLNCDecodeGeneration_ExactK(t *testing.T) {
	k := 4
	segSize := 48
	segments := make([][]byte, k)
	for i := 0; i < k; i++ {
		segments[i] = make([]byte, segSize)
		rand.Read(segments[i])
	}

	hash := sha256.Sum256([]byte("test"))
	// Use redundancy 1.0 to get exactly K packets.
	packets := EncodeGeneration(0, hash, segments, 1.0)
	if len(packets) != k {
		t.Fatalf("expected %d packets, got %d", k, len(packets))
	}

	gen := NewRLNCGeneration(0, k, segSize)
	for _, pkt := range packets {
		gen.AddPacket(pkt)
	}

	decoded, err := gen.TryDecode()
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(decoded) != k {
		t.Fatalf("got %d decoded segments, want %d", len(decoded), k)
	}
	for i := 0; i < k; i++ {
		if !bytes.Equal(decoded[i], segments[i]) {
			t.Fatalf("segment %d mismatch after decode", i)
		}
	}
}

func TestRLNCDecodeGeneration_MoreThanK(t *testing.T) {
	k := 4
	segSize := 32
	segments := make([][]byte, k)
	for i := 0; i < k; i++ {
		segments[i] = make([]byte, segSize)
		rand.Read(segments[i])
	}

	hash := sha256.Sum256([]byte("overdetermined"))
	packets := EncodeGeneration(5, hash, segments, 1.5) // 6 packets for K=4

	gen := NewRLNCGeneration(5, k, segSize)
	for _, pkt := range packets {
		gen.AddPacket(pkt)
	}

	decoded, err := gen.TryDecode()
	if err != nil {
		t.Fatalf("decode error with overdetermined system: %v", err)
	}
	for i := 0; i < k; i++ {
		if !bytes.Equal(decoded[i], segments[i]) {
			t.Fatalf("segment %d mismatch with overdetermined decode", i)
		}
	}
}

func TestRLNCDecodeGeneration_LessThanK(t *testing.T) {
	k := 4
	segSize := 16
	segments := make([][]byte, k)
	for i := 0; i < k; i++ {
		segments[i] = make([]byte, segSize)
		rand.Read(segments[i])
	}

	hash := sha256.Sum256([]byte("insufficient"))
	packets := EncodeGeneration(0, hash, segments, 1.0)

	gen := NewRLNCGeneration(0, k, segSize)
	// Only add K-1 packets.
	for i := 0; i < k-1; i++ {
		ready := gen.AddPacket(packets[i])
		if ready {
			t.Fatal("should not be decodable with fewer than K packets")
		}
	}

	_, err := gen.TryDecode()
	if err == nil {
		t.Fatal("expected error with K-1 packets, got nil")
	}
}

func TestRLNCE2E_WithLoss(t *testing.T) {
	k := 8
	segSize := 100
	redundancy := 1.3

	segments := make([][]byte, k)
	for i := 0; i < k; i++ {
		segments[i] = make([]byte, segSize)
		rand.Read(segments[i])
	}

	hash := sha256.Sum256([]byte("lossy channel"))
	packets := EncodeGeneration(99, hash, segments, redundancy)
	n := len(packets)
	t.Logf("generated %d coded packets for K=%d (redundancy=%.1f)", n, k, redundancy)

	// Drop ~20% of packets. For K=8, redundancy=1.3 gives ceil(10.4)=11 packets.
	// Dropping 2 of 11 leaves 9 >= K=8, which should be decodable.
	dropCount := n / 5 // 20% drop
	if dropCount == 0 {
		dropCount = 1
	}
	surviving := packets[dropCount:] // drop the first dropCount packets
	t.Logf("after dropping %d packets, %d surviving (need %d)", dropCount, len(surviving), k)

	if len(surviving) < k {
		t.Skipf("not enough surviving packets: %d < %d (increase redundancy)", len(surviving), k)
	}

	gen := NewRLNCGeneration(99, k, segSize)
	for _, pkt := range surviving {
		gen.AddPacket(pkt)
	}

	decoded, err := gen.TryDecode()
	if err != nil {
		t.Fatalf("decode failed after packet loss: %v", err)
	}
	for i := 0; i < k; i++ {
		if !bytes.Equal(decoded[i], segments[i]) {
			t.Fatalf("segment %d mismatch after lossy decode", i)
		}
	}
}

func TestRLNCDecodeGeneration_AlreadyDecoded(t *testing.T) {
	k := 2
	segSize := 8
	segments := make([][]byte, k)
	for i := 0; i < k; i++ {
		segments[i] = make([]byte, segSize)
		rand.Read(segments[i])
	}

	hash := sha256.Sum256([]byte("idempotent"))
	packets := EncodeGeneration(0, hash, segments, 1.0)

	gen := NewRLNCGeneration(0, k, segSize)
	for _, pkt := range packets {
		gen.AddPacket(pkt)
	}

	// Decode once.
	decoded1, err := gen.TryDecode()
	if err != nil {
		t.Fatalf("first decode failed: %v", err)
	}

	// Calling TryDecode again should return the same result.
	decoded2, err := gen.TryDecode()
	if err != nil {
		t.Fatalf("second decode failed: %v", err)
	}
	for i := 0; i < k; i++ {
		if !bytes.Equal(decoded1[i], decoded2[i]) {
			t.Fatalf("segment %d differs between decode calls", i)
		}
	}

	// AddPacket after decode should return true (already decoded).
	if !gen.AddPacket(packets[0]) {
		t.Fatal("AddPacket should return true when already decoded")
	}
}

func TestRLNCEncodeGeneration_UnequalSegments(t *testing.T) {
	// Segments of different lengths should be zero-padded to the max.
	segments := [][]byte{
		{0x01, 0x02},
		{0x03, 0x04, 0x05, 0x06},
		{0x07},
	}

	hash := sha256.Sum256([]byte("unequal"))
	// Use redundancy 2.0 so the encoder produces 2k=6 packets instead
	// of exactly k=3. At redundancy=1.0 there is zero margin — a single
	// linearly-dependent coefficient draw (column has no pivot) gives a
	// spurious "rank deficient" decode failure. Observed flaky in CI
	// pipeline #22397 (TestRLNCEncodeGeneration_UnequalSegments, column
	// 2 has no pivot). With 2k packets the probability of not finding k
	// independent rows over GF(256) is negligible. [MESHSAT-551]
	packets := EncodeGeneration(0, hash, segments, 2.0)

	// All payloads should have length 4 (max segment length).
	for i, pkt := range packets {
		if len(pkt.Payload) != 4 {
			t.Fatalf("packet %d: payload len %d, want 4", i, len(pkt.Payload))
		}
	}

	gen := NewRLNCGeneration(0, 3, 4)
	for _, pkt := range packets {
		gen.AddPacket(pkt)
	}

	decoded, err := gen.TryDecode()
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}

	// The decoded segments should be the originals, zero-padded.
	expected := [][]byte{
		{0x01, 0x02, 0x00, 0x00},
		{0x03, 0x04, 0x05, 0x06},
		{0x07, 0x00, 0x00, 0x00},
	}
	for i, exp := range expected {
		if !bytes.Equal(decoded[i], exp) {
			t.Fatalf("segment %d: got %v, want %v", i, decoded[i], exp)
		}
	}
}

func TestRLNCEncodeGeneration_Empty(t *testing.T) {
	hash := sha256.Sum256([]byte("empty"))
	packets := EncodeGeneration(0, hash, nil, 1.0)
	if packets != nil {
		t.Fatalf("expected nil for empty segments, got %d packets", len(packets))
	}
}

func TestRLNCEncodeGeneration_RedundancyBelowOne(t *testing.T) {
	k := 3
	segSize := 16
	segments := make([][]byte, k)
	for i := 0; i < k; i++ {
		segments[i] = make([]byte, segSize)
		rand.Read(segments[i])
	}

	hash := sha256.Sum256([]byte("low redundancy"))
	// Redundancy < 1.0 should be clamped to 1.0 (at least K packets).
	packets := EncodeGeneration(0, hash, segments, 0.5)
	if len(packets) < k {
		t.Fatalf("got %d packets for redundancy=0.5, want at least K=%d", len(packets), k)
	}
}
