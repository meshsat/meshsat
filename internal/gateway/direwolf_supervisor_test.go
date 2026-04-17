package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRenderDirewolfConf_Shape(t *testing.T) {
	cfg := APRSConfig{
		KISSPort:  8001,
		Callsign:  "PA3XYZ",
		SSID:      10,
		AudioCard: "AllInOneCable",
		PTTDevice: "/dev/ttyACM1",
		PTTLine:   "RTS",
		ModemBaud: 1200,
	}
	got := renderDirewolfConf(cfg)
	for _, want := range []string{
		"ADEVICE  plughw:AllInOneCable,0",
		"MYCALL PA3XYZ-10",
		"MODEM 1200",
		"PTT /dev/ttyACM1 RTS",
		"KISSPORT 8001",
		"AGWPORT 0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered conf missing %q\n---\n%s", want, got)
		}
	}
}

func TestRenderDirewolfConf_9600Baud(t *testing.T) {
	cfg := APRSConfig{KISSPort: 8001, Callsign: "N0CALL", SSID: 1, AudioCard: "hw", PTTDevice: "/dev/null", PTTLine: "DTR", ModemBaud: 9600}
	got := renderDirewolfConf(cfg)
	if !strings.Contains(got, "MODEM 9600") {
		t.Errorf("expected MODEM 9600 in rendered conf, got:\n%s", got)
	}
}

func TestWriteDirewolfConf_Perms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "direwolf.conf")
	cfg := DefaultAPRSConfig()
	cfg.Callsign = "PA3XYZ"
	if err := writeDirewolfConf(path, cfg); err != nil {
		t.Fatalf("writeDirewolfConf: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("expected 0600, got %o", mode)
	}
}

// TestSupervisor_RestartsOnExit verifies that the supervisor respawns the
// child after it exits and honours ctx cancel. The direwolf binary is
// replaced with /bin/sh to avoid requiring the real binary in unit tests.
func TestSupervisor_RestartsOnExit(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this platform")
	}
	// Override the binary path via a sentinel: we copy /bin/sh to a temp
	// file and symlink /usr/local/bin/direwolf in a test-only hook would
	// require root. Instead we run the supervisor with a direct exec path
	// by reaching into internals — acceptable for a small test.
	oldBinary, oldConfPath := origSupervisorPaths()
	t.Cleanup(func() { setSupervisorPaths(oldBinary, oldConfPath) })

	dir := t.TempDir()
	// Fake direwolf: prints something and exits 0 after a short sleep.
	// The supervisor's 60s healthy threshold won't fire, so backoff will
	// grow — but we only care that a restart happens at all within 12s.
	fake := filepath.Join(dir, "fake_direwolf.sh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'fake direwolf started'\nsleep 1\n"), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	setSupervisorPaths(fake, filepath.Join(dir, "direwolf.conf"))

	sup := NewDirewolfSupervisor(DefaultAPRSConfig())
	sup.cfg.Callsign = "TEST"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait up to 12s for at least one restart. Backoff is 5s min so we
	// need to cover: run(1s) + backoff(5s) + run(1s) + backoff(10s) = ~17s
	// between the 0th and 2nd respawn. One restart (count >= 1) is enough.
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if sup.RestartCount() >= 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if sup.RestartCount() < 1 {
		t.Errorf("expected at least 1 restart within 12s, got %d", sup.RestartCount())
	}

	cancel()
	time.Sleep(500 * time.Millisecond)
	if sup.Running() {
		t.Errorf("supervisor still reports Running after cancel")
	}
}

// Package-level paths are overridden for testing. Saved/restored by the
// test so parallel test runs don't clobber each other.
func origSupervisorPaths() (string, string) { return direwolfBinary, direwolfConfPath }

func setSupervisorPaths(bin, conf string) {
	direwolfBinary = bin
	direwolfConfPath = conf
	// Preflight may not exist in the fake environment — point it at the
	// binary so Stat fails cleanly and we skip preflight in tests.
	direwolfPreflight = bin + ".preflight.does.not.exist"
}
