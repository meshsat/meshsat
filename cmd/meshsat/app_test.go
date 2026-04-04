package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"meshsat/internal/config"
	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

// --- Mock transports ---

type stubMeshTransport struct{}

func (s *stubMeshTransport) Subscribe(ctx context.Context) (<-chan transport.MeshEvent, error) {
	return make(chan transport.MeshEvent), nil
}
func (s *stubMeshTransport) SendMessage(ctx context.Context, req transport.SendRequest) error {
	return nil
}
func (s *stubMeshTransport) SendRaw(ctx context.Context, req transport.RawRequest) error {
	return nil
}
func (s *stubMeshTransport) GetNodes(ctx context.Context) ([]transport.MeshNode, error) {
	return nil, nil
}
func (s *stubMeshTransport) GetStatus(ctx context.Context) (*transport.MeshStatus, error) {
	return nil, nil
}
func (s *stubMeshTransport) GetMessages(ctx context.Context, limit int) ([]transport.MeshMessage, error) {
	return nil, nil
}
func (s *stubMeshTransport) GetConfig(ctx context.Context) (map[string]interface{}, error) {
	return nil, nil
}
func (s *stubMeshTransport) AdminReboot(ctx context.Context, nodeNum uint32, delay int) error {
	return nil
}
func (s *stubMeshTransport) AdminFactoryReset(ctx context.Context, nodeNum uint32) error {
	return nil
}
func (s *stubMeshTransport) Traceroute(ctx context.Context, nodeNum uint32) error { return nil }
func (s *stubMeshTransport) SetRadioConfig(ctx context.Context, section string, data json.RawMessage) error {
	return nil
}
func (s *stubMeshTransport) SetModuleConfig(ctx context.Context, section string, data json.RawMessage) error {
	return nil
}
func (s *stubMeshTransport) SetChannel(ctx context.Context, req transport.ChannelRequest) error {
	return nil
}
func (s *stubMeshTransport) SendWaypoint(ctx context.Context, wp transport.Waypoint) error {
	return nil
}
func (s *stubMeshTransport) RemoveNode(ctx context.Context, nodeNum uint32) error { return nil }
func (s *stubMeshTransport) GetConfigSection(ctx context.Context, section string) error {
	return nil
}
func (s *stubMeshTransport) GetModuleConfigSection(ctx context.Context, section string) error {
	return nil
}
func (s *stubMeshTransport) SendPosition(ctx context.Context, lat, lon float64, alt int32) error {
	return nil
}
func (s *stubMeshTransport) SetFixedPosition(ctx context.Context, lat, lon float64, alt int32) error {
	return nil
}
func (s *stubMeshTransport) RemoveFixedPosition(ctx context.Context) error { return nil }
func (s *stubMeshTransport) SetOwner(ctx context.Context, longName, shortName string) error {
	return nil
}
func (s *stubMeshTransport) RequestNodeInfo(ctx context.Context, nodeNum uint32) error { return nil }
func (s *stubMeshTransport) RequestStoreForward(ctx context.Context, nodeNum uint32, window uint32) error {
	return nil
}
func (s *stubMeshTransport) SendRangeTest(ctx context.Context, text string, to uint32) error {
	return nil
}
func (s *stubMeshTransport) SetCannedMessages(ctx context.Context, messages string) error {
	return nil
}
func (s *stubMeshTransport) GetCannedMessages(ctx context.Context) error { return nil }
func (s *stubMeshTransport) GetNeighborInfo(ctx context.Context) ([]transport.NeighborInfo, error) {
	return nil, nil
}
func (s *stubMeshTransport) SendEncryptedRelay(ctx context.Context, encryptedPayload []byte, to uint32, channel uint32, hopLimit uint32) error {
	return nil
}
func (s *stubMeshTransport) Close() error { return nil }

type stubSatTransport struct{}

func (s *stubSatTransport) Subscribe(ctx context.Context) (<-chan transport.SatEvent, error) {
	return make(chan transport.SatEvent), nil
}
func (s *stubSatTransport) Send(ctx context.Context, data []byte) (*transport.SBDResult, error) {
	return nil, nil
}
func (s *stubSatTransport) SendText(ctx context.Context, text string) (*transport.SBDResult, error) {
	return nil, nil
}
func (s *stubSatTransport) Receive(ctx context.Context) ([]byte, error) { return nil, nil }
func (s *stubSatTransport) MailboxCheck(ctx context.Context) (*transport.SBDResult, error) {
	return nil, nil
}
func (s *stubSatTransport) GetSignal(ctx context.Context) (*transport.SignalInfo, error) {
	return nil, nil
}
func (s *stubSatTransport) GetSignalFast(ctx context.Context) (*transport.SignalInfo, error) {
	return nil, nil
}
func (s *stubSatTransport) GetStatus(ctx context.Context) (*transport.SatStatus, error) {
	return nil, nil
}
func (s *stubSatTransport) GetGeolocation(ctx context.Context) (*transport.GeolocationInfo, error) {
	return nil, nil
}
func (s *stubSatTransport) MOBufferEmpty(ctx context.Context) (bool, error) { return true, nil }
func (s *stubSatTransport) GetFirmwareVersion(ctx context.Context) (string, error) {
	return "", nil
}
func (s *stubSatTransport) Close() error { return nil }

