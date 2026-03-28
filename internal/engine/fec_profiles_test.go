package engine

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"testing"
)

func TestFECProfileLookup(t *testing.T) {
	// All known channel types should have profiles.
	knownTypes := []string{
		"lora", "mesh", "sbd", "iridium", "imt", "iridium_imt",
		"astrocast", "ax25", "tcp", "mqtt", "webhook",
		"cellular", "sms", "zigbee",
	}
	for _, ct := range knownTypes {
		p, ok := LookupFECProfile(ct)
		if !ok {
			t.Errorf("missing profile for %q", ct)
			continue
		}
		// TCP/MQTT/webhook should have 0/0 (no FEC).
		switch ct {
		case "tcp", "mqtt", "webhook":
			if p.DataShards != 0 || p.ParityShards != 0 {
				t.Errorf("%q should have no FEC, got k=%d m=%d", ct, p.DataShards, p.ParityShards)
			}
		default:
			if p.DataShards < 1 || p.ParityShards < 1 {
				t.Errorf("%q has invalid FEC params k=%d m=%d", ct, p.DataShards, p.ParityShards)
			}
		}
	}

	// Unknown type should return not-found.
	_, ok := LookupFECProfile("unknown_type")
	if ok {
		t.Error("expected not-found for unknown type")
	}
}

func TestResolveFECParamsNamedProfile(t *testing.T) {
	params := map[string]string{"profile": "lora"}
	ds, ps, il, ild := resolveFECParams(params)
	if ds != 4 || ps != 2 {
		t.Fatalf("lora profile: expected k=4 m=2, got k=%d m=%d", ds, ps)
	}
	if !il || ild != 8 {
		t.Fatalf("lora profile: expected interleave=true depth=8, got %v %d", il, ild)
	}
}

func TestResolveFECParamsAutoProfile(t *testing.T) {
	params := map[string]string{"profile": "auto", "channel": "astrocast"}
	ds, ps, il, ild := resolveFECParams(params)
	if ds != 3 || ps != 2 {
		t.Fatalf("astrocast auto: expected k=3 m=2, got k=%d m=%d", ds, ps)
	}
	if !il || ild != 4 {
		t.Fatalf("astrocast auto: expected interleave=true depth=4, got %v %d", il, ild)
	}
}

func TestResolveFECParamsNoFECProfile(t *testing.T) {
	params := map[string]string{"profile": "tcp"}
	ds, ps, _, _ := resolveFECParams(params)
	if ds != 0 || ps != 0 {
		t.Fatalf("tcp profile: expected k=0 m=0, got k=%d m=%d", ds, ps)
	}
}

func TestResolveFECParamsExplicitOverride(t *testing.T) {
	params := map[string]string{"data_shards": "8", "parity_shards": "4"}
	ds, ps, il, _ := resolveFECParams(params)
	if ds != 8 || ps != 4 {
		t.Fatalf("explicit: expected k=8 m=4, got k=%d m=%d", ds, ps)
	}
	if il {
		t.Fatal("explicit without interleave flag should not interleave")
	}
}

func TestResolveFECParamsExplicitWithInterleave(t *testing.T) {
	params := map[string]string{
		"data_shards": "4", "parity_shards": "2",
		"interleave": "true", "interleave_depth": "12",
	}
	ds, ps, il, ild := resolveFECParams(params)
	if ds != 4 || ps != 2 {
		t.Fatalf("expected k=4 m=2, got k=%d m=%d", ds, ps)
	}
	if !il || ild != 12 {
		t.Fatalf("expected interleave=true depth=12, got %v %d", il, ild)
	}
}

func TestResolveFECParamsDefault(t *testing.T) {
	// No profile, no explicit params → defaults.
	params := map[string]string{}
	ds, ps, il, _ := resolveFECParams(params)
	if ds != 4 || ps != 2 {
		t.Fatalf("default: expected k=4 m=2, got k=%d m=%d", ds, ps)
	}
	if il {
		t.Fatal("default should not interleave")
	}
}

