package gateway

import (
	"strings"
	"testing"
)

// The status beacon is a plain APRS status frame from the kit's callsign,
// no digipeater path, readable by any receiver. [MESHSAT-857]
func TestAPRSBeaconFrame(t *testing.T) {
	g := &APRSGateway{config: APRSConfig{Callsign: "MSTSRT", SSID: 10, BeaconSecs: 90}}
	frame := g.beaconFrame(7)
	ax, err := DecodeAX25Frame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if FormatCallsign(ax.Src) != "MSTSRT-10" || ax.Dst.Call != "APMSHT" || len(ax.Path) != 0 {
		t.Fatalf("addresses: src=%s dst=%s path=%d", FormatCallsign(ax.Src), ax.Dst.Call, len(ax.Path))
	}
	if info := string(ax.Info); !strings.HasPrefix(info, ">MeshSat MSTSRT ok 7") {
		t.Fatalf("info %q", info)
	}
	g.config.BeaconText = "TTC booth kit A"
	if info := string(mustDecode(t, g.beaconFrame(8)).Info); info != ">TTC booth kit A 8" {
		t.Fatalf("custom text: %q", info)
	}
}

func mustDecode(t *testing.T, frame []byte) *AX25Frame {
	t.Helper()
	ax, err := DecodeAX25Frame(frame)
	if err != nil {
		t.Fatal(err)
	}
	return ax
}
