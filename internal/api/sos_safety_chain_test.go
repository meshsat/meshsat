package api

import (
	"context"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/hubreporter"
	"meshsat/internal/transport"
)

// The safety chain, proven by tests: SIDN fonds project 261042, deliverable
// D2 [MESHSAT-1136]. The contract names four tests: the SOS carries the
// sender's position (1), and reaches the satellite on both modem types
// through the delivery ledger (2: SBD, 3: IMT); the dead man's switch runs
// in the shipped binary (4: cmd/meshsat/deadman_wiring_test.go). The modems
// are fakes that record what they are given: no satellite session opens.

const (
	chainLat = 52.1601
	chainLon = 4.497
)

// satelliteFrameOf decodes the SOS frame a satellite leg carries.
func satelliteFrameOf(t *testing.T, frame []byte) (lat, lon float64, words string) {
	t.Helper()
	header, payload, err := hubreporter.DecodeSatUplink(frame)
	if err != nil || header.MsgType != hubreporter.SatMsgSOS {
		t.Fatalf("not an SOS frame: %v", err)
	}
	_, _, lat, lon, words, _, err = hubreporter.DecodeSatSOS(payload)
	if err != nil {
		t.Fatal(err)
	}
	return lat, lon, words
}

func samePlace(lat, lon, wantLat, wantLon float64) bool {
	// float32 on the wire: about a metre.
	return math.Abs(lat-wantLat) < 1e-4 && math.Abs(lon-wantLon) < 1e-4
}

