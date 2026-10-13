// Package cell runs the MeshSat 2G cell inside the Bridge: when the cell's
// SDR (a Pluto Nano on the meshsat-cell image) is on USB, the Osmocom core
// and BTS (packaged in the Bridge image) are started as child processes
// with configs rendered here, the Pluto's USB network is set up, the test
// SIMs are provisioned in the HLR, and the radio keys up with a TX-log row
// written first. Nothing to configure on a kit: like a Meshtastic radio or
// a satellite modem, plugging the SDR in is the configuration. [MESHSAT-1578]
//
// The licence the cell runs under (an RDI experimental licence held by the
// owner) allows one GSM-1800 carrier in 1875.0-1879.9 MHz (ARFCN 861-885)
// at up to 200 mW ERP, and asks for every emission to be logged. Those
// terms are constants here: an ARFCN outside the block is refused, the TX
// settings are fixed to the bare Pluto's, and every key-up and key-down is
// a row in the TX log before and after the fact.
package cell

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Licence block: ARFCN 861-885 = 1875.0-1879.9 MHz downlink.
const (
	ARFCNMin     = 861
	ARFCNMax     = 885
	DefaultARFCN = 873 // 1877.4 MHz, the verified clear channel
	Band         = "DCS1800"
)

// Fixed ports on the loopback: the Osmocom VTYs and the SMPP server.
const (
	VTYBSC   = 4342 // not libosmovty's 4242: the Bridge's Reticulum TCP interface holds it
	VTYMSC   = 4254
	VTYHLR   = 4258
	SMPPPort = 2775
)

// Pluto USB network: the Pluto is 192.168.2.1 and expects the host at .10.
const (
	PlutoIP = "192.168.2.1"
	HostIP  = "192.168.2.10"
)

// FreqMHz is the downlink frequency of a DCS1800 ARFCN.
func FreqMHz(arfcn int) float64 { return 1805.2 + 0.2*float64(arfcn-512) }

// Subscriber is one SIM the HLR knows.
type Subscriber struct {
	IMSI   string
	MSISDN string
}

// DefaultSubscribers are the four test USIMs (card n = MSISDN n00n).
var DefaultSubscribers = []Subscriber{
	{"001010000000001", "1001"}, {"001010000000002", "2002"}, {"001010000000003", "3003"}, {"001010000000004", "4004"},
}

// Config is what the runner needs.
type Config struct {
	DataDir      string // persistent: hlr.db, sms.db, the rendered configs
	ARFCN        int
	AutoRadio    bool   // key the radio up when the SDR is adopted
	Licence      string // cited on every TX-log row (empty: "not set")
	Auth         string // "optional" (bench exception while the USIM keys are unknown) or "required"
	SMPPSystemID string
	SMPPPassword string // rendered into osmo-msc.cfg; at most 8 characters
	ShortCode    string
	Subscribers  []Subscriber
	Operator     string // the TX-log's operator column (the kit's hostname)
	BinDir       string // "" = PATH
}

// Validate applies the licence block and the SMPP limits.
func (c *Config) Validate() error {
	if c.ARFCN < ARFCNMin || c.ARFCN > ARFCNMax {
		return fmt.Errorf("cell: ARFCN %d is outside the licensed block %d-%d (1875.0-1879.9 MHz)", c.ARFCN, ARFCNMin, ARFCNMax)
	}
	if c.DataDir == "" {
		return fmt.Errorf("cell: data dir is required")
	}
	if c.Auth != "optional" && c.Auth != "required" {
		return fmt.Errorf("cell: auth must be optional or required, not %q", c.Auth)
	}
	if len(c.SMPPSystemID) == 0 || len(c.SMPPSystemID) > 15 {
		return fmt.Errorf("cell: smpp system id must be 1 to 15 characters")
	}
	if len(c.SMPPPassword) == 0 || len(c.SMPPPassword) > 8 {
		return fmt.Errorf("cell: smpp password must be 1 to 8 characters")
	}
	for _, r := range c.SMPPPassword {
		if r < 0x21 || r > 0x7e {
			return fmt.Errorf("cell: smpp password must be printable ASCII")
		}
	}
	for _, s := range c.Subscribers {
		if len(s.IMSI) < 6 || len(s.IMSI) > 15 || !digits(s.IMSI) || !digits(s.MSISDN) || s.MSISDN == "" {
			return fmt.Errorf("cell: subscriber %q/%q is not IMSI:MSISDN digits", s.IMSI, s.MSISDN)
		}
	}
	if c.Operator == "" {
		c.Operator, _ = os.Hostname()
	}
	return nil
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ParseSubscribers reads "IMSI:MSISDN,IMSI:MSISDN".
func ParseSubscribers(s string) ([]Subscriber, error) {
	var out []Subscriber
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		imsi, msisdn, ok := strings.Cut(part, ":")
		if !ok {
			return nil, fmt.Errorf("cell: subscriber %q is not IMSI:MSISDN", part)
		}
		out = append(out, Subscriber{IMSI: strings.TrimSpace(imsi), MSISDN: strings.TrimSpace(msisdn)})
	}
	return out, nil
}