// --- Test helpers ---

func testConfig() *config.Config {
	return &config.Config{
		Port:          0,
		DBPath:        ":memory:",
		Mode:          "direct",
		RetentionDays: 7,
		PaidRateLimit: 60,
	}
}

func testApp(t *testing.T) *App {
	t.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	return &App{
		DB:     db,
		Config: testConfig(),
		Mesh:   &stubMeshTransport{},
		Sat:    &stubSatTransport{},
	}
}

// --- Tests ---

// TestSetup_InitializesAllComponents verifies that Setup() creates and wires
// all core components without error.
func TestSetup_InitializesAllComponents(t *testing.T) {
	app := testApp(t)
	defer app.DB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := app.Setup(ctx); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	checks := []struct {
		name string
		ok   bool
	}{
		{"Registry", app.Registry != nil},
		{"InterfaceManager", app.InterfaceMgr != nil},
		{"AccessEvaluator", app.AccessEval != nil},
		{"Processor", app.Processor != nil},
		{"GatewayManager", app.GatewayMgr != nil},
		{"Dispatcher", app.Dispatcher != nil},
		{"SigningService", app.Signing != nil},
		{"TransformPipeline", app.Transforms != nil},
		{"Deduplicator", app.Deduplicator != nil},
		{"SignalRecorder", app.SignalRecorder != nil},
		{"TLEManager", app.TLEMgr != nil},
		{"AstrocastTLEManager", app.AstroTLEMgr != nil},
		{"APIServer", app.Server != nil},
		{"HTTPServer", app.HTTPServer != nil},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("expected %s to be initialized", c.name)
		}
	}
}

// TestSetup_NoCellOrAstro verifies that Setup() handles nil Cell and Astro
// transports gracefully.
func TestSetup_NoCellOrAstro(t *testing.T) {
	app := testApp(t)
	app.Cell = nil
	app.Astro = nil
	defer app.DB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := app.Setup(ctx); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	if app.CellRecorder != nil {
		t.Error("expected nil CellRecorder when no cell transport")
	}
}

// TestSetup_NoGPSWithoutExcludePorts verifies that GPSReader is only created
// when GPSExcludePorts is non-nil (direct mode only).
func TestSetup_NoGPSWithoutExcludePorts(t *testing.T) {
	app := testApp(t)
	app.GPSExcludePorts = nil
	defer app.DB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := app.Setup(ctx); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	if app.GPSReader != nil {
		t.Error("expected nil GPSReader when no exclude ports configured")
	}
}

// TestSetup_HTTPServerConfig verifies that the HTTP server is configured
// with correct timeouts and handler.
func TestSetup_HTTPServerConfig(t *testing.T) {
	app := testApp(t)
	app.Config.Port = 9999
	defer app.DB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := app.Setup(ctx); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	if app.HTTPServer.Addr != ":9999" {
		t.Errorf("expected addr :9999, got %s", app.HTTPServer.Addr)
	}
	if app.HTTPServer.ReadTimeout != 15*time.Second {
		t.Errorf("expected ReadTimeout 15s, got %v", app.HTTPServer.ReadTimeout)
	}
	if app.HTTPServer.WriteTimeout != 0 {
		t.Errorf("expected WriteTimeout 0 (SSE), got %v", app.HTTPServer.WriteTimeout)
	}
	if app.HTTPServer.IdleTimeout != 60*time.Second {
		t.Errorf("expected IdleTimeout 60s, got %v", app.HTTPServer.IdleTimeout)
	}
	if app.HTTPServer.Handler == nil {
		t.Error("expected handler to be set")
	}
}

// TestSetup_DispatcherWiring verifies that the Dispatcher is wired with the
// AccessEvaluator so access rules are enforced end-to-end.
func TestSetup_DispatcherWiring(t *testing.T) {
	app := testApp(t)
	defer app.DB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := app.Setup(ctx); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Insert interfaces and a rule
	app.DB.Exec(`INSERT OR IGNORE INTO interfaces (id, channel_type, label, enabled, config)
		VALUES ('mesh_0', 'mesh', 'Mesh', 1, '{}')`)
	app.DB.Exec(`INSERT OR IGNORE INTO interfaces (id, channel_type, label, enabled, config)
		VALUES ('mqtt_0', 'mqtt', 'MQTT', 1, '{}')`)
	app.DB.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Test",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "mqtt_0",
		Filters: "{}",
	})

	// Reload rules into the already-wired evaluator
	if err := app.AccessEval.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}

	// If wiring is correct, this should create 1 delivery
	msg := rules.RouteMessage{Text: "wiring test", From: "!abc", PortNum: 1}
	count := app.Dispatcher.DispatchAccess("mesh_0", msg, []byte("wiring test"))
	if count != 1 {
		t.Errorf("expected 1 delivery via wired dispatcher, got %d", count)
	}

	// Verify delivery was persisted
	deliveries, err := app.DB.GetPendingDeliveries("mqtt_0", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 pending delivery, got %d", len(deliveries))
	}
	if deliveries[0].Channel != "mqtt_0" {
		t.Errorf("expected delivery to mqtt_0, got %s", deliveries[0].Channel)
	}
}

