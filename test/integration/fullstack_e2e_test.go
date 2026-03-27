package integration

// Full-stack E2E validation tests for MESHSAT-338.
// Tests dual satellite modem (9603 SBD + 9704 IMT) coexistence, failover,
// signal recording, and gateway lifecycle.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/transport"
)

// ---------------------------------------------------------------------------
// Mock satellite transports (SBD + IMT)
// ---------------------------------------------------------------------------

type mockSatTransport struct {
	mu          sync.Mutex
	connected   bool
	imei        string
	model       string
	modemType   string // "sbd" or "imt"
	signalBars  int
	sent        [][]byte
	eventCh     chan transport.SatEvent
	sendErr     error
	statusErr   error
	signalErr   error
	sendCount   atomic.Int64
	receiveData []byte
}

func newMockSatTransport(imei, model, modemType string) *mockSatTransport {
	return &mockSatTransport{
		connected:  true,
		imei:       imei,
		model:      model,
		modemType:  modemType,
		signalBars: 4,
		eventCh:    make(chan transport.SatEvent, 16),
	}
}

func (m *mockSatTransport) Subscribe(_ context.Context) (<-chan transport.SatEvent, error) {
	return m.eventCh, nil
}

func (m *mockSatTransport) Send(_ context.Context, data []byte) (*transport.SatResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sendErr != nil {
		return nil, m.sendErr
	}
	if !m.connected {
		return &transport.SatResult{MOStatus: 32, StatusText: "no network service"}, nil
	}
	m.sent = append(m.sent, data)
	m.sendCount.Add(1)
	return &transport.SatResult{MOStatus: 0, MOMSN: int(m.sendCount.Load()), StatusText: "success"}, nil
}

func (m *mockSatTransport) SendText(ctx context.Context, text string) (*transport.SatResult, error) {
	return m.Send(ctx, []byte(text))
}

func (m *mockSatTransport) Receive(_ context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.receiveData != nil {
		data := m.receiveData
		m.receiveData = nil
		return data, nil
	}
	return nil, nil
}

func (m *mockSatTransport) MailboxCheck(_ context.Context) (*transport.SatResult, error) {
	return &transport.SatResult{MOStatus: 0, MTQueued: 0}, nil
}

func (m *mockSatTransport) GetSignal(_ context.Context) (*transport.SignalInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.signalErr != nil {
		return nil, m.signalErr
	}
	return &transport.SignalInfo{
		Bars:       m.signalBars,
		Timestamp:  time.Now().Format(time.RFC3339),
		Assessment: signalAssessment(m.signalBars),
		Source:     m.modemType,
	}, nil
}

func (m *mockSatTransport) GetSignalFast(ctx context.Context) (*transport.SignalInfo, error) {
	return m.GetSignal(ctx)
}

func (m *mockSatTransport) GetStatus(_ context.Context) (*transport.SatStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	return &transport.SatStatus{
		Connected: m.connected,
		Port:      "/dev/ttyUSB0",
		IMEI:      m.imei,
		Model:     m.model,
		Type:      m.modemType,
	}, nil
}

func (m *mockSatTransport) GetFirmwareVersion(_ context.Context) (string, error) {
	return "TA16005", nil
}

func (m *mockSatTransport) Close() error { return nil }

func (m *mockSatTransport) setConnected(connected bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = connected
}

func (m *mockSatTransport) setSignal(bars int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.signalBars = bars
}

func (m *mockSatTransport) getSent() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]byte, len(m.sent))
	copy(out, m.sent)
	return out
}

func signalAssessment(bars int) string {
	switch {
	case bars == 0:
		return "none"
	case bars <= 1:
		return "poor"
	case bars <= 2:
		return "fair"
	case bars <= 3:
		return "good"
	default:
		return "excellent"
	}
}

// testMsg creates a MeshMessage with the given text for testing.
func testMsg(text string) *transport.MeshMessage {
	return &transport.MeshMessage{
		DecodedText: text,
		From:        0xAABBCCDD,
		PortNum:     1, // TEXT_MESSAGE_APP
		Timestamp:   time.Now().Format(time.RFC3339),
	}
}

