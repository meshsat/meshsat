package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/smpp/smpptest"
	"meshsat/internal/transport"
)

// The cell's SDR on a kit: adopted through the host agent, released when it
// goes, the radio only on an operator's word. [MESHSAT-1578]

type fakeAgent struct {
	mu        sync.Mutex
	available bool
	installed bool
	password  string
	bind      string
	port      int
	calls     []string
	radio     bool
	core      bool
	siteOK    bool
}

func (a *fakeAgent) Available() bool { return a.available }

func (a *fakeAgent) Call(ctx context.Context, action string, args map[string]any) (map[string]any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, action+":"+strings.TrimSpace(anyStr(args["op"])+" "+anyStr(args["purpose"])))
	if !a.installed {
		return nil, errors.New("rc=1 cell not installed")
	}
	status := func() map[string]any {
		return map[string]any{
			"ok":    true,
			"pluto": map[string]any{"present": true, "state": "run", "reachable": true},
			"core":  map[string]any{"up": a.core},
			"radio": map[string]any{"active": a.radio},
			"smpp": map[string]any{"bind": a.bind, "port": float64(a.port), "system_id": "meshsat-bridge",
				"short_code": "1000", "password_set": a.password != ""},
			"licence": map[string]any{"number": "RDI-2610-00385", "site_ok": a.siteOK},
		}
	}
	switch action {
	case "cell_status":
		return status(), nil
	case "cell_adopt":
		st := status()
		st["smpp"].(map[string]any)["password"] = a.password
		return st, nil
	case "cell_core":
		a.core = args["op"] == "start"
		if !a.core {
			a.radio = false
		}
		return map[string]any{"ok": true, "core": args["op"]}, nil
	case "cell_radio":
		if args["op"] == "start" {
			if !a.siteOK {
				return nil, errors.New("rc=2 REFUSING: CELL_SITE_OK is not yes")
			}
			a.radio = true
			return map[string]any{"ok": true, "radio": "up", "arfcn": []any{float64(873)}}, nil
		}
		a.radio = false
		return map[string]any{"ok": true, "radio": "down"}, nil
	}
	return nil, errors.New("unknown action")
}

func anyStr(v any) string {
	s, _ := v.(string)
	return s
}

