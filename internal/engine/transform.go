package engine

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
	"github.com/rs/zerolog/log"

	"meshsat/internal/compress"
)

// TransformSpec defines a single transform step.
type TransformSpec struct {
	Type   string            `json:"type"`
	Params map[string]string `json:"params,omitempty"`
}

// TransformPipeline applies ordered transforms to payloads.
type TransformPipeline struct {
	llamazip *compress.LlamaZipClient
}

// NewTransformPipeline creates a new pipeline.
func NewTransformPipeline() *TransformPipeline {
	return &TransformPipeline{}
}

// SetLlamaZipClient sets the optional llama-zip sidecar client.
func (tp *TransformPipeline) SetLlamaZipClient(c *compress.LlamaZipClient) {
	tp.llamazip = c
}

// ApplyEgress applies egress transforms in order (compress, encode, etc.)
func (tp *TransformPipeline) ApplyEgress(data []byte, transformsJSON string) ([]byte, error) {
	transforms, err := parseTransforms(transformsJSON)
	if err != nil {
		return nil, fmt.Errorf("parse egress transforms: %w", err)
	}
	if len(transforms) == 0 {
		return data, nil
	}

	result := data
	for _, t := range transforms {
		result, err = tp.applyTransform(t, result)
		if err != nil {
			return nil, fmt.Errorf("egress transform %q: %w", t.Type, err)
		}
	}
	return result, nil
}

// ApplyIngress applies ingress transforms in reverse order (decode, decompress, etc.)
func (tp *TransformPipeline) ApplyIngress(data []byte, transformsJSON string) ([]byte, error) {
	transforms, err := parseTransforms(transformsJSON)
	if err != nil {
		return nil, fmt.Errorf("parse ingress transforms: %w", err)
	}
	if len(transforms) == 0 {
		return data, nil
	}

	// Reverse order for ingress
	result := data
	for i := len(transforms) - 1; i >= 0; i-- {
		result, err = tp.reverseTransform(transforms[i], result)
		if err != nil {
			return nil, fmt.Errorf("ingress transform %q: %w", transforms[i].Type, err)
		}
	}
	return result, nil
}

func parseTransforms(jsonStr string) ([]TransformSpec, error) {
	if jsonStr == "" || jsonStr == "[]" {
		return nil, nil
	}
	var specs []TransformSpec
	if err := json.Unmarshal([]byte(jsonStr), &specs); err != nil {
		return nil, err
	}
	return specs, nil
}

func (tp *TransformPipeline) applyTransform(t TransformSpec, data []byte) ([]byte, error) {
	switch t.Type {
	case "zstd":
		encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
		if err != nil {
			return nil, err
		}
		defer encoder.Close()
		return encoder.EncodeAll(data, nil), nil
	case "base64":
		dst := make([]byte, base64.StdEncoding.EncodedLen(len(data)))
		base64.StdEncoding.Encode(dst, data)
		return dst, nil
	case "smaz2":
		dict := compress.DictDefault
		if t.Params["dict"] == "meshtastic" {
			dict = compress.DictMeshtastic
		}
		return compress.Compress(data, dict), nil
	case "llamazip":
		if tp.llamazip == nil || !tp.llamazip.IsReady() {
			log.Warn().Msg("transform: llamazip sidecar not available, falling back to smaz2")
			dict := compress.DictMeshtastic
			return compress.Compress(data, dict), nil
		}
		compressed, durationMs, err := tp.llamazip.Compress(data)
		if err != nil {
			log.Warn().Err(err).Msg("transform: llamazip compress failed, falling back to smaz2")
			dict := compress.DictMeshtastic
			return compress.Compress(data, dict), nil
		}
		log.Debug().
			Int("original", len(data)).
			Int("compressed", len(compressed)).
			Int("duration_ms", durationMs).
			Msg("transform: llamazip compressed")
		return compressed, nil
	case "encrypt":
		return encryptAESGCM(data, t.Params["key"])
	default:
		log.Warn().Str("type", t.Type).Msg("transform: unknown type, skipping")
		return data, nil
	}
}

