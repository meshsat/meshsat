package codec

import (
	"math"
	"testing"
)

func almostEqual(a, b, eps float64) bool {
	return math.Abs(a-b) < eps
}

func TestEncodeDecodePosition(t *testing.T) {
	tests := []struct {
		name string
		pos  Position
	}{
		{
			name: "typical GPS fix",
			pos:  Position{Lat: 52.520008, Lon: 13.404954, Alt: 34, Heading: 180, Speed: 500, Battery: 85},
		},
		{
			name: "negative lat/lon",
			pos:  Position{Lat: -33.868820, Lon: -151.209296, Alt: 10, Heading: 0, Speed: 0, Battery: 100},
		},
		{
			name: "high altitude",
			pos:  Position{Lat: 27.988056, Lon: 86.925278, Alt: 8848, Heading: 359, Speed: 100, Battery: 20},
		},
		{
			name: "zero speed at sea level",
			pos:  Position{Lat: 0.0, Lon: 0.0, Alt: 0, Heading: 0, Speed: 0, Battery: 0},
		},
		{
			name: "negative altitude",
			pos:  Position{Lat: 31.5, Lon: 35.5, Alt: -430, Heading: 90, Speed: 0, Battery: 50},
		},
		{
			name: "max heading and speed",
			pos:  Position{Lat: 45.0, Lon: 90.0, Alt: 100, Heading: 359, Speed: 65535, Battery: 100},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := EncodePosition(tt.pos)
			if len(data) != FullPositionSize {
				t.Fatalf("expected %d bytes, got %d", FullPositionSize, len(data))
			}
			if data[0] != HeaderFull {
				t.Fatalf("expected header 0x%02X, got 0x%02X", HeaderFull, data[0])
			}

			got, err := DecodePosition(data)
			if err != nil {
				t.Fatalf("decode error: %v", err)
			}

			// lat/lon have 1e-6 precision loss
			if !almostEqual(got.Lat, tt.pos.Lat, 1e-6) {
				t.Errorf("lat: got %f, want %f", got.Lat, tt.pos.Lat)
			}
			if !almostEqual(got.Lon, tt.pos.Lon, 1e-6) {
				t.Errorf("lon: got %f, want %f", got.Lon, tt.pos.Lon)
			}
			if got.Alt != tt.pos.Alt {
				t.Errorf("alt: got %d, want %d", got.Alt, tt.pos.Alt)
			}
			if got.Heading != tt.pos.Heading {
				t.Errorf("heading: got %d, want %d", got.Heading, tt.pos.Heading)
			}
			if got.Speed != tt.pos.Speed {
				t.Errorf("speed: got %d, want %d", got.Speed, tt.pos.Speed)
			}
			if got.Battery != tt.pos.Battery {
				t.Errorf("battery: got %d, want %d", got.Battery, tt.pos.Battery)
			}
		})
	}
}

func TestDecodePositionErrors(t *testing.T) {
	if _, err := DecodePosition([]byte{0x50}); err == nil {
		t.Error("expected error for short data")
	}
	if _, err := DecodePosition(make([]byte, FullPositionSize)); err == nil {
		t.Error("expected error for wrong header")
	}
}

func TestDeltaEncoderFirstPositionIsFull(t *testing.T) {
	enc := &DeltaEncoder{}
	p := Position{Lat: 52.52, Lon: 13.40, Alt: 34, Heading: 180, Speed: 500, Battery: 85}
	data := enc.EncodeDelta(p)
	if len(data) != FullPositionSize {
		t.Fatalf("first encode should be full (%d bytes), got %d", FullPositionSize, len(data))
	}
	if data[0] != HeaderFull {
		t.Fatalf("first encode header: got 0x%02X, want 0x%02X", data[0], HeaderFull)
	}
}

func TestDeltaEncoderSmallMovement(t *testing.T) {
	enc := &DeltaEncoder{}
	p1 := Position{Lat: 52.520000, Lon: 13.400000, Alt: 34, Heading: 180, Speed: 500, Battery: 85}
	p2 := Position{Lat: 52.520100, Lon: 13.400200, Alt: 35, Heading: 182, Speed: 510, Battery: 84}

	enc.EncodeDelta(p1) // full
	data := enc.EncodeDelta(p2)
	if len(data) != DeltaPositionSize {
		t.Fatalf("second encode should be delta (%d bytes), got %d", DeltaPositionSize, len(data))
	}
	if data[0] != HeaderDelta {
		t.Fatalf("delta header: got 0x%02X, want 0x%02X", data[0], HeaderDelta)
	}
}

func TestDeltaEncoderLargeMovementFallsBackToFull(t *testing.T) {
	enc := &DeltaEncoder{}
	p1 := Position{Lat: 52.52, Lon: 13.40, Alt: 34, Heading: 180, Speed: 500, Battery: 85}
	p2 := Position{Lat: 10.00, Lon: -70.00, Alt: 100, Heading: 90, Speed: 1000, Battery: 50}

	enc.EncodeDelta(p1)
	data := enc.EncodeDelta(p2)
	if len(data) != FullPositionSize {
		t.Fatalf("large movement should produce full frame (%d bytes), got %d", FullPositionSize, len(data))
	}
}

func TestDeltaEncoderRoundTrip(t *testing.T) {
	positions := []Position{
		{Lat: 52.520000, Lon: 13.400000, Alt: 34, Heading: 180, Speed: 500, Battery: 85},
		{Lat: 52.520100, Lon: 13.400200, Alt: 35, Heading: 182, Speed: 510, Battery: 84},
		{Lat: 52.520200, Lon: 13.400300, Alt: 36, Heading: 185, Speed: 520, Battery: 83},
		{Lat: -33.000000, Lon: 151.000000, Alt: 0, Heading: 0, Speed: 0, Battery: 100}, // large jump
		{Lat: -33.000100, Lon: 151.000100, Alt: 1, Heading: 5, Speed: 50, Battery: 99},
	}

	encoder := &DeltaEncoder{}
	decoder := &DeltaEncoder{}

	for i, p := range positions {
		data := encoder.EncodeDelta(p)
		got, err := decoder.DecodeDelta(data)
		if err != nil {
			t.Fatalf("position %d: decode error: %v", i, err)
		}
		if !almostEqual(got.Lat, p.Lat, 1e-6) || !almostEqual(got.Lon, p.Lon, 1e-6) {
			t.Errorf("position %d: lat/lon mismatch: got (%f,%f), want (%f,%f)", i, got.Lat, got.Lon, p.Lat, p.Lon)
		}
		if got.Alt != p.Alt || got.Heading != p.Heading || got.Speed != p.Speed || got.Battery != p.Battery {
			t.Errorf("position %d: field mismatch", i)
		}
	}
}

func TestDeltaDecodeDeltaWithoutFull(t *testing.T) {
	dec := &DeltaEncoder{}
	// craft a delta frame manually
	data := make([]byte, DeltaPositionSize)
	data[0] = HeaderDelta
	_, err := dec.DecodeDelta(data)
	if err == nil {
		t.Error("expected error decoding delta without prior full position")
	}
}

func TestDeltaEncoderReset(t *testing.T) {
	enc := &DeltaEncoder{}
	p := Position{Lat: 52.52, Lon: 13.40, Alt: 34, Heading: 180, Speed: 500, Battery: 85}
	enc.EncodeDelta(p)
	enc.Reset()

	// after reset, next encode should be full
	data := enc.EncodeDelta(p)
	if data[0] != HeaderFull {
		t.Errorf("after reset, expected full frame, got header 0x%02X", data[0])
	}
}
