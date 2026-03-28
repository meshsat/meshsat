package engine

import (
	"bytes"
	"crypto/rand"
	"testing"
	"time"
)

func TestBundleHeaderMarshalRoundtrip(t *testing.T) {
	hdr := &BundleHeader{
		Version:   BundleVersion,
		FragIndex: 3,
		FragTotal: 10,
		TotalSize: 123456,
	}
	if _, err := rand.Read(hdr.BundleID[:]); err != nil {
		t.Fatal(err)
	}

	data := MarshalBundleHeader(hdr)
	if len(data) != BundleHeaderLen {
		t.Fatalf("expected %d bytes, got %d", BundleHeaderLen, len(data))
	}

	got, err := UnmarshalBundleHeader(data)
	if err != nil {
		t.Fatalf("UnmarshalBundleHeader: %v", err)
	}
	if got.Version != hdr.Version {
		t.Fatalf("Version mismatch: got %d, want %d", got.Version, hdr.Version)
	}
	if got.BundleID != hdr.BundleID {
		t.Fatal("BundleID mismatch")
	}
	if got.FragIndex != hdr.FragIndex {
		t.Fatalf("FragIndex mismatch: got %d, want %d", got.FragIndex, hdr.FragIndex)
	}
	if got.FragTotal != hdr.FragTotal {
		t.Fatalf("FragTotal mismatch: got %d, want %d", got.FragTotal, hdr.FragTotal)
	}
	if got.TotalSize != hdr.TotalSize {
		t.Fatalf("TotalSize mismatch: got %d, want %d", got.TotalSize, hdr.TotalSize)
	}
}

func TestBundleHeaderTooShort(t *testing.T) {
	_, err := UnmarshalBundleHeader([]byte{0x01, 0x00})
	if err == nil {
		t.Fatal("expected error for too-short data")
	}
}

func TestBundleHeaderWrongVersion(t *testing.T) {
	data := make([]byte, BundleHeaderLen)
	data[0] = 0xFF // wrong version
	_, err := UnmarshalBundleHeader(data)
	if err == nil {
		t.Fatal("expected error for wrong version byte")
	}
}

func TestBundleHeaderZeroFragTotal(t *testing.T) {
	data := make([]byte, BundleHeaderLen)
	data[0] = BundleVersion
	// FragIndex=0, FragTotal=0 (bytes 17-20 are all zero)
	_, err := UnmarshalBundleHeader(data)
	if err == nil {
		t.Fatal("expected error for zero frag_total")
	}
}

func TestBundleHeaderFragIndexOutOfRange(t *testing.T) {
	hdr := &BundleHeader{
		Version:   BundleVersion,
		FragIndex: 5,
		FragTotal: 3, // index >= total
		TotalSize: 100,
	}
	data := MarshalBundleHeader(hdr)
	_, err := UnmarshalBundleHeader(data)
	if err == nil {
		t.Fatal("expected error for frag_index >= frag_total")
	}
}

func TestFragmentSingleFragment(t *testing.T) {
	payload := []byte("short message")
	mtu := 500 // much larger than payload + header

	bundleID, frags, err := Fragment(payload, mtu)
	if err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	if len(frags) != 1 {
		t.Fatalf("expected 1 fragment, got %d", len(frags))
	}

	// Verify header
	hdr, err := UnmarshalBundleHeader(frags[0])
	if err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if hdr.BundleID != bundleID {
		t.Fatal("BundleID mismatch")
	}
	if hdr.FragIndex != 0 {
		t.Fatalf("expected FragIndex 0, got %d", hdr.FragIndex)
	}
	if hdr.FragTotal != 1 {
		t.Fatalf("expected FragTotal 1, got %d", hdr.FragTotal)
	}
	if hdr.TotalSize != uint32(len(payload)) {
		t.Fatalf("TotalSize mismatch: got %d, want %d", hdr.TotalSize, len(payload))
	}

	// Verify payload recovery
	fragPayload := frags[0][BundleHeaderLen:]
	if !bytes.Equal(fragPayload, payload) {
		t.Fatalf("payload mismatch: got %q, want %q", fragPayload, payload)
	}
}