func (tp *TransformPipeline) reverseTransform(t TransformSpec, data []byte) ([]byte, error) {
	switch t.Type {
	case "zstd":
		decoder, err := zstd.NewReader(nil)
		if err != nil {
			return nil, err
		}
		defer decoder.Close()
		return decoder.DecodeAll(data, nil)
	case "base64":
		dst := make([]byte, base64.StdEncoding.DecodedLen(len(data)))
		n, err := base64.StdEncoding.Decode(dst, data)
		if err != nil {
			return nil, err
		}
		return dst[:n], nil
	case "smaz2":
		dict := compress.DictDefault
		if t.Params["dict"] == "meshtastic" {
			dict = compress.DictMeshtastic
		}
		return compress.Decompress(data, dict)
	case "llamazip":
		if tp.llamazip == nil || !tp.llamazip.IsReady() {
			return nil, fmt.Errorf("llamazip sidecar not available for decompression")
		}
		decompressed, _, err := tp.llamazip.Decompress(data)
		if err != nil {
			return nil, fmt.Errorf("llamazip decompress: %w", err)
		}
		return decompressed, nil
	case "encrypt":
		return decryptAESGCM(data, t.Params["key"])
	default:
		log.Warn().Str("type", t.Type).Msg("transform: unknown reverse type, skipping")
		return data, nil
	}
}

// ValidateTransforms checks if a transform chain is compatible with a channel.
// Returns a list of warnings (non-fatal) and errors (fatal).
func ValidateTransforms(transformsJSON string, binaryCapable bool, maxPayload int) (warnings []string, errors []string) {
	transforms, err := parseTransforms(transformsJSON)
	if err != nil {
		return nil, []string{"invalid transforms JSON: " + err.Error()}
	}
	if len(transforms) == 0 {
		return nil, nil
	}

	hasBinaryOutput := false // does the chain produce binary output?
	endsWithBase64 := false
	hasBase64 := false

	for _, t := range transforms {
		switch t.Type {
		case "encrypt":
			hasBinaryOutput = true
			endsWithBase64 = false
			if t.Params["key"] == "" {
				errors = append(errors, "encrypt transform requires a 'key' param")
			}
		case "zstd", "smaz2", "llamazip":
			hasBinaryOutput = true
			endsWithBase64 = false
		case "base64":
			hasBinaryOutput = false // base64 output is text-safe
			endsWithBase64 = true
			hasBase64 = true
		}
	}

	// Check: text-only transport with binary output
	if !binaryCapable && hasBinaryOutput && !endsWithBase64 {
		errors = append(errors, "text-only transport (SMS/MQTT/webhook) requires base64 as the final transform after encrypt/compress")
	}

	// Estimate usable capacity on constrained channels.
	// Work backwards from maxPayload through the transform chain (reversed).
	if maxPayload > 0 {
		usable := maxPayload
		for i := len(transforms) - 1; i >= 0; i-- {
			switch transforms[i].Type {
			case "base64":
				usable = usable * 3 / 4 // base64 decoding recovers 3/4
			case "encrypt":
				usable -= 28 // 12 nonce + 16 GCM tag
			}
		}
		if usable < 20 {
			warnings = append(warnings, fmt.Sprintf("transforms leave very little usable payload (~%d bytes of %d)", usable, maxPayload))
		} else if hasBase64 && float64(usable)/float64(maxPayload) < 0.6 {
			warnings = append(warnings, fmt.Sprintf("transforms reduce usable capacity to ~%d bytes (of %d max)", usable, maxPayload))
		}
	}

	return warnings, errors
}

// encryptAESGCM encrypts data using AES-256-GCM with the given hex-encoded key.
// Output format: 12-byte nonce || ciphertext+tag
func encryptAESGCM(data []byte, hexKey string) ([]byte, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("invalid encryption key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes (got %d)", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	// nonce || ciphertext+tag
	ciphertext := gcm.Seal(nonce, nonce, data, nil)
	return ciphertext, nil
}

// decryptAESGCM decrypts data encrypted by encryptAESGCM.
// Input format: 12-byte nonce || ciphertext+tag
func decryptAESGCM(data []byte, hexKey string) ([]byte, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("invalid decryption key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("decryption key must be 32 bytes (got %d)", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short (need at least %d bytes for nonce)", nonceSize)
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}

// GenerateEncryptionKey generates a random 32-byte AES-256 key and returns it hex-encoded.
func GenerateEncryptionKey() (string, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return hex.EncodeToString(key), nil
}
