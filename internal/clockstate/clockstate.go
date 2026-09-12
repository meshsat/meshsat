// Package clockstate reports whether the host's wall clock can be trusted.
//
// A field kit Pi 5 has no RTC backup cell: the kernel comes up at the epoch and
// `fixrtc` restores the root filesystem's last mount time, so a cold boot can
// start hours in the past and stay there until chrony steps it — which on
// 12 Sep 2026 happened 17 s into one boot and never at all in the next. The
// boot-time guard (deploy/time/meshsat-clock-guard) establishes the clock
// before docker starts and records the outcome in a small key=value file. This
// package reads that file so the bridge can refuse to hand a clock it does not
// trust to the radio or to mesh peers. [MESHSAT-1056]
//
// Every failure mode reads as trusted. A kit without the guard, an unreadable
// file, a malformed file or a file left behind by an earlier boot must all
// behave exactly as the bridge did before this existed.
package clockstate

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Source values written by the guard, plus the two this package infers.
const (
	SourceUnknown = "unknown" // no state file: no guard installed
	SourceStale   = "stale"   // state file is from an earlier boot
)

// State is the guard's verdict on the host clock.
type State struct {
	// Trusted is false only when the guard ran during this boot and could
	// not establish the time from any source.
	Trusted bool `json:"trusted"`
	// Source names what set the clock: floor, chrony, cellular, gps, none,
	// or one of SourceUnknown / SourceStale.
	Source string `json:"source"`
	// CheckedAt is when the guard wrote its verdict. Zero when unknown.
	CheckedAt time.Time `json:"checked_at,omitzero"`
	// BootID is the boot the verdict belongs to.
	BootID string `json:"-"`
}

// DefaultPath returns the state file the guard writes next to the database.
// The data directory is already bind-mounted into the bridge container, so the
// file needs no compose change to be visible.
func DefaultPath(dbPath string) string {
	if dbPath == "" {
		return "/data/clock-state"
	}
	return filepath.Join(filepath.Dir(dbPath), "clock-state")
}

// Parse reads the guard's key=value file. Unknown keys are ignored.
func Parse(r io.Reader) State {
	st := State{Trusted: true, Source: SourceUnknown}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"`)
		switch key {
		case "trusted":
			// Only an explicit "no" withdraws trust; anything else, including
			// a value this build does not recognise, leaves it granted.
			st.Trusted = !strings.EqualFold(val, "no")
		case "source":
			if val != "" {
				st.Source = val
			}
		case "boot_id":
			st.BootID = val
		case "checked_at":
			if t, err := time.Parse(time.RFC3339, val); err == nil {
				st.CheckedAt = t
			}
		}
	}
	if err := sc.Err(); err != nil {
		// A file we could not read to the end is a failure like any other.
		return State{Trusted: true, Source: SourceUnknown}
	}
	return st
}

// bootIDPath is the kernel's per-boot identifier, readable inside a container.
const bootIDPath = "/proc/sys/kernel/random/boot_id"

func readBootID() string {
	b, err := os.ReadFile(bootIDPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Provider caches the state file and re-reads it at most once per TTL, so the
// hot paths that ask on every mesh handshake or time-sync request do not stat
// the filesystem each time.
type Provider struct {
	path string
	ttl  time.Duration

	mu       sync.Mutex
	cur      State
	lastRead time.Time

	// Test seams.
	now    func() time.Time
	bootID func() string
}

// NewProvider returns a provider for the given state file. An empty path
// disables the check entirely and every read reports trusted.
func NewProvider(path string) *Provider {
	return &Provider{
		path:   path,
		ttl:    15 * time.Second,
		now:    time.Now,
		bootID: readBootID,
	}
}

// State returns the current verdict, re-reading the file when the cache is old.
func (p *Provider) State() State {
	if p == nil || p.path == "" {
		return State{Trusted: true, Source: SourceUnknown}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if !p.lastRead.IsZero() && now.Sub(p.lastRead) < p.ttl {
		return p.cur
	}
	p.cur = p.load()
	p.lastRead = now
	return p.cur
}

// Trusted is the hot-path helper: true unless the guard positively said no.
func (p *Provider) Trusted() bool { return p.State().Trusted }

func (p *Provider) load() State {
	f, err := os.Open(p.path)
	if err != nil {
		return State{Trusted: true, Source: SourceUnknown}
	}
	defer f.Close()

	st := Parse(f)

	// A verdict from an earlier boot says nothing about this one. This is the
	// case where the guard is installed but did not run — an operator started
	// docker by hand, or the unit was disabled — and the stale file must not
	// keep the bridge muted for the rest of the day.
	if cur := p.bootID(); cur != "" && st.BootID != "" && st.BootID != cur {
		return State{Trusted: true, Source: SourceStale, CheckedAt: st.CheckedAt}
	}
	return st
}
