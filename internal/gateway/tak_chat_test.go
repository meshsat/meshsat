package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"meshsat/internal/transport"
)

// GeoChats as TAK clients write them (MESHSAT-1705): ATAK and WinTAK name the
// recipient in <marti><dest callsign>, iTAK in <dest uid>; a chat to the room
// has no dest at all.
const (
	atakDirect = `<event version="2.0" uid="GeoChat.ANDROID-abc.meshsat-a1b3c3a4.m1" type="b-t-f" how="h-g-i-g-o" time="2026-10-27T10:00:00.000Z" start="2026-10-27T10:00:00.000Z" stale="2026-10-28T10:00:00.000Z">` +
		`<point lat="52.1" lon="4.5" hae="9999999.0" ce="9999999.0" le="9999999.0"/><detail>` +
		`<__chat parent="RootContactGroup" groupOwner="false" messageId="m1" chatroom="MESHSAT-c3a4" id="meshsat-a1b3c3a4" senderCallsign="DISPATCH-1">` +
		`<chatgrp uid0="ANDROID-abc" uid1="meshsat-a1b3c3a4" id="meshsat-a1b3c3a4"/></__chat>` +
		`<link uid="ANDROID-abc" type="a-f-G-U-C" relation="p-p"/>` +
		`<remarks source="BAO.F.ATAK.ANDROID-abc" to="meshsat-a1b3c3a4" time="2026-10-27T10:00:00.000Z">check in please</remarks>` +
		`<__serverdestination destinations="10.0.0.2:4242:tcp:ANDROID-abc"/>` +
		`<marti><dest callsign="MESHSAT-c3a4"/></marti></detail></event>`
	itakDirect = `<event version="2.0" uid="GeoChat.ITAK-1.meshsat-a1b3c3a4.m2" type="b-t-f" how="h-g-i-g-o" time="2026-10-27T10:00:00.000Z" start="2026-10-27T10:00:00.000Z" stale="2026-10-28T10:00:00.000Z">` +
		`<point lat="0" lon="0" hae="0" ce="9999999" le="9999999"/><detail>` +
		`<__chat parent="RootContactGroup" groupOwner="false" messageId="m2" chatroom="MESHSAT-c3a4" id="meshsat-a1b3c3a4" senderCallsign="ITAK-OPS">` +
		`<chatgrp uid0="ITAK-1" uid1="meshsat-a1b3c3a4" id="meshsat-a1b3c3a4"/></__chat>` +
		`<link uid="ITAK-1" type="a-f-G-U-C" relation="p-p"/>` +
		`<remarks source="ITAK-1" to="meshsat-a1b3c3a4">go north</remarks>` +
		`<marti><dest uid="meshsat-a1b3c3a4"/></marti></detail></event>`
	atakRoom = `<event version="2.0" uid="GeoChat.ANDROID-abc.All Chat Rooms.m3" type="b-t-f" how="h-g-i-g-o" time="2026-10-27T10:00:00.000Z" start="2026-10-27T10:00:00.000Z" stale="2026-10-28T10:00:00.000Z">` +
		`<point lat="0" lon="0" hae="0" ce="9999999" le="9999999"/><detail>` +
		`<__chat parent="RootContactGroup" groupOwner="false" messageId="m3" chatroom="All Chat Rooms" id="All Chat Rooms" senderCallsign="DISPATCH-1">` +
		`<chatgrp uid0="ANDROID-abc" uid1="All Chat Rooms" id="All Chat Rooms"/></__chat>` +
		`<link uid="ANDROID-abc" type="a-f-G-U-C" relation="p-p"/>` +
		`<remarks source="BAO.F.ATAK.ANDROID-abc" to="All Chat Rooms">everyone hold</remarks></detail></event>`
)

