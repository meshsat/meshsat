package gateway

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
)

// Paths are package vars (not const) so unit tests can point them at a
// fake shell script without needing root or the real direwolf binary.
var (
	direwolfBinary    = "/usr/local/bin/direwolf"
	direwolfPreflight = "/usr/local/bin/direwolf-preflight.sh"
	direwolfConfPath  = "/run/meshsat/direwolf.conf"
)

const (
	supervisorStopGrace  = 5 * time.Second
	supervisorHealthy    = 60 * time.Second
	supervisorBackoffMin = 5 * time.Second
	supervisorBackoffMax = 5 * time.Minute
)

// DirewolfSupervisor runs Direwolf as a child process of the MeshSat bridge.
// When APRSConfig.ExternalDirewolf is true, callers must not construct a
// supervisor — APRSGateway connects to whatever KISS server the operator
// provides on KISSHost:KISSPort. [MESHSAT-516]
type DirewolfSupervisor struct {
	cfg APRSConfig

	running      atomic.Bool
	restartCount atomic.Int64
	lastExitCode atomic.Int32

	cmdMu sync.Mutex
	cmd   *exec.Cmd

	cancel context.CancelFunc
	done   chan struct{}
}

// NewDirewolfSupervisor returns a supervisor that will write its config to
// /run/meshsat/direwolf.conf and exec the bundled binary.
func NewDirewolfSupervisor(cfg APRSConfig) *DirewolfSupervisor {
	return &DirewolfSupervisor{cfg: cfg, done: make(chan struct{})}
}

// Running reports whether the child process is currently alive.
func (s *DirewolfSupervisor) Running() bool { return s.running.Load() }

// RestartCount reports how many times the child has been respawned since
// Start was called.
func (s *DirewolfSupervisor) RestartCount() int64 { return s.restartCount.Load() }

// Start writes the rendered config, runs the preflight, and launches the
// supervisor goroutine. Safe to call once per lifetime.
func (s *DirewolfSupervisor) Start(ctx context.Context) error {
	if _, err := os.Stat(direwolfBinary); err != nil {
		return fmt.Errorf("direwolf supervisor: binary not found at %s: %w", direwolfBinary, err)
	}
	if err := writeDirewolfConf(direwolfConfPath, s.cfg); err != nil {
		return fmt.Errorf("direwolf supervisor: write conf: %w", err)
	}

	ctx, s.cancel = context.WithCancel(ctx)
	go s.run(ctx)
	log.Info().
		Str("conf", direwolfConfPath).
		Str("callsign", fmt.Sprintf("%s-%d", s.cfg.Callsign, s.cfg.SSID)).
		Int("baud", s.cfg.ModemBaud).
		Msg("direwolf: supervisor started")
	return nil
}

// Stop cancels the supervisor and waits up to supervisorStopGrace for the
// child to exit; after that it SIGKILLs. Idempotent.
func (s *DirewolfSupervisor) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	select {
	case <-s.done:
	case <-time.After(supervisorStopGrace + 2*time.Second):
		// run() couldn't reap in time; nothing more we can do here.
	}
}

func (s *DirewolfSupervisor) run(ctx context.Context) {
	defer close(s.done)
	backoff := supervisorBackoffMin
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		s.runPreflight(ctx)

		startedAt := time.Now()
		if err := s.spawn(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Warn().Err(err).Msg("direwolf: spawn failed")
		}

		// If the child stayed up long enough to be considered healthy, reset
		// backoff — a transient crash should not permanently slow recovery.
		if time.Since(startedAt) >= supervisorHealthy {
			backoff = supervisorBackoffMin
		}

		if ctx.Err() != nil {
			return
		}
		log.Warn().
			Dur("retry_in", backoff).
			Int64("restart_count", s.restartCount.Load()).
			Msg("direwolf: child exited, restarting")

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > supervisorBackoffMax {
			backoff = supervisorBackoffMax
		}
		s.restartCount.Add(1)
	}
}

func (s *DirewolfSupervisor) runPreflight(ctx context.Context) {
	if _, err := os.Stat(direwolfPreflight); err != nil {
		// Preflight is optional — log once and continue.
		log.Debug().Err(err).Msg("direwolf: preflight script missing; skipping")
		return
	}
	cmd := exec.CommandContext(ctx, direwolfPreflight)
	out, err := cmd.CombinedOutput()
	for _, line := range splitLines(out) {
		log.Info().Str("src", "direwolf-preflight").Msg(line)
	}
	if err != nil {
		log.Warn().Err(err).Msg("direwolf: preflight returned non-zero (continuing)")
	}
}

