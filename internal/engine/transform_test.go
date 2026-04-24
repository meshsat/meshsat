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
	// [MESHSAT-680] Unknown transform types MUST fail loud in both
	// directions. Old behaviour silently returned data unchanged, which
	// shipped plaintext for any typo and corrupted inbound decrypts.
	tp := NewTransformPipeline()
	original := []byte("unknown transform type test")

	if _, err := tp.ApplyEgress(original, `[{"type":"rot13"}]`); err == nil {
		t.Fatal("ApplyEgress accepted unknown type 'rot13'; expected error")
	}

	if _, err := tp.ApplyIngress(original, `[{"type":"rot13"}]`); err == nil {
		t.Fatal("ApplyIngress accepted unknown type 'rot13'; expected error")
	}
}

func TestTransformPipeline_DecryptAlias(t *testing.T) {
	// [MESHSAT-680] "decrypt" on the ingress side is an operator-facing
	// alias for "encrypt" — same key resolver, runs decryptAESGCM. The
	// canonical form is still "encrypt" (ingress chain is walked in
	// reverse and reverseTransform(encrypt, ...) decrypts), but an
	// operator who writes {"type":"decrypt","params":{"key_ref":"x"}}
	// in an ingress chain should get the same result, not a silent fail.
	tp := NewTransformPipeline()
	key, err := GenerateEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	tp.SetKeyResolver(stubKeyResolver{hexKey: key})

	plain := []byte("decrypt-alias check")
	enc, err := tp.ApplyEgress(plain, `[{"type":"encrypt","params":{"key_ref":"x"}}]`)
	if err != nil {
		t.Fatalf("egress encrypt: %v", err)
	}
	// Ingress chain using the "decrypt" alias should recover plaintext.
	out, err := tp.ApplyIngress(enc, `[{"type":"decrypt","params":{"key_ref":"x"}}]`)
	if err != nil {
		t.Fatalf("ingress decrypt alias: %v", err)
	}
	if !bytes.Equal(out, plain) {
		t.Fatalf("decrypt alias mismatch: got %q want %q", out, plain)
	}
}

func TestValidateTransforms_UnknownTypeIsError(t *testing.T) {
	// [MESHSAT-680] Write-time gate: unknown types must appear in the
	// errors slice so POST/PUT /api/interfaces rejects the payload.
	_, errs := ValidateTransforms(`[{"type":"aes-gcm"}]`, true, 500)
	if len(errs) == 0 {
		t.Fatal("ValidateTransforms accepted unknown type 'aes-gcm'; expected error")
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

func TestValidateTransforms_TextChannelNeedsBase64(t *testing.T) {
	// Text-only channel (SMS) with encrypt but no base64 → error
	_, errs := ValidateTransforms(`[{"type":"encrypt","params":{"key":"aabb"}}]`, false, 160)
	if len(errs) == 0 {
		t.Error("expected error for encrypt on text-only channel without base64")
	}

	// Same with smaz2
	_, errs = ValidateTransforms(`[{"type":"smaz2"}]`, false, 160)
	if len(errs) == 0 {
		t.Error("expected error for smaz2 on text-only channel without base64")
	}

	// With base64 at end → no error
	key, _ := GenerateEncryptionKey()
	_, errs = ValidateTransforms(`[{"type":"encrypt","params":{"key":"`+key+`"}},{"type":"base64"}]`, false, 160)
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestValidateTransforms_BinaryChannelNoBase64Needed(t *testing.T) {
	key, _ := GenerateEncryptionKey()
	_, errs := ValidateTransforms(`[{"type":"encrypt","params":{"key":"`+key+`"}}]`, true, 340)
	if len(errs) != 0 {
		t.Errorf("binary channel should allow encrypt without base64: %v", errs)
	}
}

func TestValidateTransforms_OverheadWarning(t *testing.T) {
	key, _ := GenerateEncryptionKey()
	// Encrypt on 160-byte binary channel (28 bytes overhead, usable=132, 82%)
	warns, _ := ValidateTransforms(`[{"type":"encrypt","params":{"key":"`+key+`"}}]`, true, 160)
	for _, w := range warns {
		t.Logf("warning: %s", w)
	}

	// Encrypt + base64 on 160-char text channel
	// Usable: 160 * 3/4 = 120 (base64 decode), 120 - 28 = 92 bytes usable (57.5%)
	// 92/160 = 57.5% < 60% → should warn
	warns, _ = ValidateTransforms(`[{"type":"encrypt","params":{"key":"`+key+`"}},{"type":"base64"}]`, false, 160)
	if len(warns) == 0 {
		t.Error("expected overhead warning for encrypt+base64 on 160-char channel")
	}
	for _, w := range warns {
		t.Logf("warning: %s", w)
	}
}

func TestValidateTransforms_EmptyIsValid(t *testing.T) {
	warns, errs := ValidateTransforms("", false, 160)
	if len(warns) != 0 || len(errs) != 0 {
		t.Errorf("empty transforms should be valid, got warns=%v errs=%v", warns, errs)
	}
	warns, errs = ValidateTransforms("[]", true, 340)
	if len(warns) != 0 || len(errs) != 0 {
		t.Errorf("empty array transforms should be valid, got warns=%v errs=%v", warns, errs)
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
