package engine

import (
	"bytes"
	"testing"
)

func TestTransformPipeline_Zstd(t *testing.T) {
	tp := NewTransformPipeline()
	original := []byte("hello world this is a test of zstd compression in meshsat transforms")

	compressed, err := tp.ApplyEgress(original, `[{"type":"zstd"}]`)
	if err != nil {
		t.Fatalf("egress zstd: %v", err)
	}
	// Compressed output should differ from original
	if bytes.Equal(compressed, original) {
		t.Error("compressed data should differ from original")
	}

	decompressed, err := tp.ApplyIngress(compressed, `[{"type":"zstd"}]`)
	if err != nil {
		t.Fatalf("ingress zstd: %v", err)
	}
	if !bytes.Equal(decompressed, original) {
		t.Errorf("round-trip mismatch: got %q, want %q", decompressed, original)
	}
}

func TestTransformPipeline_Base64(t *testing.T) {
	tp := NewTransformPipeline()
	original := []byte("binary\x00data\xff\xfe")

	encoded, err := tp.ApplyEgress(original, `[{"type":"base64"}]`)
	if err != nil {
		t.Fatalf("egress base64: %v", err)
	}
	// Encoded output should be printable ASCII
	for _, b := range encoded {
		if b > 127 {
			t.Error("base64 output contains non-ASCII byte")
			break
		}
	}

	decoded, err := tp.ApplyIngress(encoded, `[{"type":"base64"}]`)
	if err != nil {
		t.Fatalf("ingress base64: %v", err)
	}
	if !bytes.Equal(decoded, original) {
		t.Errorf("round-trip mismatch: got %x, want %x", decoded, original)
	}
}

func TestTransformPipeline_Chain(t *testing.T) {
	tp := NewTransformPipeline()
	original := []byte("chain test: compress then base64 encode for text-safe satellite transport")

	// Egress: zstd → base64 (applied in order)
	egressJSON := `[{"type":"zstd"},{"type":"base64"}]`
	transformed, err := tp.ApplyEgress(original, egressJSON)
	if err != nil {
		t.Fatalf("egress chain: %v", err)
	}
	// Result should be base64 (all ASCII)
	for _, b := range transformed {
		if b > 127 {
			t.Error("chained output contains non-ASCII byte")
			break
		}
	}

	// Ingress: base64 → zstd (reversed automatically)
	ingressJSON := `[{"type":"zstd"},{"type":"base64"}]`
	restored, err := tp.ApplyIngress(transformed, ingressJSON)
	if err != nil {
		t.Fatalf("ingress chain: %v", err)
	}
	if !bytes.Equal(restored, original) {
		t.Errorf("chain round-trip mismatch: got %q, want %q", restored, original)
	}
}

func TestTransformPipeline_EmptyConfig(t *testing.T) {
	tp := NewTransformPipeline()
	original := []byte("passthrough")

	tests := []struct {
		name string
		json string
	}{
		{"empty string", ""},
		{"empty array", "[]"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tp.ApplyEgress(original, tc.json)
			if err != nil {
				t.Fatalf("egress: %v", err)
			}
			if !bytes.Equal(out, original) {
				t.Errorf("egress should passthrough, got %q", out)
			}

			out, err = tp.ApplyIngress(original, tc.json)
			if err != nil {
				t.Fatalf("ingress: %v", err)
			}
			if !bytes.Equal(out, original) {
				t.Errorf("ingress should passthrough, got %q", out)
			}
		})
	}
}

func TestTransformPipeline_UnknownType(t *testing.T) {
	tp := NewTransformPipeline()
	original := []byte("unknown transform type test")

	out, err := tp.ApplyEgress(original, `[{"type":"rot13"}]`)
	if err != nil {
		t.Fatalf("egress unknown: %v", err)
	}
	if !bytes.Equal(out, original) {
		t.Errorf("unknown type should passthrough, got %q", out)
	}

	out, err = tp.ApplyIngress(original, `[{"type":"rot13"}]`)
	if err != nil {
		t.Fatalf("ingress unknown: %v", err)
	}
	if !bytes.Equal(out, original) {
		t.Errorf("unknown reverse type should passthrough, got %q", out)
	}
}