func mustParse(t *testing.T, doc string) *CotEvent {
	t.Helper()
	ev, err := ParseCotEvent([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return ev
}

func TestChatAddresseesAsEachClientWritesThem(t *testing.T) {
	crew := ChatPeer{UID: "meshsat-a1b3c3a4", Callsign: "MESHSAT-c3a4"}
	cases := []struct {
		name   string
		doc    string
		to     []ChatPeer
		room   bool
		from   ChatPeer
		text   string
		withID string
	}{
		{"atak by callsign", atakDirect, []ChatPeer{crew}, false, ChatPeer{"ANDROID-abc", "DISPATCH-1"}, "check in please", "m1"},
		{"itak by uid", itakDirect, []ChatPeer{crew}, false, ChatPeer{"ITAK-1", "ITAK-OPS"}, "go north", "m2"},
		{"the room", atakRoom, nil, true, ChatPeer{"ANDROID-abc", "DISPATCH-1"}, "everyone hold", "m3"},
		{"a mission dest is not a person", strings.Replace(atakRoom, `</remarks>`, `</remarks><marti><dest mission="ops"/></marti>`, 1),
			nil, true, ChatPeer{"ANDROID-abc", "DISPATCH-1"}, "everyone hold", "m3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := mustParse(t, c.doc)
			to, room := ChatAddressees(ev)
			if room != c.room || fmt.Sprint(to) != fmt.Sprint(c.to) {
				t.Errorf("addressees = %v room %v, want %v room %v", to, room, c.to, c.room)
			}
			if got := ChatSender(ev); got != c.from {
				t.Errorf("sender = %+v, want %+v", got, c.from)
			}
			if got := ChatText(ev); got != c.text {
				t.Errorf("text = %q, want %q", got, c.text)
			}
			if got := chatMessageID(ev); got != c.withID {
				t.Errorf("message id = %q, want %q", got, c.withID)
			}
		})
	}
	// Not a chat: nobody.
	if to, room := ChatAddressees(mustParse(t, string(mustMarshal(t, BuildPositionEvent("meshsat-a1b3c3a4", "MESHSAT-c3a4", 1, 2, 0, 60))))); to != nil || room {
		t.Errorf("a position has addressees %v room %v", to, room)
	}
}

func mustMarshal(t *testing.T, ev CotEvent) []byte {
	t.Helper()
	b, err := MarshalCotEvent(ev)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBuildGeoChatReadsBackAsATAKWritesIt(t *testing.T) {
	from := ChatPeer{UID: "meshsat-a1b3c3a4", Callsign: "MESHSAT-c3a4"}
	to := ChatPeer{UID: "ANDROID-abc", Callsign: "DISPATCH-1"}
	ev := mustParse(t, string(mustMarshal(t, BuildGeoChat(from, to, "on my way <5 min> & dry"))))
	if ev.Type != CotEventTypeChat || !strings.HasPrefix(ev.UID, "GeoChat.meshsat-a1b3c3a4.ANDROID-abc.") {
		t.Errorf("type %q uid %q", ev.Type, ev.UID)
	}
	got, room := ChatAddressees(ev)
	if room || len(got) != 1 || got[0] != to {
		t.Errorf("addressees %v room %v, want %v", got, room, to)
	}
	if s := ChatSender(ev); s != from {
		t.Errorf("sender %+v, want %+v", s, from)
	}
	if ChatText(ev) != "on my way <5 min> & dry" {
		t.Errorf("text %q", ChatText(ev))
	}
	if ev.Detail.Marti == nil || ev.Detail.Marti.Dests[0].UID != to.UID || ev.Detail.Marti.Dests[0].Callsign != to.Callsign {
		t.Errorf("marti %+v: the server routes by dest, and both forms must be there", ev.Detail.Marti)
	}
	// To the room: no dest, so the server sends it to the group.
	room1 := mustParse(t, string(mustMarshal(t, BuildChatEvent("MESHSAT-de11f199", "MESHSAT-F199", "hello all", 300))))
	if room1.Detail.Marti != nil {
		t.Errorf("a room chat carries a dest: %+v", room1.Detail.Marti)
	}
	if _, isRoom := ChatAddressees(room1); !isRoom {
		t.Error("BuildChatEvent is not a chat to All Chat Rooms")
	}
	if room1.Detail.Contact == nil || room1.Detail.Contact.Callsign != "MESHSAT-F199" {
		t.Errorf("BuildChatEvent lost its contact: %+v", room1.Detail.Contact)
	}
}

func TestGeoChatSurvivesTAKProtocolV1(t *testing.T) {
	from := ChatPeer{UID: "ANDROID-abc", Callsign: "DISPATCH-1"}
	to := ChatPeer{UID: "meshsat-a1b3c3a4", Callsign: "MESHSAT-c3a4"}
	msg, err := CotEventToProto(BuildGeoChat(from, to, `a <b> & "c"`))
	if err != nil {
		t.Fatal(err)
	}
	back, err := ProtoToCotEvent(msg)
	if err != nil {
		t.Fatal(err)
	}
	if ChatText(back) != `a <b> & "c"` {
		t.Errorf("text after protobuf %q; it used to be dropped here", ChatText(back))
	}
	if got, _ := ChatAddressees(back); len(got) != 1 || got[0] != to {
		t.Errorf("addressees after protobuf %v", got)
	}
	if ChatSender(back) != from {
		t.Errorf("sender after protobuf %+v", ChatSender(back))
	}
	// A position keeps its endpoint through protobuf too.
	pmsg, err := CotEventToProto(BuildPositionEvent("meshsat-a1b3c3a4", "MESHSAT-c3a4", 52, 4, 0, 60))
	if err != nil {
		t.Fatal(err)
	}
	pback, err := ProtoToCotEvent(pmsg)
	if err != nil {
		t.Fatal(err)
	}
	if pback.Detail.Contact.Endpoint != TAKContactEndpoint {
		t.Errorf("endpoint after protobuf %q", pback.Detail.Contact.Endpoint)
	}
}

func TestExportedPositionsOfferChat(t *testing.T) {
	ev := BuildPositionEvent("meshsat-a1b3c3a4", "MESHSAT-c3a4", 52, 4, 0, 60)
	if ev.Detail.Contact.Endpoint != TAKContactEndpoint {
		t.Errorf("contact endpoint %q: a TAK client offers no chat without it", ev.Detail.Contact.Endpoint)
	}
	if !strings.Contains(string(mustMarshal(t, ev)), `endpoint="*:-1:stcp"`) {
		t.Error("the endpoint is not in the XML")
	}
}

func TestInboundChatNamesItsSender(t *testing.T) {
	if got := CotEventToInboundMessage(mustParse(t, atakDirect)).Text; got != "DISPATCH-1: check in please" {
		t.Errorf("inbound text %q", got)
	}
}

// fakeThreads is TAKChatThreads in memory.
type fakeThreads struct {
	mu   sync.Mutex
	rows map[uint32][3]string
	at   map[uint32]time.Time
}

func newFakeThreads() *fakeThreads {
	return &fakeThreads{rows: map[uint32][3]string{}, at: map[uint32]time.Time{}}
}

func (f *fakeThreads) OpenTAKChatThread(node uint32, uid, callsign string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[node] = [3]string{uid, callsign}
	f.at[node] = at
	return nil
}

func (f *fakeThreads) TAKChatThread(node uint32, since time.Time) (string, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[node]
	if !ok || f.at[node].Before(since) {
		return "", "", false, nil
	}
	return r[0], r[1], true, nil
}

type sent struct {
	node uint32
	text string
}

type chatRig struct {
	chat       *TAKChat
	dms        []sent
	broadcasts []string
	threads    *fakeThreads
	gw         *TAKGateway
	// Every ledger row the rig queued gets an id from 100 up; sendErr makes
	// the ledger refuse. reports is what the Hub would be told.
	nextID  int64
	sendErr error
	reports []TAKChatStatus
}

func newChatRig(heard ...uint32) *chatRig {
	r := &chatRig{threads: newFakeThreads(), nextID: 100}
	r.gw = NewTAKGateway(TAKConfig{CallsignPrefix: "MESHSAT", CotStaleSec: 300}, nil)
	r.gw.SetHooks(TAKHooks{NodeID: func() string { return "de11f199" }})
	r.chat = NewTAKChat(TAKChatDeps{
		Heard:     func() []uint32 { return heard },
		LocalNode: func() (uint32, bool) { return 0xde11f199, true },
		SendDM: func(node uint32, text string) (int64, error) {
			if r.sendErr != nil {
				return 0, r.sendErr
			}
			r.dms = append(r.dms, sent{node, text})
			r.nextID++
			return r.nextID, nil
		},
		Broadcast: func(text string) (int64, error) {
			if r.sendErr != nil {
				return 0, r.sendErr
			}
			r.broadcasts = append(r.broadcasts, text)
			r.nextID++
			return r.nextID, nil
		},
		Threads: r.threads,
		Gateway: func() *TAKGateway { return r.gw },
	})
	r.chat.SetReport(func(st TAKChatStatus) { r.reports = append(r.reports, st) })
	return r
}

// brief is a report as a test reads it: status, reason and crew.
func brief(st TAKChatStatus) string {
	return st.MessageID + " " + st.CrewUID + " " + st.Status + " " + st.Reason
}

func TestTAKChatDeliversToTheCrewItNames(t *testing.T) {
	r := newChatRig(0xa1b3c3a4, 0xde11f199, 0x0000beef)
	if n := r.chat.Deliver(mustParse(t, atakDirect)); n != 1 {
		t.Fatalf("delivered %d", n)
	}
	if len(r.dms) != 1 || r.dms[0] != (sent{0xa1b3c3a4, "DISPATCH-1: check in please"}) {
		t.Fatalf("direct messages %+v", r.dms)
	}
	if len(r.broadcasts) != 0 {
		t.Errorf("a direct chat was also broadcast: %v", r.broadcasts)
	}
	uid, callsign, ok, _ := r.threads.TAKChatThread(0xa1b3c3a4, time.Now().Add(-time.Hour))
	if !ok || uid != "ANDROID-abc" || callsign != "DISPATCH-1" {
		t.Errorf("thread %q %q %v: the answer would have nowhere to go", uid, callsign, ok)
	}
	// The same chat again (a QoS 1 redelivery): not a second message.
	if n := r.chat.Deliver(mustParse(t, atakDirect)); n != 0 || len(r.dms) != 1 {
		t.Errorf("a repeated chat was sent again: %d, %+v", n, r.dms)
	}
	// iTAK's form reaches the same crew (positive control for the dedup: a
	// different message id).
	if n := r.chat.Deliver(mustParse(t, itakDirect)); n != 1 || r.dms[1] != (sent{0xa1b3c3a4, "ITAK-OPS: go north"}) {
		t.Errorf("itak: %d %+v", n, r.dms)
	}
}

func TestTAKChatGoesNowhereItShouldNot(t *testing.T) {
	cases := []struct {
		name  string
		heard []uint32
		doc   string
	}{
		{"a crew this Bridge does not hear", []uint32{0x0000beef}, atakDirect},
		{"the room", []uint32{0xa1b3c3a4}, atakRoom},
		{"a crew's own chat coming back", []uint32{0xa1b3c3a4},
			strings.NewReplacer(`<link uid="ANDROID-abc"`, `<link uid="meshsat-0000beef"`, `messageId="m1"`, `messageId="m9"`).Replace(atakDirect)},
		{"no words", []uint32{0xa1b3c3a4}, strings.Replace(atakDirect, "check in please", "  ", 1)},
		{"a callsign two heard nodes share", []uint32{0x1111c3a4, 0x2222c3a4},
			strings.NewReplacer(` id="meshsat-a1b3c3a4"`, ` id=""`, `uid1="meshsat-a1b3c3a4"`, `uid1=""`, `to="meshsat-a1b3c3a4"`, `to=""`).Replace(atakDirect)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newChatRig(c.heard...)
			if n := r.chat.Deliver(mustParse(t, c.doc)); n != 0 || len(r.dms) != 0 || len(r.broadcasts) != 0 {
				t.Errorf("sent %d: dms %+v broadcasts %v", n, r.dms, r.broadcasts)
			}
		})
	}
	// Positive control: the callsign alone resolves when one heard node has it.
	r := newChatRig(0x1111c3a4, 0x0000beef)
	doc := strings.NewReplacer(` id="meshsat-a1b3c3a4"`, ` id=""`, `uid1="meshsat-a1b3c3a4"`, `uid1=""`, `to="meshsat-a1b3c3a4"`, `to=""`).Replace(atakDirect)
	if n := r.chat.Deliver(mustParse(t, doc)); n != 1 || r.dms[0].node != 0x1111c3a4 {
		t.Errorf("a callsign one heard node has went to %+v", r.dms)
	}
}

func TestTAKChatToThisBridgeGoesToTheWholeMesh(t *testing.T) {
	r := newChatRig(0xa1b3c3a4)
	doc := strings.NewReplacer("meshsat-a1b3c3a4", "MESHSAT-de11f199", "MESHSAT-c3a4", "MESHSAT-F199").Replace(atakDirect)
	if n := r.chat.Deliver(mustParse(t, doc)); n != 1 {
		t.Fatalf("delivered %d", n)
	}
	if len(r.broadcasts) != 1 || r.broadcasts[0] != "DISPATCH-1: check in please" || len(r.dms) != 0 {
		t.Errorf("broadcasts %v dms %+v", r.broadcasts, r.dms)
	}
}

func TestTAKChatLineIsCutOnACharacter(t *testing.T) {
	text := strings.Repeat("é", 150) // 300 bytes
	line, cut := takChatLine(ChatPeer{Callsign: "DISPATCH-1"}, text)
	if !cut || len(line) > TAKChatMaxBytes || !utf8.ValidString(line) || !strings.HasPrefix(line, "DISPATCH-1: ") {
		t.Errorf("cut %v, %d bytes, valid %v: %q", cut, len(line), utf8.ValidString(line), line)
	}
	if line, cut := takChatLine(ChatPeer{Callsign: "D"}, "short"); cut || line != "D: short" {
		t.Errorf("a short text was changed: %q %v", line, cut)
	}
}

func TestTAKChatAnswerGoesBackToTheWriter(t *testing.T) {
	hub := &fakeTAKHub{connected: true}
	r := newChatRig(0xa1b3c3a4)
	r.gw = NewTAKGateway(TAKConfig{CallsignPrefix: "MESHSAT", CotStaleSec: 300, CoalesceSeconds: 3600, HubExport: true}, nil)
	r.gw.SetHooks(TAKHooks{Hub: hub, NodeID: func() string { return "de11f199" }})
	if err := r.gw.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer r.gw.Stop()

	// Nobody wrote to this node: its text is not an answer, nothing goes.
	if r.chat.Reply(0xa1b3c3a4, "anyone?") {
		t.Fatal("a text with no thread was sent as an answer")
	}
	if n := r.chat.Deliver(mustParse(t, atakDirect)); n != 1 {
		t.Fatalf("delivered %d", n)
	}
	if !r.chat.Reply(0xa1b3c3a4, "on my way") {
		t.Fatal("the answer was not sent")
	}
	ev := hub.waitFor(t, "the answer", func(ev CotEvent) bool {
		return ev.Type == CotEventTypeChat && ChatText(&ev) == "on my way"
	})
	if s := ChatSender(&ev); s != (ChatPeer{UID: "meshsat-a1b3c3a4", Callsign: "MESHSAT-c3a4"}) {
		t.Errorf("answer from %+v", s)
	}
	if to, _ := ChatAddressees(&ev); len(to) != 1 || to[0] != (ChatPeer{UID: "ANDROID-abc", Callsign: "DISPATCH-1"}) {
		t.Errorf("answer to %v", to)
	}
	if containsEvent(hub.events(t), func(ev CotEvent) bool { return ChatText(&ev) == "anyone?" }) {
		t.Error("the text with no thread reached the Hub")
	}
	// A day later the thread is closed.
	r.chat.now = func() time.Time { return time.Now().Add(TAKChatThreadLife + time.Minute) }
	if r.chat.Reply(0xa1b3c3a4, "late") {
		t.Error("an answer went back to a thread older than a day")
	}
}

// MESHSAT-1746: every addressee gets one word on what became of the chat, so
// a dispatcher's message never vanishes without a trace.
func TestTAKChatReportsWhatBecameOfEachChat(t *testing.T) {
	t.Run("queued", func(t *testing.T) {
		r := newChatRig(0xa1b3c3a4)
		r.chat.Deliver(mustParse(t, atakDirect))
		if len(r.reports) != 1 || brief(r.reports[0]) != "m1 meshsat-a1b3c3a4 queued " {
			t.Fatalf("reports %+v", r.reports)
		}
		if st := r.reports[0]; st.Detail != "queued as a direct message to !a1b3c3a4" || st.At.IsZero() {
			t.Errorf("report %+v", st)
		}
		// A redelivery of the same chat: the first report stands.
		r.chat.Deliver(mustParse(t, atakDirect))
		if len(r.reports) != 1 {
			t.Errorf("a repeated chat was reported again: %+v", r.reports)
		}
	})
	t.Run("to this kit, the whole mesh", func(t *testing.T) {
		r := newChatRig(0xa1b3c3a4)
		doc := strings.NewReplacer("meshsat-a1b3c3a4", "MESHSAT-de11f199", "MESHSAT-c3a4", "MESHSAT-F199").Replace(atakDirect)
		r.chat.Deliver(mustParse(t, doc))
		if len(r.reports) != 1 || brief(r.reports[0]) != "m1 MESHSAT-de11f199 queued " || !strings.HasPrefix(r.reports[0].Detail, "queued to the whole mesh") {
			t.Fatalf("reports %+v", r.reports)
		}
	})
	cases := []struct {
		name   string
		heard  []uint32
		doc    string
		want   string
		detail string
	}{
		{"a crew this kit has not heard", []uint32{0x0000beef}, atakDirect,
			"m1 meshsat-a1b3c3a4 undelivered not_heard", "the kit has not heard MESHSAT-c3a4 in the last day"},
		{"no words", []uint32{0xa1b3c3a4}, strings.Replace(atakDirect, "check in please", "  ", 1),
			"m1 meshsat-a1b3c3a4 undelivered no_text", "the chat carries no words"},
		{"a crew's own chat", []uint32{0xa1b3c3a4},
			strings.NewReplacer(`<link uid="ANDROID-abc"`, `<link uid="meshsat-0000beef"`, `messageId="m1"`, `messageId="m9"`).Replace(atakDirect),
			"m9 meshsat-a1b3c3a4 undelivered own_chat", ""},
		{"a callsign two heard nodes share", []uint32{0x1111c3a4, 0x2222c3a4},
			strings.NewReplacer(` id="meshsat-a1b3c3a4"`, ` id=""`, `uid1="meshsat-a1b3c3a4"`, `uid1=""`, `to="meshsat-a1b3c3a4"`, `to=""`).Replace(atakDirect),
			"m1 MESHSAT-c3a4 undelivered ambiguous_callsign", "two nodes the kit heard answer to MESHSAT-c3a4; name the uid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newChatRig(c.heard...)
			if n := r.chat.Deliver(mustParse(t, c.doc)); n != 0 {
				t.Errorf("sent %d", n)
			}
			if len(r.reports) != 1 || brief(r.reports[0]) != c.want {
				t.Fatalf("reports %+v, want %q", r.reports, c.want)
			}
			if c.detail != "" && r.reports[0].Detail != c.detail {
				t.Errorf("detail %q, want %q", r.reports[0].Detail, c.detail)
			}
		})
	}
	t.Run("the room and nobody get no report", func(t *testing.T) {
		r := newChatRig(0xa1b3c3a4)
		r.chat.Deliver(mustParse(t, atakRoom))
		if len(r.reports) != 0 {
			t.Errorf("reports %+v", r.reports)
		}
	})
	t.Run("the ledger refused the row", func(t *testing.T) {
		r := newChatRig(0xa1b3c3a4)
		r.sendErr = fmt.Errorf("queue delivery: database is locked")
		r.chat.Deliver(mustParse(t, atakDirect))
		if len(r.reports) != 1 || brief(r.reports[0]) != "m1 meshsat-a1b3c3a4 undelivered send_failed" ||
			r.reports[0].Detail != "not queued to the radio: queue delivery: database is locked" {
			t.Fatalf("reports %+v", r.reports)
		}
	})
	t.Run("no radio", func(t *testing.T) {
		r := newChatRig(0xa1b3c3a4)
		r.chat.deps.SendDM = nil
		r.chat.Deliver(mustParse(t, atakDirect))
		if len(r.reports) != 1 || brief(r.reports[0]) != "m1 meshsat-a1b3c3a4 undelivered no_radio" {
			t.Fatalf("reports %+v", r.reports)
		}
	})
	t.Run("without a Hub nothing is reported and nothing breaks", func(t *testing.T) {
		r := newChatRig(0xa1b3c3a4)
		r.chat.SetReport(nil)
		if n := r.chat.Deliver(mustParse(t, atakDirect)); n != 1 || len(r.reports) != 0 {
			t.Errorf("sent %d, reports %+v", n, r.reports)
		}
	})
}