// queuedSatelliteFrame waits for the SOS's satellite leg and returns the
// ledger row and the frame it carries.
func queuedSatelliteFrame(t *testing.T, s *Server) (sosStatusAnswer, *database.MessageDelivery) {
	t.Helper()
	st := waitSOS(t, s, "the satellite leg queued", queued("satellite"))
	del, err := s.db.GetDelivery(legOf(st, "satellite").DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	return st, del
}

// 1. The SOS carries the sender's position: the one it was raised with;
// for the dead man's switch, the last position the Bridge knew when it has
// no GPS fix of its own; and when there is none at all, the frame's words
// say the position is unknown rather than send 0, 0 as a place.
func TestSafetyChain1_TheSOSCarriesTheSendersPosition(t *testing.T) {
	t.Run("the position the SOS was raised with", func(t *testing.T) {
		s := sosLegsServer(t, &apiSat{kind: "sbd", connected: true}, nil)
		t.Cleanup(stopSOS(s))
		w := post(t, s, "/api/sos/activate", `{"trigger":"hold","message":"`+sosWords+`","latitude":52.1601,"longitude":4.497}`)
		if w.Code != http.StatusOK {
			t.Fatalf("activate: %d %s", w.Code, w.Body.String())
		}
		_, del := queuedSatelliteFrame(t, s)
		lat, lon, words := satelliteFrameOf(t, del.Payload)
		if !samePlace(lat, lon, chainLat, chainLon) || strings.Contains(words, "position unknown") {
			t.Fatalf("frame says %v, %v (%q), want %v, %v", lat, lon, words, chainLat, chainLon)
		}
	})

	t.Run("the dead man's switch, with no GPS fix: its last known position", func(t *testing.T) {
		s := sosLegsServer(t, &apiSat{kind: "sbd", connected: true}, nil)
		t.Cleanup(stopSOS(s))
		if !s.TriggerSOSWithFallback("deadman", 52.1702, 4.4811) {
			t.Fatal("the SOS was refused")
		}
		_, del := queuedSatelliteFrame(t, s)
		lat, lon, words := satelliteFrameOf(t, del.Payload)
		if !samePlace(lat, lon, 52.1702, 4.4811) || strings.Contains(words, "position unknown") {
			t.Fatalf("frame says %v, %v (%q)", lat, lon, words)
		}
	})

	t.Run("no position anywhere: the words say so", func(t *testing.T) {
		s := sosLegsServer(t, &apiSat{kind: "sbd", connected: true}, nil)
		t.Cleanup(stopSOS(s))
		w := post(t, s, "/api/sos/activate", `{"trigger":"hold","message":"SOS: Kyriakos needs help."}`)
		if w.Code != http.StatusOK {
			t.Fatalf("activate: %d %s", w.Code, w.Body.String())
		}
		_, del := queuedSatelliteFrame(t, s)
		lat, lon, words := satelliteFrameOf(t, del.Payload)
		if lat != 0 || lon != 0 || !strings.Contains(words, "position unknown") {
			t.Fatalf("frame says %v, %v (%q), want 0, 0 and 'position unknown'", lat, lon, words)
		}
	})
}

// chainLiveServer is the Bridge with its delivery queue running and one
// satellite gateway: SBD on iridium_0 or IMT on iridium_imt_0, over modem.
func chainLiveServer(t *testing.T, kind string, modem transport.SatTransport) *Server {
	t.Helper()
	s := newTestServerWithDB(t)
	var mgr *gateway.Manager
	if kind == "sbd" {
		mgr = gateway.NewManager(s.db, modem)
	} else {
		mgr = gateway.NewManager(s.db, nil)
		mgr.SetIMTTransport(modem)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if kind == "sbd" {
		if err := mgr.ConfigureInstance(ctx, "iridium", "iridium_0", true, `{"mailbox_mode":"off","auto_receive":false}`); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := mgr.ConfigureInstance(ctx, "iridium_imt", "iridium_imt_0", true, `{}`); err != nil {
			t.Fatal(err)
		}
	}
	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	d := engine.NewDispatcher(s.db, reg, mgr, nil)
	d.Start(ctx)
	t.Cleanup(func() {
		cancel()
		d.Wait()
		mgr.Stop()
	})
	s.gwManager = mgr
	s.SetDispatcher(d)
	s.SetHubBridgeID("msa-flaneur")
	old := sosFollowEvery
	sosFollowEvery = 20 * time.Millisecond
	t.Cleanup(func() { sosFollowEvery = old })
	return s
}

// throughTheLedger raises an SOS with a position on a live Bridge and
// follows its satellite leg: one ledger row (sos-<id>-satellite, priority
// 0, tried until it goes) on the modem's link, the frame handed to the
// modem with the position in it, and the row marked sent.
func throughTheLedger(t *testing.T, kind, link string) {
	modem := &okSat{apiSat: &apiSat{kind: kind, connected: true}}
	s := chainLiveServer(t, kind, modem)
	t.Cleanup(stopSOS(s))
	w := post(t, s, "/api/sos/activate", `{"trigger":"hold","message":"`+sosWords+`","latitude":52.1601,"longitude":4.497}`)
	if w.Code != http.StatusOK {
		t.Fatalf("activate: %d %s", w.Code, w.Body.String())
	}
	st, del := queuedSatelliteFrame(t, s)
	if del.Channel != link || del.MsgRef != "sos-"+sosItoa(st.ID)+"-satellite" || del.Priority != 0 || del.MaxRetries != 0 ||
		del.Class != database.DeliveryClassHubUplink {
		t.Fatalf("the ledger row: channel %s ref %s priority %d max_retries %d class %s", del.Channel, del.MsgRef, del.Priority, del.MaxRetries, del.Class)
	}
	rows := 0
	for _, leg := range st.Legs {
		if leg.Route == "satellite" {
			rows++
		}
	}
	if rows != 1 {
		t.Fatalf("%d satellite legs, want one ledger row per SOS", rows)
	}

	deadline := time.Now().Add(10 * time.Second)
	var frame []byte
	for frame == nil {
		for _, f := range modem.sentFrames() {
			if hubreporter.IsSatSOS(f) {
				frame = f
			}
		}
		if frame == nil && time.Now().After(deadline) {
			t.Fatalf("the SOS frame never reached the %s modem (it got %d frames)", kind, len(modem.sentFrames()))
		}
		time.Sleep(20 * time.Millisecond)
	}
	lat, lon, _ := satelliteFrameOf(t, frame)
	if !samePlace(lat, lon, chainLat, chainLon) {
		t.Fatalf("the modem got %v, %v, want %v, %v", lat, lon, chainLat, chainLon)
	}
	for {
		got, err := s.db.GetDelivery(del.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "sent" || got.Status == "delivered" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ledger row is %q, want sent or delivered", got.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 2. The SOS reaches the SBD satellite modem (Iridium 9602/9603, iridium_0)
// through the delivery ledger.
func TestSafetyChain2_TheSOSReachesTheSBDModemThroughTheLedger(t *testing.T) {
	throughTheLedger(t, "sbd", "iridium_0")
}

// 3. The SOS reaches the IMT satellite modem (RockBLOCK 9704,
// iridium_imt_0) through the delivery ledger.
func TestSafetyChain3_TheSOSReachesTheIMTModemThroughTheLedger(t *testing.T) {
	throughTheLedger(t, "imt", "iridium_imt_0")
}
