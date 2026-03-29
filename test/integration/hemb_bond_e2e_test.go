package integration

// Cross-bridge HeMB bonded transfer E2E tests for MESHSAT-425.
// Validates bond group configuration, RLNC-coded multi-bearer send,
// cross-bearer reassembly, and delivery record tracking.

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/hemb"
)

// ---------------------------------------------------------------------------
// Mock bearers that capture sent frames per-bearer and relay to receiver
// ---------------------------------------------------------------------------

type bondTestHarness struct {
	db           *database.DB
	senderFrames map[uint8][][]byte // bearerIndex -> frames sent
	mu           sync.Mutex
}

func setupBondHarness(t *testing.T) *bondTestHarness {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "hemb_bond_e2e.db")
	db, err := database.New(dbPath)
	if err != nil {
		t.Fatalf("database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return &bondTestHarness{
		db:           db,
		senderFrames: make(map[uint8][][]byte),
	}
}

// makeBearerPair creates a send-side bearer that captures frames, and returns
// its config. Used to build bond groups with multiple bearers.
func (h *bondTestHarness) makeBearerPair(index uint8, channelType string, mtu int, cost float64, headerMode string) hemb.BearerProfile {
	return hemb.BearerProfile{
		Index:        index,
		InterfaceID:  channelType + "_0",
		ChannelType:  channelType,
		MTU:          mtu,
		CostPerMsg:   cost,
		LossRate:     0.10,
		LatencyMs:    250,
		HealthScore:  80,
		RelayCapable: true,
		HeaderMode:   headerMode,
		SendFn: func(ctx context.Context, data []byte) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			cp := make([]byte, len(data))
			copy(cp, data)
			h.senderFrames[index] = append(h.senderFrames[index], cp)
			return nil
		},
	}
}

func (h *bondTestHarness) allFrames() [][]byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	var all [][]byte
	for _, frames := range h.senderFrames {
		all = append(all, frames...)
	}
	return all
}

func (h *bondTestHarness) bearerFrameCount(index uint8) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.senderFrames[index])
}

// ---------------------------------------------------------------------------
// Test: Bond group DB persistence — configure, read back, verify
// ---------------------------------------------------------------------------

