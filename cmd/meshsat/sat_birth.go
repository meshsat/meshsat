package main

import (
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