func deliveryEvent(t *testing.T, typ string, id int64, errText string) transport.MeshEvent {
	t.Helper()
	data, err := json.Marshal(map[string]any{"id": id, "channel": "mesh_0", "status": "dead", "error": errText})
	if err != nil {
		t.Fatal(err)
	}
	return transport.MeshEvent{Type: typ, Data: data, Time: time.Now().UTC().Format(time.RFC3339)}
}

// The ledger's own verdict on a queued chat: given up is reported as failed;
// a row that went to the radio is forgotten without a word, because the mesh
// send reads no routing ack and "delivered" would be the radio's receipt, not
// the node's.
func TestTAKChatReportsALedgerThatGaveUp(t *testing.T) {
	r := newChatRig(0xa1b3c3a4)
	r.chat.Deliver(mustParse(t, atakDirect)) // row 101
	r.chat.Deliver(mustParse(t, itakDirect)) // row 102
	if len(r.reports) != 2 {
		t.Fatalf("reports %+v", r.reports)
	}
	r.chat.ObserveDelivery(deliveryEvent(t, "delivery_retry", 101, "not connected"))
	r.chat.ObserveDelivery(deliveryEvent(t, "delivery_dead", 999, "somebody else's row"))
	r.chat.ObserveDelivery(transport.MeshEvent{Type: "delivery_dead", Data: json.RawMessage(`not json`)})
	if len(r.reports) != 2 {
		t.Fatalf("a retry, a stranger's row or garbage was reported: %+v", r.reports[2:])
	}
	r.chat.ObserveDelivery(deliveryEvent(t, "delivery_dead", 101, "mesh_0: not connected"))
	if len(r.reports) != 3 || brief(r.reports[2]) != "m1 meshsat-a1b3c3a4 failed send_failed" ||
		r.reports[2].Detail != "the kit's delivery ledger gave up on it: mesh_0: not connected" {
		t.Fatalf("reports %+v", r.reports)
	}
	// Once given up, the row is forgotten: a second dead event says nothing.
	r.chat.ObserveDelivery(deliveryEvent(t, "delivery_dead", 101, "again"))
	// The other row went out: forgotten too, and a later dead for it is not ours.
	r.chat.ObserveDelivery(deliveryEvent(t, "delivery_acked", 102, ""))
	r.chat.ObserveDelivery(deliveryEvent(t, "delivery_dead", 102, "late"))
	if len(r.reports) != 3 {
		t.Fatalf("reports %+v", r.reports[3:])
	}
	if n := len(r.chat.pending); n != 0 {
		t.Errorf("%d rows still watched", n)
	}
}