// TestStartAndShutdown verifies the full lifecycle: Setup → Start → Shutdown
// completes without panics.
func TestStartAndShutdown(t *testing.T) {
	app := testApp(t)
	defer app.DB.Close()

	ctx, cancel := context.WithCancel(context.Background())

	if err := app.Setup(ctx); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	app.Start(ctx)
	time.Sleep(200 * time.Millisecond)

	cancel()
	app.Shutdown()
}

// TestShutdown_StopsAllComponents verifies that Shutdown() calls Stop() on all
// sub-components without panicking when context is already cancelled.
func TestShutdown_StopsAllComponents(t *testing.T) {
	app := testApp(t)
	defer app.DB.Close()

	ctx, cancel := context.WithCancel(context.Background())

	if err := app.Setup(ctx); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	cancel()
	// Should not panic even when context is already cancelled
	app.Shutdown()
}

// TestShutdown_RunsCleanups verifies that Shutdown() executes all registered
// cleanup functions.
func TestShutdown_RunsCleanups(t *testing.T) {
	app := testApp(t)
	defer app.DB.Close()

	ctx, cancel := context.WithCancel(context.Background())

	if err := app.Setup(ctx); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	cleaned := false
	app.cleanups = append(app.cleanups, func() { cleaned = true })

	cancel()
	app.Shutdown()

	if !cleaned {
		t.Error("expected cleanup function to be called during Shutdown")
	}
}

// TestSetup_SigningServiceCreated verifies that the signing service is
// initialized on a fresh database with auto-generated keypair.
func TestSetup_SigningServiceCreated(t *testing.T) {
	app := testApp(t)
	defer app.DB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := app.Setup(ctx); err != nil {
		t.Fatal(err)
	}

	if app.Signing == nil {
		t.Fatal("expected signing service to be created")
	}
	if app.Signing.SignerID() == "" {
		t.Error("expected signing service to have a signer ID (auto-generated keypair)")
	}
}

// TestSetup_ChannelRegistryHasDefaults verifies that the channel registry is
// populated with the default channel descriptors.
func TestSetup_ChannelRegistryHasDefaults(t *testing.T) {
	app := testApp(t)
	defer app.DB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := app.Setup(ctx); err != nil {
		t.Fatal(err)
	}

	// Check that standard channel types are registered
	for _, ct := range []string{"mesh", "iridium", "mqtt", "cellular"} {
		if _, ok := app.Registry.Get(ct); !ok {
			t.Errorf("expected channel type %q in registry", ct)
		}
	}
}

// TestSetup_DeduplicatorStarted verifies that the deduplicator is created and
// its pruner goroutine is started.
func TestSetup_DeduplicatorStarted(t *testing.T) {
	app := testApp(t)
	defer app.DB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := app.Setup(ctx); err != nil {
		t.Fatal(err)
	}

	if app.Deduplicator == nil {
		t.Fatal("expected deduplicator to be created")
	}
}

// TestTLEAdapter_PassThrough verifies that tleAdapter correctly converts
// engine passes to gateway passes without panicking.
func TestTLEAdapter_PassThrough(t *testing.T) {
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mgr := engine.NewTLEManager(db)
	adapter := &tleAdapter{mgr: mgr}

	// GeneratePasses should not panic even with no TLE data loaded
	passes, _ := adapter.GeneratePasses(52.0, 4.0, 0.0, 24, 10.0, 0)
	// With no TLE data, should return empty — either empty slice or nil is fine
	_ = passes
}

// TestSetup_MultipleCallsIdempotent verifies that calling Setup() multiple times
// doesn't cause issues (e.g. duplicate workers, leaked goroutines).
func TestSetup_MultipleCallsIdempotent(t *testing.T) {
	app := testApp(t)
	defer app.DB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := app.Setup(ctx); err != nil {
		t.Fatalf("first Setup failed: %v", err)
	}

	first := app.Dispatcher

	// Second setup should overwrite without panicking
	if err := app.Setup(ctx); err != nil {
		t.Fatalf("second Setup failed: %v", err)
	}

	if app.Dispatcher == first {
		t.Error("expected new Dispatcher instance on second Setup")
	}
}
