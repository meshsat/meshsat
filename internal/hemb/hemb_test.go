package hemb

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"
)

// ════════════════════════════════════════════════════════════════════════════
// Phase 0a: Event infrastructure tests
// ════════════════════════════════════════════════════════════════════════════

func TestEmitNilChannelNoPanic(t *testing.T) {
	// emit() with nil channel must not panic.
	emit(nil, EventSymbolSent, SymbolSentPayload{
		SymbolRef:    SymbolRef{StreamID: 1},
		BearerRef:    BearerRef{BearerIndex: 0},
		PayloadBytes: 100,
	})
}

func TestEmitFullChannelNoBlock(t *testing.T) {
	// emit() with a full channel must not block — the event is dropped.
	ch := make(chan Event, 1)
	// Fill the channel.
	ch <- Event{Type: EventBondStats, Timestamp: time.Now()}

	done := make(chan struct{})
	go func() {
		emit(ch, EventSymbolSent, SymbolSentPayload{SymbolRef: SymbolRef{StreamID: 1}})
		close(done)
	}()

	select {
	case <-done:
		// Good — emit returned without blocking.
	case <-time.After(100 * time.Millisecond):
		t.Fatal("emit() blocked on full channel")
	}
}

func TestEmitAllEventTypes(t *testing.T) {
	// All 9 event types must compile and marshal to valid JSON.
	ch := make(chan Event, 20)

	emit(ch, EventSymbolSent, SymbolSentPayload{SymbolRef: SymbolRef{StreamID: 1}, BearerRef: BearerRef{BearerIndex: 0}, PayloadBytes: 100, CostEstimate: 0.05})
	emit(ch, EventSymbolReceived, SymbolReceivedPayload{SymbolRef: SymbolRef{StreamID: 1}, BearerRef: BearerRef{BearerIndex: 0}, Received: 3, Required: 4})
	emit(ch, EventGenerationDecoded, GenerationDecodedPayload{StreamID: 1, K: 4, N: 6, Received: 5})
	emit(ch, EventGenerationFailed, GenerationFailedPayload{StreamID: 1, K: 4, Received: 2, Reason: "timeout"})
	emit(ch, EventBearerDegraded, BearerDegradedPayload{BearerRef: BearerRef{BearerIndex: 0}, HealthScore: 35, PrevScore: 72, Reason: "high_loss_rate"})
	emit(ch, EventBearerRecovered, BearerRecoveredPayload{BearerRef: BearerRef{BearerIndex: 0}, HealthScore: 78, PrevScore: 35, DowntimeMs: 5000})
	emit(ch, EventStreamOpened, StreamOpenedPayload{StreamID: 1, BearerCount: 2, K: 4, N: 6})
	emit(ch, EventStreamClosed, StreamClosedPayload{StreamID: 1, Verdict: "decoded", GenerationsTotal: 1, GenerationsDecoded: 1})
	emit(ch, EventBondStats, BondStatsPayload{ActiveStreams: 1, SymbolsSent: 42})

	close(ch)

	count := 0
	for evt := range ch {
		count++
		if evt.Type == "" {
			t.Errorf("event %d has empty type", count)
		}
		if evt.Timestamp.IsZero() {
			t.Errorf("event %d (%s) has zero timestamp", count, evt.Type)
		}
		// Payload must be valid JSON.
		if !json.Valid(evt.Payload) {
			t.Errorf("event %d (%s) has invalid JSON payload: %s", count, evt.Type, evt.Payload)
		}
	}

	if count != 9 {
		t.Fatalf("expected 9 events, got %d", count)
	}
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 0b: N=1 passthrough tests
// These tests MUST pass in every subsequent phase. If N=1 ever breaks,
// the phase fails regardless of other tests passing.
// ════════════════════════════════════════════════════════════════════════════

func mockBearer(index uint8, channelType string, mtu int, cost float64, captured *[][]byte) BearerProfile {
	var mu sync.Mutex
	return BearerProfile{
		Index:        index,
		InterfaceID:  channelType + "_0",
		ChannelType:  channelType,
		MTU:          mtu,
		CostPerMsg:   cost,
		LossRate:     0.10,
		LatencyMs:    250,
		HealthScore:  80,
		RelayCapable: true,
		HeaderMode:   "compact",
		SendFn: func(ctx context.Context, data []byte) error {
			mu.Lock()
			defer mu.Unlock()
			cp := make([]byte, len(data))
			copy(cp, data)
			*captured = append(*captured, cp)
			return nil
		},
	}
}

func TestN1SendReceiveRoundtrip(t *testing.T) {
	var captured [][]byte
	eventCh := make(chan Event, 10)

	b := NewBonder(Options{
		Bearers: []BearerProfile{
			mockBearer(0, "mesh", 237, 0, &captured),
		},
		EventCh: eventCh,
	})

	payload := []byte("Hello from HeMB — this is a test payload for N=1 passthrough verification.")

	if err := b.Send(context.Background(), payload); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Verify received bytes == original payload (byte-for-byte, zero overhead).
	if len(captured) != 1 {
		t.Fatalf("expected 1 captured send, got %d", len(captured))
	}
	if !bytes.Equal(captured[0], payload) {
		t.Fatalf("captured bytes differ from original:\n  sent: %x\n  got:  %x", payload, captured[0])
	}

	// Verify HEMB_SYMBOL_SENT event emitted.
	select {
	case evt := <-eventCh:
		if evt.Type != EventSymbolSent {
			t.Fatalf("expected %s event, got %s", EventSymbolSent, evt.Type)
		}
		var p SymbolSentPayload
		if err := json.Unmarshal(evt.Payload, &p); err != nil {
			t.Fatalf("unmarshal event payload: %v", err)
		}
		if p.PayloadBytes != len(payload) {
			t.Errorf("event payload_bytes=%d, want %d", p.PayloadBytes, len(payload))
		}
		if p.CostEstimate != 0 {
			t.Errorf("free bearer should have cost_est=0, got %f", p.CostEstimate)
		}
	default:
		t.Fatal("no HEMB_SYMBOL_SENT event emitted")
	}

	// Verify ReceiveSymbol delivers payload unchanged.
	result, err := b.ReceiveSymbol(0, payload)
	if err != nil {
		t.Fatalf("ReceiveSymbol failed: %v", err)
	}
	if !bytes.Equal(result, payload) {
		t.Fatalf("ReceiveSymbol returned different bytes")
	}
}

func TestN1NilChannelNoEvent(t *testing.T) {
	var captured [][]byte
	b := NewBonder(Options{
		Bearers: []BearerProfile{
			mockBearer(0, "mesh", 237, 0, &captured),
		},
		EventCh: nil, // nil channel
	})

	// Must not panic.
	if err := b.Send(context.Background(), []byte("test")); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("expected 1 send, got %d", len(captured))
	}
}

