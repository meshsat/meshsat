package engine

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"testing"
)

func TestFECRoundTrip(t *testing.T) {
	data := []byte("Hello, FEC! This is a test payload for Reed-Solomon encoding.")
	encoded, err := fecEncode(data, 4, 2)
	if err != nil {
		t.Fatalf("fecEncode: %v", err)
	}
	if len(encoded) <= len(data) {
		t.Fatalf("encoded should be larger than original: %d vs %d", len(encoded), len(data))
	}

	decoded, err := fecDecode(encoded)
	if err != nil {
		t.Fatalf("fecDecode: %v", err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatalf("roundtrip mismatch: got %q, want %q", decoded, data)
	}
}

func TestFECSmallData(t *testing.T) {
	// Data smaller than a single shard.
	data := []byte("Hi")
	encoded, err := fecEncode(data, 4, 2)
	if err != nil {
		t.Fatalf("fecEncode: %v", err)
	}
	decoded, err := fecDecode(encoded)
	if err != nil {
		t.Fatalf("fecDecode: %v", err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatalf("mismatch: got %q, want %q", decoded, data)
	}
}

func TestFECEmptyData(t *testing.T) {
	data := []byte{}
	encoded, err := fecEncode(data, 4, 2)
	if err != nil {
		t.Fatalf("fecEncode: %v", err)
	}
	decoded, err := fecDecode(encoded)
	if err != nil {
		t.Fatalf("fecDecode: %v", err)
	}
	if len(decoded) != 0 {
		t.Fatalf("expected empty, got %d bytes", len(decoded))
	}
}

func TestFECLargeData(t *testing.T) {
	data := make([]byte, 10000)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	encoded, err := fecEncode(data, 8, 2)
	if err != nil {
		t.Fatalf("fecEncode: %v", err)
	}
	decoded, err := fecDecode(encoded)
	if err != nil {
		t.Fatalf("fecDecode: %v", err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("large data roundtrip mismatch")
	}
}

func TestFECWithErasures(t *testing.T) {
	data := []byte("Test data for erasure recovery with Reed-Solomon FEC coding.")
	encoded, err := fecEncode(data, 4, 2)
	if err != nil {
		t.Fatalf("fecEncode: %v", err)
	}

	// Erase 1 shard (within parity capacity of 2).
	decoded, recovered, err := fecDecodeWithErasures(encoded, []int{1})
	if err != nil {
		t.Fatalf("fecDecodeWithErasures (1 erasure): %v", err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("1-erasure recovery mismatch")
	}
	if recovered != 1 {
		t.Fatalf("expected 1 recovered, got %d", recovered)
	}

	// Erase 2 shards (max parity capacity).
	decoded, recovered, err = fecDecodeWithErasures(encoded, []int{0, 3})
	if err != nil {
		t.Fatalf("fecDecodeWithErasures (2 erasures): %v", err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("2-erasure recovery mismatch")
	}
	if recovered != 2 {
		t.Fatalf("expected 2 recovered, got %d", recovered)
	}
}

func TestFECTooManyErasures(t *testing.T) {
	data := []byte("Test data for too many erasures.")
	encoded, err := fecEncode(data, 4, 2)
	if err != nil {
		t.Fatalf("fecEncode: %v", err)
	}

	// Erase 3 shards (exceeds parity capacity of 2).
	_, _, err = fecDecodeWithErasures(encoded, []int{0, 1, 2})
	if err == nil {
		t.Fatal("expected error for 3 erasures with m=2, got nil")
	}
}

func TestFECInvalidHeader(t *testing.T) {
	// Too short.
	_, err := fecDecode([]byte{0x02, 0x04})
	if err == nil {
		t.Fatal("expected error for truncated header")
	}

	// Wrong version.
	_, err = fecDecode([]byte{0xFF, 0x04, 0x02, 0x00, 0x10, 0x00})
	if err == nil {
		t.Fatal("expected error for wrong version")
	}

	// Zero shards.
	_, err = fecDecode([]byte{0x02, 0x00, 0x02, 0x00, 0x10, 0x00})
	if err == nil {
		t.Fatal("expected error for zero data shards")
	}

	// Payload too short for declared shards.
	_, err = fecDecode([]byte{0x02, 0x04, 0x02, 0x10, 0x00, 0x00})
	if err == nil {
		t.Fatal("expected error for payload too short")
	}
}

func TestFECHeaderSelfDescribing(t *testing.T) {
	// Encode with one set of parameters, decode should auto-detect.
	data := []byte("Self-describing FEC test — params from header only.")

	for _, tc := range []struct {
		k, m int
	}{
		{2, 1},
		{4, 2},
		{8, 4},
		{16, 4},
	} {
		encoded, err := fecEncode(data, tc.k, tc.m)
		if err != nil {
			t.Fatalf("fecEncode k=%d m=%d: %v", tc.k, tc.m, err)
		}

		// Decode reads k,m from header — no params passed.
		decoded, err := fecDecode(encoded)
		if err != nil {
			t.Fatalf("fecDecode k=%d m=%d: %v", tc.k, tc.m, err)
		}
		if !bytes.Equal(decoded, data) {
			t.Fatalf("k=%d m=%d: roundtrip mismatch", tc.k, tc.m)
		}
	}
}

func TestFECPipelineIntegration(t *testing.T) {
	tp := NewTransformPipeline()

	// Build a transform chain: fec -> base64.
	transforms := []TransformSpec{
		{Type: "fec", Params: map[string]string{"data_shards": "4", "parity_shards": "2"}},
		{Type: "base64"},
	}
	jsonBytes, _ := json.Marshal(transforms)
	jsonStr := string(jsonBytes)

	data := []byte("Pipeline integration test with FEC and base64 encoding.")

	// Egress: fec -> base64.
	encoded, err := tp.ApplyEgress(data, jsonStr)
	if err != nil {
		t.Fatalf("ApplyEgress: %v", err)
	}

	// Ingress: base64 -> fec (reversed).
	decoded, err := tp.ApplyIngress(encoded, jsonStr)
	if err != nil {
		t.Fatalf("ApplyIngress: %v", err)
	}

	if !bytes.Equal(decoded, data) {
		t.Fatalf("pipeline roundtrip mismatch: got %q, want %q", decoded, data)
	}

	// Verify metrics.
	if tp.fecMetrics.EncodeOK.Load() != 1 {
		t.Fatalf("expected 1 encode OK, got %d", tp.fecMetrics.EncodeOK.Load())
	}
	if tp.fecMetrics.DecodeOK.Load() != 1 {
		t.Fatalf("expected 1 decode OK, got %d", tp.fecMetrics.DecodeOK.Load())
	}
}

func TestFECValidateTransforms(t *testing.T) {
	// FEC on a constrained channel (LoRa, 230B max).
	transforms := `[{"type":"fec","params":{"data_shards":"4","parity_shards":"2"}},{"type":"base64"}]`
	warnings, errors := ValidateTransforms(transforms, true, 230)
	if len(errors) > 0 {
		t.Fatalf("unexpected errors: %v", errors)
	}
	// FEC + base64 reduces capacity significantly on 230B; expect a warning.
	t.Logf("warnings: %v", warnings)

	// FEC without base64 on text-only transport should error.
	transforms = `[{"type":"fec","params":{"data_shards":"4","parity_shards":"2"}}]`
	_, errors = ValidateTransforms(transforms, false, 340)
	if len(errors) == 0 {
		t.Fatal("expected error for FEC without base64 on text-only transport")
	}
}

func TestParseIntParam(t *testing.T) {
	params := map[string]string{
		"data_shards":   "8",
		"parity_shards": "3",
		"empty":         "",
	}

	if v := parseIntParam(params, "data_shards", 4); v != 8 {
		t.Fatalf("expected 8, got %d", v)
	}
	if v := parseIntParam(params, "parity_shards", 2); v != 3 {
		t.Fatalf("expected 3, got %d", v)
	}
	if v := parseIntParam(params, "missing", 99); v != 99 {
		t.Fatalf("expected 99, got %d", v)
	}
	if v := parseIntParam(params, "empty", 42); v != 42 {
		t.Fatalf("expected 42, got %d", v)
	}
}

func BenchmarkFECEncode(b *testing.B) {
	data := make([]byte, 300) // typical satellite payload
	rand.Read(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fecEncode(data, 4, 2)
	}
}

func BenchmarkFECDecode(b *testing.B) {
	data := make([]byte, 300)
	rand.Read(data)
	encoded, _ := fecEncode(data, 4, 2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fecDecode(encoded)
	}
}
