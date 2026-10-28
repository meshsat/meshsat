package gateway

import (
	"context"
	"testing"
	"time"

	mpb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshsat/internal/transport"
)

// MESHSAT-1736: a position event is stamped with the fix's own time, so a
// replay of an old fix is not a crew seen this minute on the Hub.
func TestAPositionEventCarriesTheFixTime(t *testing.T) {
	fix := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	ev := BuildEnrichedPositionEvent("meshsat-a1b3c2ec", "MSPA", PositionEnrichment{Lat: 52.1, Lon: 4.5, FixTime: fix}, 1800)
	if ev.Time != "2026-10-19T10:00:00Z" || ev.Start != ev.Time {
		t.Fatalf("time %s start %s", ev.Time, ev.Start)
	}
	if stale, _ := time.Parse(cotTimeFormat, ev.Stale); time.Until(stale) < 29*time.Minute {
		t.Fatalf("stale %s should be the export's, not the fix's", ev.Stale)
	}
	// No clock on the node: the moment of export, as before.
	ev = BuildEnrichedPositionEvent("meshsat-a1b3c2ec", "MSPA", PositionEnrichment{Lat: 52.1, Lon: 4.5}, 1800)
	if got, err := time.Parse(cotTimeFormat, ev.Time); err != nil || time.Since(got) > time.Minute {
		t.Fatalf("time %s (%v)", ev.Time, err)
	}
	// A fix stamped in the future (a node with a wrong clock) is not trusted.
	ev = BuildEnrichedPositionEvent("meshsat-a1b3c2ec", "MSPA", PositionEnrichment{Lat: 52.1, Lon: 4.5, FixTime: time.Now().Add(time.Hour)}, 1800)
	if got, _ := time.Parse(cotTimeFormat, ev.Time); time.Since(got) > time.Minute {
		t.Fatalf("a future fix time was used: %s", ev.Time)
	}
}

// positionMessage is a POSITION_APP packet as the mesh transport hands it to
// the rules: the payload with or without the fix's own time, and rx_time as
// the radio stamped it (0 when nothing did). The node id is made up.
func positionMessage(t *testing.T, from uint32, rxTime int64, fixTime uint32) *transport.MeshMessage {
	t.Helper()
	lat, lon := int32(521000000), int32(45000000)
	payload, err := proto.Marshal(&mpb.Position{LatitudeI: &lat, LongitudeI: &lon, Time: fixTime})
	if err != nil {
		t.Fatal(err)
	}
	return &transport.MeshMessage{
		From: from, To: 0xffffffff, ID: uint32(rxTime) ^ from,
		PortNum: transport.PortNumPosition, PortNumName: "POSITION_APP",
		RxTime: rxTime, RawPayload: payload,
	}
}

// hubOnlyTAK is a running TAK gateway whose only output is a connected Hub.
func hubOnlyTAK(t *testing.T, staleSec int, hooks TAKHooks) (*TAKGateway, *fakeTAKHub) {
	t.Helper()
	hub := &fakeTAKHub{connected: true}
	gw := NewTAKGateway(TAKConfig{CallsignPrefix: "MESHSAT", CotStaleSec: staleSec, HubExport: true}, nil)
	hooks.Hub = hub
	gw.SetHooks(hooks)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gw.Stop() })
	return gw, hub
}

// MESHSAT-1750: a position event is dated by the fix's own time, else by when
// the kit heard the packet (its rx_time, else the node table's last-heard),
// never by the moment of export. One nothing dates is not exported.
func TestAPositionEventIsDatedByWhenTheKitHeardIt(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	const node = 0x0a0bc2ec
	cases := []struct {
		name    string
		rxTime  int64
		fixTime uint32
		heard   func(uint32) (time.Time, bool)
		want    time.Time // zero: not exported
	}{
		{"the fix's own time", now.Add(-time.Minute).Unix(), uint32(now.Add(-2 * time.Minute).Unix()), nil, now.Add(-2 * time.Minute)},
		{"no fix time: the packet's rx_time", now.Add(-time.Minute).Unix(), 0, nil, now.Add(-time.Minute)},
		{"a fix time in the future: the packet's rx_time", now.Add(-time.Minute).Unix(), uint32(now.Add(time.Hour).Unix()), nil, now.Add(-time.Minute)},
		{"no fix time, no rx_time: the node table's last-heard", 0, 0, func(n uint32) (time.Time, bool) {
			return now.Add(-90 * time.Second), n == node
		}, now.Add(-90 * time.Second)},
		{"no fix time, no rx_time, not in the node table: not exported", 0, 0, func(uint32) (time.Time, bool) {
			return time.Time{}, false
		}, time.Time{}},
		{"nothing dates it: not exported", 0, 0, nil, time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gw, hub := hubOnlyTAK(t, 1800, TAKHooks{NodeHeardAt: tc.heard})
			if err := gw.Forward(context.Background(), positionMessage(t, node, tc.rxTime, tc.fixTime)); err != nil {
				t.Fatalf("forward: %v", err)
			}
			if tc.want.IsZero() {
				time.Sleep(150 * time.Millisecond)
				if n := len(hub.events(t)); n != 0 || gw.Status().MessagesOut != 0 {
					t.Fatalf("exported: %d documents, %d out", n, gw.Status().MessagesOut)
				}
				return
			}
			ev := hub.waitFor(t, "the position", func(ev CotEvent) bool { return ev.Type == CotEventTypePosition })
			if want := tc.want.UTC().Format(cotTimeFormat); ev.Time != want || ev.Start != want {
				t.Fatalf("time %s start %s, want %s", ev.Time, ev.Start, want)
			}
			if stale, _ := time.Parse(cotTimeFormat, ev.Stale); time.Until(stale) < 29*time.Minute {
				t.Fatalf("stale %s should be the export's", ev.Stale)
			}
		})
	}
}