// Render writes the seven Osmocom configs into DataDir/conf and returns
// that directory. osmo-msc.cfg holds the SMPP password and is 0600.
func (c *Config) Render() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	dir := filepath.Join(c.DataDir, "conf")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	files := map[string]string{
		"osmo-stp.cfg":     c.stp(),
		"osmo-hlr.cfg":     c.hlr(),
		"osmo-mgw.cfg":     c.mgw(),
		"osmo-msc.cfg":     c.msc(),
		"osmo-cbc.cfg":     c.cbc(),
		"osmo-bsc.cfg":     c.bsc(),
		"osmo-bts-trx.cfg": c.bts(),
	}
	for name, body := range files {
		mode := os.FileMode(0o644)
		if name == "osmo-msc.cfg" {
			mode = 0o600
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
			return "", err
		}
	}
	return dir, nil
}

const logHead = `log stderr
 logging color 0
 logging print category 1
 logging timestamp 0
 logging print level 1
 logging level set-all notice
`

func (c *Config) stp() string {
	return "! MeshSat cell: osmo-stp (the packaged default). Rendered by the Bridge.\n" + logHead +
		`line vty
 no login
 bind 127.0.0.1
cs7 instance 0
 xua rkm routing-key-allocation dynamic-permitted
 listen m3ua 2905
  accept-asp-connections dynamic-permitted
`
}

func (c *Config) hlr() string {
	return "! MeshSat cell: osmo-hlr. Only the SIMs the Bridge provisions exist. Rendered by the Bridge.\n" + logHead +
		` logging level linp error
line vty
 no login
 bind 127.0.0.1
ctrl
 bind 127.0.0.1
hlr
 gsup
  bind ip 127.0.0.1
 ussd route prefix *#100# internal own-msisdn
 ussd route prefix *#101# internal own-imsi
`
}

func (c *Config) mgw() string {
	return "! MeshSat cell: osmo-mgw (the packaged default). Rendered by the Bridge.\n" + logHead +
		`line vty
 no login
 bind 127.0.0.1
mgcp
  bind ip 127.0.0.1
  rtp port-range 4002 16001
  rtp bind-ip 127.0.0.1
  rtp ip-probing
  rtp ip-dscp 46
  bind port 2427
  number endpoints 512
  force-realloc 1
  rtcp-omit
  rtp-patch ssrc
  rtp-patch timestamp
`
}

func (c *Config) msc() string {
	return fmt.Sprintf("! MeshSat cell: osmo-msc, internal SMSC and SMPP server for the Bridge. Rendered by the Bridge.\n"+logHead+
		`line vty
 no login
 bind 127.0.0.1
network
 network country code 001
 mobile network code 01
 short name MeshSat
 long name MeshSat Test
 authentication %s
 encryption a5 0
 rrlp mode none
 mm info 1
 mgw 0
  remote-ip 127.0.0.1
  remote-port 2427
  local-port 2728
msc
 no assign-tmsi
 auth-tuple-max-reuse-count 3
 auth-tuple-reuse-on-error 1
smpp
 local-tcp-ip 127.0.0.1 %d
 policy closed
 no smpp-first
 esme %s
  password %s
  default-route
  no deliver-src-imsi
  no alert-notifications
  no osmocom-extensions
  no dcs-transparent
`, c.Auth, SMPPPort, c.SMPPSystemID, c.SMPPPassword)
}

