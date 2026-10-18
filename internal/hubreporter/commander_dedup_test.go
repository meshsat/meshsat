package hubreporter

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// memCommandLog is CommandLog in memory, with the database's rules.
type memCommandLog struct {
	mu   sync.Mutex
	rows map[string]*memCommand
}

type memCommand struct{ resp, run, args string }

func (l *memCommandLog) ClaimHubCommand(id, _, args, run string, _ time.Time) (CommandClaim, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.rows[id]
	switch {
	case !ok:
		l.rows[id] = &memCommand{run: run, args: args}
		return CommandClaim{Fresh: true}, nil
	case r.args != args:
		return CommandClaim{Conflict: true}, nil
	case r.resp == "" && r.run != run:
		r.run = run
		return CommandClaim{Fresh: true}, nil
	}
	return CommandClaim{Response: r.resp}, nil
}

func (l *memCommandLog) FinishHubCommand(id, run, resp string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r, ok := l.rows[id]; ok && r.run == run && r.resp == "" {
		r.resp = resp
	}
	return nil
}

func newDedupHandler(l CommandLog, runs *int) *CommandHandler {
	ch := NewCommandHandler(nil, "bridge-1", func() BridgeHealth { return BridgeHealth{} })
	ch.SetCommandLog(l)
	ch.handlers["send_text"] = func(Command) (json.RawMessage, error) {
		*runs++
		return json.RawMessage(`{"delivery_id":1}`), nil
	}
	return ch
}

func sendText(ch *CommandHandler, id, text string) {
	b, _ := json.Marshal(Command{Protocol: ProtocolVersion, Cmd: "send_text", RequestID: id, Payload: json.RawMessage(`{"text":"` + text + `"}`)})
	ch.HandleCommand(b)
}

// The broker delivers a QoS 1 command again after a reconnect: the same
// request id runs once; the same id with other arguments is not run; a
// command without one runs every time. [MESHSAT-1584]
func TestHandleCommandRunsARepeatedRequestIDOnce(t *testing.T) {
	l := &memCommandLog{rows: map[string]*memCommand{}}
	runs := 0
	ch := newDedupHandler(l, &runs)
	var replies []CommandResponse
	ch.onResponse = func(r CommandResponse) { replies = append(replies, r) }
	sendText(ch, "req-1", "hello")
	sendText(ch, "req-1", "hello")
	sendText(ch, "req-1", "hello")
	if runs != 1 {
		t.Fatalf("ran %d times for one request id; want 1", runs)
	}
	var resp CommandResponse
	if err := json.Unmarshal([]byte(l.rows["req-1"].resp), &resp); err != nil || resp.Status != "ok" || string(resp.Result) != `{"delivery_id":1}` {
		t.Fatalf("stored reply %q: %v", l.rows["req-1"].resp, err)
	}
	replies = nil
	sendText(ch, "req-1", "another text")
	if runs != 1 {
		t.Fatal("the same request id with other arguments ran")
	}
	if len(replies) != 1 || replies[0].Status != "error" || replies[0].RequestID != "req-1" {
		t.Fatalf("other arguments under a used id: replies %+v; want one error reply", replies)
	}
	sendText(ch, "", "x")
	sendText(ch, "", "x")
	if runs != 3 {
		t.Fatalf("ran %d times; a command without a request id runs each time", runs)
	}
}

// A process that claimed a command and died before it finished does not
// keep it from running: the next process (another run id) runs it.
// [MESHSAT-1584]
func TestHandleCommandAClaimTheLastProcessNeverFinishedRunsAgain(t *testing.T) {
	l := &memCommandLog{rows: map[string]*memCommand{}}
	l.rows["req-9"] = &memCommand{run: "a process that is gone", args: commandArgsDigest(Command{Cmd: "send_text", Payload: json.RawMessage(`{"text":"hello"}`)})}
	runs := 0
	ch := newDedupHandler(l, &runs)
	sendText(ch, "req-9", "hello")
	if runs != 1 {
		t.Fatalf("ran %d times; want the unfinished command run once by the new process", runs)
	}
	sendText(ch, "req-9", "hello")
	if runs != 1 {
		t.Fatal("ran twice in one process")
	}
}

// A refusal that comes while the command owning the id still runs in this
// process is not stored: the command's own reply is (FA-R2). [MESHSAT-1584]
func TestARefusalWhileTheCommandRunsDoesNotTakeItsReply(t *testing.T) {
	l := &memCommandLog{rows: map[string]*memCommand{}}
	runs := 0
	ch := newDedupHandler(l, &runs)
	l.rows["req-7"] = &memCommand{run: ch.runID, args: "running"}
	b, _ := json.Marshal(Command{Protocol: ProtocolVersion, Cmd: "not_a_registered_command", RequestID: "req-7"})
	ch.HandleCommand(b)
	if got := l.rows["req-7"].resp; got != "" {
		t.Fatalf("the refusal was stored as the running command's reply: %s", got)
	}
}