// MESHSAT-1750: after each config download the radio replays every node in
// its NodeDB as a position packet with rx_time = the node's last_heard and
// the fix's own time often 0 (Meshtastic 2.8 PhoneAPI::makeReplayPositionPacket;
// the transport's TestHandlePacket_ReplayedPositionKeepsTheRadiosRxTime pins
// what reaches the rules). One for a node heard longer ago than
// cot_stale_seconds is not exported on any output: not the TAK server, not SA
// multicast, not the Hub. One heard within it is, dated by its rx_time.
func TestAStalePositionReplayIsNotExportedOnAnyOutput(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	cases := []struct {
		name     string
		rxTime   int64
		exported bool
	}{
		{"a node last heard on 19 Sep (the replay after a Bridge restart)", time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC).Unix(), false},
		{"a node last heard 31 minutes ago", now.Add(-31 * time.Minute).Unix(), false},
		{"a node last heard 10 minutes ago", now.Add(-10 * time.Minute).Unix(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newMockTAKServer(t)
			defer srv.close()
			udp, udpAddr := saListener(t)
			hub := &fakeTAKHub{connected: true}
			cfg := TAKConfig{CallsignPrefix: "MESHSAT", CotStaleSec: 1800, Multicast: true, HubExport: true}
			cfg.Host, cfg.Port = splitHostPort(t, srv.addr())
			gw := NewTAKGateway(cfg, nil)
			gw.mcastDst = udpAddr
			gw.SetHooks(TAKHooks{Hub: hub})
			if err := gw.Start(context.Background()); err != nil {
				t.Fatalf("start: %v", err)
			}
			defer gw.Stop()

			msg := positionMessage(t, 0x0a0bc2ec, tc.rxTime, 0)
			if err := gw.Forward(context.Background(), msg); err != nil {
				t.Fatalf("forward: %v", err)
			}
			if !tc.exported {
				time.Sleep(200 * time.Millisecond)
				if _, got := readSA(t, udp, 200*time.Millisecond); got {
					t.Error("SA multicast carried the stale position")
				}
				if n := len(srv.events()); n != 0 {
					t.Errorf("the TAK server got %d events", n)
				}
				if n := len(hub.events(t)); n != 0 {
					t.Errorf("the Hub got %d documents", n)
				}
				if st := gw.Status(); st.MessagesOut != 0 || st.Errors != 0 {
					t.Errorf("out %d errors %d", st.MessagesOut, st.Errors)
				}
				return
			}
			want := time.Unix(tc.rxTime, 0).UTC().Format(cotTimeFormat)
			ev := hub.waitFor(t, "the position", func(ev CotEvent) bool { return ev.Type == CotEventTypePosition })
			if ev.Time != want {
				t.Errorf("the Hub's copy is dated %s, want the rx_time %s", ev.Time, want)
			}
			if frame, got := readSA(t, udp, 2*time.Second); !got {
				t.Error("no SA multicast")
			} else if ev := decodeSA(t, frame); ev.Type != CotEventTypePosition {
				t.Errorf("SA: type %q", ev.Type)
			}
			deadline := time.Now().Add(3 * time.Second)
			for len(srv.events()) == 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if evs := srv.events(); len(evs) == 0 || evs[0].Time != want {
				t.Errorf("the TAK server's copy: %+v, want one dated %s", evs, want)
			}
		})
	}
}
