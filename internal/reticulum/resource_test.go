package reticulum

import (
	"bytes"
	"testing"
)

func TestResourceAdv_MarshalUnmarshal(t *testing.T) {
	adv := &ResourceAdvertisement{
		TotalSize:    12345,
		SegmentSize:  180,
		SegmentCount: 69,
	}
	for i := range adv.ResourceHash {
		adv.ResourceHash[i] = byte(i)
	}

	data := MarshalResourceAdv(adv)
	got, err := UnmarshalResourceAdv(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ResourceHash != adv.ResourceHash {
		t.Fatal("ResourceHash mismatch")
	}
	if got.TotalSize != 12345 {
		t.Fatalf("TotalSize = %d, want 12345", got.TotalSize)
	}
	if got.SegmentSize != 180 {
		t.Fatalf("SegmentSize = %d, want 180", got.SegmentSize)
	}
	if got.SegmentCount != 69 {
		t.Fatalf("SegmentCount = %d, want 69", got.SegmentCount)
	}
}

func TestResourceReq_MarshalUnmarshal(t *testing.T) {
	req := &ResourceRequest{
		Bitmap: []byte{0xFF, 0x0F, 0x01},
	}
	for i := range req.ResourceHash {
		req.ResourceHash[i] = byte(i + 0x20)
	}

	data := MarshalResourceReq(req)
	got, err := UnmarshalResourceReq(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ResourceHash != req.ResourceHash {
		t.Fatal("ResourceHash mismatch")
	}
	if !bytes.Equal(got.Bitmap, req.Bitmap) {
		t.Fatalf("Bitmap = %x, want %x", got.Bitmap, req.Bitmap)
	}
}

func TestResourceSegment_MarshalUnmarshal(t *testing.T) {
	seg := &ResourceSegment{
		SegmentIndex: 42,
		Data:         []byte("hello resource segment"),
	}
	for i := range seg.ResourceHash {
		seg.ResourceHash[i] = byte(i + 0x40)
	}

	data := MarshalResourceSegment(seg)
	got, err := UnmarshalResourceSegment(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ResourceHash != seg.ResourceHash {
		t.Fatal("ResourceHash mismatch")
	}
	if got.SegmentIndex != 42 {
		t.Fatalf("SegmentIndex = %d, want 42", got.SegmentIndex)
	}
	if !bytes.Equal(got.Data, seg.Data) {
		t.Fatal("Data mismatch")
	}
}

func TestResourceProof_MarshalUnmarshal(t *testing.T) {
	prf := &ResourceProof{}
	for i := range prf.ResourceHash {
		prf.ResourceHash[i] = byte(i + 0x60)
	}

	data := MarshalResourceProof(prf)
	got, err := UnmarshalResourceProof(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ResourceHash != prf.ResourceHash {
		t.Fatal("ResourceHash mismatch")
	}
}

func TestComputeSegmentCount(t *testing.T) {
	tests := []struct {
		total, seg, want int
	}{
		{0, 180, 0},
		{100, 180, 1},
		{180, 180, 1},
		{181, 180, 2},
		{360, 180, 2},
		{361, 180, 3},
		{1000, 180, 6},
	}
	for _, tt := range tests {
		got := ComputeSegmentCount(tt.total, tt.seg)
		if got != tt.want {
			t.Errorf("ComputeSegmentCount(%d, %d) = %d, want %d", tt.total, tt.seg, got, tt.want)
		}
	}
}

func TestBitmap(t *testing.T) {
	// 10 segments = 2 bytes bitmap
	bm := NewBitmap(10)
	if len(bm) != 2 {
		t.Fatalf("bitmap len = %d, want 2", len(bm))
	}

	// All bits set initially
	for i := 0; i < 10; i++ {
		if !BitmapGet(bm, i) {
			t.Fatalf("bit %d should be set", i)
		}
	}
	// Bits 10-15 should not be set (unused high bits cleared)
	for i := 10; i < 16; i++ {
		if BitmapGet(bm, i) {
			t.Fatalf("bit %d should not be set (unused)", i)
		}
	}

	// Clear some bits
	BitmapClear(bm, 0)
	BitmapClear(bm, 5)
	BitmapClear(bm, 9)

	if BitmapGet(bm, 0) {
		t.Fatal("bit 0 should be cleared")
	}
	if BitmapGet(bm, 5) {
		t.Fatal("bit 5 should be cleared")
	}
	if !BitmapGet(bm, 1) {
		t.Fatal("bit 1 should still be set")
	}

	if BitmapAllClear(bm) {
		t.Fatal("not all cleared yet")
	}

	// Clear all
	for i := 0; i < 10; i++ {
		BitmapClear(bm, i)
	}
	if !BitmapAllClear(bm) {
		t.Fatal("all should be cleared")
	}
}

func TestBitmap_Exact8(t *testing.T) {
	bm := NewBitmap(8)
	if len(bm) != 1 {
		t.Fatalf("bitmap len = %d, want 1", len(bm))
	}
	if bm[0] != 0xFF {
		t.Fatalf("byte = 0x%02x, want 0xFF", bm[0])
	}
}

func TestBitmap_Single(t *testing.T) {
	bm := NewBitmap(1)
	if len(bm) != 1 {
		t.Fatalf("bitmap len = %d, want 1", len(bm))
	}
	if bm[0] != 0x01 {
		t.Fatalf("byte = 0x%02x, want 0x01", bm[0])
	}
}

func TestComputeResourceHash(t *testing.T) {
	data := []byte("test resource data")
	hash := ComputeResourceHash(data)
	if hash == [FullHashLen]byte{} {
		t.Fatal("hash should not be zero")
	}
	// Same data should produce same hash
	hash2 := ComputeResourceHash(data)
	if hash != hash2 {
		t.Fatal("hash should be deterministic")
	}
}
