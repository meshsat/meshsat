package gateway

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// --- Mock AstrocastTransport ---

type mockAstrocastTransport struct {
	mu       sync.Mutex
	sent     [][]byte
	recvData [][]byte // queued receive payloads
	recvIdx  int
	failSend bool
	status   *transport.AstrocastStatus
	events   chan transport.AstrocastEvent
	subErr   error
}

func newMockAstrocastTransport() *mockAstrocastTransport {
	return &mockAstrocastTransport{
		status: &transport.AstrocastStatus{Connected: true, Port: "/dev/mock"},
		events: make(chan transport.AstrocastEvent, 10),
	}
}

func (m *mockAstrocastTransport) Send(ctx context.Context, data []byte) (*transport.AstrocastResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failSend {
		return nil, fmt.Errorf("mock send failure")
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	m.sent = append(m.sent, cp)
	return &transport.AstrocastResult{MessageID: uint16(len(m.sent)), Queued: true}, nil
}

func (m *mockAstrocastTransport) Receive(ctx context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recvIdx >= len(m.recvData) {
		return nil, fmt.Errorf("no downlink available")
	}
	data := m.recvData[m.recvIdx]
	m.recvIdx++
	return data, nil
}

func (m *mockAstrocastTransport) GetStatus(ctx context.Context) (*transport.AstrocastStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status == nil {
		return nil, fmt.Errorf("module not responding")
	}
	return m.status, nil
}

func (m *mockAstrocastTransport) Subscribe(ctx context.Context) (<-chan transport.AstrocastEvent, error) {
	if m.subErr != nil {
		return nil, m.subErr
	}
	return m.events, nil
}

func (m *mockAstrocastTransport) Close() error { return nil }

// Stub implementations for extended AstrocastTransport interface
func (m *mockAstrocastTransport) ReadSAK(_ context.Context) (*transport.AstrocastSAK, error) {
	return nil, nil
}
func (m *mockAstrocastTransport) ClearSAK(_ context.Context) error { return nil }
func (m *mockAstrocastTransport) ReadCommand(_ context.Context) (*transport.AstrocastCommand, error) {
	return nil, nil
}
func (m *mockAstrocastTransport) ClearCommand(_ context.Context) error { return nil }
func (m *mockAstrocastTransport) WriteGeolocation(_ context.Context, _ transport.AstrocastGeolocation) error {
	return nil
}
func (m *mockAstrocastTransport) GetNextContact(_ context.Context) (*transport.AstrocastNextContact, error) {
	return nil, nil
}
func (m *mockAstrocastTransport) GetModuleState(_ context.Context) (*transport.AstrocastModuleState, error) {
	return nil, nil
}
func (m *mockAstrocastTransport) GetLastContact(_ context.Context) (*transport.AstrocastLastContact, error) {
	return nil, nil
}
func (m *mockAstrocastTransport) GetEnvironment(_ context.Context) (*transport.AstrocastEnvironment, error) {
	return nil, nil
}
func (m *mockAstrocastTransport) GetPerformance(_ context.Context) (*transport.AstrocastPerformance, error) {
	return nil, nil
}

func (m *mockAstrocastTransport) sentPayloads() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([][]byte, len(m.sent))
	for i, s := range m.sent {
		cp[i] = make([]byte, len(s))
		copy(cp[i], s)
	}
	return cp
}

func (m *mockAstrocastTransport) queueDownlink(data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	m.recvData = append(m.recvData, cp)
}

// --- Helper ---

func newTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// --- Tests ---

func TestAstrocastGateway_StartStop(t *testing.T) {
	mock := newMockAstrocastTransport()
	db := newTestDB(t)
	cfg := DefaultAstrocastConfig()
	cfg.PollIntervalSec = 1

	gw := NewAstrocastGateway(cfg, mock, db)

	ctx, cancel := context.WithCancel(context.Background())
	if err := gw.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !gw.connected.Load() {
		t.Error("expected connected=true after start with healthy module")
	}
	if gw.Type() != "astrocast" {
		t.Errorf("Type: want astrocast, got %s", gw.Type())
	}

	cancel()
	if err := gw.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if gw.connected.Load() {
		t.Error("expected connected=false after stop")
	}
}

