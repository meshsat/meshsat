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
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/rs/zerolog/log"

	"meshsat/internal/compress"
)

// TransformSpec defines a single transform step.
type TransformSpec struct {
	Type   string            `json:"type"`
	Params map[string]string `json:"params,omitempty"`
}

// KeyResolver resolves a key_ref (e.g. "sms:+31653618463") to a hex-encoded
// AES-256 key. Used by the transform pipeline to avoid inline key material
// in interface config. [MESHSAT-447]
type KeyResolver interface {
	ResolveKeyHex(keyRef string) (string, error)
}

// TransformPipeline applies ordered transforms to payloads.
type TransformPipeline struct {
	llamazip           *compress.LlamaZipClient
	msvqsc             *compress.MSVQSCClient
	codebook           *compress.Codebook
	keyResolver        KeyResolver        // resolves "channel_type:address" key_ref → hex key [MESHSAT-447]
	contactKeyResolver ContactKeyResolver // resolves "contact:<uuid>" key_ref → hex key [MESHSAT-537]
	fecMetrics         FECMetrics
}

// FECStats returns the FEC encode/decode metrics.
func (tp *TransformPipeline) FECStats() *FECMetrics { return &tp.fecMetrics }

// NewTransformPipeline creates a new pipeline.
func NewTransformPipeline() *TransformPipeline {
	return &TransformPipeline{}
}

// SetLlamaZipClient sets the optional llama-zip sidecar client.
func (tp *TransformPipeline) SetLlamaZipClient(c *compress.LlamaZipClient) {
	tp.llamazip = c
}

// SetMSVQSCClient sets the optional MSVQ-SC sidecar client (lossy semantic compression).
func (tp *TransformPipeline) SetMSVQSCClient(c *compress.MSVQSCClient) {
	tp.msvqsc = c
}

// SetCodebook sets the MSVQ-SC codebook for pure-Go decode (no sidecar needed).
func (tp *TransformPipeline) SetCodebook(cb *compress.Codebook) {
	tp.codebook = cb
}

// SetKeyResolver sets the resolver for "channel_type:address" key_ref in
// encrypt transforms. [MESHSAT-447]
func (tp *TransformPipeline) SetKeyResolver(kr KeyResolver) {
	tp.keyResolver = kr
}

