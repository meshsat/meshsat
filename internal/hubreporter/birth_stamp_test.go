package hubreporter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// memStamps is a BirthStampStore whose reads and saves can be made to fail.
type memStamps struct {
	mu                 sync.Mutex
	last               time.Time
	saved              []time.Time
	failLoad, failSave bool
}

var errStampStore = errors.New("injected stamp store failure")

func (m *memStamps) LastBirthStamp() (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failLoad {
		return time.Time{}, errStampStore
	}
	return m.last, nil
}

func (m *memStamps) SaveBirthStamp(t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failSave {
		return errStampStore
	}
	m.last = t
	m.saved = append(m.saved, t)
	return nil
}

func (m *memStamps) set(failLoad, failSave bool) {
	m.mu.Lock()
	m.failLoad, m.failSave = failLoad, failSave
	m.mu.Unlock()
}

func (m *memStamps) stored() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
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
		if got, err := r.birthStamp(s.now); err != nil || !got.Equal(s.want) {
			t.Fatalf("step %d: stamp %v %v, want %v", i, got, err, s.want)
		}
	}
	if len(m.saved) != len(steps) || !m.last.Equal(steps[len(steps)-1].want) {
		t.Fatalf("stored %v", m.saved)
	}
	restarted := &HubReporter{cfg: ReporterConfig{BirthStamps: m}}
	if got, err := restarted.birthStamp(t0); err != nil || !got.After(steps[len(steps)-1].want) {
		t.Fatalf("after a restart with the clock behind: %v %v", got, err)
	}
	inMemory := &HubReporter{}
	a, _ := inMemory.birthStamp(t0.Add(time.Minute))
	if b, _ := inMemory.birthStamp(t0); !b.After(a) {
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
			if got := m.stored(); !got.Equal(birth.Timestamp) {
				t.Fatalf("stored %v, published %v", got, birth.Timestamp)
			}
			return
		case <-deadline:
			t.Fatal("no birth published")
		}
	}
}

// publishedBirth is a birth as a fake broker received it.
type publishedBirth struct {
	BridgeBirth
	raw []byte
}

// birthsOn reads the births a fake broker received within d. A QoS 1
// publish can arrive twice (at least once): a payload already seen in this
// test is the same birth, not another.
func birthsOn(t *testing.T, b *fakeBroker, seen map[string]bool, d time.Duration) []publishedBirth {
	t.Helper()
	var out []publishedBirth
	deadline := time.After(d)
	for {
		select {
		case p := <-b.pubs:
			if !strings.HasSuffix(p.topic, "/birth") || seen[string(p.payload)] {
				continue
			}
			seen[string(p.payload)] = true
			var birth BridgeBirth
			if err := json.Unmarshal(p.payload, &birth); err != nil {
				t.Fatal(err)
			}
			out = append(out, publishedBirth{birth, p.payload})
		case <-deadline:
			return out
		}
	}
}

func connectedReporter(t *testing.T, b *fakeBroker, m BirthStampStore, clock time.Time, format string) *HubReporter {
	t.Helper()
	r := NewHubReporter(ReporterConfig{HubURL: "tcp://" + b.addr(), BridgeID: "kit-a", BirthStamps: m},
		func() BridgeBirth {
			return BridgeBirth{BridgeID: "kit-a", TenantID: "default", Interfaces: []InterfaceInfo{{
				Name: "iridium_imt", Type: "iridium_imt", Status: "online", IMEI: "300434060000041",
				EgressFormat: format, EgressSealed: format == "sealed"}}}
		},
		func() BridgeHealth { return BridgeHealth{BridgeID: "kit-a"} })
	r.clock = func() time.Time { return clock }
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for deadline := time.Now().Add(10 * time.Second); !r.IsConnected(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("never connected")
		}
	}
	return r
}

// A birth whose stamp cannot be read or stored does not go out: no lower
// stamp is published and the stored floor is not lowered. Once the store
// answers again the birth goes out by itself, stamped after every earlier
// one, and a restart with the clock behind stamps after them too. The births
// published are written to MESHSAT_BIRTH_FIXTURE_OUT when it is set, for the
// Hub's side of the same run (meshsat-hub, TestBirthStampFaultRunOnTheHub).
// [MESHSAT-1584]
func TestBirthWithheldUntilItsStampIsStored(t *testing.T) {
	old := birthRetryEvery
	birthRetryEvery = 50 * time.Millisecond
	t.Cleanup(func() { birthRetryEvery = old })
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	b := newFakeBroker(t, "127.0.0.1:0")
	m := &memStamps{}
	seen := map[string]bool{}
	var all []publishedBirth

	// Before the restart: the clock an hour ahead, the modem plain.
	r1 := connectedReporter(t, b, m, t0.Add(time.Hour), "plain")
	got := birthsOn(t, b, seen, time.Second)
	if len(got) != 1 {
		t.Fatalf("first birth: %d published", len(got))
	}
	all = append(all, got...)
	r1.Stop()

	// The restart: the clock behind, the chain now sealed, and the store
	// cannot be read. Nothing goes out while it cannot.
	m.set(true, false)
	r2 := connectedReporter(t, b, m, t0, "sealed")
	defer r2.Stop()
	if got := birthsOn(t, b, seen, 400*time.Millisecond); len(got) != 0 {
		t.Fatalf("published while the stored stamp could not be read: %+v", got)
	}
	m.set(false, false)
	got = birthsOn(t, b, seen, time.Second)
	if len(got) != 1 {
		t.Fatalf("after the store could be read again: %d births, want the one held back", len(got))
	}
	all = append(all, got[0])

	// A save that fails: nothing goes out, and nothing older is stored.
	floor := m.stored()
	m.set(false, true)
	r2.RepublishBirth()
	if got := birthsOn(t, b, seen, 400*time.Millisecond); len(got) != 0 {
		t.Fatalf("published a stamp that was not stored: %+v", got)
	}
	if !m.stored().Equal(floor) {
		t.Fatalf("stored floor moved while saves failed: %v, was %v", m.stored(), floor)
	}
	m.set(false, false)
	got = birthsOn(t, b, seen, time.Second)
	if len(got) != 1 {
		t.Fatalf("after the store took saves again: %d births, want the one held back", len(got))
	}
	all = append(all, got[0])
	r2.Stop()

	// Another restart, the clock still behind.
	r3 := connectedReporter(t, b, m, t0, "sealed")
	defer r3.Stop()
	got = birthsOn(t, b, seen, time.Second)
	if len(got) != 1 {
		for _, g := range got {
			t.Logf("published %s %s", g.Interfaces[0].EgressFormat, g.Timestamp.Format(time.RFC3339Nano))
		}
		t.Fatalf("after the second restart: %d published", len(got))
	}
	all = append(all, got...)

	for i := 1; i < len(all); i++ {
		if !all[i].Timestamp.After(all[i-1].Timestamp) {
			t.Fatalf("birth %d stamped %v, not after birth %d at %v", i, all[i].Timestamp, i-1, all[i-1].Timestamp)
		}
	}
	if !m.stored().Equal(all[len(all)-1].Timestamp) {
		t.Fatalf("stored %v, last published %v", m.stored(), all[len(all)-1].Timestamp)
	}
	for i, b := range all {
		t.Logf("birth %d: %s %s", i, b.Interfaces[0].EgressFormat, b.Timestamp.Format(time.RFC3339Nano))
	}
	if out := os.Getenv("MESHSAT_BIRTH_FIXTURE_OUT"); out != "" {
		var lines []byte
		for _, b := range all {
			lines = append(append(lines, b.raw...), '\n')
		}
		if err := os.WriteFile(out, lines, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
