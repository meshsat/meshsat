package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/hubreporter"
)

// A Hub command against the real command log, the real database and the
// real keyed queue (review 2026-10-18 18:26, FA-R1 and FA-R2): a command run
// again after a restart queues nothing twice and answers its first
// delivery; a failed log runs nothing unprotected; a refusal never replaces
// a finished command's reply; a crash before the effect loses nothing.
// [MESHSAT-1584]

type commandRig struct {
	t       *testing.T
	path    string
	db      *database.DB
	replies []hubreporter.CommandResponse
	mu      sync.Mutex
	flushed int
}

func newCommandRig(t *testing.T) *commandRig {
	r := &commandRig{t: t, path: filepath.Join(t.TempDir(), "meshsat.db")}
	r.open()
	t.Cleanup(func() { r.db.Close() })
	return r
}

func (r *commandRig) open() {
	db, err := database.New(r.path)
	if err != nil {
		r.t.Fatal(err)
	}
	r.db = db
}

func (r *commandRig) reopen() {
	r.db.Close()
	r.open()
}

// flakyLog is the real log with Claim or Finish made to fail.
type flakyLog struct {
	hubCommandLog
	failClaim, failFinish bool
}

func (l flakyLog) ClaimHubCommand(id, cmd, args, run string, now time.Time) (hubreporter.CommandClaim, error) {
	if l.failClaim {
		return hubreporter.CommandClaim{}, errors.New("disk full")
	}
	return l.hubCommandLog.ClaimHubCommand(id, cmd, args, run, now)
}

func (l flakyLog) FinishHubCommand(id, run, resp string) error {
	if l.failFinish {
		return errors.New("disk full")
	}
	return l.hubCommandLog.FinishHubCommand(id, run, resp)
}

// handler is a new process's command handler (its own run id).
func (r *commandRig) handler(log hubreporter.CommandLog) *hubreporter.CommandHandler {
	d := engine.NewDispatcher(r.db, channel.NewRegistry(), nil, nil)
	ch := hubreporter.NewCommandHandler(nil, "bridge-kit-a", func() hubreporter.BridgeHealth { return hubreporter.BridgeHealth{} })
	ch.SetDeps(hubreporter.CommandDeps{
		SendText: func(iface, text string) (int64, string, error) { return d.QueueDirectSend(iface, text, "") },
		SendTextKeyed: func(id, fp, iface, text string) (int64, string, error) {
			del, ref, _, err := d.QueueDirectSendKeyed(engine.HubCommandSource, id, fp, iface, text, engine.DirectSendOptions{})
			return del, ref, err
		},
		FlushBurst: func(context.Context) ([]byte, int, error) {
			r.mu.Lock()
			r.flushed++
			r.mu.Unlock()
			return nil, 0, nil
		},
		BurstPending: func() int { return 1 },
	})
	ch.SetCommandLog(log)
	ch.SetResponseObserver(func(resp hubreporter.CommandResponse) {
		r.mu.Lock()
		r.replies = append(r.replies, resp)
		r.mu.Unlock()
	})
	return ch
}

func (r *commandRig) send(ch *hubreporter.CommandHandler, id, cmd, payload string) {
	b, _ := json.Marshal(hubreporter.Command{Protocol: hubreporter.ProtocolVersion, Cmd: cmd, RequestID: id, Payload: json.RawMessage(payload)})
	ch.HandleCommand(b)
}

func (r *commandRig) last() hubreporter.CommandResponse {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.replies) == 0 {
		r.t.Fatal("no reply")
	}
	return r.replies[len(r.replies)-1]
}

func deliveryID(t *testing.T, resp hubreporter.CommandResponse) int64 {
	var res struct {
		DeliveryID int64 `json:"delivery_id"`
	}
	if resp.Status != "ok" || json.Unmarshal(resp.Result, &res) != nil || res.DeliveryID == 0 {
		t.Fatalf("reply %+v; want ok with a delivery id", resp)
	}
	return res.DeliveryID
}