func TestFragmentMultipleFragments(t *testing.T) {
	payload := make([]byte, 1000)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	mtu := 100 // header=25, so 75 bytes per fragment payload
	bundleID, frags, err := Fragment(payload, mtu)
	if err != nil {
		t.Fatalf("Fragment: %v", err)
	}

	maxPayload := mtu - BundleHeaderLen
	expectedFrags := (len(payload) + maxPayload - 1) / maxPayload
	if len(frags) != expectedFrags {
		t.Fatalf("expected %d fragments, got %d", expectedFrags, len(frags))
	}

	// All fragments should reference the same bundle ID
	for i, frag := range frags {
		hdr, err := UnmarshalBundleHeader(frag)
		if err != nil {
			t.Fatalf("fragment %d unmarshal: %v", i, err)
		}
		if hdr.BundleID != bundleID {
			t.Fatalf("fragment %d has wrong BundleID", i)
		}
		if hdr.FragIndex != uint16(i) {
			t.Fatalf("fragment %d has FragIndex %d", i, hdr.FragIndex)
		}
		if hdr.FragTotal != uint16(expectedFrags) {
			t.Fatalf("fragment %d has FragTotal %d, want %d", i, hdr.FragTotal, expectedFrags)
		}
		if hdr.TotalSize != uint32(len(payload)) {
			t.Fatalf("fragment %d has TotalSize %d", i, hdr.TotalSize)
		}
		// No fragment should exceed MTU
		if len(frag) > mtu {
			t.Fatalf("fragment %d is %d bytes (exceeds MTU %d)", i, len(frag), mtu)
		}
	}

	// Concatenate fragment payloads and verify
	var reassembled []byte
	for _, frag := range frags {
		reassembled = append(reassembled, frag[BundleHeaderLen:]...)
	}
	if !bytes.Equal(reassembled, payload) {
		t.Fatal("concatenated fragment payloads do not match original")
	}
}

func TestFragmentEmptyPayload(t *testing.T) {
	_, frags, err := Fragment([]byte{}, 100)
	if err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	if len(frags) != 1 {
		t.Fatalf("expected 1 fragment for empty payload, got %d", len(frags))
	}
	hdr, err := UnmarshalBundleHeader(frags[0])
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if hdr.TotalSize != 0 {
		t.Fatalf("expected TotalSize 0, got %d", hdr.TotalSize)
	}
	if hdr.FragTotal != 1 {
		t.Fatalf("expected FragTotal 1, got %d", hdr.FragTotal)
	}
}

func TestFragmentMTUTooSmall(t *testing.T) {
	_, _, err := Fragment([]byte("test"), BundleHeaderLen)
	if err == nil {
		t.Fatal("expected error for MTU <= header length")
	}
}

func TestFragmentExactMTU(t *testing.T) {
	// Payload exactly fills one fragment's capacity
	maxPayload := 75 // MTU 100 - header 25
	payload := make([]byte, maxPayload)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	_, frags, err := Fragment(payload, 100)
	if err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	if len(frags) != 1 {
		t.Fatalf("expected 1 fragment, got %d", len(frags))
	}
	if len(frags[0]) != 100 {
		t.Fatalf("expected fragment to be exactly MTU size (100), got %d", len(frags[0]))
	}
}

func TestFragmentExactMTUBoundary(t *testing.T) {
	// Payload one byte over single fragment capacity
	maxPayload := 75
	payload := make([]byte, maxPayload+1)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	_, frags, err := Fragment(payload, 100)
	if err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	if len(frags) != 2 {
		t.Fatalf("expected 2 fragments, got %d", len(frags))
	}
}

