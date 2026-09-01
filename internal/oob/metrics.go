package oob

import (
	"maps"
	"sync"
)

// Metrics holds the process-wide OOB counters. The Prometheus collector in
// internal/api reads Snapshot on every scrape, the same bridge pattern the
// HeMB counters use. Frame results: accepted, replay, bad_tag, unknown_peer,
// disabled_peer, disabled, bad_header. Command results: ResultCode strings.
type Metrics struct {
	mu       sync.Mutex
	frames   map[string]uint64
	commands map[string]map[string]uint64
}

// Global is the shared instance.
var Global = &Metrics{
	frames:   map[string]uint64{},
	commands: map[string]map[string]uint64{},
}

// IncFrame counts one inbound frame outcome.
func (m *Metrics) IncFrame(result string) {
	m.mu.Lock()
	m.frames[result]++
	m.mu.Unlock()
}

// IncCommand counts one executed command outcome.
func (m *Metrics) IncCommand(cmd, result string) {
	m.mu.Lock()
	if m.commands[cmd] == nil {
		m.commands[cmd] = map[string]uint64{}
	}
	m.commands[cmd][result]++
	m.mu.Unlock()
}

// Snapshot returns copies of both counter maps.
func (m *Metrics) Snapshot() (frames map[string]uint64, commands map[string]map[string]uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	frames = maps.Clone(m.frames)
	commands = make(map[string]map[string]uint64, len(m.commands))
	for c, res := range m.commands {
		commands[c] = maps.Clone(res)
	}
	return frames, commands
}
