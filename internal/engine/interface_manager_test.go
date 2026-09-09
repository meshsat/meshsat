package engine

import (
	"testing"

	"meshsat/internal/transport"
)

// TestInterfaceManager_ClassifyPort locks down the rule behind MESHSAT-815:
// the scan labels a port through the injected classifier (the
// DeviceSupervisor's claimed role) and otherwise through the VID:PID table,
// and never through a probe that opens the port.
func TestInterfaceManager_ClassifyPort(t *testing.T) {
	roles := map[string]transport.DeviceRole{
		"/dev/ttyUSB0": transport.RoleZigBee,
		"/dev/ttyACM2": transport.RoleCellular,
	}
	supervisorBacked := func(vidpid, port string) string {
		if t := transport.DeviceTypeForRole(roles[port]); t != "" {
			return t
		}
		return transport.ClassifyDevice(vidpid)
	}

	cases := []struct {
		name       string
		classifier func(vidpid, port string) string
		vidpid     string
		port       string
		want       string
	}{
		{"no classifier, unique vidpid", nil, "2886:0059", "/dev/ttyACM3", "meshtastic"},
		{"no classifier, shared vidpid stays ambiguous", nil, "10c4:ea60", "/dev/ttyUSB0", "ambiguous"},
		{"claimed zigbee port resolves by role", supervisorBacked, "10c4:ea60", "/dev/ttyUSB0", "zigbee"},
		{"claimed cellular port resolves by role", supervisorBacked, "1a86:55d4", "/dev/ttyACM2", "cellular"},
		{"unclaimed shared vidpid falls back to the table", supervisorBacked, "1a86:55d4", "/dev/ttyACM9", "ambiguous"},
		{"classifier returning empty falls back to the table", func(string, string) string { return "" }, "1546:01a7", "/dev/ttyACM0", "gps"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewInterfaceManager(nil)
			if tc.classifier != nil {
				m.SetPortClassifier(tc.classifier)
			}
			if got := m.classifyPort(tc.vidpid, tc.port); got != tc.want {
				t.Errorf("classifyPort(%q, %q) = %q, want %q", tc.vidpid, tc.port, got, tc.want)
			}
		})
	}
}
