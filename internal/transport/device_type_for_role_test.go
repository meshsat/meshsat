package transport

import "testing"

// TestDeviceTypeForRole pins the role-to-device-type vocabulary the
// InterfaceManager scan relies on instead of probing ports. [MESHSAT-815]
func TestDeviceTypeForRole(t *testing.T) {
	cases := []struct {
		role DeviceRole
		want string
	}{
		{RoleMeshtastic, "meshtastic"},
		{RoleIridium9603, "iridium"},
		{RoleIridium9704, "iridium"},
		{RoleCellular, "cellular"},
		{RoleZigBee, "zigbee"},
		{RoleGPS, "gps"},
		{RoleTetra, "tetra"},
		{RoleNone, ""},
		{DeviceRole("something-new"), ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.role), func(t *testing.T) {
			if got := DeviceTypeForRole(tc.role); got != tc.want {
				t.Errorf("DeviceTypeForRole(%q) = %q, want %q", tc.role, got, tc.want)
			}
		})
	}
}

// TestDeviceTypeForRole_AmbiguousVIDPIDStaysAmbiguousWithoutRole documents
// the contract: the shared VID:PIDs are only resolved through a claimed
// role, never by opening the port from the scan.
func TestDeviceTypeForRole_AmbiguousVIDPIDStaysAmbiguousWithoutRole(t *testing.T) {
	for vidpid := range ambiguousZigBeeVIDPIDs {
		if got := ClassifyDevice(vidpid); got != "ambiguous" {
			t.Errorf("ClassifyDevice(%q) = %q, want ambiguous", vidpid, got)
		}
	}
}

// TestTetraPEIClaimedByID: a Motorola TETRA radio answers a bare AT with OK,
// so it must be claimed by USB id before the 9603 AT probe ever sees it.
// [MESHSAT-1588]
func TestTetraPEIClaimedByID(t *testing.T) {
	s := &DeviceSupervisor{skipPorts: make(map[string]bool)}
	for _, vidpid := range []string{"0cad:9011", "0cad:9012", "0cad:9013", "0cad:9015", "0cad:9016"} {
		if got := s.classifyByVIDPID(vidpid, "/dev/ttyUSB0"); got != RoleTetra {
			t.Errorf("classifyByVIDPID(%q) = %q, want %q", vidpid, got, RoleTetra)
		}
		if got := ClassifyDevice(vidpid); got != "tetra" {
			t.Errorf("ClassifyDevice(%q) = %q, want tetra", vidpid, got)
		}
	}
}
