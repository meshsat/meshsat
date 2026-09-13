package main

import (
	"testing"
	"time"
)

// radioSilentSinceConnect decides when the mesh ladder skips its soft rungs
// and cuts the hub port at once. [MESHSAT-850]
func TestRadioSilentSinceConnect(t *testing.T) {
	now := time.Date(2026, 9, 12, 13, 30, 8, 0, time.UTC)
	opened := now.Add(-30 * time.Second)
	cases := []struct {
		name        string
		connected   bool
		connectedAt time.Time
		lastFrame   time.Time
		fails       int
		want        bool
	}{
		{"handshake abandoned as silent", false, opened, opened.Add(-time.Hour), 1, true},
		{"not connected, open failed, never silent", false, opened, time.Time{}, 0, false},
		{"open 30 s, last frame from an earlier session", true, opened, opened.Add(-time.Minute), 0, true},
		{"open 30 s, never a frame", true, opened, time.Time{}, 0, true},
		{"open 30 s, frame after open", true, opened, opened.Add(time.Second), 0, false},
		{"open only 10 s", true, now.Add(-10 * time.Second), time.Time{}, 0, false},
		{"no session time", true, time.Time{}, time.Time{}, 0, false},
	}
	for _, c := range cases {
		if got := radioSilentSinceConnect(c.connected, c.connectedAt, c.lastFrame, c.fails, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
