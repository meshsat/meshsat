package gateway

import (
	"context"
	"strings"
	"sync"
	"testing"

	"meshsat/internal/transport"
)

// A detach stops the delivery workers of the SBD gateway's links before it
// stops the gateway, so no delivery meets "gateway iridium_0 not found or not
// running" (which killed QoS 0 at once and spent QoS 1+ retries in an
// ordinary Bluetooth outage); an attach resumes them once the gateway runs.
// Reconcile goes the same way. [MESHSAT-1391]
func TestSBDPipe_DetachStopsTheLinksWorkersFirstAndAttachResumesThem(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	m := NewManager(db, &fakeSat{imei: "300434067943980"})
	t.Cleanup(m.Stop)
	present := true
	m.SetSBDPipe(func() bool { return present })
	var mu sync.Mutex
	var calls []string
	running := func() string {
		if m.GetSBDGateway() != nil {
			return "gateway running"
		}
		return "gateway stopped"
	}
	m.SetSBDPipeWorkers(func(id string) {
		mu.Lock()
		calls = append(calls, "stop "+id+", "+running())
		mu.Unlock()
	}, func(id, typ string) {
		mu.Lock()
		calls = append(calls, "resume "+id+" "+typ+", "+running())
		mu.Unlock()
	})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	took := func() []string { mu.Lock(); defer mu.Unlock(); c := calls; calls = nil; return c }

	if err := m.AttachSBDPipe(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(took(), "; "); got != "resume iridium_0 iridium, gateway running" {
		t.Fatalf("attach: %s", got)
	}
	m.DetachSBDPipe()
	if got := strings.Join(took(), "; "); got != "stop iridium_0, gateway running" {
		t.Fatalf("detach: %s", got)
	}
	if m.GetSBDGateway() != nil {
		t.Fatal("the gateway runs after detach")
	}

	m.WatchDeviceEvents(ctx, transport.NewDeviceSupervisor())
	m.ReconcileWithHardware(ctx)
	if got := strings.Join(took(), "; "); got != "resume iridium_0 iridium, gateway running" {
		t.Fatalf("reconcile with the pipe there: %s", got)
	}
	present = false
	m.ReconcileWithHardware(ctx)
	if got := strings.Join(took(), "; "); got != "stop iridium_0, gateway running" {
		t.Fatalf("reconcile with the pipe gone: %s", got)
	}
	if m.GetSBDGateway() != nil {
		t.Fatal("the gateway runs without the node's modem")
	}
}

// Reconcile meets a start in flight (StartGatewayInstance's nil sentinel)
// for hardware that is not there: it leaves the start alone. It called Stop
// on the nil entry, which on a ModemManager phone (reconcile every 60 s)
// was a crash waiting for the timing. [MESHSAT-1391]
func TestReconcile_AStartInFlightIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	m := NewManager(db, &fakeSat{imei: "300434067943980"})
	t.Cleanup(m.Stop)
	if err := db.SaveGatewayConfigInstance("iridium", "iridium_0", true, "{}"); err != nil {
		t.Fatal(err)
	}
	m.WatchDeviceEvents(ctx, transport.NewDeviceSupervisor())
	m.mu.Lock()
	m.running["iridium_0"] = nil // a start in flight
	m.mu.Unlock()
	m.ReconcileWithHardware(ctx) // no 9603 in the registry: phase 1 for iridium_0
	m.mu.RLock()
	gw, ok := m.running["iridium_0"]
	m.mu.RUnlock()
	if !ok || gw != nil {
		t.Fatalf("the start in flight was not left alone: %v, %v", gw, ok)
	}
}

// With MESHSAT_IRIDIUM_PORT=ble the SBD gateway follows the Bluetooth node's
// modem pipe, never a USB 9603: attach starts iridium_0 with its config and
// link made as for a 9603 plugged in, detach stops it and keeps its config
// enabled, a 9603 the supervisor finds is left alone, and reconcile counts
// the pipe as the hardware. [MESHSAT-1391]
func TestSBDPipe_TheGatewayFollowsTheNodesModem(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	m := NewManager(db, &fakeSat{imei: "300434067943980"})
	t.Cleanup(m.Stop)
	present := false
	m.SetSBDPipe(func() bool { return present })
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	noLink(t, db, "iridium_0")

	if err := m.AttachSBDPipe(ctx); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if m.GetSBDGateway() == nil {
		t.Fatal("no SBD gateway after attach")
	}
	cfg, err := db.GetGatewayConfigByInstance("iridium_0")
	if err != nil || !cfg.Enabled || cfg.Type != "iridium" {
		t.Fatalf("config %+v, %v", cfg, err)
	}
	if l := mustLink(t, db, "iridium_0"); l.ChannelType != "iridium" || !l.Enabled {
		t.Fatalf("link %+v", l)
	}
	if err := m.AttachSBDPipe(ctx); err != nil {
		t.Fatalf("a second attach: %v", err)
	}

	m.DetachSBDPipe()
	if m.GetSBDGateway() != nil {
		t.Fatal("the SBD gateway runs after detach")
	}
	if cfg, err := db.GetGatewayConfigByInstance("iridium_0"); err != nil || !cfg.Enabled {
		t.Fatalf("detach changed the config: %+v, %v", cfg, err)
	}
	m.DetachSBDPipe() // nothing running: nothing to do

	// A USB 9603 found by the supervisor makes no config and starts nothing.
	if err := m.DeleteInstance("iridium_0"); err != nil {
		t.Fatal(err)
	}
	m.handleDeviceEvent(ctx, transport.DeviceEvent{Type: "device_connected",
		Device: &transport.SerialDeviceEntry{DevPath: "/dev/ttyUSB0", Role: transport.RoleIridium9603}})
	if _, err := db.GetGatewayConfigByInstance("iridium_0"); err == nil {
		t.Fatal("a USB 9603 made an SBD gateway config")
	}

	// Reconcile counts the pipe, not the supervisor's registry.
	m.WatchDeviceEvents(ctx, transport.NewDeviceSupervisor())
	present = true
	m.ReconcileWithHardware(ctx)
	if m.GetSBDGateway() == nil {
		t.Fatal("reconcile did not start the SBD gateway on the node's modem")
	}
	present = false
	m.ReconcileWithHardware(ctx)
	if m.GetSBDGateway() != nil {
		t.Fatal("reconcile left the SBD gateway running without the node's modem")
	}
}