// SetContactKeyResolver sets the resolver for "contact:<uuid>" key_ref in
// encrypt transforms. Coexists with the legacy KeyResolver during the
// MESHSAT-548 / S2-05 dual-read grace period. [MESHSAT-537]
func (tp *TransformPipeline) SetContactKeyResolver(cr ContactKeyResolver) {
	tp.contactKeyResolver = cr
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
	case "msvqsc":
		// Lossy semantic compression via multi-stage residual VQ.
		if tp.msvqsc == nil || !tp.msvqsc.IsReady() {
			log.Warn().Msg("transform: msvqsc sidecar not available, falling back to smaz2")
			dict := compress.DictMeshtastic
			return compress.Compress(data, dict), nil
		}
		maxStages := 0 // default: use all stages
		if s := t.Params["stages"]; s != "" && s != "auto" {
			for _, c := range s {
				if c >= '0' && c <= '9' {
					maxStages = maxStages*10 + int(c-'0')
				}
			}
		} else if s == "auto" {
			channelType := t.Params["channel"]
			maxStages = compress.SuggestStages(channelType)
		}
		encoded, stages, fidelity, err := tp.msvqsc.Encode(data, maxStages)
		if err != nil {
			log.Warn().Err(err).Msg("transform: msvqsc encode failed, falling back to smaz2")
			dict := compress.DictMeshtastic
			return compress.Compress(data, dict), nil
		}
		log.Debug().
			Int("original", len(data)).
			Int("encoded", len(encoded)).
			Int("stages", stages).
			Float32("fidelity", fidelity).
			Msg("transform: msvqsc encoded (lossy)")
		return encoded, nil
	case "fec":
		ds, ps, il, ild := resolveFECParams(t.Params)
		if ds == 0 || ps == 0 {
			log.Debug().Msg("transform: fec skipped (profile has no FEC)")
			return data, nil
		}
		var opts []fecEncodeOpts
		if il {
			opts = append(opts, fecEncodeOpts{interleave: true, interleaveDepth: ild})
		}
		encoded, err := fecEncode(data, ds, ps, opts...)
		if err != nil {
			tp.fecMetrics.EncodeFail.Add(1)
			return nil, err
		}
		tp.fecMetrics.EncodeOK.Add(1)
		log.Debug().
			Int("original", len(data)).
			Int("encoded", len(encoded)).
			Int("k", ds).
			Int("m", ps).
			Bool("interleave", il).
			Msg("transform: fec encoded")
		return encoded, nil
	case "encrypt":
		hexKey, err := tp.resolveEncryptKey(t)
		if err != nil {
			return nil, err
		}
		return encryptAESGCM(data, hexKey)
	default:
		// [MESHSAT-680] Fail loud on unknown types. Previously we logged
		// and returned data unchanged, which silently shipped plaintext
		// for any typo like "aes-gcm" / "compress" / "b64". Write-time
		// validation in ValidateTransforms catches most cases, but this
		// is the last-line defense for anything that slipped through
		// (direct DB writes, older rows from before validation, etc.).
		return nil, fmt.Errorf("unknown egress transform type %q", t.Type)
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
	case "msvqsc":
		// Prefer pure-Go codebook decode (no sidecar needed)
		if tp.codebook != nil {
			text, err := tp.codebook.DecodeIndices(data)
			if err != nil {
				return nil, fmt.Errorf("msvqsc codebook decode: %w", err)
			}
			return []byte(text), nil
		}
		// Fall back to sidecar decode
		if tp.msvqsc != nil && tp.msvqsc.IsReady() {
			decoded, _, err := tp.msvqsc.Decode(data)
			if err != nil {
				return nil, fmt.Errorf("msvqsc sidecar decode: %w", err)
			}
			return decoded, nil
		}
		return nil, fmt.Errorf("msvqsc: neither codebook nor sidecar available for decode")
	case "fec":
		decoded, err := fecDecode(data)
		if err != nil {
			tp.fecMetrics.DecodeFail.Add(1)
			return nil, err
		}
		tp.fecMetrics.DecodeOK.Add(1)
		log.Debug().
			Int("encoded", len(data)).
			Int("decoded", len(decoded)).
			Msg("transform: fec decoded")
		return decoded, nil
	case "encrypt", "decrypt":
		// "decrypt" is an operator-ergonomic alias for "encrypt" on the
		// reverse path — lots of ingress chains read more naturally as
		// "base64 -> decrypt -> smaz2" than "base64 -> encrypt -> smaz2".
		// Both resolve the same key and run decryptAESGCM. [MESHSAT-680]
		hexKey, err := tp.resolveEncryptKey(t)
		if err != nil {
			return nil, err
		}
		return decryptAESGCM(data, hexKey)
	default:
		// [MESHSAT-680] Fail loud on unknown types. See matching note on
		// applyTransform. Silently returning ciphertext-as-plaintext
		// corrupts inbound decrypts in a way that is almost impossible
		// to diagnose from the operator side.
		return nil, fmt.Errorf("unknown ingress transform type %q", t.Type)
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
		case "encrypt", "decrypt":
			hasBinaryOutput = true
			endsWithBase64 = false
			if t.Params["key"] == "" && t.Params["key_ref"] == "" {
				errors = append(errors, fmt.Sprintf("%s transform requires 'key' or 'key_ref' param", t.Type))
			}
		case "fec":
			hasBinaryOutput = true
			endsWithBase64 = false
		case "zstd", "smaz2", "llamazip", "msvqsc":
			hasBinaryOutput = true
			endsWithBase64 = false
		case "base64":
			hasBinaryOutput = false // base64 output is text-safe
			endsWithBase64 = true
			hasBase64 = true
		default:
			// [MESHSAT-680] Unknown types used to be silently skipped at
			// runtime (applyTransform/reverseTransform default→warn+pass).
			// Now they're hard errors at runtime; catch them at write time
			// too so misconfigs fail on the Settings save, not at first
			// message dispatch.
			errors = append(errors, fmt.Sprintf("unknown transform type %q — supported: encrypt, decrypt, base64, zstd, smaz2, llamazip, msvqsc, fec", t.Type))
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
			case "fec":
				ds, ps, _, _ := resolveFECParams(transforms[i].Params)
				if ds == 0 || ps == 0 {
					break // no FEC for this profile
				}
				usable = usable*ds/(ds+ps) - fecHeaderLenV2 - fecOrigLenTrailer
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

// resolveEncryptKey returns the hex-encoded AES key for an encrypt
// transform. Supports three modes:
//   - {"key":"abcd..."}                inline hex (legacy, backwards-compatible)
//   - {"key_ref":"sms:+31..."}         per-channel keystore lookup [MESHSAT-447]
//   - {"key_ref":"contact:<uuid>"}     per-contact directory lookup [MESHSAT-537]
//
// The per-channel and per-contact paths coexist during the 30-day
// dual-read grace (MESHSAT-548 / S2-05): transform specs may use
// either form; both resolve to the same AES-256 hex expected by
// encryptAESGCM.
func (tp *TransformPipeline) resolveEncryptKey(t TransformSpec) (string, error) {
	// Inline key (legacy)
	if k := t.Params["key"]; k != "" {
		return k, nil
	}
	ref := t.Params["key_ref"]
	if ref == "" {
		return "", fmt.Errorf("encrypt transform requires 'key' or 'key_ref' param")
	}

	// Per-contact key — routes to the directory-backed resolver.
	if strings.HasPrefix(ref, "contact:") {
		if tp.contactKeyResolver == nil {
			return "", fmt.Errorf("encrypt transform uses contact:<uuid> key_ref but no ContactKeyResolver configured")
		}
		return tp.contactKeyResolver.ResolveContactKey(strings.TrimPrefix(ref, "contact:"))
	}

	// Per-channel key (legacy).
	if tp.keyResolver == nil {
		return "", fmt.Errorf("encrypt transform uses key_ref but no KeyResolver configured")
	}
	return tp.keyResolver.ResolveKeyHex(ref)
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