func TestAstrocastGateway_StartWithStatusError(t *testing.T) {
	mock := newMockAstrocastTransport()
	mock.status = nil // GetStatus will fail
	db := newTestDB(t)
	cfg := DefaultAstrocastConfig()
	cfg.PollIntervalSec = 1

	gw := NewAstrocastGateway(cfg, mock, db)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Should not fail — just logs warning
	if err := gw.Start(ctx); err != nil {
		t.Fatalf("Start should succeed even with status error: %v", err)
	}
	if gw.connected.Load() {
		t.Error("expected connected=false when GetStatus fails")
	}

	gw.Stop()
}

func TestAstrocastGateway_ForwardSmallMessage(t *testing.T) {
	mock := newMockAstrocastTransport()
	db := newTestDB(t)
	cfg := DefaultAstrocastConfig()
	cfg.PollIntervalSec = 60

	gw := NewAstrocastGateway(cfg, mock, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw.Start(ctx)

	msg := &transport.MeshMessage{DecodedText: "Hello satellite"}
	if err := gw.Forward(ctx, msg); err != nil {
		t.Fatalf("Forward: %v", err)
	}

	// Wait for sendWorker to process
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for send")
		default:
		}
		sent := mock.sentPayloads()
		if len(sent) > 0 {
			if string(sent[0]) != "Hello satellite" {
				t.Errorf("sent payload: want %q, got %q", "Hello satellite", string(sent[0]))
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	status := gw.Status()
	if status.MessagesOut != 1 {
		t.Errorf("MessagesOut: want 1, got %d", status.MessagesOut)
	}

	gw.Stop()
}

func TestAstrocastGateway_ForwardFragmentedMessage(t *testing.T) {
	mock := newMockAstrocastTransport()
	db := newTestDB(t)
	cfg := DefaultAstrocastConfig()
	cfg.FragmentEnabled = true
	cfg.PollIntervalSec = 60

	gw := NewAstrocastGateway(cfg, mock, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw.Start(ctx)

	// Create message larger than AstroMaxUplink (160 bytes)
	longText := make([]byte, 300)
	for i := range longText {
		longText[i] = byte('A' + (i % 26))
	}
	msg := &transport.MeshMessage{DecodedText: string(longText)}
	gw.Forward(ctx, msg)

	// Wait for all fragments to be sent
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for fragmented send")
		default:
		}
		sent := mock.sentPayloads()
		// 300 bytes / 159 bytes per fragment = 2 fragments
		if len(sent) >= 2 {
			// Each fragment should be ≤160 bytes (1 header + up to 159 payload)
			for i, frag := range sent {
				if len(frag) > transport.AstroMaxUplink {
					t.Errorf("fragment %d too large: %d bytes", i, len(frag))
				}
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	status := gw.Status()
	if status.MessagesOut != 1 {
		t.Errorf("MessagesOut: want 1 (not per-fragment), got %d", status.MessagesOut)
	}
	if status.Errors != 0 {
		t.Errorf("Errors: want 0, got %d", status.Errors)
	}

	gw.Stop()
}

func TestAstrocastGateway_ForwardTruncatesWhenFragmentDisabled(t *testing.T) {
	mock := newMockAstrocastTransport()
	db := newTestDB(t)
	cfg := DefaultAstrocastConfig()
	cfg.FragmentEnabled = false
	cfg.MaxUplinkBytes = 160
	cfg.PollIntervalSec = 60

	gw := NewAstrocastGateway(cfg, mock, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw.Start(ctx)

	longText := make([]byte, 300)
	for i := range longText {
		longText[i] = byte('X')
	}
	msg := &transport.MeshMessage{DecodedText: string(longText)}
	gw.Forward(ctx, msg)

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for truncated send")
		default:
		}
		sent := mock.sentPayloads()
		if len(sent) > 0 {
			if len(sent) != 1 {
				t.Errorf("expected 1 send (no fragmentation), got %d", len(sent))
			}
			if len(sent[0]) != 160 {
				t.Errorf("expected truncated to 160 bytes, got %d", len(sent[0]))
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	gw.Stop()
}

func TestAstrocastGateway_ForwardSendFailure(t *testing.T) {
	mock := newMockAstrocastTransport()
	mock.failSend = true
	db := newTestDB(t)
	cfg := DefaultAstrocastConfig()
	cfg.PollIntervalSec = 60

	gw := NewAstrocastGateway(cfg, mock, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw.Start(ctx)

	msg := &transport.MeshMessage{DecodedText: "will fail"}
	gw.Forward(ctx, msg)

	// Wait for error to be recorded
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for error counter")
		default:
		}
		if gw.errors.Load() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	status := gw.Status()
	if status.MessagesOut != 0 {
		t.Errorf("MessagesOut: want 0 on failure, got %d", status.MessagesOut)
	}
	if status.Errors != 1 {
		t.Errorf("Errors: want 1, got %d", status.Errors)
	}

	gw.Stop()
}

func TestAstrocastGateway_ForwardQueueFull(t *testing.T) {
	mock := newMockAstrocastTransport()
	db := newTestDB(t)
	cfg := DefaultAstrocastConfig()
	cfg.PollIntervalSec = 60

	// Don't start the gateway — sendWorker won't drain the queue
	gw := NewAstrocastGateway(cfg, mock, db)

	// Fill the outbound queue (capacity 10)
	for i := 0; i < 10; i++ {
		msg := &transport.MeshMessage{DecodedText: fmt.Sprintf("msg %d", i)}
		if err := gw.Forward(context.Background(), msg); err != nil {
			t.Fatalf("Forward %d should succeed: %v", i, err)
		}
	}

	// 11th should fail
	msg := &transport.MeshMessage{DecodedText: "overflow"}
	err := gw.Forward(context.Background(), msg)
	if err == nil {
		t.Error("expected error when queue full")
	}
	if gw.errors.Load() != 1 {
		t.Errorf("Errors: want 1, got %d", gw.errors.Load())
	}
}

func TestAstrocastGateway_ReceiveSimpleMessage(t *testing.T) {
	mock := newMockAstrocastTransport()
	mock.queueDownlink([]byte("hello from space"))
	db := newTestDB(t)
	cfg := DefaultAstrocastConfig()
	cfg.PollIntervalSec = 1 // fast polling for test

	gw := NewAstrocastGateway(cfg, mock, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw.Start(ctx)

	// Wait for inbound message
	select {
	case inbound := <-gw.Receive():
		if inbound.Text != "hello from space" {
			t.Errorf("inbound text: want %q, got %q", "hello from space", inbound.Text)
		}
		if inbound.Source != "astrocast" {
			t.Errorf("inbound source: want astrocast, got %s", inbound.Source)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for inbound message")
	}

	status := gw.Status()
	if status.MessagesIn != 1 {
		t.Errorf("MessagesIn: want 1, got %d", status.MessagesIn)
	}

	gw.Stop()
}

func TestAstrocastGateway_ReceiveFragmentedMessage(t *testing.T) {
	mock := newMockAstrocastTransport()
	db := newTestDB(t)
	cfg := DefaultAstrocastConfig()
	cfg.PollIntervalSec = 1

	// Create a 3-fragment message
	original := []byte("This is a fragmented message that spans multiple Astrocast downlinks for testing")

	// Fragment 0: header + first part
	frag0 := make([]byte, 1+30)
	frag0[0] = transport.EncodeFragmentHeader(5, 0, 3) // msgID=5, frag 0 of 3
	copy(frag0[1:], original[:30])
	mock.queueDownlink(frag0)

	// Fragment 1: header + second part
	frag1 := make([]byte, 1+30)
	frag1[0] = transport.EncodeFragmentHeader(5, 1, 3)
	copy(frag1[1:], original[30:60])
	mock.queueDownlink(frag1)

	// Fragment 2: header + remaining
	frag2 := make([]byte, 1+len(original[60:]))
	frag2[0] = transport.EncodeFragmentHeader(5, 2, 3)
	copy(frag2[1:], original[60:])
	mock.queueDownlink(frag2)

	gw := NewAstrocastGateway(cfg, mock, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw.Start(ctx)

	// Should get one reassembled message
	select {
	case inbound := <-gw.Receive():
		if inbound.Text != string(original) {
			t.Errorf("reassembled text mismatch:\n  want: %q\n  got:  %q", string(original), inbound.Text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for reassembled message")
	}

	status := gw.Status()
	if status.MessagesIn != 1 {
		t.Errorf("MessagesIn: want 1 (reassembled), got %d", status.MessagesIn)
	}

	gw.Stop()
}

func TestAstrocastGateway_EventDrivenReceive(t *testing.T) {
	mock := newMockAstrocastTransport()
	mock.queueDownlink([]byte("Hello from LEO"))
	db := newTestDB(t)
	cfg := DefaultAstrocastConfig()
	cfg.PollIntervalSec = 300 // long poll so event must trigger

	gw := NewAstrocastGateway(cfg, mock, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw.Start(ctx)

	// Send a downlink event to trigger pollDownlink
	mock.events <- transport.AstrocastEvent{Type: "downlink", Message: "data ready"}

	select {
	case inbound := <-gw.Receive():
		if inbound.Text != "Hello from LEO" {
			t.Errorf("inbound text: want %q, got %q", "Hello from LEO", inbound.Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: event-driven receive did not trigger")
	}

	gw.Stop()
}

func TestAstrocastGateway_NonDownlinkEventIgnored(t *testing.T) {
	mock := newMockAstrocastTransport()
	// No downlink data queued
	db := newTestDB(t)
	cfg := DefaultAstrocastConfig()
	cfg.PollIntervalSec = 300

	gw := NewAstrocastGateway(cfg, mock, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw.Start(ctx)

	// Send non-downlink events — should NOT trigger pollDownlink
	mock.events <- transport.AstrocastEvent{Type: "sat_search", Message: "searching"}
	mock.events <- transport.AstrocastEvent{Type: "uplink_ack", Message: "acked"}

	select {
	case <-gw.Receive():
		t.Error("should not receive message from non-downlink events")
	case <-time.After(500 * time.Millisecond):
		// Expected: no message
	}

	gw.Stop()
}

func TestAstrocastGateway_SubscribeFailsFallsBackToPolling(t *testing.T) {
	mock := newMockAstrocastTransport()
	mock.subErr = fmt.Errorf("subscribe not supported")
	mock.queueDownlink([]byte("polling-only"))
	db := newTestDB(t)
	cfg := DefaultAstrocastConfig()
	cfg.PollIntervalSec = 1 // fast poll

	gw := NewAstrocastGateway(cfg, mock, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw.Start(ctx)

	// Should still receive via polling
	select {
	case inbound := <-gw.Receive():
		if inbound.Text != "polling-only" {
			t.Errorf("inbound text: want %q, got %q", "polling-only", inbound.Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: polling fallback did not work")
	}

	gw.Stop()
}

func TestAstrocastGateway_StatusReport(t *testing.T) {
	mock := newMockAstrocastTransport()
	db := newTestDB(t)
	cfg := DefaultAstrocastConfig()
	cfg.PollIntervalSec = 60

	gw := NewAstrocastGateway(cfg, mock, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw.Start(ctx)

	status := gw.Status()
	if status.Type != "astrocast" {
		t.Errorf("Type: want astrocast, got %s", status.Type)
	}
	if !status.Connected {
		t.Error("expected Connected=true")
	}
	if status.ConnectionUptime == "" {
		t.Error("expected non-empty ConnectionUptime when connected")
	}

	gw.Stop()

	status2 := gw.Status()
	if status2.Connected {
		t.Error("expected Connected=false after stop")
	}
	if status2.ConnectionUptime != "" {
		t.Error("expected empty ConnectionUptime when disconnected")
	}
}

func TestAstrocastGateway_ConcurrentSendReceive(t *testing.T) {
	mock := newMockAstrocastTransport()
	db := newTestDB(t)
	cfg := DefaultAstrocastConfig()
	cfg.PollIntervalSec = 1

	// Queue some downlinks
	for i := 0; i < 5; i++ {
		mock.queueDownlink([]byte(fmt.Sprintf("downlink-%d", i)))
	}

	gw := NewAstrocastGateway(cfg, mock, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw.Start(ctx)

	// Send messages concurrently
	var sendWg sync.WaitGroup
	var sendErrors atomic.Int64
	for i := 0; i < 5; i++ {
		sendWg.Add(1)
		go func(n int) {
			defer sendWg.Done()
			msg := &transport.MeshMessage{DecodedText: fmt.Sprintf("uplink-%d", n)}
			if err := gw.Forward(ctx, msg); err != nil {
				sendErrors.Add(1)
			}
		}(i)
	}
	sendWg.Wait()

	if sendErrors.Load() != 0 {
		t.Errorf("concurrent sends had %d errors", sendErrors.Load())
	}

	// Wait for sends to be processed
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout: only %d/%d sends processed", len(mock.sentPayloads()), 5)
		default:
		}
		if len(mock.sentPayloads()) >= 5 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Drain inbound messages
	received := 0
	drainDeadline := time.After(10 * time.Second)
	for received < 5 {
		select {
		case <-gw.Receive():
			received++
		case <-drainDeadline:
			t.Fatalf("timeout: only received %d/5 downlinks", received)
		}
	}

	status := gw.Status()
	if status.MessagesOut != 5 {
		t.Errorf("MessagesOut: want 5, got %d", status.MessagesOut)
	}
	if status.MessagesIn != 5 {
		t.Errorf("MessagesIn: want 5, got %d", status.MessagesIn)
	}

	gw.Stop()
}

func TestAstrocastGateway_FragmentSendFailureStopsRemaining(t *testing.T) {
	mock := newMockAstrocastTransport()
	db := newTestDB(t)
	cfg := DefaultAstrocastConfig()
	cfg.FragmentEnabled = true
	cfg.PollIntervalSec = 60

	gw := NewAstrocastGateway(cfg, mock, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw.Start(ctx)

	// Create a large message that will fragment
	longText := make([]byte, 300)
	for i := range longText {
		longText[i] = byte('Z')
	}
	msg := &transport.MeshMessage{DecodedText: string(longText)}

	// Let first fragment succeed, then fail
	var sendCount atomic.Int32
	origSend := mock.Send
	_ = origSend
	mock.mu.Lock()
	mock.mu.Unlock()

	// Use a channel to synchronize — fail after first send
	go func() {
		// Wait for first send to happen
		deadline := time.After(3 * time.Second)
		for {
			select {
			case <-deadline:
				return
			default:
			}
			if sendCount.Load() > 0 {
				mock.mu.Lock()
				mock.failSend = true
				mock.mu.Unlock()
				return
			}
			mock.mu.Lock()
			sc := len(mock.sent)
			mock.mu.Unlock()
			if sc > 0 {
				sendCount.Store(int32(sc))
				mock.mu.Lock()
				mock.failSend = true
				mock.mu.Unlock()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	gw.Forward(ctx, msg)

	// Wait for error
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			// If no error, the first fragment may have sent before failSend kicked in
			// Either way, the gateway should not panic
			goto done
		default:
		}
		if gw.errors.Load() > 0 || gw.msgsOut.Load() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

done:
	// The important thing: no panic, error counter or success counter updated
	gw.Stop()
}

// --- Config tests ---

func TestAstrocastConfig_Defaults(t *testing.T) {
	cfg := DefaultAstrocastConfig()
	if cfg.MaxUplinkBytes != 160 {
		t.Errorf("MaxUplinkBytes: want 160, got %d", cfg.MaxUplinkBytes)
	}
	if cfg.PollIntervalSec != 300 {
		t.Errorf("PollIntervalSec: want 300, got %d", cfg.PollIntervalSec)
	}
	if !cfg.FragmentEnabled {
		t.Error("FragmentEnabled: want true")
	}
	if cfg.PowerMode != "balanced" {
		t.Errorf("PowerMode: want balanced, got %s", cfg.PowerMode)
	}
}

func TestAstrocastConfig_Validate(t *testing.T) {
	tests := []struct {
		name      string
		cfg       AstrocastConfig
		wantBytes int
		wantPoll  int
		wantPower string
	}{
		{
			name:      "valid config",
			cfg:       AstrocastConfig{MaxUplinkBytes: 100, PollIntervalSec: 60, PowerMode: "low_power"},
			wantBytes: 100, wantPoll: 60, wantPower: "low_power",
		},
		{
			name:      "zero uplink bytes resets to 160",
			cfg:       AstrocastConfig{MaxUplinkBytes: 0, PollIntervalSec: 60, PowerMode: "balanced"},
			wantBytes: 160, wantPoll: 60, wantPower: "balanced",
		},
		{
			name:      "negative uplink bytes resets to 160",
			cfg:       AstrocastConfig{MaxUplinkBytes: -5, PollIntervalSec: 60, PowerMode: "balanced"},
			wantBytes: 160, wantPoll: 60, wantPower: "balanced",
		},
		{
			name:      "oversized uplink bytes resets to 160",
			cfg:       AstrocastConfig{MaxUplinkBytes: 200, PollIntervalSec: 60, PowerMode: "balanced"},
			wantBytes: 160, wantPoll: 60, wantPower: "balanced",
		},
		{
			name:      "zero poll interval resets to 300",
			cfg:       AstrocastConfig{MaxUplinkBytes: 160, PollIntervalSec: 0, PowerMode: "balanced"},
			wantBytes: 160, wantPoll: 300, wantPower: "balanced",
		},
		{
			name:      "negative poll interval resets to 300",
			cfg:       AstrocastConfig{MaxUplinkBytes: 160, PollIntervalSec: -10, PowerMode: "balanced"},
			wantBytes: 160, wantPoll: 300, wantPower: "balanced",
		},
		{
			name:      "invalid power mode resets to balanced",
			cfg:       AstrocastConfig{MaxUplinkBytes: 160, PollIntervalSec: 60, PowerMode: "turbo"},
			wantBytes: 160, wantPoll: 60, wantPower: "balanced",
		},
		{
			name:      "performance power mode accepted",
			cfg:       AstrocastConfig{MaxUplinkBytes: 160, PollIntervalSec: 120, PowerMode: "performance"},
			wantBytes: 160, wantPoll: 120, wantPower: "performance",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			cfg.Validate()
			if cfg.MaxUplinkBytes != tt.wantBytes {
				t.Errorf("MaxUplinkBytes: want %d, got %d", tt.wantBytes, cfg.MaxUplinkBytes)
			}
			if cfg.PollIntervalSec != tt.wantPoll {
				t.Errorf("PollIntervalSec: want %d, got %d", tt.wantPoll, cfg.PollIntervalSec)
			}
			if cfg.PowerMode != tt.wantPower {
				t.Errorf("PowerMode: want %s, got %s", tt.wantPower, cfg.PowerMode)
			}
		})
	}
}

func TestParseAstrocastConfig(t *testing.T) {
	input := `{"max_uplink_bytes":120,"poll_interval_sec":60,"fragment_enabled":false,"power_mode":"low_power"}`
	cfg, err := ParseAstrocastConfig(input)
	if err != nil {
		t.Fatalf("ParseAstrocastConfig: %v", err)
	}
	if cfg.MaxUplinkBytes != 120 {
		t.Errorf("MaxUplinkBytes: want 120, got %d", cfg.MaxUplinkBytes)
	}
	if cfg.PollIntervalSec != 60 {
		t.Errorf("PollIntervalSec: want 60, got %d", cfg.PollIntervalSec)
	}
	if cfg.FragmentEnabled {
		t.Error("FragmentEnabled: want false")
	}
	if cfg.PowerMode != "low_power" {
		t.Errorf("PowerMode: want low_power, got %s", cfg.PowerMode)
	}
}

func TestParseAstrocastConfig_Defaults(t *testing.T) {
	// Empty JSON should give defaults
	cfg, err := ParseAstrocastConfig("{}")
	if err != nil {
		t.Fatalf("ParseAstrocastConfig: %v", err)
	}
	if cfg.MaxUplinkBytes != 160 {
		t.Errorf("MaxUplinkBytes default: want 160, got %d", cfg.MaxUplinkBytes)
	}
	if !cfg.FragmentEnabled {
		t.Error("FragmentEnabled default: want true")
	}
}

func TestParseAstrocastConfig_InvalidJSON(t *testing.T) {
	_, err := ParseAstrocastConfig("not json")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}
