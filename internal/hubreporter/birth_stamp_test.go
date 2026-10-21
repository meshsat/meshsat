package hubreporter

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

type memStamps struct {
	mu    sync.Mutex
	last  time.Time
	saved []time.Time
}

func (m *memStamps) LastBirthStamp() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}

func (m *memStamps) SaveBirthStamp(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.last = t
	m.saved = append(m.saved, t)
}

// A birth is stamped with the clock, but always later than the last birth,
// also when the clock steps back and across a restart (a new reporter on the
// same store): the Hub orders a bridge's births by it. [MESHSAT-1584]
func TestBirthStampNeverGoesBack(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	m := &memStamps{last: t0.Add(10 * time.Second)} // stored before a restart
	r := &HubReporter{cfg: ReporterConfig{BirthStamps: m}}
	steps := []struct {
		now, want time.Time
	}{
		{t0, t0.Add(10*time.Second + time.Microsecond)},                      // clock behind the stored stamp
		{t0.Add(20 * time.Second), t0.Add(20 * time.Second)},                 // clock ahead: the clock
		{t0.Add(5 * time.Second), t0.Add(20*time.Second + time.Microsecond)}, // clock stepped back
		{t0.Add(20 * time.Second), t0.Add(20*time.Second + 2*time.Microsecond)},
	}
	for i, s := range steps {
		if got := r.birthStamp(s.now); !got.Equal(s.want) {
			t.Fatalf("step %d: stamp %v, want %v", i, got, s.want)
		}
	}
	if len(m.saved) != len(steps) || !m.last.Equal(steps[len(steps)-1].want) {
		t.Fatalf("stored %v", m.saved)
	}
	restarted := &HubReporter{cfg: ReporterConfig{BirthStamps: m}}
	if got := restarted.birthStamp(t0); !got.After(steps[len(steps)-1].want) {
		t.Fatalf("after a restart with the clock behind: %v", got)
	}
	inMemory := &HubReporter{}
	a := inMemory.birthStamp(t0.Add(time.Minute))
	if b := inMemory.birthStamp(t0); !b.After(a) {
		t.Fatalf("without a store, a clock stepped back: %v after %v", b, a)
	}
}

// The birth that goes out carries that stamp: with a stored stamp an hour
// ahead of the clock, the published birth is stamped after it.
func TestPublishedBirthIsStampedAfterTheLastOne(t *testing.T) {
	b := newFakeBroker(t, "127.0.0.1:0")
	ahead := time.Now().UTC().Add(time.Hour)
	m := &memStamps{last: ahead}
	r := NewHubReporter(ReporterConfig{HubURL: "tcp://" + b.addr(), BridgeID: "kit-a", BirthStamps: m},
		func() BridgeBirth { return BridgeBirth{BridgeID: "kit-a", Timestamp: time.Now().UTC()} },
		func() BridgeHealth { return BridgeHealth{BridgeID: "kit-a"} })
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case p := <-b.pubs:
			if !strings.HasSuffix(p.topic, "/birth") {
				continue
			}
			var birth BridgeBirth
			if err := json.Unmarshal(p.payload, &birth); err != nil {
				t.Fatal(err)
			}
			if !birth.Timestamp.After(ahead) {
				t.Fatalf("birth stamped %v, not after the stored %v", birth.Timestamp, ahead)
			}
			if got := m.LastBirthStamp(); !got.Equal(birth.Timestamp) {
				t.Fatalf("stored %v, published %v", got, birth.Timestamp)
			}
			return
		case <-deadline:
			t.Fatal("no birth published")
		}
	}
}
