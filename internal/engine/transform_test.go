package engine

import (
	"bytes"
	"encoding/hex"
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

func TestTransformPipeline_Encrypt_RoundTrip(t *testing.T) {
	tp := NewTransformPipeline()
	key, err := GenerateEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}

	original := []byte("top secret satellite payload data for Iridium SBD transmission")
	transformJSON := `[{"type":"encrypt","params":{"key":"` + key + `"}}]`

	encrypted, err := tp.ApplyEgress(original, transformJSON)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Equal(encrypted, original) {
		t.Error("encrypted data should differ from original")
	}
	// Encrypted output = 12 (nonce) + len(original) + 16 (GCM tag)
	expectedLen := 12 + len(original) + 16
	if len(encrypted) != expectedLen {
		t.Errorf("expected %d bytes, got %d", expectedLen, len(encrypted))
	}

	decrypted, err := tp.ApplyIngress(encrypted, transformJSON)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(decrypted, original) {
		t.Errorf("round-trip mismatch: got %q, want %q", decrypted, original)
	}
}

func TestTransformPipeline_Encrypt_WrongKey(t *testing.T) {
	tp := NewTransformPipeline()

	key1, _ := GenerateEncryptionKey()
	key2, _ := GenerateEncryptionKey()

	original := []byte("encrypted with key1")
	encJSON := `[{"type":"encrypt","params":{"key":"` + key1 + `"}}]`
	decJSON := `[{"type":"encrypt","params":{"key":"` + key2 + `"}}]`

	encrypted, err := tp.ApplyEgress(original, encJSON)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	_, err = tp.ApplyIngress(encrypted, decJSON)
	if err == nil {
		t.Fatal("expected error decrypting with wrong key")
	}
}

func TestTransformPipeline_Encrypt_TamperDetection(t *testing.T) {
	tp := NewTransformPipeline()
	key, _ := GenerateEncryptionKey()

	original := []byte("tamper-proof payload")
	transformJSON := `[{"type":"encrypt","params":{"key":"` + key + `"}}]`

	encrypted, err := tp.ApplyEgress(original, transformJSON)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Flip a byte in the ciphertext (after nonce)
	tampered := make([]byte, len(encrypted))
	copy(tampered, encrypted)
	tampered[15] ^= 0xFF

	_, err = tp.ApplyIngress(tampered, transformJSON)
	if err == nil {
		t.Fatal("expected error for tampered ciphertext")
	}
}

func TestTransformPipeline_Encrypt_InvalidKey(t *testing.T) {
	tp := NewTransformPipeline()

	tests := []struct {
		name string
		key  string
	}{
		{"too short", "aabbccdd"},
		{"not hex", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"empty", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			json := `[{"type":"encrypt","params":{"key":"` + tc.key + `"}}]`
			_, err := tp.ApplyEgress([]byte("test"), json)
			if err == nil {
				t.Error("expected error for invalid key")
			}
		})
	}
}

func TestTransformPipeline_Encrypt_ShortCiphertext(t *testing.T) {
	tp := NewTransformPipeline()
	key, _ := GenerateEncryptionKey()
	transformJSON := `[{"type":"encrypt","params":{"key":"` + key + `"}}]`

	_, err := tp.ApplyIngress([]byte("short"), transformJSON)
	if err == nil {
		t.Error("expected error for ciphertext shorter than nonce")
	}
}

func TestTransformPipeline_CompressThenEncrypt(t *testing.T) {
	tp := NewTransformPipeline()
	key, _ := GenerateEncryptionKey()

	original := []byte("compress then encrypt: this message should be smaller after smaz2 compression before encryption")
	transformJSON := `[{"type":"smaz2","params":{"dict":"meshtastic"}},{"type":"encrypt","params":{"key":"` + key + `"}}]`

	transformed, err := tp.ApplyEgress(original, transformJSON)
	if err != nil {
		t.Fatalf("egress: %v", err)
	}

	restored, err := tp.ApplyIngress(transformed, transformJSON)
	if err != nil {
		t.Fatalf("ingress: %v", err)
	}
	if !bytes.Equal(restored, original) {
		t.Errorf("round-trip mismatch: got %q, want %q", restored, original)
	}
}

func TestGenerateEncryptionKey(t *testing.T) {
	key, err := GenerateEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	// Should be 64 hex chars (32 bytes)
	if len(key) != 64 {
		t.Errorf("expected 64 hex chars, got %d", len(key))
	}
	decoded, err := hex.DecodeString(key)
	if err != nil {
		t.Fatalf("key should be valid hex: %v", err)
	}
	if len(decoded) != 32 {
		t.Errorf("expected 32 bytes, got %d", len(decoded))
	}

	// Two generated keys should differ
	key2, _ := GenerateEncryptionKey()
	if key == key2 {
		t.Error("two generated keys should not be identical")
	}
}
