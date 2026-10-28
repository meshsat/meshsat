package gateway

import (
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"meshsat/internal/crewchat"
)

// MESHSAT-1744: a chat that came as a frame over satellite or SMS is
// delivered as the same GeoChat, and the word on it and the crew's answer go
// back as frames on that bearer when the Hub asked or its link is down.

type frameLog struct {
	mu     sync.Mutex
	frames []string // "<bearer> <hex>"
	fail   error
}

func (l *frameLog) send(bearer string, frame []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail != nil {
		return l.fail
	}
	l.frames = append(l.frames, bearer+" "+hex.EncodeToString(frame))
	return nil
}

func (l *frameLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.frames...)
}

// viaThreads is a fakeThreads that also remembers how the writer wrote.
type viaThreads struct {
	*fakeThreads
	via map[uint32][3]string // bearer, message id, reply_here
}

func (f *viaThreads) OpenTAKChatThreadVia(node uint32, uid, callsign, bearer, messageID string, replyHere bool, at time.Time) error {
	_ = f.fakeThreads.OpenTAKChatThread(node, uid, callsign, at)
	rh := "0"
	if replyHere {
		rh = "1"
	}
	f.via[node] = [3]string{bearer, messageID, rh}
	return nil
}

func (f *viaThreads) TAKChatThreadVia(node uint32, since time.Time) (string, string, string, string, bool, bool, error) {
	uid, callsign, ok, err := f.fakeThreads.TAKChatThread(node, since)
	if !ok || err != nil {
		return "", "", "", "", false, ok, err
	}
	v := f.via[node]
	return uid, callsign, v[0], v[1], v[2] == "1", true, nil
}

func frameRig(hubUp bool, heard ...uint32) (*chatRig, *frameLog, *viaThreads) {
	r := newChatRig(heard...)
	threads := &viaThreads{fakeThreads: r.threads, via: map[uint32][3]string{}}
	r.chat.deps.Threads = threads
	l := &frameLog{}
	r.chat.SetFrames(&TAKChatFrames{SendAnswer: l.send, SendStatus: l.send, HubUp: func() bool { return hubUp }})
	return r, l, threads
}

