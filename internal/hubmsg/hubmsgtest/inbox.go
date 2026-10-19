// Package hubmsgtest runs the fixture release's inbox cases (C3) against any inbox: the one
// in memory in hubmsg's tests and the database's. [MESHSAT-1584]
package hubmsgtest

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"meshsat/internal/hubmsg"
)

// Fixture reads one file of the release (internal/hembfixtures/testdata/v1) into v.
func Fixture(t testing.TB, name string, v any) {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..", "hembfixtures", "testdata", "v1", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// InboxCase is one step of inbox_cases.json.
type InboxCase struct {
	Case     string `json:"case"`
	Capacity int    `json:"capacity"`
	Steps    []struct {
		Now              int64 `json:"now"`
		ClockEstablished bool  `json:"clock_established"`
		Submission       struct {
			Origin       string `json:"origin"`
			Recipient    string `json:"recipient"`
			OpID         string `json:"op_id"`
			Mid          string `json:"mid"`
			ContentType  string `json:"content_type"`
			MidExpiresAt int64  `json:"mid_expires_at"`
			ExpiresAt    int64  `json:"expires_at"`
			Digest       string `json:"content_sha256"`
			BodyHex      string `json:"body_hex"`
		} `json:"submission"`
		Expect hubmsg.Answer `json:"expect"`
	} `json:"steps"`
}

// LoadInboxCases reads the release's inbox cases.
func LoadInboxCases(t testing.TB) []InboxCase {
	t.Helper()
	var f struct {
		SkewMs      int64       `json:"skew_ms"`
		RetentionMs int64       `json:"retention_after_expiry_ms"`
		Capacity    int         `json:"capacity_target"`
		Cases       []InboxCase `json:"cases"`
	}
	Fixture(t, "inbox_cases.json", &f)
	if f.SkewMs != hubmsg.SkewMs || f.RetentionMs != hubmsg.RetentionAfterExpiryMs || f.Capacity != hubmsg.Capacity {
		t.Fatalf("bounds %d/%d/%d, the release has %d/%d/%d", hubmsg.SkewMs, hubmsg.RetentionAfterExpiryMs, hubmsg.Capacity, f.SkewMs, f.RetentionMs, f.Capacity)
	}
	if len(f.Cases) != 17 {
		t.Fatalf("%d cases, the release has 17", len(f.Cases))
	}
	return f.Cases
}

// RunInboxCases runs every case against a fresh inbox from open; commit runs one step.
func RunInboxCases(t testing.TB, cases []InboxCase, open func(t testing.TB) func(d hubmsg.Delivery, now time.Time, est bool, capacity int) (hubmsg.Answer, error)) {
	t.Helper()
	for _, c := range cases {
		commit := open(t)
		for i, s := range c.Steps {
			sub := s.Submission
			body, _ := hex.DecodeString(sub.BodyHex)
			d := hubmsg.Delivery{V: 1, OpID: sub.OpID, Mid: sub.Mid, From: sub.Origin, To: sub.Recipient, ContentType: sub.ContentType,
				MidExpiresAt: sub.MidExpiresAt, ExpiresAt: sub.ExpiresAt, ContentSHA256: sub.Digest, Body: string(body)}
			got, err := commit(d, time.UnixMilli(s.Now), s.ClockEstablished, c.Capacity)
			if err != nil {
				t.Fatalf("%s step %d: %v", c.Case, i+1, err)
			}
			got.MessageID = 0
			if got != s.Expect {
				t.Errorf("%s step %d: %+v, want %+v", c.Case, i+1, got, s.Expect)
			}
		}
	}
}