func TestIsBundleFragment(t *testing.T) {
	hdr := &BundleHeader{
		Version:   BundleVersion,
		FragIndex: 0,
		FragTotal: 1,
		TotalSize: 10,
	}
	data := MarshalBundleHeader(hdr)
	payload := append(data, []byte("0123456789")...)

	if !IsBundleFragment(payload) {
		t.Fatal("IsBundleFragment returned false for valid fragment")
	}
	if IsBundleFragment(nil) {
		t.Fatal("IsBundleFragment returned true for nil")
	}
	if IsBundleFragment([]byte{0xFF}) {
		t.Fatal("IsBundleFragment returned true for wrong version")
	}
	if IsBundleFragment(make([]byte, BundleHeaderLen-1)) {
		t.Fatal("IsBundleFragment returned true for too-short data")
	}
}

func TestReassemblyInOrder(t *testing.T) {
	payload := make([]byte, 300)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	_, frags, err := Fragment(payload, 100)
	if err != nil {
		t.Fatalf("Fragment: %v", err)
	}

	rb := NewReassemblyBuffer(time.Minute, 0)

	// Feed fragments in order
	for i, frag := range frags {
		result, err := rb.Reassemble(frag)
		if err != nil {
			t.Fatalf("Reassemble fragment %d: %v", i, err)
		}
		if i < len(frags)-1 {
			if result != nil {
				t.Fatalf("got result on fragment %d, expected nil (not yet complete)", i)
			}
		} else {
			if result == nil {
				t.Fatal("expected result on last fragment")
			}
			if !bytes.Equal(result, payload) {
				t.Fatal("reassembled payload mismatch")
			}
		}
	}

	if rb.PendingCount() != 0 {
		t.Fatalf("expected 0 pending after reassembly, got %d", rb.PendingCount())
	}
}

func TestReassemblyOutOfOrder(t *testing.T) {
	payload := make([]byte, 300)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	_, frags, err := Fragment(payload, 100)
	if err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	if len(frags) < 3 {
		t.Fatal("need at least 3 fragments for out-of-order test")
	}

	rb := NewReassemblyBuffer(time.Minute, 0)

	// Feed in reverse order (last first)
	for i := len(frags) - 1; i >= 0; i-- {
		result, err := rb.Reassemble(frags[i])
		if err != nil {
			t.Fatalf("Reassemble fragment %d: %v", i, err)
		}
		if i > 0 {
			if result != nil {
				t.Fatalf("got result on fragment %d (reverse), expected nil", i)
			}
		} else {
			if result == nil {
				t.Fatal("expected result on final (fragment 0 received last)")
			}
			if !bytes.Equal(result, payload) {
				t.Fatal("reassembled payload mismatch")
			}
		}
	}
}

func TestReassemblyDuplicateFragment(t *testing.T) {
	payload := make([]byte, 200)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	_, frags, err := Fragment(payload, 100)
	if err != nil {
		t.Fatalf("Fragment: %v", err)
	}

	rb := NewReassemblyBuffer(time.Minute, 0)

	// Feed first fragment twice
	result, err := rb.Reassemble(frags[0])
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if result != nil {
		t.Fatal("should not be complete yet")
	}

	// Duplicate of first fragment
	result, err = rb.Reassemble(frags[0])
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if result != nil {
		t.Fatal("duplicate should not trigger completion")
	}

	// Feed remaining fragments
	for i := 1; i < len(frags); i++ {
		result, err = rb.Reassemble(frags[i])
		if err != nil {
			t.Fatalf("fragment %d: %v", i, err)
		}
	}
	if result == nil {
		t.Fatal("expected reassembly after all unique fragments")
	}
	if !bytes.Equal(result, payload) {
		t.Fatal("payload mismatch after duplicate handling")
	}
}

func TestReassemblyReap(t *testing.T) {
	rb := NewReassemblyBuffer(10*time.Millisecond, 0)

	payload := make([]byte, 200)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	_, frags, err := Fragment(payload, 100)
	if err != nil {
		t.Fatal(err)
	}

	// Feed only the first fragment
	if _, err := rb.Reassemble(frags[0]); err != nil {
		t.Fatal(err)
	}
	if rb.PendingCount() != 1 {
		t.Fatalf("expected 1 pending, got %d", rb.PendingCount())
	}

	// Wait and reap
	time.Sleep(50 * time.Millisecond)
	reaped := rb.Reap()
	if reaped != 1 {
		t.Fatalf("expected 1 reaped, got %d", reaped)
	}
	if rb.PendingCount() != 0 {
		t.Fatalf("expected 0 pending after reap, got %d", rb.PendingCount())
	}
}

