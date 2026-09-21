package gateway

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// cancelSat is a fakeSat whose MO waits for a satellite until cancelled.
type cancelSat struct {
	fakeSat
	notYet   atomic.Int32 // CancelMO answers false this many times first
	cancels  atomic.Int32
	released chan struct{}
}

func (c *cancelSat) CancelMO() bool {
	if c.notYet.Add(-1) >= 0 {
		return false // the send has not reached the modem yet
	}
	if c.cancels.Add(1) == 1 {
		close(c.released)
	}
	return true
}

func (c *cancelSat) Send(ctx context.Context, data []byte) (*transport.SatResult, error) {
	select {
	case <-c.released:
		return &transport.SatResult{MOStatus: 32, StatusText: "message_cancelled_pre_transit"}, nil
	case <-time.After(400 * time.Millisecond):
		return &transport.SatResult{MOStatus: 0}, nil
	}
}

func (c *cancelSat) SendText(ctx context.Context, text string) (*transport.SatResult, error) {
	return c.Send(ctx, []byte(text))
}

// The MT poll (Deferred) gives way when a real message is waiting behind it
// on the same channel, and only then. [MESHSAT-1282]
func TestIMTGateway_DeferredSendYieldsToAWaitingMessage(t *testing.T) {
	old := imtYieldPoll
	imtYieldPoll = 5 * time.Millisecond
	t.Cleanup(func() { imtYieldPoll = old })

	for _, tc := range []struct {
		name       string
		precedence string
		waiting    string // precedence of a queued row, "" = none
		notYet     int32
		wantCancel bool
	}{
		{"poll with a message waiting", "Deferred", "Routine", 0, true},
		{"poll not on the modem yet when the message arrives", "Deferred", "Routine", 3, true},
		{"poll alone", "Deferred", "", 0, false},
		{"poll behind another poll", "Deferred", "Deferred", 0, false},
		{"a real message never yields", "Routine", "Flash", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := database.New(filepath.Join(t.TempDir(), "y.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if tc.waiting != "" {
				if _, err := db.InsertDelivery(database.MessageDelivery{MsgRef: "w", Channel: "iridium_imt_0", Status: "queued", Precedence: tc.waiting}); err != nil {
					t.Fatal(err)
				}
			}
			sat := &cancelSat{released: make(chan struct{})}
			sat.notYet.Store(tc.notYet)
			gw := NewIMTGateway(IridiumConfig{}, sat, db, nil)
			gw.SetPacketSink(func(PacketRecord) {}, "iridium_imt_0")

			_ = gw.sendIMT(context.Background(), &transport.MeshMessage{RawPayload: []byte{0x4d, 0x53, 0x01, 0x03}, Precedence: tc.precedence})
			if got := sat.cancels.Load() > 0; got != tc.wantCancel {
				t.Fatalf("cancelled=%v, want %v", got, tc.wantCancel)
			}
		})
	}
}
