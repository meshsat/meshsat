package compress

import (
	"encoding/binary"
	"math"
	"os"
	"testing"
)

// buildTestCodebook creates a small codebook binary for testing.
// 2 stages, K=4, dim=3
func buildTestCodebook() []byte {
	stages := 2
	k := 4
	dim := 3

	// Codebook vectors — simple known values
	vectors := [2][4][3]float32{
		// Stage 0
		{
			{1.0, 0.0, 0.0},
			{0.0, 1.0, 0.0},
			{0.0, 0.0, 1.0},
			{0.5, 0.5, 0.0},
		},
		// Stage 1 (refinement)
		{
			{0.1, 0.0, 0.0},
			{0.0, 0.1, 0.0},
			{0.0, 0.0, 0.1},
			{0.05, 0.05, 0.0},
		},
	}

	buf := make([]byte, 0, 10+stages*k*dim*4)
	buf = append(buf, "MSVQ"...)
	buf = append(buf, 1)                                           // version
	buf = append(buf, byte(stages))                                // stages
	buf = binary.LittleEndian.AppendUint16(buf, uint16(k))        // K
	buf = binary.LittleEndian.AppendUint16(buf, uint16(dim))      // dim

	for s := 0; s < stages; s++ {
		for e := 0; e < k; e++ {
			for d := 0; d < dim; d++ {
				b := make([]byte, 4)
				binary.LittleEndian.PutUint32(b, math.Float32bits(vectors[s][e][d]))
				buf = append(buf, b...)
			}
		}
	}
	return buf
}

// buildTestCorpusIndex creates a corpus index binary for testing.
func buildTestCorpusIndex(dim int) []byte {
	entries := []struct {
		text string
		emb  []float32
	}{
		{"north signal", []float32{1.0, 0.0, 0.0}},
		{"east signal", []float32{0.0, 1.0, 0.0}},
		{"up signal", []float32{0.0, 0.0, 1.0}},
		{"northeast signal", []float32{0.5, 0.5, 0.0}},
	}

	buf := make([]byte, 0, 256)
	buf = append(buf, "MCIX"...)
	buf = append(buf, 1) // version
	b4 := make([]byte, 4)
	binary.LittleEndian.PutUint32(b4, uint32(len(entries)))
	buf = append(buf, b4...)
	buf = binary.LittleEndian.AppendUint16(buf, uint16(dim))

	for _, e := range entries {
		textBytes := []byte(e.text)
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(textBytes)))
		buf = append(buf, textBytes...)
		for _, v := range e.emb {
			b := make([]byte, 4)
			binary.LittleEndian.PutUint32(b, math.Float32bits(v))
			buf = append(buf, b...)
		}
	}
	return buf
}

func TestLoadCodebook(t *testing.T) {
	data := buildTestCodebook()
	cb, err := LoadCodebook(data)
	if err != nil {
		t.Fatalf("LoadCodebook: %v", err)
	}

	if cb.Version != 1 {
		t.Errorf("expected version 1, got %d", cb.Version)
	}
	if cb.Stages != 2 {
		t.Errorf("expected 2 stages, got %d", cb.Stages)
	}
	if cb.K != 4 {
		t.Errorf("expected K=4, got %d", cb.K)
	}
	if cb.Dim != 3 {
		t.Errorf("expected dim=3, got %d", cb.Dim)
	}

	// Verify a known vector
	if cb.Vectors[0][0][0] != 1.0 {
		t.Errorf("expected vectors[0][0][0]=1.0, got %f", cb.Vectors[0][0][0])
	}
	if cb.Vectors[1][2][2] != 0.1 {
		t.Errorf("expected vectors[1][2][2]=0.1, got %f", cb.Vectors[1][2][2])
	}
}

func TestLoadCodebook_InvalidMagic(t *testing.T) {
	data := []byte("BADx\x01\x02\x04\x00\x03\x00")
	_, err := LoadCodebook(data)
	if err == nil {
		t.Fatal("expected error for invalid magic")
	}
}

func TestLoadCodebook_TooShort(t *testing.T) {
	_, err := LoadCodebook([]byte("MSVQ\x01"))
	if err == nil {
		t.Fatal("expected error for truncated data")
	}
}

func TestCorpusIndex(t *testing.T) {
	cbData := buildTestCodebook()
	cb, err := LoadCodebook(cbData)
	if err != nil {
		t.Fatalf("LoadCodebook: %v", err)
	}

	ciData := buildTestCorpusIndex(3)
	if err := cb.LoadCorpusIndex(ciData); err != nil {
		t.Fatalf("LoadCorpusIndex: %v", err)
	}

	if len(cb.Corpus) != 4 {
		t.Errorf("expected 4 corpus entries, got %d", len(cb.Corpus))
	}
	if cb.Corpus[0] != "north signal" {
		t.Errorf("expected first corpus entry 'north signal', got %q", cb.Corpus[0])
	}
}

func TestDecodeIndices(t *testing.T) {
	cbData := buildTestCodebook()
	cb, err := LoadCodebook(cbData)
	if err != nil {
		t.Fatalf("LoadCodebook: %v", err)
	}
	ciData := buildTestCorpusIndex(3)
	if err := cb.LoadCorpusIndex(ciData); err != nil {
		t.Fatalf("LoadCorpusIndex: %v", err)
	}

	// Wire format: 2 stages, version 1, indices [0, 0]
	// Stage 0, idx 0 = [1.0, 0.0, 0.0]
	// Stage 1, idx 0 = [0.1, 0.0, 0.0]
	// Reconstructed = [1.1, 0.0, 0.0] — nearest to "north signal" [1.0, 0.0, 0.0]
	wire := []byte{
		(2 << 4) | 1, // header: 2 stages, version 1
		0x00, 0x00,   // stage 0, index 0
		0x00, 0x00,   // stage 1, index 0
	}

	text, err := cb.DecodeIndices(wire)
	if err != nil {
		t.Fatalf("DecodeIndices: %v", err)
	}
	if text != "north signal" {
		t.Errorf("expected 'north signal', got %q", text)
	}
}

