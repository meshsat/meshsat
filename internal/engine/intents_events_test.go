package engine

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/directory"
	"meshsat/internal/transport"
)

// The delivery status view follows the event stream (event-driven status
// refresh, tracked since the B10 reviews): every write about an intent
// reaches the stream as an event naming it, so a card reads it again at
// once, and a closed card that stopped polling still learns of a late
// answer. [MESHSAT-1584]

type intentEventLog struct {
	mu sync.Mutex
	ev []transport.MeshEvent
}

func (l *intentEventLog) add(e transport.MeshEvent) {
	l.mu.Lock()
	l.ev = append(l.ev, e)
	l.mu.Unlock()
}

// seq is what the stream said about intent id, in order: the event type
// and its change, transition or outcome class.
func (l *intentEventLog) seq(id string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, e := range l.ev {
		var d map[string]interface{}
		if json.Unmarshal(e.Data, &d) != nil || d["intent_id"] != id {
			continue
		}
		what := ""
		for _, k := range []string{"change", "transition", "status"} {
			if v, ok := d[k].(string); ok && v != "" {
				what = v
				break
			}
		}
		out = append(out, e.Type+":"+what)
	}
	return out
}

func (l *intentEventLog) since(id string, n int) []string { return l.seq(id)[n:] }

func hasInOrder(got []string, want ...string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

func TestIntentEvents_EveryWriteAboutAnIntentNamesIt(t *testing.T) {
	h := newIntentHarness(t)
	events := &intentEventLog{}
	h.d.SetEmitter(events.add)
	for _, w := range h.workers {
		w.emit = events.add
	}
	id := h.submit(h.contact(meshAddr(0)), "events", directory.StrategyPrimaryOnly)
	if got := events.seq(id); !hasInOrder(got, "delivery_intent:written") {
		t.Fatalf("after the submission: %v; want the new intent named", got)
	}
	n := len(events.seq(id))
	h.work("mesh_0")
	if got := events.since(id, n); !hasInOrder(got, "delivery_intent:handed_off", "delivery_attempt:accepted", "delivery_intent:written") {
		t.Fatalf("one attempt: %v; want handed_off before the attempt's end, then written", got)
	}
	h.drain()
	pkt := h.mesh.lastID()

	// The receipt allowance passes: the intent closes, and the card stops
	// polling once nothing is in flight.
	h.clock.Advance(121 * time.Second)
	h.esc.Heartbeat()
	if s := h.snap(id); s.Intent.State != "closed" {
		t.Fatalf("state %s; want closed after the receipt allowance", s.Intent.State)
	}
	n = len(events.seq(id))
	// A late answer: recorded as evidence of the closed intent, which may
	// upgrade its outcome (T14). The stream names the intent for the
	// evidence and for the upgrade.
	h.esc.MeshRoutingReceipt("mesh_0", transport.MeshMessage{From: 0xabcd, RequestID: pkt,
		PortNum: int(transport.PortNumRouting), Routing: &transport.RoutingInfo{ErrorName: "NONE"}})
	h.drain()
	got := events.since(id, n)
	if !hasInOrder(got, "delivery_intent:written", "delivery_intent:T14") {
		t.Fatalf("late answer on a closed intent: %v; want written, then the T14 upgrade", got)
	}
	t.Logf("stream for the intent: %s", strings.Join(events.seq(id), ", "))
}
