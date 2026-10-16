package gateway

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type reviewAPRSWriteFault struct {
	io.ReadWriteCloser
	failBefore bool
	failAfter  bool
}

func (w *reviewAPRSWriteFault) Write(p []byte) (int, error) {
	if w.failBefore {
		return 0, errors.New("review: refused before write")
	}
	n, err := w.ReadWriteCloser.Write(p)
	if err == nil && w.failAfter {
		return n, errors.New("review: error after frame reached the TNC")
	}
	return n, err
}

func TestReviewAPRSWriteFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after bool
		want          string
		frames        int
	}{
		{"normal_write_control", false, false, IntentAccepted, 1},
		{"zero_write_control", true, false, IntentNotSent, 0},
		{"complete_frame_then_error", false, true, IntentUnknown, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tnc := newMockKISSTNC(t)
			defer tnc.close()
			gw := startAckGateway(t, tnc, APRSConfig{AckAttempts: 1, AckTimeoutMs: 100})
			defer gw.Stop()
			gw.kiss.mu.Lock()
			gw.kiss.rw = &reviewAPRSWriteFault{ReadWriteCloser: gw.kiss.rw, failBefore: tc.before, failAfter: tc.after}
			gw.kiss.mu.Unlock()
			o := gw.IntentSend(context.Background(), IntentSend{Destination: intentDest, Text: "review write", CorrValue: "WR001"})
			if tc.frames > 0 {
				waitTNCFrames(t, tnc, tc.frames, time.Second)
			}
			n := len(tnc.frames())
			t.Logf("tnc_frames=%d class=%s retryable=%v detail=%s", n, o.Class, o.Retryable, o.Detail)
			if n != tc.frames || o.Class != tc.want {
				t.Fatalf("frames=%d class=%s; want frames=%d class=%s", n, o.Class, tc.frames, tc.want)
			}
		})
	}
}

func TestReviewAPRSForeignAckDoesNotEndTheSend(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		name := "addressed_station_control"
		if foreign {
			name = "foreign_station_first"
		}
		t.Run(name, func(t *testing.T) {
			tnc := newMockKISSTNC(t)
			defer tnc.close()
			gw := startAckGateway(t, tnc, APRSConfig{AckAttempts: 2, AckTimeoutMs: 1000})
			defer gw.Stop()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result := make(chan IntentOutcome, 1)
			go func() {
				result <- gw.IntentSend(ctx, IntentSend{Destination: intentDest, Text: "review peer", CorrValue: "PEER1"})
			}()
			waitTNCFrames(t, tnc, 1, time.Second)
			if foreign {
				gw.handleAckReply(&APRSPacket{Source: "PD0XYZ-1", MsgTo: "TEST-10"}, "PEER1", false)
				select {
				case o := <-result:
					t.Fatalf("foreign ack ended send: class=%s receipt=%+v", o.Class, o.Receipt)
				case <-time.After(100 * time.Millisecond):
				}
			}
			gw.handleAckReply(&APRSPacket{Source: intentDest, MsgTo: "TEST-10"}, "PEER1", false)
			select {
			case o := <-result:
				if o.Receipt == nil || o.Receipt.Peer != intentDest || o.Receipt.Completes == "" {
					t.Fatalf("addressed station did not complete: %+v", o)
				}
			case <-ctx.Done():
				t.Fatal("no answer from addressed station")
			}
		})
	}
}
