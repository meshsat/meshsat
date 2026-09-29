package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshsat/internal/api"
	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/hubreporter"
	"meshsat/internal/transport"
)

// Of the satellite fallback's frames, only the SOS is queued at priority 0
// (Critical); all go at precedence Priority, class hub_uplink.
// [MESHSAT-1430]
func TestHubUplinkFrameOptions_OnlyTheSOSFrameIsCritical(t *testing.T) {
	now := time.Unix(1790000000, 0)
	for _, tc := range []struct {
		name     string
		frame    []byte
		critical bool
	}{
		{"SOS", hubreporter.EncodeSatSOS("nllei01tesseract01", "bridge", 52.1601, 4.4970, "SOS: Anna needs help.", now), true},
		{"position", hubreporter.EncodeSatPosition("nllei01tesseract01", 52.1601, 4.4970, 3.9, 1, now), false},
		{"health", hubreporter.HealthFrame("nllei01tesseract01", hubreporter.BridgeHealth{UptimeSec: 5311}, now), false},
		{"not a frame", []byte("SOS - EMERGENCY ALERT"), false},
	} {
		opts := hubUplinkFrameOptions(tc.frame)
		if opts.Critical != tc.critical || opts.Precedence != "Priority" || opts.Class != database.DeliveryClassHubUplink {
			t.Errorf("%s: %+v, want critical %v, Priority, hub_uplink", tc.name, opts, tc.critical)
		}
	}
}

// queueOnlyModem is a 9603 a gateway can run over while nothing is sent:
// every session fails.
type queueOnlyModem struct{}

func (queueOnlyModem) Subscribe(context.Context) (<-chan transport.SatEvent, error) {
	return nil, errors.New("no events here")
}
func (queueOnlyModem) Send(context.Context, []byte) (*transport.SatResult, error) {
	return nil, errors.New("nothing is sent here")
}
func (queueOnlyModem) SendText(context.Context, string) (*transport.SatResult, error) {
	return nil, errors.New("nothing is sent here")
}
func (queueOnlyModem) Receive(context.Context) ([]byte, error) { return nil, nil }
func (queueOnlyModem) MailboxCheck(context.Context) (*transport.SatResult, error) {
	return &transport.SatResult{NoSession: true}, nil
}
func (queueOnlyModem) GetSignal(context.Context) (*transport.SignalInfo, error) {
	return &transport.SignalInfo{}, nil
}
func (queueOnlyModem) GetSignalFast(context.Context) (*transport.SignalInfo, error) {
	return &transport.SignalInfo{}, nil
}
func (queueOnlyModem) GetStatus(context.Context) (*transport.SatStatus, error) {
	return &transport.SatStatus{Connected: true, Type: "sbd"}, nil
}
func (queueOnlyModem) GetFirmwareVersion(context.Context) (string, error) { return "TA19002", nil }
func (queueOnlyModem) Close() error                                       { return nil }

// The SOS's Hub frame goes before an alarm test waiting on the same modem,
// and before the fallback's position frame: the SOS at Priority, priority
// 0, the position at Priority, 1, the test at Routine, 1. The test is the
// one POST /api/sos/test queues, the frames the ones the fallback's send
// queues. With the test at priority 0, as it first was, it went before the
// SOS. Over SMS the SOS frame is at priority 0 as well. [MESHSAT-1430]
func TestHubUplinkSender_TheSOSFrameGoesBeforeAnAlarmTest(t *testing.T) {
	db, err := database.New(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mgr := gateway.NewManager(db, queueOnlyModem{})
	ctx, cancel := context.WithCancel(context.Background())
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		mgr.Stop()
		cancel()
	})
	if err := mgr.ConfigureInstance(ctx, "iridium", "iridium_0", true, `{"mailbox_mode":"off","auto_receive":false}`); err != nil {
		t.Fatal(err)
	}
	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	d := engine.NewDispatcher(db, reg, mgr, nil) // never started: nothing is sent
	srv := api.NewServer(db, nil, nil, mgr)
	srv.SetDispatcher(d)
	srv.SetHubBridgeID("nllei01tesseract01")

	// The alarm test, minutes before the SOS.
	req := httptest.NewRequest(http.MethodPost, "/api/sos/test", strings.NewReader(`{"satellite":true,"latitude":52.1601,"longitude":4.4970}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"interface":"iridium_0"`) {
		t.Fatalf("the alarm test: %d %s", w.Code, w.Body.String())
	}

	status := func(id string) (bool, time.Time, bool) {
		gw := mgr.GatewayByInterfaceID(id)
		if gw == nil {
			return false, time.Time{}, false
		}
		st := gw.Status()
		return st.Connected, st.LastActivity, true
	}
	now := time.Now().UTC()
	position := hubreporter.EncodeSatPosition("nllei01tesseract01", 52.1601, 4.4970, 3.9, 1, now)
	sos := hubreporter.EncodeSatSOS("nllei01tesseract01", "bridge", 52.1601, 4.4970, "SOS: Anna needs help.", now)
	sat := hubUplinkSender(d, status, "satellite", "")
	for _, frame := range [][]byte{position, sos} {
		if err := sat(frame); err != nil {
			t.Fatal(err)
		}
	}

	kind := func(del database.MessageDelivery) string {
		if del.TextPreview == "Alarm test: position report to the Hub" {
			return "test"
		}
		switch hdr, _, err := hubreporter.DecodeSatUplink(del.Payload); {
		case err != nil:
			return "?"
		case hdr.MsgType == hubreporter.SatMsgSOS:
			return "sos"
		case hdr.MsgType == hubreporter.SatMsgPosition:
			return "position"
		}
		return "?"
	}
	pending, err := db.GetPendingDeliveries("iridium_0", 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, del := range pending {
		got = append(got, fmt.Sprintf("%s %s/%d", kind(del), del.Precedence, del.Priority))
	}
	want := []string{"sos Priority/0", "position Priority/1", "test Routine/1"}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Fatalf("the queue on iridium_0 is %v, want %v", got, want)
	}

	// Over SMS to the Hub's number: the same priorities.
	sms := hubUplinkSender(d, status, "sms", "+31612345678")
	for _, frame := range [][]byte{position, sos} {
		if err := sms(frame); err != nil {
			t.Fatal(err)
		}
	}
	pending, err = db.GetPendingDeliveries("cellular_0", 10)
	if err != nil || len(pending) != 2 {
		t.Fatalf("cellular_0: %+v (%v)", pending, err)
	}
	for i, frame := range [][]byte{sos, position} {
		del := pending[i]
		if del.TextPreview != base64.StdEncoding.EncodeToString(frame) || del.Destination != "+31612345678" ||
			del.Class != database.DeliveryClassHubUplink || del.Priority != i {
			t.Fatalf("SMS %d: preview %q, to %s, class %s, priority %d", i, del.TextPreview, del.Destination, del.Class, del.Priority)
		}
	}
	if err := hubUplinkSender(d, status, "sms", "")(sos); err == nil {
		t.Fatal("no Hub SMS number and no satellite: queued anyway")
	}
}
