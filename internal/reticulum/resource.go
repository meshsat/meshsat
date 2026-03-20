package reticulum

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// Resource transfer errors.
var (
	ErrResourceTooShort  = errors.New("reticulum: resource packet too short")
	ErrResourceTooBig    = errors.New("reticulum: resource data exceeds maximum size")
	ErrSegmentOutOfRange = errors.New("reticulum: segment index out of range")
)

// Maximum resource size (1 MB — practical limit for satellite-constrained networks).
const MaxResourceSize = 1 << 20

// ResourceAdvertisement announces a resource available for transfer.
// Wire format: [resource_hash:32] [total_size:4 LE] [segment_size:2 LE] [segment_count:2 LE]
type ResourceAdvertisement struct {
	ResourceHash [FullHashLen]byte // SHA-256 of the complete resource data
	TotalSize    uint32            // Total size in bytes
	SegmentSize  uint16            // Size of each segment (except possibly last)
	SegmentCount uint16            // Total number of segments
}

// MarshalResourceAdv encodes a resource advertisement.
func MarshalResourceAdv(adv *ResourceAdvertisement) []byte {
	buf := make([]byte, FullHashLen+4+2+2)
	copy(buf[:FullHashLen], adv.ResourceHash[:])
	binary.LittleEndian.PutUint32(buf[FullHashLen:], adv.TotalSize)
	binary.LittleEndian.PutUint16(buf[FullHashLen+4:], adv.SegmentSize)
	binary.LittleEndian.PutUint16(buf[FullHashLen+6:], adv.SegmentCount)
	return buf
}

// UnmarshalResourceAdv decodes a resource advertisement.
func UnmarshalResourceAdv(data []byte) (*ResourceAdvertisement, error) {
	minLen := FullHashLen + 4 + 2 + 2
	if len(data) < minLen {
		return nil, fmt.Errorf("%w: need %d bytes, got %d", ErrResourceTooShort, minLen, len(data))
	}
	adv := &ResourceAdvertisement{}
	copy(adv.ResourceHash[:], data[:FullHashLen])
	adv.TotalSize = binary.LittleEndian.Uint32(data[FullHashLen:])
	adv.SegmentSize = binary.LittleEndian.Uint16(data[FullHashLen+4:])
	adv.SegmentCount = binary.LittleEndian.Uint16(data[FullHashLen+6:])
	return adv, nil
}

// ResourceRequest requests specific segments of a resource.
// Wire format: [resource_hash:32] [segment_bitmap:N]
// Each bit in the bitmap represents a segment: 1=requested, 0=already have.
type ResourceRequest struct {
	ResourceHash [FullHashLen]byte
	Bitmap       []byte // ceil(segment_count/8) bytes
}

// MarshalResourceReq encodes a resource request.
func MarshalResourceReq(req *ResourceRequest) []byte {
	buf := make([]byte, FullHashLen+len(req.Bitmap))
	copy(buf[:FullHashLen], req.ResourceHash[:])
	copy(buf[FullHashLen:], req.Bitmap)
	return buf
}

// UnmarshalResourceReq decodes a resource request.
func UnmarshalResourceReq(data []byte) (*ResourceRequest, error) {
	if len(data) < FullHashLen+1 {
		return nil, fmt.Errorf("%w: need at least %d bytes", ErrResourceTooShort, FullHashLen+1)
	}
	req := &ResourceRequest{}
	copy(req.ResourceHash[:], data[:FullHashLen])
	req.Bitmap = make([]byte, len(data)-FullHashLen)
	copy(req.Bitmap, data[FullHashLen:])
	return req, nil
}

// ResourceSegment carries one segment of resource data.
// Wire format: [resource_hash:32] [segment_index:2 LE] [data:N]
type ResourceSegment struct {
	ResourceHash [FullHashLen]byte
	SegmentIndex uint16
	Data         []byte
}

// MarshalResourceSegment encodes a resource segment.
func MarshalResourceSegment(seg *ResourceSegment) []byte {
	buf := make([]byte, FullHashLen+2+len(seg.Data))
	copy(buf[:FullHashLen], seg.ResourceHash[:])
	binary.LittleEndian.PutUint16(buf[FullHashLen:], seg.SegmentIndex)
	copy(buf[FullHashLen+2:], seg.Data)
	return buf
}

// UnmarshalResourceSegment decodes a resource segment.
func UnmarshalResourceSegment(data []byte) (*ResourceSegment, error) {
	if len(data) < FullHashLen+2+1 {
		return nil, fmt.Errorf("%w: need at least %d bytes", ErrResourceTooShort, FullHashLen+2+1)
	}
	seg := &ResourceSegment{}
	copy(seg.ResourceHash[:], data[:FullHashLen])
	seg.SegmentIndex = binary.LittleEndian.Uint16(data[FullHashLen:])
	seg.Data = make([]byte, len(data)-FullHashLen-2)
	copy(seg.Data, data[FullHashLen+2:])
	return seg, nil
}

// ResourceProof confirms successful receipt of a complete resource.
// Wire format: [resource_hash:32]
type ResourceProof struct {
	ResourceHash [FullHashLen]byte
}

// MarshalResourceProof encodes a resource proof.
func MarshalResourceProof(prf *ResourceProof) []byte {
	buf := make([]byte, FullHashLen)
	copy(buf, prf.ResourceHash[:])
	return buf
}

// UnmarshalResourceProof decodes a resource proof.
func UnmarshalResourceProof(data []byte) (*ResourceProof, error) {
	if len(data) < FullHashLen {
		return nil, fmt.Errorf("%w: need %d bytes", ErrResourceTooShort, FullHashLen)
	}
	prf := &ResourceProof{}
	copy(prf.ResourceHash[:], data[:FullHashLen])
	return prf, nil
}

// ComputeResourceHash returns the SHA-256 hash of a complete resource.
func ComputeResourceHash(data []byte) [FullHashLen]byte {
	return sha256.Sum256(data)
}

// ComputeSegmentCount returns the number of segments needed for a resource.
func ComputeSegmentCount(totalSize int, segmentSize int) int {
	if segmentSize <= 0 || totalSize <= 0 {
		return 0
	}
	return (totalSize + segmentSize - 1) / segmentSize
}

// NewBitmap creates a bitmap with all bits set (all segments requested).
func NewBitmap(segmentCount int) []byte {
	byteCount := (segmentCount + 7) / 8
	bm := make([]byte, byteCount)
	for i := range bm {
		bm[i] = 0xFF
	}
	// Clear unused high bits in the last byte
	if remainder := segmentCount % 8; remainder != 0 {
		bm[len(bm)-1] = (1 << remainder) - 1
	}
	return bm
}

// BitmapGet returns whether a segment is requested in the bitmap.
func BitmapGet(bitmap []byte, index int) bool {
	byteIdx := index / 8
	bitIdx := uint(index % 8)
	if byteIdx >= len(bitmap) {
		return false
	}
	return bitmap[byteIdx]&(1<<bitIdx) != 0
}

// BitmapClear clears a segment bit in the bitmap (segment received).
func BitmapClear(bitmap []byte, index int) {
	byteIdx := index / 8
	bitIdx := uint(index % 8)
	if byteIdx < len(bitmap) {
		bitmap[byteIdx] &^= 1 << bitIdx
	}
}

// BitmapAllClear returns true if all bits are cleared (all segments received).
func BitmapAllClear(bitmap []byte) bool {
	for _, b := range bitmap {
		if b != 0 {
			return false
		}
	}
	return true
}