// vecChat is the shared vector's C1 as the Hub sends it to the crew on
// parallax: operator 7 as "Anna", to meshsat-0a0b0c0d, reply on this bearer.
func vecChat(t *testing.T) crewchat.Chat {
	t.Helper()
	raw, _ := hex.DecodeString("4305" + "0102030405060708090a0b0c0d0e0f10" + "0a0b0c0d" + "3f24d8f92959c061" + "04416e6e61" + "07c48020f29794")
	c, err := crewchat.DecodeChat(raw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAChatThatCameAsAFrameIsDeliveredLikeTheGeoChat(t *testing.T) {
	r, l, threads := frameRig(true, 0x0a0b0c0d)
	f := vecChat(t)
	if n := r.chat.DeliverFrame(f, "sms"); n != 1 {
		t.Fatalf("delivered %d", n)
	}
	if len(r.dms) != 1 || r.dms[0] != (sent{0x0a0b0c0d, "Anna: check in please"}) {
		t.Fatalf("direct messages %+v", r.dms)
	}
	if len(r.reports) != 1 || r.reports[0].MessageID != f.MessageID || r.reports[0].Status != TAKChatQueued ||
		r.reports[0].Bearer != "sms" || !r.reports[0].ReplyHere || r.reports[0].CrewUID != "meshsat-0a0b0c0d" {
		t.Fatalf("reports %+v", r.reports)
	}
	// The thread remembers the operator by hash, the bearer and the id.
	uid, callsign, bearer, id, replyHere, ok, _ := threads.TAKChatThreadVia(0x0a0b0c0d, time.Now().Add(-time.Hour))
	if !ok || uid != "meshsat-hub-op-~3f24d8f92959c061" || callsign != "Anna" || bearer != "sms" || id != f.MessageID || !replyHere {
		t.Errorf("thread %q %q %q %q %v %v", uid, callsign, bearer, id, replyHere, ok)
	}
	// The Hub asked for the word on this bearer: the queued went as a C3
	// even with the Hub link up.
	got := l.list()
	if len(got) != 1 || got[0][:4] != "sms " {
		t.Fatalf("frames %q", got)
	}
	raw, _ := hex.DecodeString(got[0][4:])
	st, err := crewchat.DecodeStatus(raw[4:])
	if err != nil || raw[3] != crewchat.UplinkTypeStatus || st.MessageID != f.MessageID || st.Node != 0x0a0b0c0d || st.Status != "queued" {
		t.Errorf("C3 %+v %v", st, err)
	}
	// The same frame again (a carrier retry): not a second message, no word.
	if n := r.chat.DeliverFrame(f, "sms"); n != 0 || len(r.dms) != 1 || len(l.list()) != 1 {
		t.Errorf("a repeated frame was sent again")
	}
}

func TestACrewNotHeardIsReportedUndeliveredOnTheFramesBearer(t *testing.T) {
	// parallax has not heard MESHSAT-c2ec for a day: the integrating
	// session's live proof expects exactly this word back over SMS.
	r, l, _ := frameRig(true, 0x0000beef)
	f := vecChat(t)
	if n := r.chat.DeliverFrame(f, "sms"); n != 0 || len(r.dms) != 0 {
		t.Fatalf("sent %d", n)
	}
	if len(r.reports) != 1 || r.reports[0].Status != TAKChatUndelivered || r.reports[0].Reason != takChatReasonNotHeard || r.reports[0].Bearer != "sms" {
		t.Fatalf("reports %+v", r.reports)
	}
	got := l.list()
	if len(got) != 1 {
		t.Fatalf("frames %q", got)
	}
	raw, _ := hex.DecodeString(got[0][4:])
	st, err := crewchat.DecodeStatus(raw[4:])
	if err != nil || st.Status != "undelivered" || st.Reason != "not_heard" || st.Detail != "the kit has not heard MESHSAT-c2ec in the last day" {
		t.Errorf("C3 %+v %v", st, err)
	}
	if len(raw) > 120 {
		t.Errorf("the C3 does not fit one SMS: %d bytes", len(raw))
	}
}

func TestTheWordGoesAsAFrameOnlyWhenOwed(t *testing.T) {
	f := vecChat(t)
	f.ReplyHere = false
	// Hub link up, the Hub did not ask: MQTT only.
	r, l, _ := frameRig(true, 0x0a0b0c0d)
	r.chat.DeliverFrame(f, "sbd")
	if len(r.reports) != 1 || len(l.list()) != 0 {
		t.Errorf("link up: reports %d frames %q", len(r.reports), l.list())
	}
	// Hub link down: the frame goes, on the chat's bearer.
	r, l, _ = frameRig(false, 0x0a0b0c0d)
	r.chat.DeliverFrame(f, "sbd")
	if got := l.list(); len(got) != 1 || got[0][:4] != "sbd " {
		t.Errorf("link down: frames %q", got)
	}
	// A chat from the Hub's MQTT never owes a frame, link up or down.
	r, l, _ = frameRig(false, 0xa1b3c3a4)
	r.chat.Deliver(mustParse(t, atakDirect))
	if len(r.reports) != 1 || r.reports[0].Bearer != "" || len(l.list()) != 0 {
		t.Errorf("an MQTT chat: reports %+v frames %q", r.reports, l.list())
	}
	// Without the frames set, nothing is spent.
	r, l, _ = frameRig(false, 0x0a0b0c0d)
	r.chat.SetFrames(nil)
	r.chat.DeliverFrame(f, "sms")
	if len(l.list()) != 0 || len(r.reports) != 1 {
		t.Errorf("no frames: %q", l.list())
	}
	// The ledger giving up later is a failed on the same bearer.
	r, l, _ = frameRig(false, 0x0a0b0c0d)
	r.chat.DeliverFrame(f, "sms")
	r.chat.ObserveDelivery(deliveryEvent(t, "delivery_dead", 101, "radio gone"))
	if len(r.reports) != 2 || r.reports[1].Status != TAKChatFailed || r.reports[1].Bearer != "sms" {
		t.Fatalf("reports %+v", r.reports)
	}
	if got := l.list(); len(got) != 2 {
		t.Errorf("frames %q", got)
	}
	// A bearer that refuses the frame is logged, the MQTT report stands.
	r, l, _ = frameRig(false, 0x0a0b0c0d)
	l.fail = errors.New("no Hub SMS number")
	r.chat.DeliverFrame(f, "sms")
	if len(r.reports) != 1 {
		t.Errorf("reports %+v", r.reports)
	}
}

func TestTheCrewsAnswerGoesBackOnTheFramesBearer(t *testing.T) {
	r, l, _ := frameRig(true, 0x0a0b0c0d)
	f := vecChat(t)
	r.chat.DeliverFrame(f, "sms")
	before := len(l.list())
	if !r.chat.Reply(0x0a0b0c0d, " on my way ") {
		t.Fatal("not answered")
	}
	got := l.list()
	if len(got) != before+1 || got[before][:4] != "sms " {
		t.Fatalf("frames %q", got)
	}
	raw, _ := hex.DecodeString(got[before][4:])
	a, err := crewchat.DecodeAnswer(raw[4:])
	if err != nil || raw[3] != crewchat.UplinkTypeAnswer || a.MessageID != f.MessageID || a.Node != 0x0a0b0c0d || a.Text != "on my way" {
		t.Errorf("C2 %+v %v", a, err)
	}
	// A second answer goes the same way: the thread stayed open as it was.
	if !r.chat.Reply(0x0a0b0c0d, "still here") || len(l.list()) != before+2 {
		t.Errorf("the second answer: %q", l.list())
	}
	// With the Hub link up and no ask, the answer is a GeoChat to the hashed
	// operator, which the Hub resolves; no frame.
	f.ReplyHere = false
	r, l, _ = frameRig(true, 0x0a0b0c0d)
	r.chat.DeliverFrame(f, "sbd")
	if !r.chat.Reply(0x0a0b0c0d, "roger") || len(l.list()) != 0 {
		t.Errorf("link up: frames %q", l.list())
	}
	// A question that came over MQTT is answered over MQTT whatever the link.
	r, l, _ = frameRig(false, 0xa1b3c3a4)
	r.chat.Deliver(mustParse(t, atakDirect))
	if !r.chat.Reply(0xa1b3c3a4, "copy") || len(l.list()) != 0 {
		t.Errorf("an MQTT question: frames %q", l.list())
	}
}
