package compress

import (
	"testing"
)

func TestSMAZ2_DefaultRoundTrip(t *testing.T) {
	tests := []string{
		"Hello, World!",
		"the quick brown fox jumps over the lazy dog",
		"this is a test message with some common words",
		"I need more information about your service",
	}
	for _, input := range tests {
		compressed := Compress([]byte(input), DictDefault)
		decompressed, err := Decompress(compressed, DictDefault)
		if err != nil {
			t.Fatalf("Decompress(%q) error: %v", input, err)
		}
		if string(decompressed) != input {
			t.Errorf("round-trip failed: got %q, want %q", decompressed, input)
		}
	}
}

func TestSMAZ2_MeshtasticRoundTrip(t *testing.T) {
	tests := []string{
		"battery level 85 percent",
		"signal strength good position latitude 52.3 longitude 4.9",
		"emergency rescue request medical assistance helicopter needed",
		"mesh node status connected relay gateway bridge active",
		"satellite iridium modem serial port device sensor online",
		"north heading 42 degrees speed 12 knots bearing 180",
		"channel frequency power antenna range coverage clear",
		"message received forwarded delivered success pending queued",
		"temperature 22 humidity 65 pressure 1013 weather clear",
		"acknowledge confirmed copy over roger affirmative",
	}
	for _, input := range tests {
		compressed := Compress([]byte(input), DictMeshtastic)
		decompressed, err := Decompress(compressed, DictMeshtastic)
		if err != nil {
			t.Fatalf("Decompress(%q) error: %v", input, err)
		}
		if string(decompressed) != input {
			t.Errorf("round-trip failed:\n  got  %q\n  want %q", decompressed, input)
		}
	}
}

func TestSMAZ2_CompressesWell(t *testing.T) {
	input := []byte("the quick brown fox jumps over the lazy dog and this is more text with common words")
	compressed := Compress(input, DictDefault)
	ratio := float64(len(compressed)) / float64(len(input))
	t.Logf("Default dict: %d -> %d bytes (%.1f%%)", len(input), len(compressed), ratio*100)
	if ratio > 0.85 {
		t.Errorf("compression ratio %.2f exceeds 85%% threshold", ratio)
	}
}

func TestSMAZ2_MeshtasticBetter(t *testing.T) {
	// Text rich in Meshtastic terms should compress better with meshtastic dict.
	input := []byte("battery level 85 percent signal strength position latitude longitude altitude heading speed satellite iridium gateway mesh node relay bridge network")

	compDefault := Compress(input, DictDefault)
	compMesh := Compress(input, DictMeshtastic)

	// Verify both round-trip correctly.
	decDefault, err := Decompress(compDefault, DictDefault)
	if err != nil {
		t.Fatalf("default decompress error: %v", err)
	}
	if string(decDefault) != string(input) {
		t.Fatalf("default round-trip failed")
	}

	decMesh, err := Decompress(compMesh, DictMeshtastic)
	if err != nil {
		t.Fatalf("meshtastic decompress error: %v", err)
	}
	if string(decMesh) != string(input) {
		t.Fatalf("meshtastic round-trip failed")
	}

	t.Logf("Input: %d bytes", len(input))
	t.Logf("Default dict: %d bytes (%.1f%%)", len(compDefault), float64(len(compDefault))/float64(len(input))*100)
	t.Logf("Meshtastic dict: %d bytes (%.1f%%)", len(compMesh), float64(len(compMesh))/float64(len(input))*100)

	if len(compMesh) >= len(compDefault) {
		t.Errorf("meshtastic dict (%d bytes) should compress better than default (%d bytes) for mesh text",
			len(compMesh), len(compDefault))
	}
}

