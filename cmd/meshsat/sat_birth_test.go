package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/hubreporter"
)

// A satellite gateway's birth entry carries its modem and declares its
// format: sealed for an encrypting chain, so the Hub keeps that modem's
// ciphertext opaque; plain otherwise, explicitly, so a chain changed to plain
// replaces an earlier sealed on the Hub.
func TestSatBirthEntryDeclaresASealingChain(t *testing.T) {
	base := hubreporter.InterfaceInfo{Name: "iridium_imt", Type: "iridium_imt", Status: "online"}
	cases := []struct {
		chain  string
		sealed bool
	}{
		{`[{"type":"smaz2g","params":{"dict":"meshtastic"}},{"type":"encrypt","params":{"key_ref":"iridium:shared"}}]`, true},
		{`[{"type":"smaz2"},{"type":"encrypt","params":{"key_ref":"iridium:shared"}},{"type":"base64"}]`, true},
		{`[{"type":"smaz2","params":{"dict":"meshtastic"}}]`, false},
		{`[]`, false},
		{``, false},
		{`not a chain`, true}, // unreadable: treated as sealing, as a send would be
	}
	for _, c := range cases {
		got := satBirthEntry(base, "300434060000001", c.chain)
		want := "plain"
		if c.sealed {
			want = "sealed"
		}
		if got.IMEI != "300434060000001" || got.EgressSealed != c.sealed || got.EgressFormat != want || got.Name != "iridium_imt" || got.Status != "online" {
			t.Fatalf("chain %q: %+v, want sealed=%v", c.chain, got, c.sealed)
		}
		b, _ := json.Marshal(got)
		if strings.Contains(string(b), `"egress_sealed"`) != c.sealed || !strings.Contains(string(b), `"egress_format":"`+want+`"`) {
			t.Fatalf("chain %q: birth JSON %s", c.chain, b)
		}
	}
}

// The birth is published again whenever the satellite modems change, the
// IMEI arriving after the connect included, and not on every tick.
func TestWatchSatModemsRepublishesOnChange(t *testing.T) {
	seq := []string{"", "", "iridium_imt=", "iridium_imt=300434060000001", "iridium_imt=300434060000001", "", "iridium_imt=300434060000001"}
	var mu sync.Mutex
	i, published := 0, 0
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		watchSatModems(ctx, time.Millisecond, func() string {
			mu.Lock()
			defer mu.Unlock()
			if i == len(seq) {
				cancel()
				return seq[len(seq)-1]
			}
			i++
			return seq[i-1]
		}, func() { mu.Lock(); published++; mu.Unlock() })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not stop")
	}
	// "" -> "iridium_imt=" -> the IMEI -> "" -> the IMEI: four changes.
	if published != 4 {
		t.Fatalf("published %d times, want 4", published)
	}
}
