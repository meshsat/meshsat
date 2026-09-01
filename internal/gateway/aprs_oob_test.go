package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"meshsat/internal/transport"
)

// TestAPRSIntegration_DirectedMessage verifies that a delivery row with a
// destination is sent as a proper APRS message addressed to that station
// (`:ADDRESSEE:text`) instead of the third-party position report, which is
// what an OOB management reply needs. [MESHSAT-756]
func TestAPRSIntegration_DirectedMessage(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()

	host, port := splitHostPort(t, tnc.addr())
	cfg := APRSConfig{
		KISSHost:         host,
		KISSPort:         port,
		Callsign:         "TEST",
		SSID:             10,
		FrequencyMHz:     144.800,
		ExternalDirewolf: true,
	}
	gw := NewAPRSGateway(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()

	const frameText = "MS:9W899JR000002098WTQ26XJ7V4DYYXQ28AEY1BVR"
	msg := &transport.MeshMessage{
		PortNum:     1,
		DecodedText: frameText,
		Destination: "PD0XYZ-7",
		RawText:     true,
	}
	if err := gw.Forward(ctx, msg); err != nil {
		t.Fatalf("forward: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	frames := tnc.frames()
	if len(frames) == 0 {
		t.Fatal("expected a KISS frame, got 0")
	}
	ax25, err := DecodeAX25Frame(frames[0])
	if err != nil {
		t.Fatalf("decode AX.25: %v", err)
	}
	info := string(ax25.Info)
	if !strings.HasPrefix(info, ":PD0XYZ-7 :"+frameText) {
		t.Fatalf("info field %q, want a directed message to PD0XYZ-7 carrying the frame verbatim", info)
	}
	if strings.Contains(info, "[MeshSat") {
		t.Fatalf("directed message must not carry the third-party attribution: %q", info)
	}
	if len(info) > 67+11 { // 9-char addressee plus two colons is the APRS message envelope
		t.Fatalf("info field too long for an APRS message: %d", len(info))
	}
	if ax25.Src.Call != "TEST" || ax25.Src.SSID != 10 {
		t.Fatalf("src %s-%d", ax25.Src.Call, ax25.Src.SSID)
	}
}