func TestSMAZ2_EmptyInput(t *testing.T) {
	// Empty input should round-trip.
	compressed := Compress(nil, DictDefault)
	if compressed != nil {
		t.Errorf("Compress(nil) = %v, want nil", compressed)
	}
	decompressed, err := Decompress(nil, DictDefault)
	if err != nil {
		t.Errorf("Decompress(nil) error: %v", err)
	}
	if decompressed != nil {
		t.Errorf("Decompress(nil) = %v, want nil", decompressed)
	}

	compressed = Compress([]byte{}, DictDefault)
	if compressed != nil {
		t.Errorf("Compress([]) = %v, want nil", compressed)
	}

	// Same for meshtastic dict.
	compressed = Compress(nil, DictMeshtastic)
	if compressed != nil {
		t.Errorf("Compress(nil, Meshtastic) = %v, want nil", compressed)
	}
	decompressed, err = Decompress(nil, DictMeshtastic)
	if err != nil {
		t.Errorf("Decompress(nil, Meshtastic) error: %v", err)
	}
	if decompressed != nil {
		t.Errorf("Decompress(nil, Meshtastic) = %v, want nil", decompressed)
	}
}

func TestSMAZ2_BinaryData(t *testing.T) {
	// Binary data should round-trip (may expand but must not corrupt).
	input := make([]byte, 256)
	for i := range input {
		input[i] = byte(i)
	}

	compressed := Compress(input, DictMeshtastic)
	decompressed, err := Decompress(compressed, DictMeshtastic)
	if err != nil {
		t.Fatalf("Decompress binary data error: %v", err)
	}
	if len(decompressed) != len(input) {
		t.Fatalf("binary round-trip length: got %d, want %d", len(decompressed), len(input))
	}
	for i := range input {
		if decompressed[i] != input[i] {
			t.Errorf("binary round-trip mismatch at byte %d: got 0x%02x, want 0x%02x", i, decompressed[i], input[i])
		}
	}
}

func TestSMAZ2_MeshtasticWordBoundary(t *testing.T) {
	// Test that word matching respects boundaries (doesn't match partial words).
	tests := []struct {
		input string
	}{
		{"meshwork"}, // "mesh" is a word but "meshwork" is not — should still round-trip
		{"gateway2"},
		{"node123"},
		{"A"},
		{"ab"},
		{"abc"},
	}
	for _, tt := range tests {
		compressed := Compress([]byte(tt.input), DictMeshtastic)
		decompressed, err := Decompress(compressed, DictMeshtastic)
		if err != nil {
			t.Fatalf("Decompress(%q) error: %v", tt.input, err)
		}
		if string(decompressed) != tt.input {
			t.Errorf("round-trip failed for %q: got %q", tt.input, decompressed)
		}
	}
}

func TestSMAZ2_SingleChars(t *testing.T) {
	// Every single byte value should survive round-trip via meshtastic dict.
	for b := 0; b < 256; b++ {
		input := []byte{byte(b)}
		compressed := Compress(input, DictMeshtastic)
		decompressed, err := Decompress(compressed, DictMeshtastic)
		if err != nil {
			t.Fatalf("byte 0x%02x: decompress error: %v", b, err)
		}
		if len(decompressed) != 1 || decompressed[0] != byte(b) {
			t.Errorf("byte 0x%02x: round-trip failed: got %v", b, decompressed)
		}
	}
}

func BenchmarkSMAZ2_CompressDefault(b *testing.B) {
	data := []byte("Battery level 85 percent, signal strength good, position latitude 52.3 longitude 4.9, heading north")
	for i := 0; i < b.N; i++ {
		Compress(data, DictDefault)
	}
}

func BenchmarkSMAZ2_CompressMeshtastic(b *testing.B) {
	data := []byte("Battery level 85 percent, signal strength good, position latitude 52.3 longitude 4.9, heading north")
	for i := 0; i < b.N; i++ {
		Compress(data, DictMeshtastic)
	}
}

func BenchmarkSMAZ2_DecompressMeshtastic(b *testing.B) {
	data := []byte("Battery level 85 percent, signal strength good, position latitude 52.3 longitude 4.9, heading north")
	compressed := Compress(data, DictMeshtastic)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Decompress(compressed, DictMeshtastic)
	}
}