func (r *commandRig) rows() int {
	var n int
	if err := r.db.Get(&n, `SELECT COUNT(*) FROM message_deliveries`); err != nil {
		r.t.Fatal(err)
	}
	return n
}

const textPayload = `{"interface_id":"mesh_0","text":"from the Hub"}`

func TestHubCommandAfterARestartBeforeItsReplyWasStoredQueuesNothingTwice(t *testing.T) {
	r := newCommandRig(t)
	r.send(r.handler(flakyLog{hubCommandLog: hubCommandLog{r.db}, failFinish: true}), "req-1", "send_text", textPayload)
	first := deliveryID(t, r.last())
	r.reopen() // the process died with the delivery queued and no reply stored
	r.send(r.handler(hubCommandLog{r.db}), "req-1", "send_text", textPayload)
	if again := deliveryID(t, r.last()); again != first || r.rows() != 1 {
		t.Fatalf("after the restart: delivery %d (first %d), %d rows; want the first one, one row", again, first, r.rows())
	}
}

func TestHubCommandWithTheLogDownRunsNothingUnprotected(t *testing.T) {
	r := newCommandRig(t)
	down := r.handler(flakyLog{hubCommandLog: hubCommandLog{r.db}, failClaim: true})
	r.send(down, "req-2", "send_text", textPayload)
	r.send(down, "req-2", "send_text", textPayload)
	if r.rows() != 1 {
		t.Fatalf("%d rows for one request with the log down; the keyed queue admits one", r.rows())
	}
	r.send(down, "req-3", "flush_burst", `{}`)
	if resp := r.last(); resp.Status != "error" || resp.RequestID != "req-3" || r.flushed != 0 {
		t.Fatalf("a changing command with the log down: %+v, flushed %d; want refused, not run", resp, r.flushed)
	}
}

func TestARefusalNeverReplacesAFinishedCommandsReply(t *testing.T) {
	r := newCommandRig(t)
	ch := r.handler(hubCommandLog{r.db})
	r.send(ch, "req-4", "send_text", textPayload)
	first := deliveryID(t, r.last())
	for _, bad := range []hubreporter.Command{
		{Protocol: hubreporter.ProtocolVersion, Cmd: "not_a_registered_command", RequestID: "req-4"},
		{Protocol: "meshsat-uplink/v0", Cmd: "send_text", RequestID: "req-4"},
		{Protocol: hubreporter.ProtocolVersion, Cmd: "", RequestID: "req-4"},
	} {
		b, _ := json.Marshal(bad)
		ch.HandleCommand(b)
		if r.last().Status != "error" {
			t.Fatalf("%+v was not refused", bad)
		}
	}
	r.reopen()
	r.send(r.handler(hubCommandLog{r.db}), "req-4", "send_text", textPayload)
	resp := r.last()
	if resp.Cmd != "send_text" || deliveryID(t, resp) != first || r.rows() != 1 {
		t.Fatalf("replayed after refusals: %+v, %d rows; want the original reply", resp, r.rows())
	}
}

func TestACommandClaimedBeforeACrashRunsOnceAfterIt(t *testing.T) {
	r := newCommandRig(t)
	args := hubreporter.CommandArgsDigest(hubreporter.Command{Cmd: "send_text", Payload: json.RawMessage(textPayload)})
	if _, err := r.db.ClaimHubCommand("req-5", "send_text", args, "a run that died", time.Now()); err != nil {
		t.Fatal(err)
	}
	ch := r.handler(hubCommandLog{r.db})
	r.send(ch, "req-5", "send_text", textPayload)
	deliveryID(t, r.last())
	r.send(ch, "req-5", "send_text", textPayload)
	if r.rows() != 1 {
		t.Fatalf("%d rows; want the claimed command run once after the crash", r.rows())
	}
}
