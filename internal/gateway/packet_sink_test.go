package gateway

import (
	"context"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/transport"
)

// collectSink gathers PacketRecords from a gateway under test.
type collectSink struct {
	mu   sync.Mutex
	recs []PacketRecord
	ch   chan PacketRecord
}

func newCollectSink() *collectSink {
	return &collectSink{ch: make(chan PacketRecord, 16)}
}

func (c *collectSink) sink(rec PacketRecord) {
	c.mu.Lock()
	c.recs = append(c.recs, rec)
	c.mu.Unlock()
	c.ch <- rec
}

func (c *collectSink) wait(t *testing.T) PacketRecord {
	t.Helper()
	select {
	case rec := <-c.ch:
		return rec
	case <-time.After(3 * time.Second):
		t.Fatal("no packet record within 3 s")
		return PacketRecord{}
	}
}

// newSerialTestGateway builds an APRS gateway on an in-memory serial TNC
// with its read worker running. Returns the gateway, the fake port and a
// stop func.
func newSerialTestGateway(t *testing.T, sink PacketSink) (*APRSGateway, *pipeRW, func()) {
	t.Helper()
	cfg := DefaultAPRSConfig()
	cfg.Callsign = "MSTESS"
	cfg.SSID = 10
	cfg.KISSDevice = "/dev/null-tnc"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	g := NewAPRSGateway(cfg, nil)
	g.SetPacketSink(sink, "aprs_0")
	rw := newPipeRW()
	g.kiss = newKISSConnRW(rw)
	g.connected.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	g.wg.Add(1)
	go g.readWorker(ctx)
	return g, rw, func() {
		cancel()
		rw.Close()
		g.wg.Wait()
	}
}