func (a *fakeAgent) called(prefix string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func TestPlutoIsKnownAndNeverProbed(t *testing.T) {
	if transport.ClassifyDeviceForTest("0456:b673") != "cell_sdr" || transport.ClassifyDeviceForTest("0456:b674") != "cell_sdr" {
		t.Fatal("the Pluto's USB ids must classify as cell_sdr")
	}
	if transport.DeviceTypeForRole(transport.RoleCellSDR) != "cell_sdr" {
		t.Fatal("device type label")
	}
	if roleToGatewayType(transport.RoleCellSDR) != "" {
		t.Fatal("the SDR has no gateway of its own; the cell host adopts it")
	}
}

func TestCellHostAdoptsAndReleasesThePluto(t *testing.T) {
	srv, err := smpptest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	srv.SystemID, srv.Password = "meshsat-bridge", "kitpw"
	host, port, _ := strings.Cut(srv.Addr, ":")

	db := newTestDB(t)
	m := NewManager(db, nil)
	t.Cleanup(m.Stop)
	var events []string
	var evMu sync.Mutex
	m.SetEventEmitFunc(func(_, msg string) { evMu.Lock(); events = append(events, msg); evMu.Unlock() })
	agent := &fakeAgent{available: true, installed: true, password: "kitpw", bind: host, port: atoi(port)}
	h := NewCellHost(agent, db, m, nil)
	m.SetCellHost(h)
	t.Cleanup(func() { setCellHostPassword("") })

	// The Pluto appears: credentials from the host, cell_0 seeded and
	// enabled, the core started, the gateway bound to the host's SMPP.
	m.handleDeviceEvent(context.Background(), transport.DeviceEvent{Type: "device_connected",
		Device: &transport.SerialDeviceEntry{DevPath: "/dev/ttyACM3", VIDPID: "0456:b673", Role: transport.RoleCellSDR}})
	waitUntil(t, "adoption", func() bool { return m.GetCellGateway() != nil && m.GetCellGateway().Bound() })
	if !agent.called("cell_adopt") || !agent.called("cell_core:start") {
		t.Fatalf("agent calls %v", agent.calls)
	}
	row, err := db.GetGatewayConfigByInstance("cell_0")
	if err != nil || !row.Enabled {
		t.Fatalf("cell_0 row %+v err %v", row, err)
	}
	cfg, _ := ParseCellConfig(row.Config)
	if cfg.Host != host || cfg.Port != atoi(port) || cfg.Password != "" {
		t.Fatalf("seeded config %+v: the host and port come from the host, the password never lands in the database", cfg)
	}
	if _, err := db.GetInterface("cell_0"); err != nil {
		t.Fatal("cell_0 link must exist")
	}
	st := h.Status(context.Background())
	if !st.Agent || !st.Installed || !st.Present || st.PlutoPort != "/dev/ttyACM3" || st.AdoptedAt == nil || st.Host == nil {
		t.Fatalf("status %+v", st)
	}

	// The radio: refused without a purpose, refused by the host without the
	// site attestation, up with both, down again.
	if _, err := h.Radio(context.Background(), "start", ""); err == nil {
		t.Fatal("no purpose must be refused")
	}
	if _, err := h.Radio(context.Background(), "start", "MESHSAT-1578 test"); err == nil || !strings.Contains(err.Error(), "SITE_OK") {
		t.Fatalf("the host's refusal must come through: %v", err)
	}
	agent.mu.Lock()
	agent.siteOK = true
	agent.mu.Unlock()
	res, err := h.Radio(context.Background(), "start", "MESHSAT-1578 test")
	if err != nil || res["radio"] != "up" {
		t.Fatalf("radio start: %v %v", res, err)
	}
	if _, err := h.Radio(context.Background(), "stop", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Radio(context.Background(), "restart", ""); err == nil {
		t.Fatal("unknown action")
	}

	// The Pluto goes: radio and core stopped, the gateway stopped, cell_0 off.
	m.handleDeviceEvent(context.Background(), transport.DeviceEvent{Type: "device_removed",
		Device: &transport.SerialDeviceEntry{DevPath: "/dev/ttyACM3", VIDPID: "0456:b673", Role: transport.RoleCellSDR}})
	waitUntil(t, "release", func() bool {
		row, err := db.GetGatewayConfigByInstance("cell_0")
		return err == nil && !row.Enabled && m.GetCellGateway() == nil
	})
	if !agent.called("cell_radio:stop") || !agent.called("cell_core:stop") {
		t.Fatalf("agent calls %v", agent.calls)
	}
	if st := h.Status(context.Background()); st.Present {
		t.Fatal("present after removal")
	}
	evMu.Lock()
	n := len(events)
	evMu.Unlock()
	if n < 3 {
		t.Fatalf("operator events %v", events)
	}
}

func TestCellHostWithoutAgentOrPackage(t *testing.T) {
	db := newTestDB(t)
	m := NewManager(db, nil)
	t.Cleanup(m.Stop)
	// No agent socket: nothing happens, the status says so.
	h := NewCellHost(&fakeAgent{available: false}, db, m, nil)
	h.OnPlutoConnected(context.Background(), "/dev/ttyACM3")
	if st := h.Status(context.Background()); st.Agent || st.LastError == "" || !strings.Contains(st.LastError, "host agent") {
		t.Fatalf("status %+v", st)
	}
	if _, err := h.Radio(context.Background(), "start", "x y z"); !errors.Is(err, ErrNoCellHost) {
		t.Fatalf("%v", err)
	}
	// Agent there, no cell package: the status says so, nothing is seeded.
	h = NewCellHost(&fakeAgent{available: true, installed: false}, db, m, nil)
	h.OnPlutoConnected(context.Background(), "/dev/ttyACM3")
	if st := h.Status(context.Background()); !st.Agent || st.Installed {
		t.Fatalf("status %+v", st)
	}
	if _, err := db.GetGatewayConfigByInstance("cell_0"); err == nil {
		t.Fatal("nothing must be seeded without the cell package")
	}
	if _, err := h.Core(context.Background(), "start"); !errors.Is(err, ErrNoCellKit) {
		t.Fatalf("%v", err)
	}
	h.OnPlutoRemoved(context.Background(), "/dev/ttyACM3") // must not panic or hang
	_ = time.Second
}