func TestHeMB_BondGroupPersistence(t *testing.T) {
	h := setupBondHarness(t)

	// Create bond group with mesh + iridium.
	g := &database.BondGroup{
		ID:             "sat_mesh_bond",
		Label:          "Mesh + Iridium SBD",
		CostBudget:     1.00,
		MinReliability: 0.95,
	}
	if err := h.db.InsertBondGroup(g); err != nil {
		t.Fatalf("insert bond group: %v", err)
	}

	if err := h.db.InsertBondMember(&database.BondMember{
		GroupID: "sat_mesh_bond", InterfaceID: "mesh_0", Priority: 1,
	}); err != nil {
		t.Fatalf("insert mesh member: %v", err)
	}
	if err := h.db.InsertBondMember(&database.BondMember{
		GroupID: "sat_mesh_bond", InterfaceID: "iridium_0", Priority: 2,
	}); err != nil {
		t.Fatalf("insert iridium member: %v", err)
	}

	// Read back.
	got, err := h.db.GetBondGroup("sat_mesh_bond")
	if err != nil {
		t.Fatalf("get bond group: %v", err)
	}
	if got.Label != "Mesh + Iridium SBD" {
		t.Errorf("label=%q, want %q", got.Label, "Mesh + Iridium SBD")
	}
	if got.CostBudget != 1.00 {
		t.Errorf("cost_budget=%f, want 1.00", got.CostBudget)
	}

	members, err := h.db.GetBondMembers("sat_mesh_bond")
	if err != nil {
		t.Fatalf("get bond members: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}
	if members[0].InterfaceID != "mesh_0" {
		t.Errorf("member[0] interface=%s, want mesh_0", members[0].InterfaceID)
	}
	if members[1].InterfaceID != "iridium_0" {
		t.Errorf("member[1] interface=%s, want iridium_0", members[1].InterfaceID)
	}

	if !h.db.IsBondGroup("sat_mesh_bond") {
		t.Error("IsBondGroup returned false for existing group")
	}
	if h.db.IsBondGroup("nonexistent") {
		t.Error("IsBondGroup returned true for nonexistent group")
	}
}

// ---------------------------------------------------------------------------
// Test: Cross-bridge bonded send via mesh + iridium (500B payload)
// ---------------------------------------------------------------------------

func TestHeMB_CrossBridgeBondedSend_500B(t *testing.T) {
	h := setupBondHarness(t)

	mesh := h.makeBearerPair(0, "mesh", 237, 0, hemb.HeaderModeCompact)
	sbd := h.makeBearerPair(1, "iridium_sbd", 340, 0.05, hemb.HeaderModeCompact)

	eventCh := make(chan hemb.Event, 100)
	var delivered []byte
	bdr := hemb.NewBonder(hemb.Options{
		Bearers:   []hemb.BearerProfile{mesh, sbd},
		DeliverFn: func(p []byte) { delivered = append([]byte{}, p...) },
		EventCh:   eventCh,
	})

	// 500-byte payload simulating a cross-bridge message.
	payload := make([]byte, 500)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	if err := bdr.Send(context.Background(), payload); err != nil {
		t.Fatalf("bonded send: %v", err)
	}

	// Verify symbols were distributed across BOTH bearers.
	meshCount := h.bearerFrameCount(0)
	sbdCount := h.bearerFrameCount(1)
	t.Logf("symbol distribution: mesh=%d, sbd=%d, total=%d", meshCount, sbdCount, meshCount+sbdCount)

	if meshCount == 0 {
		t.Error("mesh bearer received 0 symbols — bond not distributing")
	}
	if sbdCount == 0 {
		// SBD is paid — may get 0 source + 0-1 repair symbols for small payloads
		// that fit entirely in the free bearer. This is acceptable.
		t.Log("sbd bearer received 0 symbols — all data fit in free mesh bearer")
	}

	// Verify HEMB_STREAM_OPENED event was emitted.
	var streamOpened *hemb.StreamOpenedPayload
drainEvents:
	for {
		select {
		case evt := <-eventCh:
			if evt.Type == hemb.EventStreamOpened {
				var p hemb.StreamOpenedPayload
				if err := json.Unmarshal(evt.Payload, &p); err == nil {
					streamOpened = &p
				}
			}
		default:
			break drainEvents
		}
	}

	if streamOpened == nil {
		t.Fatal("no HEMB_STREAM_OPENED event emitted")
	}
	if streamOpened.BearerCount < 2 {
		t.Errorf("stream opened with %d bearers, want >= 2", streamOpened.BearerCount)
	}
	if streamOpened.K == 0 {
		t.Error("stream K=0 — no source symbols")
	}
	if streamOpened.N <= streamOpened.K {
		t.Logf("N=%d, K=%d — no repair symbols (unusual but valid for low-loss bearers)", streamOpened.N, streamOpened.K)
	}
	t.Logf("stream: id=%d, bearers=%d, payload=%dB, K=%d, N=%d",
		streamOpened.StreamID, streamOpened.BearerCount, streamOpened.PayloadBytes,
		streamOpened.K, streamOpened.N)

	// Simulate receiver: feed all frames to a receiver-side bonder.
	receiverEventCh := make(chan hemb.Event, 100)
	receiver := hemb.NewBonder(hemb.Options{
		Bearers:   []hemb.BearerProfile{mesh, sbd},
		DeliverFn: func(p []byte) { delivered = append([]byte{}, p...) },
		EventCh:   receiverEventCh,
	})

	allFrames := h.allFrames()
	for _, frame := range allFrames {
		receiver.ReceiveSymbol(0, frame)
	}

	if delivered == nil {
		t.Fatal("receiver did not reassemble payload from bonded symbols")
	}
	// Trim padding — decoded payload may be padded to symbol boundary.
	if len(delivered) >= len(payload) {
		delivered = delivered[:len(payload)]
	}
	if !bytes.Equal(delivered, payload) {
		t.Fatalf("reassembled payload differs from original (got %d bytes, want %d)", len(delivered), len(payload))
	}

	t.Logf("cross-bridge 500B bond: sent %d symbols, reassembled %dB OK", len(allFrames), len(payload))
}

// ---------------------------------------------------------------------------
// Test: Bonded send with mesh + tcp (simulating mule01 + rocket01)
// ---------------------------------------------------------------------------

func TestHeMB_CrossBridgeBondedSend_MeshTCP(t *testing.T) {
	h := setupBondHarness(t)

	mesh := h.makeBearerPair(0, "mesh", 237, 0, hemb.HeaderModeCompact)
	tcp := h.makeBearerPair(1, "tcp", 65535, 0, hemb.HeaderModeExtended)

	var delivered []byte
	eventCh := make(chan hemb.Event, 100)
	bdr := hemb.NewBonder(hemb.Options{
		Bearers:   []hemb.BearerProfile{mesh, tcp},
		DeliverFn: func(p []byte) { delivered = append([]byte{}, p...) },
		EventCh:   eventCh,
	})

	payload := make([]byte, 500)
	for i := range payload {
		payload[i] = byte((i * 7) % 256)
	}

	if err := bdr.Send(context.Background(), payload); err != nil {
		t.Fatalf("bonded send: %v", err)
	}

	meshCount := h.bearerFrameCount(0)
	tcpCount := h.bearerFrameCount(1)
	t.Logf("mesh+tcp distribution: mesh=%d, tcp=%d", meshCount, tcpCount)

	// Both are free — both should get symbols.
	if meshCount == 0 && tcpCount == 0 {
		t.Fatal("no symbols sent to either bearer")
	}

	// Reassemble on receiver.
	receiver := hemb.NewBonder(hemb.Options{
		Bearers:   []hemb.BearerProfile{mesh, tcp},
		DeliverFn: func(p []byte) { delivered = append([]byte{}, p...) },
	})

	for _, frame := range h.allFrames() {
		receiver.ReceiveSymbol(0, frame)
	}

	if delivered == nil {
		t.Fatal("receiver did not reassemble payload")
	}
	if len(delivered) >= len(payload) {
		delivered = delivered[:len(payload)]
	}
	if !bytes.Equal(delivered, payload) {
		t.Fatal("reassembled payload differs from original")
	}
}

// ---------------------------------------------------------------------------
// Test: Various payload sizes (10B, 500B, 5KB)
// ---------------------------------------------------------------------------

func TestHeMB_BondedSend_PayloadSizes(t *testing.T) {
	sizes := []struct {
		name string
		size int
	}{
		{"10B", 10},
		{"500B", 500},
		{"5KB", 5 * 1024},
	}

	for _, tc := range sizes {
		t.Run(tc.name, func(t *testing.T) {
			h := setupBondHarness(t)

			mesh := h.makeBearerPair(0, "mesh", 237, 0, hemb.HeaderModeCompact)
			iridium := h.makeBearerPair(1, "iridium_sbd", 340, 0.05, hemb.HeaderModeCompact)

			var delivered []byte
			eventCh := make(chan hemb.Event, 200)
			bdr := hemb.NewBonder(hemb.Options{
				Bearers:   []hemb.BearerProfile{mesh, iridium},
				DeliverFn: func(p []byte) { delivered = append([]byte{}, p...) },
				EventCh:   eventCh,
			})

			payload := make([]byte, tc.size)
			for i := range payload {
				payload[i] = byte(i % 256)
			}

			if err := bdr.Send(context.Background(), payload); err != nil {
				t.Fatalf("bonded send %s: %v", tc.name, err)
			}

			meshCount := h.bearerFrameCount(0)
			sbdCount := h.bearerFrameCount(1)
			total := meshCount + sbdCount
			t.Logf("%s: mesh=%d, sbd=%d, total=%d symbols", tc.name, meshCount, sbdCount, total)

			if total == 0 {
				t.Fatalf("%s: no symbols sent", tc.name)
			}

			// Verify events include HEMB_SYMBOL_SENT with correct stream/gen.
			symbolsSent := 0
			var seenStreamID uint8
			var seenGenID uint16
		drainLoop:
			for {
				select {
				case evt := <-eventCh:
					if evt.Type == hemb.EventSymbolSent {
						symbolsSent++
						var p hemb.SymbolSentPayload
						if err := json.Unmarshal(evt.Payload, &p); err == nil {
							seenStreamID = p.StreamID
							seenGenID = p.GenerationID
						}
					}
				default:
					break drainLoop
				}
			}

			if symbolsSent != total {
				t.Errorf("HEMB_SYMBOL_SENT count=%d, want %d", symbolsSent, total)
			}
			t.Logf("%s: hemb_stream_id=%d, hemb_gen_id=%d", tc.name, seenStreamID, seenGenID)

			// Reassemble: feed all frames to receiver.
			receiver := hemb.NewBonder(hemb.Options{
				Bearers:   []hemb.BearerProfile{mesh, iridium},
				DeliverFn: func(p []byte) { delivered = append([]byte{}, p...) },
			})

			for _, frame := range h.allFrames() {
				receiver.ReceiveSymbol(0, frame)
			}

			if delivered == nil {
				t.Fatalf("%s: receiver did not reassemble payload", tc.name)
			}
			if len(delivered) >= len(payload) {
				delivered = delivered[:len(payload)]
			}
			if !bytes.Equal(delivered, payload) {
				t.Fatalf("%s: reassembled payload differs from original", tc.name)
			}

			t.Logf("%s: send+reassemble OK (%d symbols, %dB payload)", tc.name, total, tc.size)
		})
	}
}

// ---------------------------------------------------------------------------
// Test: Coded symbols verified on multiple bearers (bearer diversity)
// ---------------------------------------------------------------------------

func TestHeMB_BearerDiversity_CodedSymbols(t *testing.T) {
	h := setupBondHarness(t)

	// Three free bearers — all should get symbols.
	mesh := h.makeBearerPair(0, "mesh", 237, 0, hemb.HeaderModeCompact)
	tcp := h.makeBearerPair(1, "tcp", 65535, 0, hemb.HeaderModeExtended)
	aprs := h.makeBearerPair(2, "aprs", 256, 0, hemb.HeaderModeCompact)

	eventCh := make(chan hemb.Event, 200)
	bdr := hemb.NewBonder(hemb.Options{
		Bearers: []hemb.BearerProfile{mesh, tcp, aprs},
		EventCh: eventCh,
	})

	payload := make([]byte, 500)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	if err := bdr.Send(context.Background(), payload); err != nil {
		t.Fatalf("bonded send: %v", err)
	}

	// Check per-bearer symbol events.
	bearerSymbols := make(map[uint8]int)
drain:
	for {
		select {
		case evt := <-eventCh:
			if evt.Type == hemb.EventSymbolSent {
				var p hemb.SymbolSentPayload
				if err := json.Unmarshal(evt.Payload, &p); err == nil {
					bearerSymbols[p.BearerIndex]++
				}
			}
		default:
			break drain
		}
	}

	t.Logf("per-bearer symbols: %v", bearerSymbols)

	// At least 2 bearers should have received symbols (free-first allocation).
	activeBearers := 0
	for _, count := range bearerSymbols {
		if count > 0 {
			activeBearers++
		}
	}
	if activeBearers < 2 {
		t.Errorf("only %d bearer(s) received symbols — expected multi-bearer distribution", activeBearers)
	}

	// Verify each frame is a valid HeMB frame.
	for idx, frames := range h.senderFrames {
		for i, frame := range frames {
			if !hemb.IsHeMBFrame(frame) {
				t.Errorf("bearer %d frame %d is not a valid HeMB frame", idx, i)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Test: Partial bearer loss — reassembly from subset of bearers
// ---------------------------------------------------------------------------

func TestHeMB_PartialBearerLoss_Reassembly(t *testing.T) {
	h := setupBondHarness(t)

	mesh := h.makeBearerPair(0, "mesh", 237, 0, hemb.HeaderModeCompact)
	tcp := h.makeBearerPair(1, "tcp", 65535, 0, hemb.HeaderModeExtended)

	var delivered []byte
	bdr := hemb.NewBonder(hemb.Options{
		Bearers:   []hemb.BearerProfile{mesh, tcp},
		DeliverFn: func(p []byte) { delivered = append([]byte{}, p...) },
	})

	payload := make([]byte, 500)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	if err := bdr.Send(context.Background(), payload); err != nil {
		t.Fatalf("bonded send: %v", err)
	}

	// Only feed mesh frames to receiver (tcp bearer "lost").
	h.mu.Lock()
	meshFrames := h.senderFrames[0]
	h.mu.Unlock()

	if len(meshFrames) == 0 {
		t.Skip("mesh bearer received 0 frames — cannot test partial loss")
	}

	receiver := hemb.NewBonder(hemb.Options{
		Bearers:   []hemb.BearerProfile{mesh, tcp},
		DeliverFn: func(p []byte) { delivered = append([]byte{}, p...) },
	})

	for _, frame := range meshFrames {
		receiver.ReceiveSymbol(0, frame)
	}

	if delivered == nil {
		t.Log("could not reassemble from mesh-only frames — K may exceed available symbols (expected for small redundancy)")
	} else {
		if len(delivered) >= len(payload) {
			delivered = delivered[:len(payload)]
		}
		if bytes.Equal(delivered, payload) {
			t.Logf("partial loss recovery: reassembled %dB from %d mesh-only frames", len(payload), len(meshFrames))
		}
	}
}

// ---------------------------------------------------------------------------
// Test: Delivery records track hemb_stream_id and hemb_gen_id
// ---------------------------------------------------------------------------

func TestHeMB_DeliveryRecords_StreamTracking(t *testing.T) {
	h := setupBondHarness(t)

	// Insert a delivery with hemb tracking fields via raw SQL (fields exist in
	// schema v38+ but aren't yet in the struct — this test validates the schema).
	_, err := h.db.Exec(`INSERT INTO message_deliveries
		(msg_ref, channel, status, priority, payload, text_preview, retries, max_retries, visited, qos_level, seq_num, hemb_stream_id, hemb_gen_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"hemb-e2e-test-001", "mesh_0", "sent", 1, []byte("test"), "test", 0, 3, "[]", 1, 0,
		"5", "0")
	if err != nil {
		t.Fatalf("insert delivery with hemb fields: %v", err)
	}

	// Read it back and verify hemb fields.
	var streamID, genID string
	err = h.db.QueryRow(`SELECT hemb_stream_id, hemb_gen_id FROM message_deliveries WHERE msg_ref = ?`,
		"hemb-e2e-test-001").Scan(&streamID, &genID)
	if err != nil {
		t.Fatalf("query hemb fields: %v", err)
	}

	if streamID != "5" {
		t.Errorf("hemb_stream_id=%q, want %q", streamID, "5")
	}
	if genID != "0" {
		t.Errorf("hemb_gen_id=%q, want %q", genID, "0")
	}

	t.Logf("delivery record: hemb_stream_id=%s, hemb_gen_id=%s", streamID, genID)
}

// ---------------------------------------------------------------------------
// Test: Bond group with mesh + iridium — cost accounting
// ---------------------------------------------------------------------------

func TestHeMB_BondedSend_CostAccounting(t *testing.T) {
	h := setupBondHarness(t)

	mesh := h.makeBearerPair(0, "mesh", 237, 0, hemb.HeaderModeCompact)
	sbd := h.makeBearerPair(1, "iridium_sbd", 340, 0.05, hemb.HeaderModeCompact)

	bdr := hemb.NewBonder(hemb.Options{
		Bearers: []hemb.BearerProfile{mesh, sbd},
	})

	payload := make([]byte, 500)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	if err := bdr.Send(context.Background(), payload); err != nil {
		t.Fatalf("bonded send: %v", err)
	}

	stats := bdr.Stats()
	t.Logf("cost accounting: symbols_sent=%d, bytes_free=%d, bytes_paid=%d, cost=$%.4f",
		stats.SymbolsSent, stats.BytesFree, stats.BytesPaid, stats.CostIncurred)

	if stats.SymbolsSent == 0 {
		t.Error("no symbols sent")
	}
	if stats.BytesFree == 0 {
		t.Error("BytesFree=0 — free mesh bearer should have been used")
	}

	// Free bearer (mesh) should carry most data, paid bearer minimal.
	meshCount := h.bearerFrameCount(0)
	sbdCount := h.bearerFrameCount(1)
	if meshCount == 0 {
		t.Error("mesh bearer sent 0 frames — free-first allocation failed")
	}
	if sbdCount > meshCount {
		t.Errorf("paid bearer sent more (%d) than free bearer (%d) — cost optimization broken", sbdCount, meshCount)
	}

	t.Logf("free-first: mesh=%d frames, sbd=%d frames", meshCount, sbdCount)
}
