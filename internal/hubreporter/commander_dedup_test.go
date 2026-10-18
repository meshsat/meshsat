package hubreporter

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// memCommandLog is CommandLog in memory.
type memCommandLog struct {
	mu   sync.Mutex
	seen map[string]string
}

func (l *memCommandLog) ClaimHubCommand(id, _ string, _ time.Time) (bool, string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if prev, ok := l.seen[id]; ok {
		return false, prev, nil
	}
	l.seen[id] = ""
	return true, "", nil
}

func (l *memCommandLog) FinishHubCommand(id, resp string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen[id] = resp
	return nil
}

// The broker delivers a QoS 1 command again after a reconnect: the same
// request id runs once, and a command without one runs every time.
// [MESHSAT-1584]
func TestHandleCommandRunsARepeatedRequestIDOnce(t *testing.T) {
	ch := NewCommandHandler(nil, "bridge-1", func() BridgeHealth { return BridgeHealth{} })
	l := &memCommandLog{seen: map[string]string{}}
	ch.SetCommandLog(l)
	runs := 0
	ch.handlers["send_text"] = func(Command) (json.RawMessage, error) {
		runs++
		return json.RawMessage(`{"delivery_id":1}`), nil
	}
	send := func(id string) {
		b, _ := json.Marshal(Command{Protocol: ProtocolVersion, Cmd: "send_text", RequestID: id})
		ch.HandleCommand(b)
	}
	send("req-1")
	send("req-1")
	send("req-1")
	if runs != 1 {
		t.Fatalf("ran %d times for one request id; want 1", runs)
	}
	var resp CommandResponse
	if err := json.Unmarshal([]byte(l.seen["req-1"]), &resp); err != nil || resp.Status != "ok" || string(resp.Result) != `{"delivery_id":1}` {
		t.Fatalf("stored reply %q: %v", l.seen["req-1"], err)
	}
	send("")
	send("")
	if runs != 3 {
		t.Fatalf("ran %d times; a command without a request id runs each time", runs)
	}
}
