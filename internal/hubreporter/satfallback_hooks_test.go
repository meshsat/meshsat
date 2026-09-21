package hubreporter

import (
	"testing"
	"time"
)

// The reporter's connection hooks drive the fallback: a lost session records
// the disconnect time, a reconnect clears it and deactivates. [MESHSAT-963]
func TestSatFallback_HooksDriveState(t *testing.T) {
	var sent [][]byte
	sf := NewSatFallback(SatFallbackConfig{
		BridgeID:      "test",
		ActivateAfter: time.Millisecond,
		SendFn:        func(b []byte) error { sent = append(sent, b); return nil },
		PositionFn:    func() *Location { return &Location{Lat: 52.15, Lon: 4.49, Source: "gps"} },
		HealthFn:      func() BridgeHealth { return BridgeHealth{} },
	})
	r := &HubReporter{}
	r.SetConnectionHooks(sf.OnMQTTReconnect, sf.OnMQTTDisconnect)
	r.mu.Lock()
	onLost, onUp := r.onLost, r.onConnect
	r.mu.Unlock()

	onLost()
	sf.mu.Lock()
	dt := sf.disconnectTime
	sf.mu.Unlock()
	if dt.IsZero() {
		t.Fatal("disconnect not recorded through the hook")
	}
	onUp()
	sf.mu.Lock()
	dt, active := sf.disconnectTime, sf.active
	sf.mu.Unlock()
	if !dt.IsZero() || active {
		t.Fatalf("reconnect did not clear the state: dt=%v active=%v", dt, active)
	}
	// SOS goes out regardless of mode, as a decodable frame.
	if err := sf.PublishSOS("bridge", 52.15, 4.49, "help"); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || !IsSatUplink(sent[0]) {
		t.Fatalf("SOS frame not sent or not a sat uplink: %d", len(sent))
	}
	hdr, payload, err := DecodeSatUplink(sent[0])
	if err != nil || hdr.MsgType != SatMsgSOS {
		t.Fatalf("decode: %v type=%d", err, hdr.MsgType)
	}
	if id, dev, _, _, msg, _, err := DecodeSatSOS(payload); err != nil || id != "test" || dev != "bridge" || msg != "help" {
		t.Fatalf("sos fields: %v %s %s %s", err, id, dev, msg)
	}
}

// A kit that loses its Hub link must say it is alive as soon as the fallback
// arms, GPS or no GPS, and again on every new outage, however recent the last
// health frame was. The hourly health timer used to run across outages, so a
// second outage inside the hour armed and sent nothing. [MESHSAT-963]
func TestSatFallback_ArmingSendsHealthOnEveryOutage(t *testing.T) {
	var kinds []byte
	sf := NewSatFallback(SatFallbackConfig{
		BridgeID:      "nllei01tesseract01",
		ActivateAfter: 5 * time.Minute,
		SendFn: func(b []byte) error {
			hdr, _, err := DecodeSatUplink(b)
			if err != nil {
				t.Fatalf("undecodable frame: %v", err)
			}
			kinds = append(kinds, hdr.MsgType)
			return nil
		},
		PositionFn: func() *Location { return nil }, // no GPS fitted
		HealthFn:   func() BridgeHealth { return BridgeHealth{} },
	})
	var timers fallbackTimers
	t0 := time.Now()

	outage := func(start time.Time) {
		sf.OnMQTTDisconnect()
		sf.mu.Lock()
		sf.disconnectTime = start
		sf.mu.Unlock()
	}

	outage(t0)
	sf.step(t0.Add(4*time.Minute), &timers)
	if len(kinds) != 0 {
		t.Fatalf("sent %d frames before ActivateAfter", len(kinds))
	}
	sf.step(t0.Add(5*time.Minute+30*time.Second), &timers)
	if len(kinds) != 1 || kinds[0] != SatMsgHealthSummary {
		t.Fatalf("arming sent %v, want one health frame", kinds)
	}
	sf.step(t0.Add(6*time.Minute), &timers)
	if len(kinds) != 1 {
		t.Fatalf("health repeated inside its interval: %v", kinds)
	}

	// Link back, then a second outage twenty minutes later.
	sf.OnMQTTReconnect()
	t1 := t0.Add(20 * time.Minute)
	outage(t1)
	sf.step(t1.Add(5*time.Minute+30*time.Second), &timers)
	if len(kinds) != 2 || kinds[1] != SatMsgHealthSummary {
		t.Fatalf("second outage inside the hour sent %v, want a second health frame", kinds)
	}
}
