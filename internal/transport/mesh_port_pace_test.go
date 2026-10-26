package transport

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"go.bug.st/serial"
	"google.golang.org/protobuf/proto"
)

// timedWritePort records when each frame was written and the port of the
// packet in it; a write can be made to fail.
type timedWritePort struct {
	serial.Port
	mu    sync.Mutex
	at    []time.Time
	ports []int32
	fail  bool
}

func (f *timedWritePort) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return 0, errors.New("write failed")
	}
	var msg pb.ToRadio
	port := int32(-1)
	if len(p) > 4 && proto.Unmarshal(p[4:], &msg) == nil {
		if d := msg.GetPacket().GetDecoded(); d != nil {
			port = int32(d.GetPortnum())
		}
	}
	f.at = append(f.at, time.Now())
	f.ports = append(f.ports, port)
	return len(p), nil
}

func (f *timedWritePort) Close() error { return nil }

func (f *timedWritePort) writes() ([]time.Time, []int32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.at...), append([]int32(nil), f.ports...)
}

// shortRadioIntervals scales the radio's per-port throttle down so the tests
// run in milliseconds: text 200 ms, position 400 ms, 20 ms of margin, and
// waits of up to 500 ms.
func shortRadioIntervals(t *testing.T) {
	t.Helper()
	noMeshPacing(t)
	iv, margin, maxWait := radioPortInterval, radioPortMargin, radioPortMaxWait
	radioPortInterval = map[int32]time.Duration{
		int32(pb.PortNum_TEXT_MESSAGE_APP): 200 * time.Millisecond,
		int32(pb.PortNum_POSITION_APP):     400 * time.Millisecond,
	}
	radioPortMargin = 20 * time.Millisecond
	radioPortMaxWait = 500 * time.Millisecond
	t.Cleanup(func() { radioPortInterval, radioPortMargin, radioPortMaxWait = iv, margin, maxWait })
}

func pacedTransport(fp *timedWritePort) *DirectMeshTransport {
	tr := NewDirectMeshTransport("/dev/null")
	tr.file = fp
	tr.connected = true
	return tr
}

// Two texts handed over back to back, as the OOB layer writes the chunks of
// one reply: the radio drops a text that comes within 2 s of the previous
// one ("Rate limit portnum 1", parallax 24 Oct 2026), so the second must wait
// out the radio's interval. [MESHSAT-1682]
func TestPortPace_SecondTextWaitsOutTheRadiosInterval(t *testing.T) {
	shortRadioIntervals(t)
	fp := &timedWritePort{}
	tr := pacedTransport(fp)
	ctx := context.Background()
	if err := tr.SendMessage(ctx, SendRequest{Text: "chunk 1", To: "!4370c1d8"}); err != nil {
		t.Fatal(err)
	}
	if err := tr.SendMessage(ctx, SendRequest{Text: "chunk 2", To: "!4370c1d8"}); err != nil {
		t.Fatal(err)
	}
	at, _ := fp.writes()
	if len(at) != 2 {
		t.Fatalf("%d writes, want 2", len(at))
	}
	if gap := at[1].Sub(at[0]); gap < 220*time.Millisecond {
		t.Fatalf("second text %v after the first, want at least the interval plus margin (220ms)", gap)
	}
}

// The same holds when the chunks go under ids of their own (the delivery
// intents), and across the two send paths.
func TestPortPace_TextsAcrossSendPathsShareOneInterval(t *testing.T) {
	shortRadioIntervals(t)
	fp := &timedWritePort{}
	tr := pacedTransport(fp)
	ctx := context.Background()
	if err := tr.SendMessageWithID(ctx, SendRequest{Text: "a", To: "!4370c1d8"}, 7); err != nil {
		t.Fatal(err)
	}
	if err := tr.SendMessage(ctx, SendRequest{Text: "b", To: "!4370c1d8"}); err != nil {
		t.Fatal(err)
	}
	if err := tr.SendRaw(ctx, RawRequest{To: "!4370c1d8", PortNum: int(pb.PortNum_TEXT_MESSAGE_APP), Payload: "Yw=="}); err != nil {
		t.Fatal(err)
	}
	at, _ := fp.writes()
	if len(at) != 3 {
		t.Fatalf("%d writes, want 3", len(at))
	}
	for i := 1; i < 3; i++ {
		if gap := at[i].Sub(at[i-1]); gap < 220*time.Millisecond {
			t.Fatalf("text %d went %v after text %d, want at least 220ms", i+1, gap, i)
		}
	}
}

