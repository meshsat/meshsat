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