func (s *DirewolfSupervisor) spawn(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, direwolfBinary, "-t", "0", "-c", direwolfConfPath)
	// New process group so SIGTERM on ctx cancel reaches Direwolf cleanly
	// rather than being swallowed by any shell wrapper.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Graceful shutdown on cancel: exec.CommandContext sends SIGKILL by
	// default; we override with SIGTERM + delayed SIGKILL in Cancel.
	cmd.Cancel = func() error {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		return nil
	}
	cmd.WaitDelay = supervisorStopGrace

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	s.cmdMu.Lock()
	s.cmd = cmd
	s.cmdMu.Unlock()
	s.running.Store(true)
	defer s.running.Store(false)

	var wg sync.WaitGroup
	wg.Add(2)
	go pipeToLog(&wg, stdout, "direwolf")
	go pipeToLog(&wg, stderr, "direwolf")

	err = cmd.Wait()
	wg.Wait()
	if exitErr, ok := err.(*exec.ExitError); ok {
		s.lastExitCode.Store(int32(exitErr.ExitCode()))
	}
	return err
}

func pipeToLog(wg *sync.WaitGroup, r io.Reader, src string) {
	defer wg.Done()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for sc.Scan() {
		log.Info().Str("src", src).Msg(sc.Text())
	}
}

func splitLines(b []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytesReader(b))
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// bytesReader avoids importing bytes just for one NewReader.
func bytesReader(b []byte) io.Reader { return &byteSliceReader{b: b} }

type byteSliceReader struct {
	b []byte
	i int
}

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

// writeDirewolfConf renders and writes the Direwolf config file. The file
// is placed under /run (tmpfs in the container) so it disappears on
// restart — no stale configs to worry about.
func writeDirewolfConf(path string, cfg APRSConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, renderDirewolfConf(cfg)); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// renderDirewolfConf builds the minimal Direwolf config that matches
// the MeshSat APRS integration on an AIOC-attached UV-K5/Baofeng kit:
// single channel, AFSK 1200 @ 48 kHz, CM108 PTT (the AIOC keys PTT via
// the CM108/CM109/CM119 HID GPIO chip — serial RTS/DTR on the AIOC's
// USB-ACM endpoint is NOT wired to the radio), KISS on the configured
// port, no AGW, no IGate (MeshSat handles APRS-IS directly via the
// gateway).
//
// TXDELAY/TXTAIL are in 10 ms units — 300 ms preamble lets the radio's
// transmitter lock before the first AFSK byte, 100 ms tail keeps the
// carrier up past the last byte. Without these the first few bytes of
// every packet are clipped on most handhelds.
//
// Direwolf's KISSPORT directive only accepts a port number — not
// IP:PORT — and binds INADDR_ANY unconditionally. We therefore enforce
// loopback-only binding at the C source level via a Dockerfile sed
// patch (see `direwolf-builder` stage in Dockerfile). With that patch
// `KISSPORT 8001` results in a listener on 127.0.0.1:8001 only.
// [MESHSAT-517, PTT-CM108 fix 2026-04-17]
func renderDirewolfConf(cfg APRSConfig) string {
	pttLine := "PTT CM108"
	// Escape hatch: if the operator sets ptt_line to serial-like
	// (RTS/DTR) we honour the legacy serial path. Default AIOC behaviour
	// is CM108 HID and ignores PTTDevice/PTTLine.
	switch cfg.PTTLine {
	case "RTS", "DTR":
		pttLine = fmt.Sprintf("PTT %s %s", cfg.PTTDevice, cfg.PTTLine)
	}
	return fmt.Sprintf(`# Generated by meshsat DirewolfSupervisor — do not edit.
# Regenerated on every APRS gateway start. [MESHSAT-514]
ADEVICE  plughw:%s,0
ARATE 48000
ACHANNELS 1
CHANNEL 0
MYCALL %s-%d
MODEM %d
%s
TXDELAY 30
TXTAIL 10
KISSPORT %d
AGWPORT 0
`,
		cfg.AudioCard,
		cfg.Callsign, cfg.SSID,
		cfg.ModemBaud,
		pttLine,
		cfg.KISSPort,
	)
}
