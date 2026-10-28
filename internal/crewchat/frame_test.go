package crewchat

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// The shared vector (MESHSAT-1744, meshsat-hub docs/bridge-hub-protocol.md
// "Crew chat over satellite and SMS"): the Hub's internal/takchat tests hold
// the same bytes. A change here is a change there and in the doc.
const (
	vecMessageID = "01020304-0506-0708-090a-0b0c0d0e0f10"
	vecNode      = uint32(0x0a0b0c0d) // the test crew on parallax, uid meshsat-0a0b0c0d
	vecOperator  = "meshsat-hub-op-7"
	vecOpHash    = "3f24d8f92959c061"
	vecAt        = int64(1793188800) // 2026-10-28T12:00:00Z
	vecC1        = "4305" + "0102030405060708090a0b0c0d0e0f10" + "0a0b0c0d" + vecOpHash + "04416e6e61" + "07c48020f29794"
	vecC2        = "4d530143" + "0102030405060708090a0b0c0d0e0f10" + "0a0b0c0d" + "6ae1e3c0" + "01" + "86206d7920f579"
	vecC3Detail  = "the kit has not heard MESHSAT-0c0d in the last day"
	vecC3Head    = "4d530144" + "0102030405060708090a0b0c0d0e0f10" + "0a0b0c0d" + "6ae1e3c0" + "04" + "01" + "32"
)

func TestC1VectorDecodesAndEncodes(t *testing.T) {
	frame, _ := hex.DecodeString(vecC1)
	if !IsChat(frame) {
		t.Fatal("not recognised as a chat frame")
	}
	c, err := DecodeChat(frame)
	if err != nil {
		t.Fatal(err)
	}
	if c.MessageID != vecMessageID || c.Node != vecNode || c.ToKit || !c.ReplyHere || c.Callsign != "Anna" || c.Text != "check in please" {
		t.Errorf("decoded %+v", c)
	}
	if hex.EncodeToString(c.OperatorHash[:]) != vecOpHash || OperatorHash(vecOperator) != c.OperatorHash {
		t.Errorf("operator hash %x", c.OperatorHash)
	}
	if c.OperatorUID() != "meshsat-hub-op-~"+vecOpHash {
		t.Errorf("operator uid %q", c.OperatorUID())
	}
	again, err := EncodeChat(Chat{MessageID: vecMessageID, Node: vecNode, ReplyHere: true,
		OperatorHash: OperatorHash(vecOperator), Callsign: "Anna", Text: "check in please"})
	if err != nil || hex.EncodeToString(again) != vecC1 {
		t.Errorf("encoded %x %v", again, err)
	}
	for _, bad := range [][]byte{nil, frame[:30], append([]byte{0x57}, frame[1:]...), append(append([]byte(nil), frame[:31]...), 0x05, 0xff, 0xfe)} {
		if _, err := DecodeChat(bad); err == nil {
			t.Errorf("decoded %x", bad)
		}
	}
	raw, _ := EncodeChat(Chat{MessageID: vecMessageID, Node: 1, ToKit: true, Callsign: strings.Repeat("Ω", 12), Text: "日本"})
	if c, err := DecodeChat(raw); err != nil || c.Text != "日本" || !c.ToKit || len(c.Callsign) > maxCallsign {
		t.Errorf("raw text or a cut callsign: %+v %v", c, err)
	}
}

func TestC2VectorEncodesAndDecodes(t *testing.T) {
	frame, err := EncodeAnswer(Answer{MessageID: vecMessageID, Node: vecNode, At: time.Unix(vecAt, 0), Text: "on my way"})
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(frame); got != vecC2 {
		t.Fatalf("C2\n got %s\nwant %s", got, vecC2)
	}
	if frame[3] != UplinkTypeAnswer {
		t.Error("type byte")
	}
	a, err := DecodeAnswer(frame[4:])
	if err != nil || a.MessageID != vecMessageID || a.Node != vecNode || a.At.Unix() != vecAt || a.Text != "on my way" {
		t.Errorf("decoded %+v %v", a, err)
	}
	if _, err := EncodeAnswer(Answer{MessageID: "m1", Text: "x"}); err == nil {
		t.Error("a message id that is not a UUID encoded")
	}
}

func TestC3VectorEncodesAndDecodes(t *testing.T) {
	frame, err := EncodeStatus(Status{MessageID: vecMessageID, Node: vecNode, At: time.Unix(vecAt, 0),
		Status: "undelivered", Reason: "not_heard", Detail: vecC3Detail})
	if err != nil {
		t.Fatal(err)
	}
	want := vecC3Head + hex.EncodeToString([]byte(vecC3Detail))
	if got := hex.EncodeToString(frame); got != want {
		t.Fatalf("C3\n got %s\nwant %s", got, want)
	}
	if len(frame) > 120 {
		t.Errorf("the vector does not fit one SMS: %d bytes", len(frame))
	}
	s, err := DecodeStatus(frame[4:])
	if err != nil || s.MessageID != vecMessageID || s.Node != vecNode || s.At.Unix() != vecAt || s.Status != "undelivered" ||
		s.Reason != "not_heard" || s.Detail != vecC3Detail {
		t.Errorf("decoded %+v %v", s, err)
	}
	if _, err := EncodeStatus(Status{MessageID: vecMessageID, Status: "sent"}); err == nil {
		t.Error("the Hub's own word encoded as the kit's")
	}
	long, _ := EncodeStatus(Status{MessageID: vecMessageID, Status: "failed", Reason: "send_failed", Detail: strings.Repeat("é", 60)})
	if s, err := DecodeStatus(long[4:]); err != nil || len(s.Detail) > maxDetail || s.Reason != "send_failed" {
		t.Errorf("a long detail: %+v %v", s, err)
	}
}