func (c *Config) cbc() string {
	return "! MeshSat cell: osmo-cbc, the cell broadcast centre. Rendered by the Bridge.\n" + logHead +
		`line vty
 no login
 bind 127.0.0.1
cbc
 unknown-peers accept
 ecbe
  local-ip 127.0.0.1
  local-port 12345
 cbsp
  local-ip 127.0.0.1
  local-port 48049
 peer cbsp meshsat-bsc
  mode server
  remote-ip 127.0.0.1
  remote-port 48049
`
}

func (c *Config) bsc() string {
	var ts strings.Builder
	ts.WriteString("   timeslot 0\n    phys_chan_config CCCH+SDCCH4+CBCH\n    hopping enabled 0\n")
	for i := 1; i <= 7; i++ {
		fmt.Fprintf(&ts, "   timeslot %d\n    phys_chan_config TCH/F\n    hopping enabled 0\n", i)
	}
	return fmt.Sprintf("! MeshSat cell: osmo-bsc with the lockdown (classes 0-9 barred, 15 allowed, no emergency calls). Rendered by the Bridge.\n"+logHead+
		`line vty
 no login
 bind 127.0.0.1 %d
e1_input
 e1_line 0 driver ipa
network
 network country code 001
 mobile network code 01
 encryption a5 0
 neci 1
 paging any use tch 0
 handover 0
 timer net T3212 5
 mgw 0
  remote-ip 127.0.0.1
  remote-port 2427
  local-port 2727
 bts 0
  type osmo-bts
  band %s
  cell_identity 6969
  location_area_code 0x0001
  base_station_id_code 63
  ms max power 0
  rxlev access min 20
  cell reselection hysteresis 4
  radio-link-timeout 32
  channel allocator mode set-all ascending
  rach tx integer 9
  rach max transmission 7
  rach emergency call allowed 0
  rach access-control-class 0 barred
  rach access-control-class 1 barred
  rach access-control-class 2 barred
  rach access-control-class 3 barred
  rach access-control-class 4 barred
  rach access-control-class 5 barred
  rach access-control-class 6 barred
  rach access-control-class 7 barred
  rach access-control-class 8 barred
  rach access-control-class 9 barred
  rach access-control-class 15 allowed
  channel-description attach 1
  channel-description bs-pa-mfrms 5
  channel-description bs-ag-blks-res 1
  early-classmark-sending forbidden
  ipa unit-id 6969 0
  oml ipa stream-id 255 line 0
  codec-support fr
  gprs mode none
  trx 0
   rf_locked 0
   arfcn %d
   nominal power 23
   max_power_red 0
   rsl e1 tei 0
%smsc 0
 allow-emergency deny
bsc
 mid-call-timeout 0
cbc
 mode client
 client
  remote-ip 127.0.0.1
  remote-port 48049
`, VTYBSC, Band, c.ARFCN, ts.String())
}

func (c *Config) bts() string {
	return fmt.Sprintf("! MeshSat cell: osmo-bts-trx; osmo-trx-pluto runs on the Pluto over the USB network. Rendered by the Bridge.\n"+logHead+
		` logging level l1c error
 logging level l1p error
 logging level meas error
 logging level pag error
line vty
 no login
 bind 127.0.0.1
phy 0
 instance 0
 osmotrx ip local %s
 osmotrx ip remote %s
bts 0
 band 1800
 ipa unit-id 6969 0
 oml remote-ip 127.0.0.1
 gsmtap-remote-host 127.0.0.1
 gsmtap-sapi bcch
 gsmtap-sapi ccch
 gsmtap-sapi cbch
 trx 0
  phy 0 instance 0
`, HostIP, PlutoIP)
}
