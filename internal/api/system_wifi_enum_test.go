package api

import "testing"

func TestParseDefaultRouteDev(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "standard brcmfmac output",
			in:   "default via 192.168.181.1 dev wlan0 proto dhcp src 192.168.181.210 metric 600",
			want: "wlan0",
		},
		{
			name: "usb iface",
			in:   "default via 10.0.0.1 dev wlx90de80f3a70b proto dhcp",
			want: "wlx90de80f3a70b",
		},
		{
			name: "multiple lines — first dev wins",
			in:   "default via 1.1.1.1 dev eth0\ndefault via 2.2.2.2 dev wlan0",
			want: "eth0",
		},
		{
			name: "no default route",
			in:   "",
			want: "",
		},
		{
			name: "dev at end of line",
			in:   "default via 1.2.3.4 dev wlan0",
			want: "wlan0",
		},
		{
			name: "malformed — dev is last token",
			in:   "default via 1.2.3.4 dev",
			want: "",
		},
	}
	for _, c := range cases {
		if got := parseDefaultRouteDev(c.in); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestLooksLikeWiFi(t *testing.T) {
	// The sysNet path isn't used for the wl-prefix shortcut branch.
	if !looksLikeWiFi("/nope", "wlan0") {
		t.Error("wlan0 should look like WiFi by prefix")
	}
	if !looksLikeWiFi("/nope", "wlx90de80f3a70b") {
		t.Error("wlx* should look like WiFi by prefix")
	}
	if looksLikeWiFi("/nope", "eth0") {
		t.Error("eth0 must not match")
	}
	if looksLikeWiFi("/nope", "lo") {
		t.Error("lo must not match")
	}
}