func TestReassemblyBufferCapacity(t *testing.T) {
	rb := NewReassemblyBuffer(time.Minute, 2) // max 2 bundles

	// Create 3 different bundles, each needing 2+ fragments
	for i := 0; i < 3; i++ {
		payload := make([]byte, 100)
		if _, err := rand.Read(payload); err != nil {
			t.Fatal(err)
		}
		_, frags, err := Fragment(payload, 80)
		if err != nil {
			t.Fatal(err)
		}
		// Feed just the first fragment of each bundle
		_, err = rb.Reassemble(frags[0])
		if i < 2 {
			if err != nil {
				t.Fatalf("bundle %d: unexpected error: %v", i, err)
			}
		} else {
			if err == nil {
				t.Fatal("expected error when exceeding buffer capacity")
			}
		}
	}
}

func TestReassemblyPendingBundleInfo(t *testing.T) {
	rb := NewReassemblyBuffer(time.Minute, 0)

	payload := make([]byte, 300)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	bundleID, frags, err := Fragment(payload, 100)
	if err != nil {
		t.Fatal(err)
	}

	// Before any fragments
	recv, total := rb.PendingBundleInfo(bundleID)
	if recv != 0 || total != 0 {
		t.Fatalf("expected (0, 0) for unknown bundle, got (%d, %d)", recv, total)
	}

	// Feed 2 of N fragments
	for i := 0; i < 2; i++ {
		if _, err := rb.Reassemble(frags[i]); err != nil {
			t.Fatal(err)
		}
	}

	recv, total = rb.PendingBundleInfo(bundleID)
	if recv != 2 {
		t.Fatalf("expected 2 received, got %d", recv)
	}
	if total != len(frags) {
		t.Fatalf("expected total %d, got %d", len(frags), total)
	}
}

func TestReassemblyTotalSizeMismatch(t *testing.T) {
	rb := NewReassemblyBuffer(time.Minute, 0)

	// Create two fragments with different total_size values
	hdr1 := MarshalBundleHeader(&BundleHeader{
		Version:   BundleVersion,
		BundleID:  [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		FragIndex: 0,
		FragTotal: 2,
		TotalSize: 100,
	})
	frag1 := append(hdr1, make([]byte, 50)...)

	hdr2 := MarshalBundleHeader(&BundleHeader{
		Version:   BundleVersion,
		BundleID:  [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		FragIndex: 1,
		FragTotal: 2,
		TotalSize: 200, // different from frag1
	})
	frag2 := append(hdr2, make([]byte, 50)...)

	if _, err := rb.Reassemble(frag1); err != nil {
		t.Fatalf("frag1: %v", err)
	}

	_, err := rb.Reassemble(frag2)
	if err == nil {
		t.Fatal("expected error for total_size mismatch")
	}
}

func TestFragmentLargePayload(t *testing.T) {
	// 10KB payload with small MTU
	payload := make([]byte, 10240)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	mtu := 60 // 25 header + 35 payload per fragment
	bundleID, frags, err := Fragment(payload, mtu)
	if err != nil {
		t.Fatalf("Fragment: %v", err)
	}

	// Reassemble
	rb := NewReassemblyBuffer(time.Minute, 0)
	var result []byte
	for i, frag := range frags {
		var err error
		result, err = rb.Reassemble(frag)
		if err != nil {
			t.Fatalf("fragment %d: %v", i, err)
		}
	}
	if result == nil {
		t.Fatal("expected result after all fragments")
	}
	if !bytes.Equal(result, payload) {
		t.Fatal("large payload reassembly mismatch")
	}
	_ = bundleID
}
