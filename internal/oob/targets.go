package oob

import "strings"

// TargetKind classifies a reset target.
type TargetKind uint8

// Target kinds.
const (
	KindInterface TargetKind = iota + 1 // a gateway instance / transport
	KindDevice                          // a USB device without a gateway of its own
	KindHost                            // the host operating system
	KindProcess                         // the bridge process
)

func (k TargetKind) String() string {
	switch k {
	case KindInterface:
		return "interface"
	case KindDevice:
		return "device"
	case KindHost:
		return "host"
	case KindProcess:
		return "process"
	}
	return "unknown"
}

// Reset levels. See spec section 7.
const (
	LevelSoft   byte = 1 // in-process reconnect of the transport or gateway
	LevelDevice byte = 2 // a reset the device itself understands
	LevelHard   byte = 3 // USB re-enumeration, power pin cycle, host reboot
)

// Target codes.
const (
	TargetWiFi     byte = 0x01
	TargetUSBWiFi  byte = 0x02
	TargetCellular byte = 0x03
	TargetMesh     byte = 0x04
	TargetIridium  byte = 0x05
	TargetIMT      byte = 0x06
	TargetZigBee   byte = 0x07
	TargetBLE      byte = 0x08
	TargetAPRS     byte = 0x09
	TargetGPS      byte = 0x0A
	TargetRTLSDR   byte = 0x0B
	TargetBridge   byte = 0x7E
	TargetHost     byte = 0x7F
)

// ResetTarget is one row of the shared RESET / BEARER table. Bridge-side
// actions per level are registered at wiring time (Deps.Actions); host
// agent actions per level are static data here. Index HostActions by level.
// "usb_power_cycle:<dev>" cuts the device's hub port power when it sits on
// a per-port switchable hub and otherwise falls back to the agent's sysfs
// rebind, so a kit without such a hub behaves as before. [MESHSAT-786]
type ResetTarget struct {
	Code        byte                 `json:"code"`
	Name        string               `json:"name"`
	Kind        TargetKind           `json:"kind"`
	IfaceID     string               `json:"iface_id,omitempty"`
	FollowUp    bool                 `json:"follow_up"`
	HostActions [MaxLevel + 1]string `json:"-"`
}

// Targets is the fixed table. Names are what the API, UI and Hub use; bytes
// are what rides the wire.
var Targets = []ResetTarget{
	{Code: TargetWiFi, Name: "wifi", Kind: KindHost, FollowUp: true,
		HostActions: [MaxLevel + 1]string{"", "wifi_reassociate", "wifi_restart", ""}},
	{Code: TargetUSBWiFi, Name: "usb_wifi", Kind: KindDevice, FollowUp: true,
		HostActions: [MaxLevel + 1]string{"", "p2p_restart", "p2p_restart", "usb_power_cycle:usb_wifi"}},
	{Code: TargetCellular, Name: "cellular", Kind: KindInterface, IfaceID: "cellular_0"},
	{Code: TargetMesh, Name: "mesh", Kind: KindInterface, IfaceID: "mesh_0"},
	{Code: TargetIridium, Name: "iridium", Kind: KindInterface, IfaceID: "iridium_0"},
	{Code: TargetIMT, Name: "imt", Kind: KindInterface, IfaceID: "iridium_imt_0"},
	{Code: TargetZigBee, Name: "zigbee", Kind: KindInterface, IfaceID: "zigbee_0"},
	{Code: TargetBLE, Name: "ble", Kind: KindInterface, IfaceID: "ble_0",
		HostActions: [MaxLevel + 1]string{"", "", "", "service_restart:bluetooth"}},
	{Code: TargetAPRS, Name: "aprs", Kind: KindInterface, IfaceID: "aprs_0",
		HostActions: [MaxLevel + 1]string{"", "", "", "usb_power_cycle:aioc"}},
	{Code: TargetGPS, Name: "gps", Kind: KindDevice},
	{Code: TargetRTLSDR, Name: "rtl_sdr", Kind: KindDevice},
	{Code: TargetBridge, Name: "bridge", Kind: KindProcess},
	{Code: TargetHost, Name: "host", Kind: KindHost},
}

// TargetByCode looks a target up by wire code.
func TargetByCode(code byte) (ResetTarget, bool) {
	for _, t := range Targets {
		if t.Code == code {
			return t, true
		}
	}
	return ResetTarget{}, false
}

// TargetByName looks a target up by name, case-insensitively.
func TargetByName(name string) (ResetTarget, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, t := range Targets {
		if t.Name == n {
			return t, true
		}
	}
	return ResetTarget{}, false
}

// TargetByIface looks a target up by its gateway interface id.
func TargetByIface(ifaceID string) (ResetTarget, bool) {
	for _, t := range Targets {
		if t.IfaceID != "" && t.IfaceID == ifaceID {
			return t, true
		}
	}
	return ResetTarget{}, false
}

// SplitHostAction splits "usb_rebind:aioc" into ("usb_rebind", "aioc").
func SplitHostAction(s string) (action, arg string) {
	action, arg, _ = strings.Cut(s, ":")
	return action, arg
}
