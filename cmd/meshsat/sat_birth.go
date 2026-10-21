package main

import (
	"context"
	"strings"
	"time"

	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/hubreporter"
)

// satModem names the running satellite gateway of a type: its modem's IMEI
// and the interface its messages leave through. ifaceID is "" for any other
// gateway type or when that gateway is not running.
func satModem(m *gateway.Manager, gwType string) (imei, ifaceID string) {
	switch gwType {
	case "iridium_imt":
		if gw := m.GetIMTGateway(); gw != nil {
			return gw.IMEI(), "iridium_imt_0"
		}
	case "iridium":
		if gw := m.GetSBDGateway(); gw != nil {
			return gw.IMEI(), "iridium_0"
		}
	}
	return "", ""
}

// satBirthEntry is a satellite gateway's birth entry: the modem's IMEI, which
// lets the Hub find the bridge that owns it, and whether the interface's send
// chain seals every payload. The Hub keeps such a modem's versioned payloads
// opaque instead of guessing text in them (HeMB-NG S3-2), and reads the
// payloads of a modem without the declaration as before.
func satBirthEntry(info hubreporter.InterfaceInfo, imei, egressChain string) hubreporter.InterfaceInfo {
	info.IMEI = imei
	info.EgressSealed = engine.ChainEncrypts(egressChain)
	return info
}

// satModemsKey names the running satellite gateways' modems, "type=imei"
// each, in a fixed order.
func satModemsKey(m *gateway.Manager) string {
	var parts []string
	for _, typ := range []string{"iridium_imt", "iridium"} {
		if imei, id := satModem(m, typ); id != "" {
			parts = append(parts, typ+"="+imei)
		}
	}
	return strings.Join(parts, ",")
}

// watchSatModems republishes the birth when the satellite modems key names
// change. The birth goes out at the Hub connect, which comes before a 9704
// has told its IMEI (2 s later on parallax, 21 Oct 2026), and the Hub ties a
// modem to its bridge, and a sealing declaration to the modem, by that IMEI.
func watchSatModems(ctx context.Context, every time.Duration, key func() string, republish func()) {
	last := ""
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if cur := key(); cur != last {
			last = cur
			republish()
		}
	}
}
