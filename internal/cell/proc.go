package cell

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// process is one supervised Osmocom daemon: restarted with a capped
// backoff while it is wanted, stopped with SIGTERM then SIGKILL.
type process struct {
	name   string
	argv   []string
	dir    string
	env    []string
	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{}
	wanted atomic.Bool
	starts atomic.Int64
	since  time.Time
}

func newProcess(name string, dir string, argv ...string) *process {
	return &process{name: name, argv: argv, dir: dir}
}

// start launches the daemon and a keeper that restarts it while wanted.
func (p *process) start(parent context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.wanted.Load() {
		return
	}
	p.wanted.Store(true)
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	p.done = make(chan struct{})
	go p.keep(ctx)
}

func (p *process) keep(ctx context.Context) {
	defer close(p.done)
	wait := 2 * time.Second
	for p.wanted.Load() && ctx.Err() == nil {
		cmd := exec.Command(p.argv[0], p.argv[1:]...)
		cmd.Dir = p.dir
		cmd.Env = append(os.Environ(), p.env...)
		bindToParent(cmd)
		stdout, _ := cmd.StdoutPipe()
		stderr, _ := cmd.StderrPipe()
		if err := cmd.Start(); err != nil {
			log.Error().Err(err).Str("proc", p.name).Msg("cell: daemon did not start")
			if !sleepCtx(ctx, wait) {
				return
			}
			continue
		}
		p.mu.Lock()
		p.cmd = cmd
		p.since = time.Now()
		p.mu.Unlock()
		p.starts.Add(1)
		log.Info().Str("proc", p.name).Int("pid", cmd.Process.Pid).Msg("cell: daemon started")
		go pipeLog(p.name, stdout)
		go pipeLog(p.name, stderr)
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		select {
		case <-ctx.Done():
			terminate(cmd)
			<-exited
			return
		case err := <-exited:
			p.mu.Lock()
			p.cmd = nil
			p.mu.Unlock()
			if !p.wanted.Load() || ctx.Err() != nil {
				log.Info().Str("proc", p.name).Msg("cell: daemon stopped")
				return
			}
			up := time.Since(p.since)
			log.Warn().Err(err).Str("proc", p.name).Dur("ran", up.Truncate(time.Second)).Dur("retry_in", wait).Msg("cell: daemon exited, restarting")
			if !sleepCtx(ctx, wait) {
				return
			}
			if up > time.Minute {
				wait = 2 * time.Second
			} else if wait < 30*time.Second {
				wait *= 2
			}
		}
	}
}

func terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	cmd.Process.Signal(os.Interrupt)
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()
	done := make(chan struct{})
	go func() {
		for {
			if cmd.ProcessState != nil {
				close(done)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	select {
	case <-done:
	case <-t.C:
		cmd.Process.Kill()
	}
}

// stop ends the daemon and its keeper.
func (p *process) stop() {
	p.mu.Lock()
	if !p.wanted.Load() {
		p.mu.Unlock()
		return
	}
	p.wanted.Store(false)
	cancel, done := p.cancel, p.done
	cmd := p.cmd
	p.mu.Unlock()
	if cancel != nil {
		cancel() // the keeper sees the stop before the exit, and does not log a restart
	}
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Signal(os.Interrupt)
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			log.Warn().Str("proc", p.name).Msg("cell: daemon did not stop in 10 s")
		}
	}
}

// running reports whether the daemon is up right now.
func (p *process) running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd != nil && p.cmd.Process != nil && p.cmd.ProcessState == nil
}

func (p *process) upSince() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil {
		return time.Time{}
	}
	return p.since
}

func pipeLog(name string, r interface{ Read([]byte) (int, error) }) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 256*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		log.Debug().Str("proc", name).Msg(line)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// lookPath finds a daemon in BinDir or PATH.
func lookPath(binDir, name string) (string, bool) {
	if binDir != "" {
		p := filepath.Join(binDir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
		return "", false
	}
	p, err := exec.LookPath(name)
	return p, err == nil
}