func TestFECV2RoundTripWithInterleave(t *testing.T) {
	data := []byte("FEC v2 with interleaving test payload for LoRa burst protection.")
	opts := fecEncodeOpts{interleave: true, interleaveDepth: 8}
	encoded, err := fecEncode(data, 4, 2, opts)
	if err != nil {
		t.Fatalf("fecEncode v2: %v", err)
	}

	// Verify v2 header.
	if encoded[0] != FECVersion2 {
		t.Fatalf("expected version 0x%02x, got 0x%02x", FECVersion2, encoded[0])
	}
	flags := encoded[5]
	if flags&fecFlagInterleaved == 0 {
		t.Fatal("interleave flag not set")
	}
	depthEnc := int((flags>>1)&0x0F) + 1
	if depthEnc != 8 {
		t.Fatalf("expected depth 8, got %d", depthEnc)
	}

	decoded, err := fecDecode(encoded)
	if err != nil {
		t.Fatalf("fecDecode v2: %v", err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("v2 round-trip mismatch")
	}
}

func TestFECV2RoundTripNoInterleave(t *testing.T) {
	data := []byte("FEC v2 without interleaving.")
	encoded, err := fecEncode(data, 6, 2)
	if err != nil {
		t.Fatalf("fecEncode v2: %v", err)
	}

	if encoded[0] != FECVersion2 {
		t.Fatalf("expected version 0x%02x, got 0x%02x", FECVersion2, encoded[0])
	}
	if encoded[5] != 0 {
		t.Fatalf("expected flags 0, got 0x%02x", encoded[5])
	}

	decoded, err := fecDecode(encoded)
	if err != nil {
		t.Fatalf("fecDecode: %v", err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("round-trip mismatch")
	}
}

func TestFECV2InterleaveDepthEncoding(t *testing.T) {
	data := make([]byte, 200)
	rand.Read(data)

	// Test all valid depths 1-16.
	for depth := 1; depth <= 16; depth++ {
		opts := fecEncodeOpts{interleave: true, interleaveDepth: depth}
		encoded, err := fecEncode(data, 4, 2, opts)
		if err != nil {
			t.Fatalf("depth %d: encode: %v", depth, err)
		}

		decoded, err := fecDecode(encoded)
		if err != nil {
			t.Fatalf("depth %d: decode: %v", depth, err)
		}
		if !bytes.Equal(decoded, data) {
			t.Fatalf("depth %d: round-trip mismatch", depth)
		}
	}
}

func TestFECProfilePipelineIntegration(t *testing.T) {
	tp := NewTransformPipeline()

	// Use profile-based FEC (lora profile includes interleaving).
	transforms := []TransformSpec{
		{Type: "fec", Params: map[string]string{"profile": "lora"}},
		{Type: "base64"},
	}
	jsonBytes, _ := json.Marshal(transforms)
	jsonStr := string(jsonBytes)

	data := []byte("Pipeline test with FEC profile and interleaving.")

	encoded, err := tp.ApplyEgress(data, jsonStr)
	if err != nil {
		t.Fatalf("ApplyEgress: %v", err)
	}

	decoded, err := tp.ApplyIngress(encoded, jsonStr)
	if err != nil {
		t.Fatalf("ApplyIngress: %v", err)
	}

	if !bytes.Equal(decoded, data) {
		t.Fatalf("profile pipeline mismatch: got %q, want %q", decoded, data)
	}

	if tp.fecMetrics.EncodeOK.Load() != 1 {
		t.Fatalf("expected 1 encode, got %d", tp.fecMetrics.EncodeOK.Load())
	}
}

func TestFECAutoProfilePipeline(t *testing.T) {
	tp := NewTransformPipeline()

	// Auto profile with channel hint.
	transforms := []TransformSpec{
		{Type: "fec", Params: map[string]string{"profile": "auto", "channel": "ax25"}},
	}
	jsonBytes, _ := json.Marshal(transforms)
	jsonStr := string(jsonBytes)

	data := []byte("AX.25 auto-profile test with 43% redundancy + interleave depth 16.")

	encoded, err := tp.ApplyEgress(data, jsonStr)
	if err != nil {
		t.Fatalf("ApplyEgress: %v", err)
	}

	decoded, err := tp.ApplyIngress(encoded, jsonStr)
	if err != nil {
		t.Fatalf("ApplyIngress: %v", err)
	}

	if !bytes.Equal(decoded, data) {
		t.Fatal("auto profile pipeline mismatch")
	}
}

func TestFECSkippedForTCPProfile(t *testing.T) {
	tp := NewTransformPipeline()

	transforms := []TransformSpec{
		{Type: "fec", Params: map[string]string{"profile": "tcp"}},
	}
	jsonBytes, _ := json.Marshal(transforms)
	jsonStr := string(jsonBytes)

	data := []byte("TCP needs no FEC")

	encoded, err := tp.ApplyEgress(data, jsonStr)
	if err != nil {
		t.Fatalf("ApplyEgress: %v", err)
	}

	// Should be passthrough — no encoding.
	if !bytes.Equal(encoded, data) {
		t.Fatal("TCP profile should skip FEC encoding")
	}

	// Encode count should be 0.
	if tp.fecMetrics.EncodeOK.Load() != 0 {
		t.Fatalf("expected 0 encodes for TCP, got %d", tp.fecMetrics.EncodeOK.Load())
	}
}

func TestFECProfileValidateTransforms(t *testing.T) {
	// Profile-based FEC on LoRa (230B MTU).
	transforms := `[{"type":"fec","params":{"profile":"lora"}},{"type":"base64"}]`
	warnings, errors := ValidateTransforms(transforms, true, 230)
	if len(errors) > 0 {
		t.Fatalf("unexpected errors: %v", errors)
	}
	t.Logf("warnings: %v", warnings)
}

func TestAdaptFECProfile(t *testing.T) {
	base := FECProfile{DataShards: 4, ParityShards: 2, Interleave: true, InterleaveDepth: 8}

	// Healthy channel — no change.
	adapted := AdaptFECProfile(base, 90)
	if adapted.ParityShards != 2 {
		t.Fatalf("healthy: expected m=2, got m=%d", adapted.ParityShards)
	}

	// Degraded channel — +1 parity.
	adapted = AdaptFECProfile(base, 60)
	if adapted.ParityShards != 3 {
		t.Fatalf("degraded: expected m=3, got m=%d", adapted.ParityShards)
	}

	// Poor channel — +2 parity.
	adapted = AdaptFECProfile(base, 30)
	if adapted.ParityShards != 4 {
		t.Fatalf("poor: expected m=4, got m=%d", adapted.ParityShards)
	}

	// Zero health.
	adapted = AdaptFECProfile(base, 0)
	if adapted.ParityShards != 4 {
		t.Fatalf("zero health: expected m=4, got m=%d", adapted.ParityShards)
	}

	// No-FEC profile unchanged.
	noFEC := FECProfile{DataShards: 0, ParityShards: 0}
	adapted = AdaptFECProfile(noFEC, 10)
	if adapted.DataShards != 0 || adapted.ParityShards != 0 {
		t.Fatal("no-FEC profile should not change")
	}

	// Interleave preserved.
	adapted = AdaptFECProfile(base, 40)
	if !adapted.Interleave || adapted.InterleaveDepth != 8 {
		t.Fatal("interleave settings should be preserved")
	}
}

func BenchmarkFECV2EncodeInterleave(b *testing.B) {
	data := make([]byte, 300)
	rand.Read(data)
	opts := fecEncodeOpts{interleave: true, interleaveDepth: 8}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fecEncode(data, 4, 2, opts)
	}
}

func BenchmarkFECV2DecodeInterleave(b *testing.B) {
	data := make([]byte, 300)
	rand.Read(data)
	opts := fecEncodeOpts{interleave: true, interleaveDepth: 8}
	encoded, _ := fecEncode(data, 4, 2, opts)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fecDecode(encoded)
	}
}
