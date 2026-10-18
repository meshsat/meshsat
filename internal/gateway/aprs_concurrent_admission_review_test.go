package gateway

// The closure review of 20443a7 (17 Oct 2026 20:48 CEST), its added case
// as supplied: 64 concurrent arrivals against the gateway's own queue while
// it is full, after it drains, and while it is full again. [MESHSAT-1584]

import (
	"sync"
	"testing"
)

func reviewConcurrentAdmissions(g *APRSGateway, msg InboundMessage, id string, encrypted bool) map[rxAdmission]int {
	start := make(chan struct{})
	out := make(chan rxAdmission, 64)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out <- g.admitInbound(msg, id, encrypted)
		}()
	}
	close(start)
	wg.Wait()
	close(out)
	counts := map[rxAdmission]int{}
	for result := range out {
		counts[result]++
	}
	return counts
}

func TestReviewCumulative4ConcurrentQueueRecovery(t *testing.T) {
	cases := []struct {
		name      string
		id        string
		encrypted bool
	}{
		{name: "plain_with_id", id: "RX001"},
		{name: "plain_without_id"},
		{name: "encrypted_without_id", encrypted: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewAPRSGateway(APRSConfig{Callsign: "TEST", SSID: 10}, nil)
			msg := InboundMessage{Text: "concurrent target", Source: "aprs", FromAddr: "PA3ENC-7"}
			reviewFillInbound(g)
			rejected := reviewConcurrentAdmissions(g, msg, tc.id, tc.encrypted)
			if rejected[rxRejected] != 64 || g.msgsIn.Load() != 0 || g.rxDuplicates.Load() != 0 {
				t.Fatalf("full queue: results=%v admitted=%d duplicates=%d", rejected, g.msgsIn.Load(), g.rxDuplicates.Load())
			}
			reviewDrainInbound(g)
			recovered := reviewConcurrentAdmissions(g, msg, tc.id, tc.encrypted)
			queued := reviewDrainInbound(g)
			if recovered[rxAdmitted] != 1 || recovered[rxCopy] != 63 || len(queued) != 1 || queued[0].Text != msg.Text || g.msgsIn.Load() != 1 || g.rxDuplicates.Load() != 63 {
				t.Fatalf("recovered: results=%v queue=%v admitted=%d duplicates=%d", recovered, queued, g.msgsIn.Load(), g.rxDuplicates.Load())
			}
			reviewFillInbound(g)
			copies := reviewConcurrentAdmissions(g, msg, tc.id, tc.encrypted)
			queued = reviewDrainInbound(g)
			if copies[rxCopy] != 64 || g.msgsIn.Load() != 1 || g.rxDuplicates.Load() != 127 || len(queued) != cap(g.inCh) {
				t.Fatalf("known copies while full: results=%v queue=%d admitted=%d duplicates=%d", copies, len(queued), g.msgsIn.Load(), g.rxDuplicates.Load())
			}
			for _, entry := range queued {
				if entry.Source != "fixture" {
					t.Fatalf("known copy reached the full queue: %+v", entry)
				}
			}
			t.Log("full queue: 64 rejected, 0 duplicates; recovered: 1 admitted, 63 copies; full again: 64 copies, no additional admission")
		})
	}
}
