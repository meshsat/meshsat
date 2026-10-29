package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The body of github.com/meshsat/meshsat/issues/4: a callsign that carries
// a line break, so the rest of it lands in direwolf.conf as a SPEECH
// directive, which Direwolf runs with system() while parsing. [MESHSAT-1754]
const issue4Body = `{"enabled":true,"config":{"mode":"kiss","callsign":"N0CALL\nSPEECH /usr/bin/id>/tmp/meshsat-rce;#\n#","external_direwolf":false,"audio_card":"null"}}`

func TestAPRSConfigValidate_RefusesDirewolfDirectiveInjection(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{"issue 4 callsign", `{"mode":"kiss","callsign":"N0CALL\nSPEECH /usr/bin/id>/tmp/meshsat-rce;#\n#","audio_card":"null"}`, "callsign must be 1-6"},
		{"callsign CR", `{"mode":"kiss","callsign":"N0CALL\rSPEECH id"}`, "callsign must be 1-6"},
		{"callsign hash", `{"mode":"kiss","callsign":"N0CALL#"}`, "callsign must be 1-6"},
		{"callsign with SSID inline", `{"mode":"kiss","callsign":"N0CALL-10"}`, "callsign must be 1-6"},
		{"audio card LF", `{"mode":"kiss","callsign":"N0CALL","audio_card":"null,0\nSPEECH /usr/bin/id>/tmp/x;#"}`, "audio_card:"},
		{"audio card space", `{"mode":"kiss","callsign":"N0CALL","audio_card":"null SPEECH id"}`, "audio_card:"},
		{"ptt device LF", `{"mode":"kiss","callsign":"N0CALL","ptt_line":"RTS","ptt_device":"/dev/ttyUSB0\nSPEECH /usr/bin/id;#"}`, "ptt_device:"},
		{"ptt line LF", `{"mode":"kiss","callsign":"N0CALL","ptt_line":"RTS\nSPEECH id","ptt_device":"/dev/ttyUSB0"}`, "ptt_line:"},
		{"external direwolf still checked", `{"mode":"kiss","callsign":"N0CALL","external_direwolf":true,"audio_card":"x\nSPEECH id"}`, "audio_card:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseAPRSConfig(tc.json)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			err = cfg.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %s", tc.json)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name the field: want %q", err, tc.want)
			}
		})
	}
}

// The issue's full PUT body parses the way handlePutGateway sees it: the
// gateway manager validates the inner config and must refuse it.
func TestAPRSConfigValidate_Issue4BodyRefused(t *testing.T) {
	start := strings.Index(issue4Body, `"config":`)
	inner := issue4Body[start+len(`"config":`) : len(issue4Body)-1]
	cfg, err := ParseAPRSConfig(inner)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("the issue 4 body passed Validate")
	}
}

// What the two kits store today and what an operator types must keep
// passing: six letters, the SSID in its own field, a serial KISS TNC path,
// an ALSA card name and a serial PTT device.
func TestAPRSConfigValidate_KitAndOperatorConfigsPass(t *testing.T) {
	for _, js := range []string{
		`{"mode":"kiss","callsign":"MSTSRT","ssid":10,"kiss_device":"/dev/serial/by-id/usb-Silicon_Labs_CP2102N_USB_to_UART_Bridge_Controller_36414a145c57ed11aeab52ca5720eef3-if00-port0","kiss_baud":115200,"audio_card":"AllInOneCable","ptt_device":"","ptt_line":"","external_direwolf":false,"beacon_text":""}`,
		`{"mode":"kiss","callsign":"MSPRLX","ssid":10,"kiss_device":"/dev/serial/by-id/usb-Silicon_Labs_CP2102N_USB_to_UART_Bridge_Controller_1cb04e3ba957ed11928b4aca5720eef3-if00-port0","kiss_baud":115200,"audio_card":"AllInOneCable"}`,
		`{"mode":"kiss","callsign":"PA3XYZ","ssid":7,"audio_card":"plughw:CARD=Device,DEV=0"}`,
		`{"mode":"kiss","callsign":"N0CALL","ptt_line":"RTS","ptt_device":"/dev/ttyACM1"}`,
		`{"mode":"kiss","callsign":"n0call"}`,
	} {
		cfg, err := ParseAPRSConfig(js)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate refused a legitimate config %s: %v", js, err)
		}
	}
}

func TestDirewolfTokenOK(t *testing.T) {
	for _, ok := range []string{"", "AllInOneCable", "/dev/ttyACM1", "plughw:CARD=Device,DEV=0", "N0CALL", "a-b_c.d"} {
		if !direwolfTokenOK(ok) {
			t.Errorf("direwolfTokenOK(%q) = false", ok)
		}
	}
	for _, bad := range []string{"a\nb", "a\rb", "a b", "a\tb", "a#b", `a"b`, "a'b", "a;b", "a>b", "a$b", "é", strings.Repeat("a", 129)} {
		if direwolfTokenOK(bad) {
			t.Errorf("direwolfTokenOK(%q) = true", bad)
		}
	}
}

// writeDirewolfConf is the last gate before the file exists: a config that
// bypassed Validate must still not reach the disk.
func TestWriteDirewolfConf_RefusesInjectedValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "direwolf.conf")
	for _, cfg := range []APRSConfig{
		{Callsign: "N0CALL\nSPEECH /usr/bin/id>/tmp/meshsat-rce;#\n#", SSID: 10, AudioCard: "null", ModemBaud: 1200, KISSPort: 8001},
		{Callsign: "N0CALL", AudioCard: "null,0\nSPEECH id", ModemBaud: 1200, KISSPort: 8001},
		{Callsign: "N0CALL", AudioCard: "x", PTTLine: "RTS", PTTDevice: "/dev/x\nSPEECH id", ModemBaud: 1200, KISSPort: 8001},
	} {
		err := writeDirewolfConf(path, cfg)
		if err == nil {
			t.Fatalf("writeDirewolfConf accepted %+v", cfg)
		}
		if !strings.Contains(err.Error(), "refusing to write direwolf.conf") {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("direwolf.conf was written despite the refusal: %v", statErr)
		}
		if _, statErr := os.Stat(path + ".tmp"); !os.IsNotExist(statErr) {
			t.Fatalf("direwolf.conf.tmp was left behind: %v", statErr)
		}
	}
	// And the honest config still renders.
	good := APRSConfig{Callsign: "MSTSRT", SSID: 10, AudioCard: "AllInOneCable", ModemBaud: 1200, KISSPort: 8001}
	if err := writeDirewolfConf(path, good); err != nil {
		t.Fatalf("writeDirewolfConf refused a legitimate config: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "MYCALL MSTSRT-10") {
		t.Fatalf("rendered conf lacks MYCALL:\n%s", b)
	}
}