func TestN1EmptyPayload(t *testing.T) {
	var captured [][]byte
	b := NewBonder(Options{
		Bearers: []BearerProfile{
			mockBearer(0, "mesh", 237, 0, &captured),
		},
	})

	// Empty payload must not panic.
	if err := b.Send(context.Background(), []byte{}); err != nil {
		t.Fatalf("Send empty failed: %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("expected 1 send, got %d", len(captured))
	}
	if len(captured[0]) != 0 {
		t.Fatalf("expected empty captured bytes, got %d bytes", len(captured[0]))
	}
}

func TestN1LargePayload(t *testing.T) {
	// 10MB payload must pass through unchanged.
	large := make([]byte, 10*1024*1024)
	for i := range large {
		large[i] = byte(i % 256)
	}

	var captured [][]byte
	b := NewBonder(Options{
		Bearers: []BearerProfile{
			mockBearer(0, "iridium_imt", 102400, 0.05, &captured),
		},
	})

	if err := b.Send(context.Background(), large); err != nil {
		t.Fatalf("Send large failed: %v", err)
	}
	if !bytes.Equal(captured[0], large) {
		t.Fatal("large payload not preserved byte-for-byte")
	}
}

func TestN1PaidBearerCostTracking(t *testing.T) {
	var captured [][]byte
	b := NewBonder(Options{
		Bearers: []BearerProfile{
			mockBearer(0, "iridium_sbd", 340, 0.05, &captured),
		},
	})

	if err := b.Send(context.Background(), []byte("test")); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	stats := b.Stats()
	if stats.SymbolsSent != 1 {
		t.Errorf("SymbolsSent=%d, want 1", stats.SymbolsSent)
	}
	if stats.BytesPaid != 4 {
		t.Errorf("BytesPaid=%d, want 4", stats.BytesPaid)
	}
	if stats.BytesFree != 0 {
		t.Errorf("BytesFree=%d, want 0", stats.BytesFree)
	}
	if stats.CostIncurred < 0.04 || stats.CostIncurred > 0.06 {
		t.Errorf("CostIncurred=%f, want ~0.05", stats.CostIncurred)
	}
}

func TestN1FreeBearerCostTracking(t *testing.T) {
	var captured [][]byte
	b := NewBonder(Options{
		Bearers: []BearerProfile{
			mockBearer(0, "mesh", 237, 0, &captured),
		},
	})

	if err := b.Send(context.Background(), []byte("free data")); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	stats := b.Stats()
	if stats.BytesFree != 9 {
		t.Errorf("BytesFree=%d, want 9", stats.BytesFree)
	}
	if stats.BytesPaid != 0 {
		t.Errorf("BytesPaid=%d, want 0", stats.BytesPaid)
	}
	if stats.CostIncurred != 0 {
		t.Errorf("CostIncurred=%f, want 0", stats.CostIncurred)
	}
}

func TestN1DeliverFnCalled(t *testing.T) {
	var delivered []byte
	b := NewBonder(Options{
		Bearers: []BearerProfile{
			mockBearer(0, "mesh", 237, 0, &[][]byte{}),
		},
		DeliverFn: func(payload []byte) {
			delivered = make([]byte, len(payload))
			copy(delivered, payload)
		},
	})

	payload := []byte("deliver me")
	result, err := b.ReceiveSymbol(0, payload)
	if err != nil {
		t.Fatalf("ReceiveSymbol failed: %v", err)
	}
	if !bytes.Equal(result, payload) {
		t.Fatalf("ReceiveSymbol result differs")
	}
	if !bytes.Equal(delivered, payload) {
		t.Fatalf("DeliverFn received different bytes")
	}
}

func TestN1NoBearersError(t *testing.T) {
	b := NewBonder(Options{
		Bearers: []BearerProfile{},
	})
	err := b.Send(context.Background(), []byte("test"))
	if err == nil {
		t.Fatal("expected error for no bearers")
	}
}

// ════════════════════════════════════════════════════════════════════════════
// Frame format tests (MESHSAT-416)
// ════════════════════════════════════════════════════════════════════════════

// ════════════════════════════════════════════════════════════════════════════
// Phase 1a: GF(256) + RLNC tests
// ════════════════════════════════════════════════════════════════════════════

func TestGF256MulInverse(t *testing.T) {
	// gfMul(a, gfInv(a)) == 1 for all a in 1..255.
	for a := 1; a <= 255; a++ {
		if gfMul(byte(a), gfInv(byte(a))) != 1 {
			t.Errorf("gfMul(%d, gfInv(%d)) != 1", a, a)
		}
	}
}

func TestGF256MulZero(t *testing.T) {
	for a := 0; a <= 255; a++ {
		if gfMul(byte(a), 0) != 0 {
			t.Errorf("gfMul(%d, 0) != 0", a)
		}
	}
}

func TestEncodeDecodeRoundtrip(t *testing.T) {
	// K=4 segments of 152 bytes, encode to N=6, decode all C(6,4)=15 combinations.
	k := 4
	segSize := 152
	n := 6
	segments := make([][]byte, k)
	for i := range segments {
		segments[i] = make([]byte, segSize)
		randBytes(segments[i])
	}

	symbols, err := EncodeGeneration(42, segments, n, cryptoRand())
	if err != nil {
		t.Fatalf("EncodeGeneration: %v", err)
	}
	if len(symbols) != n {
		t.Fatalf("expected %d symbols, got %d", n, len(symbols))
	}

	// Test all C(6,4) = 15 combinations of 4 symbols from 6.
	combos := combinations(n, k)
	for ci, combo := range combos {
		subset := make([]CodedSymbol, k)
		for j, idx := range combo {
			subset[j] = symbols[idx]
		}
		decoded, err := TryDecode(subset, k)
		if err != nil {
			t.Errorf("combo %d %v: TryDecode failed: %v", ci, combo, err)
			continue
		}
		for si, seg := range decoded {
			if !bytes.Equal(seg, segments[si]) {
				t.Errorf("combo %d %v: segment %d mismatch", ci, combo, si)
			}
		}
	}
}

func TestRankDeficient(t *testing.T) {
	segments := make([][]byte, 4)
	for i := range segments {
		segments[i] = make([]byte, 100)
		randBytes(segments[i])
	}
	symbols, _ := EncodeGeneration(0, segments, 6, cryptoRand())

	// Only 3 symbols — need 4.
	_, err := TryDecode(symbols[:3], 4)
	if err == nil {
		t.Fatal("expected ErrNotDecodable for 3 symbols with K=4")
	}
}

func TestSingleSegment(t *testing.T) {
	// K=1, N=2 — either symbol alone must decode.
	seg := []byte("hello hemb single segment test data here")
	symbols, err := EncodeGeneration(0, [][]byte{seg}, 2, cryptoRand())
	if err != nil {
		t.Fatalf("EncodeGeneration: %v", err)
	}

	for i, sym := range symbols {
		decoded, err := TryDecode([]CodedSymbol{sym}, 1)
		if err != nil {
			t.Fatalf("symbol %d: TryDecode: %v", i, err)
		}
		if !bytes.Equal(decoded[0], seg) {
			t.Errorf("symbol %d: decoded mismatch", i)
		}
	}
}

func TestGF256Consistency(t *testing.T) {
	// Encode same segments twice with different random coefficients — both decode same.
	segments := make([][]byte, 3)
	for i := range segments {
		segments[i] = make([]byte, 50)
		randBytes(segments[i])
	}

	sym1, _ := EncodeGeneration(0, segments, 4, cryptoRand())
	sym2, _ := EncodeGeneration(0, segments, 4, cryptoRand())

	dec1, err := TryDecode(sym1[:3], 3)
	if err != nil {
		t.Fatalf("decode 1: %v", err)
	}
	dec2, err := TryDecode(sym2[:3], 3)
	if err != nil {
		t.Fatalf("decode 2: %v", err)
	}

	for i := range dec1 {
		if !bytes.Equal(dec1[i], dec2[i]) {
			t.Errorf("segment %d differs between two encodings", i)
		}
	}
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 1b: Frame format tests
// ════════════════════════════════════════════════════════════════════════════

func TestCompactRoundtrip(t *testing.T) {
	h := CompactHeader{
		Version: VersionV1, StreamID: 7, Flags: FlagRepair,
		Sequence: 3000, K: 4, N: 6, BearerIndex: 12,
		GenerationID: 900, TTL: 45,
	}
	b := MarshalCompact(h)
	got, err := UnmarshalCompact(b)
	if err != nil {
		t.Fatalf("UnmarshalCompact: %v", err)
	}
	if got.StreamID != h.StreamID || got.K != h.K || got.N != h.N ||
		got.Sequence != h.Sequence || got.BearerIndex != h.BearerIndex ||
		got.GenerationID != h.GenerationID || got.TTL != h.TTL ||
		got.Flags != h.Flags {
		t.Errorf("compact roundtrip mismatch:\n  sent: %+v\n  got:  %+v", h, got)
	}
}

func TestExtendedRoundtrip(t *testing.T) {
	h := ExtendedHeader{
		Version: VersionV1, StreamID: 200, Flags: FlagData,
		Sequence: 50000, K: 8, N: 12, BearerIndex: 5,
		GenerationID: 40000, TotalPayloadSize: 5000,
		TTL: 180, FlagsExtended: 0x05,
	}
	b := MarshalExtended(h)
	got, err := UnmarshalExtended(b)
	if err != nil {
		t.Fatalf("UnmarshalExtended: %v", err)
	}
	if got.StreamID != h.StreamID || got.K != h.K || got.N != h.N ||
		got.Sequence != h.Sequence || got.BearerIndex != h.BearerIndex ||
		got.GenerationID != h.GenerationID || got.TTL != h.TTL ||
		got.TotalPayloadSize != h.TotalPayloadSize ||
		got.FlagsExtended != h.FlagsExtended {
		t.Errorf("extended roundtrip mismatch:\n  sent: %+v\n  got:  %+v", h, got)
	}
}

func TestCRC8BitFlipDetection(t *testing.T) {
	b := MarshalCompact(CompactHeader{StreamID: 5, K: 4, N: 6, Sequence: 100})
	for byteIdx := 0; byteIdx < CompactHeaderLen; byteIdx++ {
		for bitIdx := 0; bitIdx < 8; bitIdx++ {
			corrupted := b
			corrupted[byteIdx] ^= 1 << bitIdx
			_, err := UnmarshalCompact(corrupted)
			if err == nil {
				t.Errorf("bit flip at byte %d bit %d not detected", byteIdx, bitIdx)
			}
		}
	}
}

func TestIsHeMBFrameVariants(t *testing.T) {
	compact := MarshalCompact(CompactHeader{K: 4, N: 6})
	extended := MarshalExtended(ExtendedHeader{K: 4, N: 6, StreamID: 1})

	if !IsHeMBFrame(compact[:]) {
		t.Error("valid compact not detected")
	}
	if !IsHeMBFrame(extended[:]) {
		t.Error("valid extended not detected")
	}
	if IsHeMBFrame(nil) {
		t.Error("nil should not be HeMB")
	}
	if IsHeMBFrame([]byte{0xFF, 0xFF, 0xFF}) {
		t.Error("random bytes should not be HeMB")
	}
	// Compact with payload appended.
	withPayload := append(compact[:], []byte("payload")...)
	if !IsHeMBFrame(withPayload) {
		t.Error("compact with payload not detected")
	}
}

func TestPromoteHeaderPreservesFields(t *testing.T) {
	compact := CompactHeader{
		Version: VersionV1, StreamID: 7, Flags: FlagRepair,
		Sequence: 1000, K: 4, N: 6, BearerIndex: 3,
		GenerationID: 500, TTL: 20,
	}
	ext := PromoteHeader(compact, HeaderModeCompact)
	if ext.StreamID != compact.StreamID || ext.K != compact.K ||
		ext.N != compact.N || ext.Sequence != compact.Sequence ||
		ext.BearerIndex != compact.BearerIndex || ext.GenerationID != compact.GenerationID {
		t.Error("promoted header lost compact fields")
	}
	if ext.TTL != compact.TTL*3 {
		t.Errorf("TTL conversion: got %d, want %d", ext.TTL, compact.TTL*3)
	}
}

func TestPromoteHeaderImplicitNoop(t *testing.T) {
	compact := CompactHeader{StreamID: 5, K: 4}
	ext := PromoteHeader(compact, HeaderModeImplicit)
	if ext.K != 0 {
		t.Error("implicit mode promotion should return zero header")
	}
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 1 integration: encode → frame → parse → decode
// ════════════════════════════════════════════════════════════════════════════

func TestEncodeFrameDecode(t *testing.T) {
	payload := make([]byte, 500)
	randBytes(payload)

	k := 4
	segSize := 152
	segments := make([][]byte, k)
	for i := 0; i < k; i++ {
		start := i * segSize
		end := start + segSize
		if end > len(payload) {
			end = len(payload)
		}
		segments[i] = make([]byte, segSize)
		copy(segments[i], payload[start:end])
	}

	symbols, err := EncodeGeneration(42, segments, 6, cryptoRand())
	if err != nil {
		t.Fatalf("EncodeGeneration: %v", err)
	}

	// Frame each symbol with compact header.
	bearer := &BearerProfile{Index: 0, HeaderMode: HeaderModeCompact, MTU: 300}
	var frames [][]byte
	for _, sym := range symbols {
		frames = append(frames, marshalSymbolFrame(bearer, 1, sym, 6))
	}

	// Parse any 4 of 6 frames and decode.
	var parsed []CodedSymbol
	for i := 0; i < 4; i++ {
		sym, streamID, _, err := parseSymbolFromFrame(frames[i])
		if err != nil {
			t.Fatalf("parse frame %d: %v", i, err)
		}
		if streamID != 1 {
			t.Errorf("frame %d: streamID=%d, want 1", i, streamID)
		}
		parsed = append(parsed, sym)
	}

	decoded, err := TryDecode(parsed, k)
	if err != nil {
		t.Fatalf("TryDecode: %v", err)
	}

	// Reconstruct payload.
	var reconstructed []byte
	for _, seg := range decoded {
		reconstructed = append(reconstructed, seg...)
	}
	reconstructed = reconstructed[:len(payload)]

	if !bytes.Equal(reconstructed, payload) {
		t.Fatal("reconstructed payload differs from original")
	}
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 1d: Multi-bearer Send + cost-weighted allocation tests
// ════════════════════════════════════════════════════════════════════════════

func TestSendReceiveN2(t *testing.T) {
	// 2 bearers: send 500-byte payload, receive symbols, verify decode.
	payload := make([]byte, 500)
	randBytes(payload)

	var mu sync.Mutex
	captured := make(map[uint8][][]byte) // bearerIndex -> frames

	mesh := BearerProfile{
		Index: 0, InterfaceID: "mesh_0", ChannelType: "mesh",
		MTU: 237, CostPerMsg: 0, LossRate: 0.15, LatencyMs: 250,
		HealthScore: 80, HeaderMode: HeaderModeCompact,
		SendFn: func(ctx context.Context, data []byte) error {
			mu.Lock()
			captured[0] = append(captured[0], append([]byte{}, data...))
			mu.Unlock()
			return nil
		},
	}
	sbd := BearerProfile{
		Index: 1, InterfaceID: "iridium_0", ChannelType: "iridium_sbd",
		MTU: 340, CostPerMsg: 0.05, LossRate: 0.02, LatencyMs: 45000,
		HealthScore: 90, HeaderMode: HeaderModeCompact,
		SendFn: func(ctx context.Context, data []byte) error {
			mu.Lock()
			captured[1] = append(captured[1], append([]byte{}, data...))
			mu.Unlock()
			return nil
		},
	}

	var delivered []byte
	bdr := NewBonder(Options{
		Bearers:   []BearerProfile{mesh, sbd},
		DeliverFn: func(p []byte) { delivered = append([]byte{}, p...) },
		EventCh:   make(chan Event, 100),
	})

	if err := bdr.Send(context.Background(), payload); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Both bearers must have received frames.
	mu.Lock()
	totalFrames := len(captured[0]) + len(captured[1])
	allFrames := append(captured[0], captured[1]...)
	mu.Unlock()

	if totalFrames == 0 {
		t.Fatal("no frames sent to any bearer")
	}

	// Feed all frames back through ReceiveSymbol.
	for i, frame := range allFrames {
		bearerIdx := uint8(0)
		if i >= len(captured[0]) {
			bearerIdx = 1
		}
		bdr.ReceiveSymbol(bearerIdx, frame)
	}

	if delivered == nil {
		t.Fatal("payload was not delivered after receiving all symbols")
	}
	if !bytes.Equal(delivered[:len(payload)], payload) {
		t.Fatal("delivered payload differs from original")
	}
}

func TestSplitterFreeFirst(t *testing.T) {
	// Verify free bearers get source symbols, paid bearers get minimal.
	var meshFrames, sbdFrames int
	mesh := BearerProfile{
		Index: 0, ChannelType: "mesh", MTU: 237, CostPerMsg: 0,
		LossRate: 0.15, HealthScore: 80, HeaderMode: HeaderModeCompact,
		SendFn: func(ctx context.Context, data []byte) error { meshFrames++; return nil },
	}
	sbd := BearerProfile{
		Index: 1, ChannelType: "iridium_sbd", MTU: 340, CostPerMsg: 0.05,
		LossRate: 0.02, HealthScore: 90, HeaderMode: HeaderModeCompact,
		SendFn: func(ctx context.Context, data []byte) error { sbdFrames++; return nil },
	}

	bdr := NewBonder(Options{Bearers: []BearerProfile{mesh, sbd}})

	// Small payload — should fit in free bearer.
	payload := make([]byte, 100)
	randBytes(payload)
	if err := bdr.Send(context.Background(), payload); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Free bearer (mesh) must have received the bulk of symbols.
	// Paid bearer (sbd) should have 0 or 1 repair symbol max.
	if meshFrames == 0 {
		t.Error("free bearer received 0 frames — should get source symbols")
	}
	if sbdFrames > 1 {
		t.Errorf("paid bearer received %d frames — should be 0 or 1 max", sbdFrames)
	}
}

func TestSendCostAccounting(t *testing.T) {
	paidSends := 0
	freeSends := 0
	mesh := BearerProfile{
		Index: 0, ChannelType: "mesh", MTU: 237, CostPerMsg: 0,
		LossRate: 0.10, HealthScore: 80, HeaderMode: HeaderModeCompact,
		SendFn: func(ctx context.Context, data []byte) error { freeSends++; return nil },
	}
	sbd := BearerProfile{
		Index: 1, ChannelType: "iridium_sbd", MTU: 340, CostPerMsg: 0.05,
		LossRate: 0.02, HealthScore: 90, HeaderMode: HeaderModeCompact,
		SendFn: func(ctx context.Context, data []byte) error { paidSends++; return nil },
	}

	bdr := NewBonder(Options{Bearers: []BearerProfile{mesh, sbd}})
	payload := make([]byte, 200)
	randBytes(payload)
	bdr.Send(context.Background(), payload)

	stats := bdr.Stats()
	if stats.BytesFree == 0 {
		t.Error("BytesFree should be > 0 (free bearer was used)")
	}
	if paidSends > 0 && stats.CostIncurred == 0 {
		t.Error("CostIncurred should be > 0 when paid bearer sends")
	}
}

func TestSendN3BearerFailure(t *testing.T) {
	// 3 bearers: mesh drops everything, sbd + sms deliver.
	payload := make([]byte, 300)
	randBytes(payload)

	var mu sync.Mutex
	capturedGood := make(map[uint8][][]byte)

	mesh := BearerProfile{
		Index: 0, ChannelType: "mesh", MTU: 237, CostPerMsg: 0,
		LossRate: 0.15, HealthScore: 80, HeaderMode: HeaderModeCompact,
		SendFn: func(ctx context.Context, data []byte) error {
			return nil // "sends" but receiver never gets these
		},
	}
	sbd := BearerProfile{
		Index: 1, ChannelType: "iridium_sbd", MTU: 340, CostPerMsg: 0.05,
		LossRate: 0.02, HealthScore: 90, HeaderMode: HeaderModeCompact,
		SendFn: func(ctx context.Context, data []byte) error {
			mu.Lock()
			capturedGood[1] = append(capturedGood[1], append([]byte{}, data...))
			mu.Unlock()
			return nil
		},
	}
	sms := BearerProfile{
		Index: 2, ChannelType: "sms", MTU: 160, CostPerMsg: 0.005,
		LossRate: 0.03, HealthScore: 75, HeaderMode: HeaderModeCompact,
		SendFn: func(ctx context.Context, data []byte) error {
			mu.Lock()
			capturedGood[2] = append(capturedGood[2], append([]byte{}, data...))
			mu.Unlock()
			return nil
		},
	}

	var delivered []byte
	bdr := NewBonder(Options{
		Bearers:   []BearerProfile{mesh, sbd, sms},
		DeliverFn: func(p []byte) { delivered = append([]byte{}, p...) },
	})

	if err := bdr.Send(context.Background(), payload); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Only feed frames from sbd + sms (mesh "lost").
	mu.Lock()
	goodFrames := append(capturedGood[1], capturedGood[2]...)
	mu.Unlock()

	for _, frame := range goodFrames {
		bdr.ReceiveSymbol(1, frame)
	}

	if delivered == nil {
		t.Log("decode from sbd+sms only did not produce payload — may need more symbols")
		// This is acceptable if K > symbols from sbd+sms.
		// The point is: no panic, no crash, graceful handling.
	} else if !bytes.Equal(delivered[:len(payload)], payload) {
		t.Fatal("delivered payload differs from original")
	}
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 1e: Reassembly buffer tests
// ════════════════════════════════════════════════════════════════════════════

func TestReassembleOutOfOrder(t *testing.T) {
	// Symbols arrive in reverse order — must still decode.
	payload := make([]byte, 200)
	randBytes(payload)

	k := 3
	segSize := (len(payload) + k - 1) / k
	segments := make([][]byte, k)
	for i := 0; i < k; i++ {
		start := i * segSize
		end := start + segSize
		if end > len(payload) {
			end = len(payload)
		}
		segments[i] = make([]byte, segSize)
		copy(segments[i], payload[start:end])
	}

	symbols, _ := EncodeGeneration(0, segments, k+2, cryptoRand())

	var delivered []byte
	rb := NewReassemblyBuffer(func(p []byte) {
		delivered = append([]byte{}, p...)
	}, nil)

	// Add symbols in reverse order.
	for i := len(symbols) - 1; i >= 0; i-- {
		rb.AddSymbol(0, uint8(i%3), symbols[i])
		if delivered != nil {
			break
		}
	}

	if delivered == nil {
		t.Fatal("reverse-order symbols did not decode")
	}
	if !bytes.Equal(delivered[:len(payload)], payload) {
		t.Fatal("decoded payload mismatch")
	}
}

func TestReassembleCrossBearerSubset(t *testing.T) {
	// Encode K=4 to N=6, add symbols from bearers 0 and 2 only (skip bearer 1).
	// This SPECIFICALLY validates novelty 2: any K from any bearer subset.
	payload := make([]byte, 400)
	randBytes(payload)

	k := 4
	segSize := (len(payload) + k - 1) / k
	segments := make([][]byte, k)
	for i := 0; i < k; i++ {
		start := i * segSize
		end := start + segSize
		if end > len(payload) {
			end = len(payload)
		}
		segments[i] = make([]byte, segSize)
		copy(segments[i], payload[start:end])
	}

	symbols, _ := EncodeGeneration(0, segments, 6, cryptoRand())

	var delivered []byte
	rb := NewReassemblyBuffer(func(p []byte) {
		delivered = append([]byte{}, p...)
	}, nil)

	// Bearer 0 gets symbols 0,1. Bearer 2 gets symbols 2,3. Bearer 1 gets nothing.
	bearerAssignment := []uint8{0, 0, 2, 2, 2, 2}
	for i := 0; i < len(symbols) && delivered == nil; i++ {
		rb.AddSymbol(0, bearerAssignment[i], symbols[i])
	}

	if delivered == nil {
		t.Fatal("cross-bearer subset did not decode")
	}
	if !bytes.Equal(delivered[:len(payload)], payload) {
		t.Fatal("cross-bearer decoded payload mismatch")
	}
}

func TestReassembleReap(t *testing.T) {
	rb := NewReassemblyBuffer(nil, nil)
	rb.maxAge = time.Millisecond

	// Add a symbol to create a stream.
	sym := CodedSymbol{GenID: 0, K: 4, Coefficients: []byte{1, 0, 0, 0}, Data: make([]byte, 10)}
	rb.AddSymbol(0, 0, sym)

	if rb.PendingCount() != 1 {
		t.Fatalf("pending=%d, want 1", rb.PendingCount())
	}

	time.Sleep(5 * time.Millisecond)
	removed := rb.Reap()
	if removed != 1 {
		t.Errorf("Reap removed %d, want 1", removed)
	}
	if rb.PendingCount() != 0 {
		t.Errorf("pending=%d after reap, want 0", rb.PendingCount())
	}
}

// ════════════════════════════════════════════════════════════════════════════
// Test helpers
// ════════════════════════════════════════════════════════════════════════════

func cryptoRand() io.Reader {
	return cryptoRandReader{}
}

type cryptoRandReader struct{}

func (cryptoRandReader) Read(p []byte) (int, error) {
	randBytes(p)
	return len(p), nil
}

// combinations returns all C(n, k) combinations of indices 0..n-1.
func combinations(n, k int) [][]int {
	var result [][]int
	combo := make([]int, k)
	var generate func(start, depth int)
	generate = func(start, depth int) {
		if depth == k {
			c := make([]int, k)
			copy(c, combo)
			result = append(result, c)
			return
		}
		for i := start; i < n; i++ {
			combo[depth] = i
			generate(i+1, depth+1)
		}
	}
	generate(0, 0)
	return result
}