func TestAPRSPacketFeed_ReceiveMessageFrame(t *testing.T) {
	c := newCollectSink()
	g, rw, stop := newSerialTestGateway(t, c.sink)
	defer stop()

	// PA3XYZ-7 sends a directed APRS message to MSPRLX-10 via WIDE1-1,WIDE2-1,
	// with WIDE1-1 already used (H bit set) as a digipeater would leave it.
	src := AX25Address{Call: "PA3XYZ", SSID: 7}
	dst := AX25Address{Call: "APMSHT"}
	path := []AX25Address{{Call: "WIDE1", SSID: 1}, {Call: "WIDE2", SSID: 1}}
	info := EncodeAPRSMessage("MSPRLX-10", "hello from the field", "07")
	frame := EncodeAX25Frame(dst, src, path, info)
	frame[14+6] |= 0x80 // first path slot repeated
	rw.feed(KISSEncode(frame))

	// The gateway still parses the frame into an inbound message.
	select {
	case msg := <-g.Receive():
		if msg.FromAddr != "PA3XYZ-7" {
			t.Errorf("inbound from = %q", msg.FromAddr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no inbound message")
	}

	rec := c.wait(t)
	if rec.Bearer != BearerAPRS || rec.Dir != DirRX || rec.Iface != "aprs_0" {
		t.Errorf("bearer/dir/iface: %+v", rec)
	}
	if rec.From != "PA3XYZ-7" {
		t.Errorf("from = %q, want PA3XYZ-7", rec.From)
	}
	if rec.To != "APMSHT" {
		t.Errorf("to = %q, want APMSHT", rec.To)
	}
	if rec.Path != "WIDE1-1,WIDE2-1" {
		t.Errorf("path = %q", rec.Path)
	}
	if rec.Hops != 1 {
		t.Errorf("hops = %d, want 1 (one H bit set)", rec.Hops)
	}
	if rec.Bytes != len(frame) {
		t.Errorf("bytes = %d, want %d", rec.Bytes, len(frame))
	}
	if rec.Raw != hex.EncodeToString(frame) {
		t.Errorf("raw hex mismatch:\n got %s\nwant %s", rec.Raw, hex.EncodeToString(frame))
	}
	if rec.Text != "hello from the field" {
		t.Errorf("text = %q", rec.Text)
	}
	if rec.Time.IsZero() {
		t.Error("time not stamped")
	}
	if rec.MsgRef != "" {
		t.Errorf("rx record must not carry a msg_ref: %q", rec.MsgRef)
	}
}

func TestAPRSPacketFeed_TransmitPaths(t *testing.T) {
	c := newCollectSink()
	g, rw, stop := newSerialTestGateway(t, c.sink)
	defer stop()

	// 1. The gateway's own plaintext frame carries the delivery msg_ref.
	g.sendMessage(&transport.MeshMessage{
		From:        0x0a0b0c0d,
		DecodedText: "relay test",
		MsgRef:      "20260906-120000-00001",
	})
	rec := c.wait(t)
	if rec.Dir != DirTX || rec.Bearer != BearerAPRS {
		t.Errorf("dir/bearer: %+v", rec)
	}
	if rec.From != "MSTESS-10" || rec.To != "APMSHT" {
		t.Errorf("from/to = %q/%q", rec.From, rec.To)
	}
	if rec.Path != "WIDE1-1,WIDE2-1" || rec.Hops != 0 {
		t.Errorf("fresh tx path %q hops %d", rec.Path, rec.Hops)
	}
	if !strings.Contains(rec.Text, "relay test") {
		t.Errorf("text = %q", rec.Text)
	}
	if rec.MsgRef != "20260906-120000-00001" {
		t.Errorf("msg_ref = %q", rec.MsgRef)
	}
	if rw.written.Len() == 0 {
		t.Error("nothing written to the TNC")
	}

	// 2. A directed message to a station.
	g.sendMessage(&transport.MeshMessage{DecodedText: "PING 1", Destination: "MSPRLX-10", RawText: true})
	rec = c.wait(t)
	if rec.Text != "PING 1" {
		t.Errorf("directed text = %q", rec.Text)
	}

	// 3. An encrypted frame: no text, still counted, no digipeater path.
	g.sendMessage(&transport.MeshMessage{DecodedText: "AAAA", Encrypted: true})
	rec = c.wait(t)
	if rec.Text != "" {
		t.Errorf("encrypted tx must not expose text, got %q", rec.Text)
	}
	if rec.Path != "" {
		t.Errorf("encrypted tx has no path, got %q", rec.Path)
	}

	// 4. A raw Reticulum frame through KISSSendFrame (the ax25_0 interface).
	rns := EncodeAX25Frame(AX25Address{Call: "RTICUL"}, AX25Address{Call: "MSTESS", SSID: 10}, nil,
		[]byte{0x00, 0x01, 0x02, 0x7f, 0x80, 0xff, 0x03, 0x04})
	if err := g.KISSSendFrame(rns); err != nil {
		t.Fatalf("KISSSendFrame: %v", err)
	}
	rec = c.wait(t)
	if rec.To != "RTICUL" || rec.Text != "" || rec.Bytes != len(rns) {
		t.Errorf("reticulum tx record %+v", rec)
	}

	// 5. A failed write records nothing.
	rw.Close()
	if err := g.KISSSendFrame(rns); err == nil {
		t.Fatal("write on a closed port must fail")
	}
	select {
	case rec := <-c.ch:
		t.Errorf("failed send must not be recorded: %+v", rec)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestAPRSPacketRecord_Shapes(t *testing.T) {
	src := AX25Address{Call: "PD0ABC", SSID: 9}
	dst := AX25Address{Call: "APRS"}
	long := strings.Repeat("y", 400)
	tests := []struct {
		name     string
		payload  []byte
		wantFrom string
		wantTo   string
		wantText string
		wantRaw  int // hex length
	}{
		{
			name:     "position with comment",
			payload:  EncodeAX25Frame(dst, src, nil, EncodeAPRSPosition(52.3676, 4.9041, '/', '-', "Mobile")),
			wantFrom: "PD0ABC-9", wantTo: "APRS", wantText: "Mobile",
		},
		{
			name:     "status frame keeps the raw info",
			payload:  EncodeAX25Frame(dst, src, nil, []byte(">on the road")),
			wantFrom: "PD0ABC-9", wantTo: "APRS", wantText: ">on the road",
		},
		{
			name:     "encrypted E1 frame has no text",
			payload:  EncodeAX25Frame(dst, src, nil, []byte(aprsEncryptedPrefix+"c2VjcmV0")),
			wantFrom: "PD0ABC-9", wantTo: "APRS", wantText: "",
		},
		{
			name:     "reticulum destination has no text",
			payload:  EncodeAX25Frame(AX25Address{Call: "RTICUL"}, src, nil, []byte("not aprs")),
			wantFrom: "PD0ABC-9", wantTo: "RTICUL", wantText: "",
		},
		{
			name:    "undecodable frame keeps raw and size only",
			payload: []byte{1, 2, 3},
		},
		{
			name:     "text and raw are capped",
			payload:  EncodeAX25Frame(dst, src, nil, []byte(">"+long)),
			wantFrom: "PD0ABC-9", wantTo: "APRS", wantText: ">" + long[:PacketTextCap-1],
			wantRaw: PacketRawHexCap,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := aprsPacketRecord(DirRX, "aprs_0", tc.payload, "")
			if rec.From != tc.wantFrom || rec.To != tc.wantTo {
				t.Errorf("from/to = %q/%q, want %q/%q", rec.From, rec.To, tc.wantFrom, tc.wantTo)
			}
			if rec.Text != tc.wantText {
				t.Errorf("text = %q, want %q", rec.Text, tc.wantText)
			}
			if rec.Bytes != len(tc.payload) {
				t.Errorf("bytes = %d, want %d", rec.Bytes, len(tc.payload))
			}
			wantRaw := tc.wantRaw
			if wantRaw == 0 {
				wantRaw = len(tc.payload) * 2
			}
			if len(rec.Raw) != wantRaw {
				t.Errorf("raw hex length = %d, want %d", len(rec.Raw), wantRaw)
			}
		})
	}
}

func TestAX25RepeatedCount(t *testing.T) {
	src := AX25Address{Call: "PD0ABC", SSID: 9}
	dst := AX25Address{Call: "APRS"}
	path := []AX25Address{{Call: "WIDE1", SSID: 1}, {Call: "WIDE2", SSID: 2}, {Call: "PI1XYZ", SSID: 3}}
	tests := []struct {
		name string
		mark []int // path slots to flag as repeated
		want int
	}{
		{"none", nil, 0},
		{"first", []int{0}, 1},
		{"first two", []int{0, 1}, 2},
		{"all three", []int{0, 1, 2}, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			frame := EncodeAX25Frame(dst, src, path, []byte(">x"))
			for _, slot := range tc.mark {
				frame[14+slot*7+6] |= 0x80
			}
			if got := ax25RepeatedCount(frame); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
	if got := ax25RepeatedCount(EncodeAX25Frame(dst, src, nil, []byte(">x"))); got != 0 {
		t.Errorf("no path: got %d", got)
	}
	if got := ax25RepeatedCount([]byte{1, 2}); got != 0 {
		t.Errorf("short: got %d", got)
	}
}
