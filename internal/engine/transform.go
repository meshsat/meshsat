package engine

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

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
type TransformPipeline struct{}

// NewTransformPipeline creates a new pipeline.
func NewTransformPipeline() *TransformPipeline {
	return &TransformPipeline{}
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
		result, err = applyTransform(t, result)
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
		result, err = reverseTransform(transforms[i], result)
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

func applyTransform(t TransformSpec, data []byte) ([]byte, error) {
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
	default:
		log.Warn().Str("type", t.Type).Msg("transform: unknown type, skipping")
		return data, nil
	}
}

func reverseTransform(t TransformSpec, data []byte) ([]byte, error) {
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
	default:
		log.Warn().Str("type", t.Type).Msg("transform: unknown reverse type, skipping")
		return data, nil
	}
}