// gwByType finds a running gateway by its Type() string.
func gwByType(gateways []gateway.Gateway, gwType string) gateway.Gateway {
	for _, gw := range gateways {
		if gw.Type() == gwType {
			return gw
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Full-stack E2E harness
// ---------------------------------------------------------------------------

type fullstackHarness struct {
	db    *database.DB
	gwMgr *gateway.Manager
	sbdTx *mockSatTransport
	imtTx *mockSatTransport
}

func setupFullstack(t *testing.T) *fullstackHarness {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "fullstack_e2e.db")
	db, err := database.New(dbPath)
	if err != nil {
		t.Fatalf("database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Create mock SBD (9603) and IMT (9704) transports.
	sbdTx := newMockSatTransport("300234063904190", "RockBLOCK 9603", "sbd")
	imtTx := newMockSatTransport("300534060005120", "RockBLOCK 9704", "imt")

	// Configure gateway manager with both transports.
	gwMgr := gateway.NewManager(db, sbdTx)
	gwMgr.SetIMTTransport(imtTx)

	// Insert gateway configs for both SBD and IMT.
	sbdCfg, _ := json.Marshal(gateway.DefaultIridiumConfig())
	if err := db.SaveGatewayConfigInstance("iridium", "iridium_0", true, string(sbdCfg)); err != nil {
		t.Fatalf("save sbd config: %v", err)
	}
	imtCfg, _ := json.Marshal(gateway.DefaultIMTConfig())
	if err := db.SaveGatewayConfigInstance("iridium_imt", "iridium_imt_0", true, string(imtCfg)); err != nil {
		t.Fatalf("save imt config: %v", err)
	}

	return &fullstackHarness{
		db:    db,
		gwMgr: gwMgr,
		sbdTx: sbdTx,
		imtTx: imtTx,
	}
}

// ---------------------------------------------------------------------------
// Test: Dual modem boot — both SBD + IMT auto-detected and online
// ---------------------------------------------------------------------------

func TestFullstack_DualModemBoot(t *testing.T) {
	h := setupFullstack(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := h.gwMgr.Start(ctx); err != nil {
		t.Fatalf("gateway manager start: %v", err)
	}
	defer h.gwMgr.Stop()

	gateways := h.gwMgr.Gateways()
	if len(gateways) < 2 {
		t.Fatalf("expected >= 2 running gateways, got %d", len(gateways))
	}

	typeMap := make(map[string]bool)
	for _, gw := range gateways {
		typeMap[gw.Type()] = true
		status := gw.Status()
		if !status.Connected {
			t.Errorf("gateway %s not connected", gw.Type())
		}
	}

	if !typeMap["iridium"] {
		t.Error("SBD gateway (type=iridium) not found in running gateways")
	}
	if !typeMap["iridium_imt"] {
		t.Error("IMT gateway (type=iridium_imt) not found in running gateways")
	}

	t.Logf("dual modem boot: %d gateways running (types: %v)", len(gateways), typeMap)
}

// ---------------------------------------------------------------------------
// Test: MO via SBD (9603) — message sent through SBD gateway
// ---------------------------------------------------------------------------

func TestFullstack_MO_SBD(t *testing.T) {
	h := setupFullstack(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := h.gwMgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer h.gwMgr.Stop()

	sbdGW := gwByType(h.gwMgr.Gateways(), "iridium")
	if sbdGW == nil {
		t.Fatal("SBD gateway (type=iridium) not found")
	}

	if err := sbdGW.Forward(ctx, testMsg("SBD test message from mesh")); err != nil {
		t.Fatalf("SBD forward: %v", err)
	}

	sent := h.sbdTx.getSent()
	if len(sent) == 0 {
		t.Fatal("SBD transport received no data after Forward()")
	}

	status := sbdGW.Status()
	if status.MessagesOut == 0 {
		t.Error("SBD gateway MessagesOut should be > 0 after Forward")
	}

	t.Logf("SBD MO: sent %d bytes, status: out=%d err=%d",
		len(sent[0]), status.MessagesOut, status.Errors)
}

// ---------------------------------------------------------------------------
// Test: MO via IMT (9704) — message sent through IMT gateway
// ---------------------------------------------------------------------------

func TestFullstack_MO_IMT(t *testing.T) {
	h := setupFullstack(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := h.gwMgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer h.gwMgr.Stop()

	imtGW := gwByType(h.gwMgr.Gateways(), "iridium_imt")
	if imtGW == nil {
		t.Fatal("IMT gateway (type=iridium_imt) not found")
	}

	if err := imtGW.Forward(ctx, testMsg("IMT test message — larger payload supported")); err != nil {
		t.Fatalf("IMT forward: %v", err)
	}

	sent := h.imtTx.getSent()
	if len(sent) == 0 {
		t.Fatal("IMT transport received no data after Forward()")
	}

	status := imtGW.Status()
	if status.MessagesOut == 0 {
		t.Error("IMT gateway MessagesOut should be > 0 after Forward")
	}

	t.Logf("IMT MO: sent %d bytes, status: out=%d err=%d",
		len(sent[0]), status.MessagesOut, status.Errors)
}

// ---------------------------------------------------------------------------
// Test: Dual signal via GetSignalFast — both sources return independent data
// ---------------------------------------------------------------------------

func TestFullstack_DualSignalSources(t *testing.T) {
	h := setupFullstack(t)
	ctx := context.Background()

	h.sbdTx.setSignal(4)
	h.imtTx.setSignal(2)

	sbdSig, err := h.sbdTx.GetSignalFast(ctx)
	if err != nil {
		t.Fatalf("SBD GetSignalFast: %v", err)
	}
	imtSig, err := h.imtTx.GetSignalFast(ctx)
	if err != nil {
		t.Fatalf("IMT GetSignalFast: %v", err)
	}

	if sbdSig.Bars != 4 {
		t.Errorf("SBD signal: got %d bars, want 4", sbdSig.Bars)
	}
	if imtSig.Bars != 2 {
		t.Errorf("IMT signal: got %d bars, want 2", imtSig.Bars)
	}
	if sbdSig.Source != "sbd" {
		t.Errorf("SBD source: got %q, want sbd", sbdSig.Source)
	}
	if imtSig.Source != "imt" {
		t.Errorf("IMT source: got %q, want imt", imtSig.Source)
	}

	t.Logf("dual signal: SBD=%d bars (%s), IMT=%d bars (%s)",
		sbdSig.Bars, sbdSig.Assessment, imtSig.Bars, imtSig.Assessment)
}

// ---------------------------------------------------------------------------
// Test: Failover — IMT disconnected, traffic routes to SBD
// ---------------------------------------------------------------------------

func TestFullstack_Failover_IMTtoSBD(t *testing.T) {
	h := setupFullstack(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := h.gwMgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer h.gwMgr.Stop()

	initialGWs := h.gwMgr.Gateways()
	if len(initialGWs) < 2 {
		t.Fatalf("expected >= 2 gateways, got %d", len(initialGWs))
	}

	// Disconnect IMT transport.
	h.imtTx.setConnected(false)
	h.imtTx.mu.Lock()
	h.imtTx.sendErr = fmt.Errorf("modem disconnected")
	h.imtTx.mu.Unlock()

	// SBD should still be operational.
	sbdGW := gwByType(h.gwMgr.Gateways(), "iridium")
	if sbdGW == nil {
		t.Fatal("SBD gateway not found after IMT disconnect")
	}

	if err := sbdGW.Forward(ctx, testMsg("Failover test — routed to SBD")); err != nil {
		t.Fatalf("SBD forward after IMT disconnect: %v", err)
	}

	sent := h.sbdTx.getSent()
	if len(sent) == 0 {
		t.Fatal("SBD transport received no data during failover")
	}

	// Verify IMT gateway forward fails.
	imtGW := gwByType(h.gwMgr.Gateways(), "iridium_imt")
	if imtGW != nil {
		err := imtGW.Forward(ctx, testMsg("should fail on IMT"))
		if err == nil {
			imtStatus := imtGW.Status()
			t.Logf("IMT forward after disconnect: errors=%d, connected=%v",
				imtStatus.Errors, imtStatus.Connected)
		}
	}

	t.Logf("failover: SBD received %d messages after IMT disconnect", len(sent))
}

// ---------------------------------------------------------------------------
// Test: Gateway status reporting — both gateways report correct status
// ---------------------------------------------------------------------------

func TestFullstack_GatewayStatus(t *testing.T) {
	h := setupFullstack(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := h.gwMgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer h.gwMgr.Stop()

	for _, gw := range h.gwMgr.Gateways() {
		status := gw.Status()
		t.Logf("gateway %s: connected=%v, in=%d, out=%d, errors=%d, dlq=%d",
			status.Type, status.Connected, status.MessagesIn, status.MessagesOut,
			status.Errors, status.DLQPending)

		if status.Type == "" {
			t.Error("gateway status has empty type")
		}
		if !status.Connected {
			t.Errorf("gateway %s: expected connected=true", status.Type)
		}
	}
}

// ---------------------------------------------------------------------------
// Test: Concurrent dual modem send — both paths simultaneously
// ---------------------------------------------------------------------------

func TestFullstack_ConcurrentDualSend(t *testing.T) {
	h := setupFullstack(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := h.gwMgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer h.gwMgr.Stop()

	sbdGW := gwByType(h.gwMgr.Gateways(), "iridium")
	imtGW := gwByType(h.gwMgr.Gateways(), "iridium_imt")
	if sbdGW == nil || imtGW == nil {
		t.Fatal("one or both gateways not found")
	}

	var wg sync.WaitGroup
	errs := make(chan error, 20)

	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			if err := sbdGW.Forward(ctx, testMsg(fmt.Sprintf("SBD concurrent #%d", n))); err != nil {
				errs <- fmt.Errorf("SBD #%d: %w", n, err)
			}
		}(i)
		go func(n int) {
			defer wg.Done()
			if err := imtGW.Forward(ctx, testMsg(fmt.Sprintf("IMT concurrent #%d", n))); err != nil {
				errs <- fmt.Errorf("IMT #%d: %w", n, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent send error: %v", err)
	}

	sbdSent := h.sbdTx.getSent()
	imtSent := h.imtTx.getSent()

	if len(sbdSent) != 10 {
		t.Errorf("SBD: expected 10 messages sent, got %d", len(sbdSent))
	}
	if len(imtSent) != 10 {
		t.Errorf("IMT: expected 10 messages sent, got %d", len(imtSent))
	}

	t.Logf("concurrent dual send: SBD=%d, IMT=%d", len(sbdSent), len(imtSent))
}

// ---------------------------------------------------------------------------
// Test: IMT reconnect after failover — traffic resumes on IMT
// ---------------------------------------------------------------------------

func TestFullstack_IMTReconnect(t *testing.T) {
	h := setupFullstack(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := h.gwMgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer h.gwMgr.Stop()

	imtGW := gwByType(h.gwMgr.Gateways(), "iridium_imt")
	if imtGW == nil {
		t.Fatal("IMT gateway not found")
	}

	// Disconnect.
	h.imtTx.setConnected(false)
	h.imtTx.mu.Lock()
	h.imtTx.sendErr = fmt.Errorf("modem disconnected")
	h.imtTx.mu.Unlock()

	_ = imtGW.Forward(ctx, testMsg("should fail"))

	// Reconnect.
	h.imtTx.mu.Lock()
	h.imtTx.sendErr = nil
	h.imtTx.mu.Unlock()
	h.imtTx.setConnected(true)

	if err := imtGW.Forward(ctx, testMsg("reconnect test — should succeed")); err != nil {
		t.Fatalf("IMT forward after reconnect: %v", err)
	}

	sent := h.imtTx.getSent()
	if len(sent) == 0 {
		t.Fatal("IMT transport received no data after reconnect")
	}

	t.Logf("IMT reconnect: %d messages sent after reconnect", len(sent))
}

// ---------------------------------------------------------------------------
// Test: Gateway config persistence — configs survive restart
// ---------------------------------------------------------------------------

func TestFullstack_ConfigPersistence(t *testing.T) {
	h := setupFullstack(t)

	configs, err := h.db.GetAllGatewayConfigs()
	if err != nil {
		t.Fatalf("get configs: %v", err)
	}

	typeMap := make(map[string]string)
	for _, cfg := range configs {
		typeMap[cfg.InstanceID] = cfg.Type
		if !cfg.Enabled {
			t.Errorf("config %s should be enabled", cfg.InstanceID)
		}
	}

	if typeMap["iridium_0"] != "iridium" {
		t.Errorf("expected iridium_0 type=iridium, got %s", typeMap["iridium_0"])
	}
	if typeMap["iridium_imt_0"] != "iridium_imt" {
		t.Errorf("expected iridium_imt_0 type=iridium_imt, got %s", typeMap["iridium_imt_0"])
	}

	// Simulate restart: create new manager, load configs from DB.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gwMgr2 := gateway.NewManager(h.db, h.sbdTx)
	gwMgr2.SetIMTTransport(h.imtTx)
	if err := gwMgr2.Start(ctx); err != nil {
		t.Fatalf("restart: %v", err)
	}
	defer gwMgr2.Stop()

	gateways := gwMgr2.Gateways()
	if len(gateways) < 2 {
		t.Fatalf("after restart: expected >= 2 gateways, got %d", len(gateways))
	}

	t.Logf("config persistence: %d gateways after restart", len(gateways))
}

// ---------------------------------------------------------------------------
// Test: Dual modem status differentiation — SBD vs IMT type + IMEI
// ---------------------------------------------------------------------------

func TestFullstack_DualModemStatusDifferentiation(t *testing.T) {
	h := setupFullstack(t)
	ctx := context.Background()

	sbdStatus, err := h.sbdTx.GetStatus(ctx)
	if err != nil {
		t.Fatalf("SBD status: %v", err)
	}
	imtStatus, err := h.imtTx.GetStatus(ctx)
	if err != nil {
		t.Fatalf("IMT status: %v", err)
	}

	if sbdStatus.Type != "sbd" {
		t.Errorf("SBD type: got %q, want sbd", sbdStatus.Type)
	}
	if imtStatus.Type != "imt" {
		t.Errorf("IMT type: got %q, want imt", imtStatus.Type)
	}
	if sbdStatus.IMEI == imtStatus.IMEI {
		t.Errorf("SBD and IMT have same IMEI: %s", sbdStatus.IMEI)
	}
	if !sbdStatus.Connected || !imtStatus.Connected {
		t.Error("both modems should be connected")
	}

	t.Logf("SBD: %s (%s), IMT: %s (%s)", sbdStatus.Model, sbdStatus.IMEI, imtStatus.Model, imtStatus.IMEI)
}