// Ports pace independently: a position right after a text does not wait
// for the text's interval, and a port the radio does not throttle (the
// bridge's own routing packets) never waits.
func TestPortPace_OtherPortsDoNotWait(t *testing.T) {
	shortRadioIntervals(t)
	fp := &timedWritePort{}
	tr := pacedTransport(fp)
	ctx := context.Background()
	if err := tr.SendMessage(ctx, SendRequest{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := tr.SendPosition(ctx, 52.0, 4.5, 0); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := tr.SendRaw(ctx, RawRequest{PortNum: 256, Payload: "AQID"}); err != nil {
			t.Fatal(err)
		}
	}
	at, ports := fp.writes()
	if len(at) != 5 {
		t.Fatalf("%d writes, want 5", len(at))
	}
	if total := at[4].Sub(at[0]); total > 150*time.Millisecond {
		t.Fatalf("text, position and three routing packets took %v, want no waiting (ports %v)", total, ports)
	}
}

// A second position waits out the position interval, not the text one.
func TestPortPace_PositionsKeepTheirOwnInterval(t *testing.T) {
	shortRadioIntervals(t)
	fp := &timedWritePort{}
	tr := pacedTransport(fp)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := tr.SendPosition(ctx, 52.0, 4.5, 0); err != nil {
			t.Fatal(err)
		}
	}
	at, _ := fp.writes()
	if gap := at[1].Sub(at[0]); gap < 420*time.Millisecond {
		t.Fatalf("second position %v after the first, want at least 420ms", gap)
	}
}

// A wait longer than the most a send may wait (an API call's position,
// waypoint or traceroute against the radio's 10 s and 30 s) is refused at
// once with ErrRadioPortBusy, which says when the next can go, and nothing is
// written: today the radio drops it while the API answers success.
func TestPortPace_LongWaitIsRefusedAtOnce(t *testing.T) {
	shortRadioIntervals(t)
	radioPortMaxWait = 100 * time.Millisecond
	fp := &timedWritePort{}
	tr := pacedTransport(fp)
	ctx := context.Background()
	if err := tr.SendPosition(ctx, 52.0, 4.5, 0); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := tr.SendPosition(ctx, 52.0, 4.5, 0)
	if !errors.Is(err, ErrRadioPortBusy) || !errors.Is(err, ErrNotSubmitted) {
		t.Fatalf("err = %v, want ErrRadioPortBusy (an ErrNotSubmitted)", err)
	}
	if took := time.Since(start); took > 50*time.Millisecond {
		t.Fatalf("refusal took %v, want at once", took)
	}
	if at, _ := fp.writes(); len(at) != 1 {
		t.Fatalf("%d writes, want only the first", len(at))
	}
}

// A caller that gives up while waiting gets ErrNotSubmitted and nothing is
// written, so the delivery ledger knows the radio never had it.
func TestPortPace_CancelledWaitSendsNothing(t *testing.T) {
	shortRadioIntervals(t)
	fp := &timedWritePort{}
	tr := pacedTransport(fp)
	if err := tr.SendMessage(context.Background(), SendRequest{Text: "first"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := tr.SendMessageWithID(ctx, SendRequest{Text: "second"}, 9)
	if !errors.Is(err, ErrNotSubmitted) {
		t.Fatalf("err = %v, want ErrNotSubmitted", err)
	}
	if at, _ := fp.writes(); len(at) != 1 {
		t.Fatalf("%d writes, want only the first", len(at))
	}
}

// A write that failed never reached the radio, so it does not start the
// radio's interval: the next text goes at once.
func TestPortPace_FailedWriteDoesNotStartTheInterval(t *testing.T) {
	shortRadioIntervals(t)
	fp := &timedWritePort{fail: true}
	tr := pacedTransport(fp)
	ctx := context.Background()
	if err := tr.SendMessage(ctx, SendRequest{Text: "lost"}); err == nil {
		t.Fatal("want the write error")
	}
	fp.mu.Lock()
	fp.fail = false
	fp.mu.Unlock()
	start := time.Now()
	if err := tr.SendMessage(ctx, SendRequest{Text: "next"}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 150*time.Millisecond {
		t.Fatalf("the text after a failed write waited %v, want none", took)
	}
}

// The table follows the radio firmware the kits run (2.8.0, PhoneAPI::handleToRadio).
func TestPortPace_TableFollowsTheFirmware(t *testing.T) {
	want := map[int32]time.Duration{
		int32(pb.PortNum_TEXT_MESSAGE_APP): 2 * time.Second,
		int32(pb.PortNum_POSITION_APP):     10 * time.Second,
		int32(pb.PortNum_WAYPOINT_APP):     10 * time.Second,
		int32(pb.PortNum_ALERT_APP):        10 * time.Second,
		int32(pb.PortNum_TELEMETRY_APP):    10 * time.Second,
		int32(pb.PortNum_TRACEROUTE_APP):   30 * time.Second,
	}
	if len(radioPortInterval) != len(want) {
		t.Fatalf("%d throttled ports, want %d", len(radioPortInterval), len(want))
	}
	for port, d := range want {
		if radioPortInterval[port] != d {
			t.Errorf("port %d: %v, want %v", port, radioPortInterval[port], d)
		}
	}
	if radioPortMargin <= 0 {
		t.Error("no margin over the radio's interval")
	}
	// A text always waits (the delivery queues expect it); the 10 s and 30 s
	// ports come from API calls and are refused rather than left hanging.
	if radioPortMaxWait < want[int32(pb.PortNum_TEXT_MESSAGE_APP)]+radioPortMargin {
		t.Errorf("max wait %v is shorter than a text's interval plus margin", radioPortMaxWait)
	}
	if radioPortMaxWait >= 10*time.Second {
		t.Errorf("max wait %v would leave an API call hanging for a position", radioPortMaxWait)
	}
}