func TestDecodeIndices_Stage1Only(t *testing.T) {
	cbData := buildTestCodebook()
	cb, err := LoadCodebook(cbData)
	if err != nil {
		t.Fatalf("LoadCodebook: %v", err)
	}
	ciData := buildTestCorpusIndex(3)
	if err := cb.LoadCorpusIndex(ciData); err != nil {
		t.Fatalf("LoadCorpusIndex: %v", err)
	}

	// 1 stage, index 1 = [0.0, 1.0, 0.0] → nearest "east signal"
	wire := []byte{
		(1 << 4) | 1, // header: 1 stage, version 1
		0x01, 0x00,   // stage 0, index 1
	}

	text, err := cb.DecodeIndices(wire)
	if err != nil {
		t.Fatalf("DecodeIndices: %v", err)
	}
	if text != "east signal" {
		t.Errorf("expected 'east signal', got %q", text)
	}
}

func TestDecodeIndices_InvalidVersion(t *testing.T) {
	wire := []byte{(2 << 4) | 5} // version 5
	cb := &Codebook{Dim: 3, Stages: 2, K: 4}
	_, err := cb.DecodeIndices(wire)
	if err == nil {
		t.Fatal("expected error for invalid version")
	}
}

func TestDecodeIndices_TooShort(t *testing.T) {
	cb := &Codebook{Dim: 3, Stages: 2, K: 4}
	_, err := cb.DecodeIndices([]byte{})
	if err == nil {
		t.Fatal("expected error for empty wire")
	}
}

func TestDecodeIndices_NoCorpus(t *testing.T) {
	cbData := buildTestCodebook()
	cb, err := LoadCodebook(cbData)
	if err != nil {
		t.Fatalf("LoadCodebook: %v", err)
	}
	// No corpus loaded
	wire := []byte{(1 << 4) | 1, 0x00, 0x00}
	_, err = cb.DecodeIndices(wire)
	if err == nil {
		t.Fatal("expected error when no corpus loaded")
	}
}

func TestSuggestStages(t *testing.T) {
	tests := []struct {
		channel string
		want    int
	}{
		{"zigbee", 2},
		{"cellular", 3},
		{"astrocast", 3},
		{"mesh", 4},
		{"iridium", 6},
		{"webhook", 8},
		{"mqtt", 8},
		{"unknown", 8},
	}
	for _, tt := range tests {
		got := SuggestStages(tt.channel)
		if got != tt.want {
			t.Errorf("SuggestStages(%q) = %d, want %d", tt.channel, got, tt.want)
		}
	}
}

// TestCodebookRealFile loads the real trained codebook and corpus if available,
// verifying the full Go decode path works with production artifacts.
func TestCodebookRealFile(t *testing.T) {
	cbPath := "../../sidecar/msvqsc/models/codebook_v1.bin"
	ciPath := "../../sidecar/msvqsc/models/corpus_index.bin"

	cbData, err := os.ReadFile(cbPath)
	if err != nil {
		t.Skipf("real codebook not found at %s (run train.py first): %v", cbPath, err)
	}

	cb, err := LoadCodebook(cbData)
	if err != nil {
		t.Fatalf("LoadCodebook real file: %v", err)
	}
	if cb.Stages != 8 {
		t.Errorf("expected 8 stages, got %d", cb.Stages)
	}
	if cb.K != 1024 {
		t.Errorf("expected K=1024, got %d", cb.K)
	}
	if cb.Dim != 384 {
		t.Errorf("expected dim=384, got %d", cb.Dim)
	}

	ciData, err := os.ReadFile(ciPath)
	if err != nil {
		t.Fatalf("read corpus index: %v", err)
	}
	if err := cb.LoadCorpusIndex(ciData); err != nil {
		t.Fatalf("LoadCorpusIndex: %v", err)
	}
	if len(cb.Corpus) != 45 {
		t.Errorf("expected 45 corpus entries, got %d", len(cb.Corpus))
	}

	// Decode with index 0 at stage 0 — should return a valid corpus entry
	wire := []byte{(2 << 4) | 1, 0x00, 0x00, 0x00, 0x00}
	text, err := cb.DecodeIndices(wire)
	if err != nil {
		t.Fatalf("DecodeIndices: %v", err)
	}
	if text == "" {
		t.Error("expected non-empty decoded text")
	}
	t.Logf("Real codebook decode: %q", text)

	// Verify all stage counts work
	for _, stages := range []int{1, 2, 3, 4, 6, 8} {
		w := make([]byte, 1+stages*2)
		w[0] = byte((stages&0x0F)<<4 | 1)
		_, err := cb.DecodeIndices(w)
		if err != nil {
			t.Errorf("decode %d stages: %v", stages, err)
		}
	}
}

func TestWireSize(t *testing.T) {
	if WireSize(2) != 5 {
		t.Errorf("WireSize(2) = %d, want 5", WireSize(2))
	}
	if WireSize(6) != 13 {
		t.Errorf("WireSize(6) = %d, want 13", WireSize(6))
	}
	if WireSize(8) != 17 {
		t.Errorf("WireSize(8) = %d, want 17", WireSize(8))
	}
}
