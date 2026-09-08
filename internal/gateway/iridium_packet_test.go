package gateway

import (
	"context"
	"errors"
	"testing"

	"meshsat/internal/transport"
)

// fakeSat is the minimum SatTransport for the packet-feed tests: MO sends
// answer with a fixed mo_status, status carries an IMEI.
type fakeSat struct {
	moStatus int
	imei     string
	sent     [][]byte
	texts    []string
}

func (f *fakeSat) Subscribe(ctx context.Context) (<-chan transport.SatEvent, error) {
	return nil, errors.New("not in this test")
}
func (f *fakeSat) Send(ctx context.Context, data []byte) (*transport.SatResult, error) {
	f.sent = append(f.sent, data)
	return &transport.SatResult{MOStatus: f.moStatus}, nil
}
func (f *fakeSat) SendText(ctx context.Context, text string) (*transport.SatResult, error) {
	f.texts = append(f.texts, text)
	return &transport.SatResult{MOStatus: f.moStatus}, nil
}
func (f *fakeSat) Receive(ctx context.Context) ([]byte, error) { return nil, nil }
func (f *fakeSat) MailboxCheck(ctx context.Context) (*transport.SatResult, error) {
	return &transport.SatResult{}, nil
}
func (f *fakeSat) GetSignal(ctx context.Context) (*transport.SignalInfo, error) {
	return &transport.SignalInfo{}, nil
}
func (f *fakeSat) GetSignalFast(ctx context.Context) (*transport.SignalInfo, error) {
	return &transport.SignalInfo{}, nil
}
func (f *fakeSat) GetStatus(ctx context.Context) (*transport.SatStatus, error) {
	return &transport.SatStatus{Connected: true, IMEI: f.imei, Type: "imt"}, nil
}
func (f *fakeSat) GetFirmwareVersion(ctx context.Context) (string, error) { return "test", nil }
func (f *fakeSat) Close() error                                           { return nil }

// A successful IMT MO lands in the packet feed as bearer sat, dir tx, with
// the IMEI, the destination, the text and the mo_status. [MESHSAT-962]
func TestIMTPacketFeed_MOSuccess(t *testing.T) {
	sat := &fakeSat{moStatus: 0, imei: "300258060902280"}
	gw := NewIMTGateway(IridiumConfig{}, sat, nil, nil)
	st, _ := sat.GetStatus(context.Background())
	gw.rememberIMEI(st)
	sink := newCollectSink()
	gw.SetPacketSink(sink.sink, "iridium_imt_0")

	if err := gw.sendIMT(context.Background(), &transport.MeshMessage{PortNum: 1, DecodedText: "hello sky", MsgRef: "m-1"}); err != nil {
		t.Fatal(err)
	}
	rec := sink.wait(t)
	if rec.Bearer != BearerSat || rec.Dir != DirTX || rec.Iface != "iridium_imt_0" || rec.From != "300258060902280" ||
		rec.To != "cloudloop" || rec.Text != "hello sky" || rec.MsgRef != "m-1" || rec.Path != "mo_status=0" || rec.Bytes != 9 {
		t.Fatalf("record: %+v", rec)
	}
	if at, mo := gw.LastMO(); at.IsZero() || mo != 0 {
		t.Fatalf("LastMO not recorded: %v %d", at, mo)
	}
	if gw.IMEI() != "300258060902280" {
		t.Fatalf("IMEI: %q", gw.IMEI())
	}
}

// A failed MO records nothing and leaves LastMO untouched.
func TestIMTPacketFeed_MOFailureRecordsNothing(t *testing.T) {
	sat := &fakeSat{moStatus: 32}
	gw := NewIMTGateway(IridiumConfig{}, sat, nil, nil)
	sink := newCollectSink()
	gw.SetPacketSink(sink.sink, "iridium_imt_0")
	if err := gw.sendIMT(context.Background(), &transport.MeshMessage{PortNum: 1, DecodedText: "no sky"}); err == nil {
		t.Fatal("expected an error for mo_status 32")
	}
	select {
	case rec := <-sink.ch:
		t.Fatalf("unexpected record on failure: %+v", rec)
	default:
	}
	if at, _ := gw.LastMO(); !at.IsZero() {
		t.Fatal("LastMO set on failure")
	}
}

// An MT received is recorded as bearer sat, dir rx from cloudloop, and
// LastMT is set.
func TestIMTPacketFeed_MTReceived(t *testing.T) {
	gw := NewIMTGateway(IridiumConfig{}, &fakeSat{imei: "300258060902280"}, nil, nil)
	sink := newCollectSink()
	gw.SetPacketSink(sink.sink, "iridium_imt_0")
	gw.noteMTReceived("cloudloop", 5, "hello")
	rec := sink.wait(t)
	if rec.Bearer != BearerSat || rec.Dir != DirRX || rec.From != "cloudloop" || rec.Text != "hello" || rec.Bytes != 5 {
		t.Fatalf("record: %+v", rec)
	}
	if gw.LastMT().IsZero() {
		t.Fatal("LastMT not set")
	}
}

// The SBD gateway records a successful MO the same way, to rock7.
func TestSBDPacketFeed_MOSuccess(t *testing.T) {
	sat := &fakeSat{moStatus: 1, imei: "300234063904190"}
	gw := NewSBDGateway(IridiumConfig{}, sat, nil, nil)
	st, _ := sat.GetStatus(context.Background())
	gw.rememberIMEI(st)
	sink := newCollectSink()
	gw.SetPacketSink(sink.sink, "iridium_0")
	if err := gw.sendSBD(context.Background(), &transport.MeshMessage{PortNum: 1, DecodedText: "hi"}); err != nil {
		t.Fatal(err)
	}
	rec := sink.wait(t)
	if rec.Bearer != BearerSat || rec.Dir != DirTX || rec.Iface != "iridium_0" || rec.To != "rock7" || rec.Path != "mo_status=1" || rec.From != "300234063904190" {
		t.Fatalf("record: %+v", rec)
	}
}
