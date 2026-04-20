package api

import "testing"

func TestParseP2PPeerInfo(t *testing.T) {
	info := `02:1a:2b:3c:4d:5e
pri_dev_type=10-0050F204-5
device_name=parallax-kit
manufacturer=
model_name=
model_number=
serial_number=
config_methods=0x0188
device_capability=0x25
group_capability=0x0
interface_addr=02:1a:2b:3c:4d:5f`
	var p WiFiP2PPeer
	p.Address = "02:1a:2b:3c:4d:5e"
	parseP2PPeerInfo(info, &p)
	if p.DeviceName != "parallax-kit" {
		t.Errorf("device_name: got %q", p.DeviceName)
	}
	if p.PriDevType != "10-0050F204-5" {
		t.Errorf("pri_dev_type: got %q", p.PriDevType)
	}
	if p.WPSMethods != "0x0188" {
		t.Errorf("config_methods: got %q", p.WPSMethods)
	}
	if p.DeviceType != "0x25" {
		t.Errorf("device_capability: got %q", p.DeviceType)
	}
}

func TestParentIfaceFromP2PGroup(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"p2p-wlx90de80f3a70b-0", "wlx90de80f3a70b"},
		{"p2p-wlan0-1", "wlan0"},
		{"p2p-wlan0-12", "wlan0"},
		{"p2p-wlx90de80f3a70b", "wlx90de80f3a70b"}, // no trailing index
		{"wlan0", ""},                              // not a p2p name
		{"", ""},
	}
	for _, c := range cases {
		if got := parentIfaceFromP2PGroup(c.in); got != c.want {
			t.Errorf("parentIfaceFromP2PGroup(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsTrailingNumericIndex(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"p2p-wlan0-0", true},
		{"p2p-wlx90de80f3a70b-0", true},
		{"p2p-wlan0-12", true},
		{"p2p-dev-wlan0", false},       // mgmt iface, non-numeric suffix
		{"p2p-wlx90de80f3a70b", false}, // no trailing index
		{"wlan0", false},
		{"", false},
		{"p2p-", false},
	}
	for _, c := range cases {
		if got := isTrailingNumericIndex(c.in); got != c.want {
			t.Errorf("isTrailingNumericIndex(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestIsValidMAC(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"02:1a:2b:3c:4d:5e", true},
		{"AA:BB:CC:DD:EE:FF", true},
		{"aa:bb:cc:dd:ee:ff", true},
		{"02-1a-2b-3c-4d-5e", false},    // dash sep not accepted here
		{"02:1a:2b:3c:4d", false},       // too short
		{"02:1a:2b:3c:4d:5e:7f", false}, // too long
		{"02:1a:2b:3c:4d:5g", false},    // non-hex
		{"", false},
	}
	for _, c := range cases {
		if got := isValidMAC(c.in); got != c.want {
			t.Errorf("isValidMAC(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
