package cell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func baseConfig(t *testing.T) Config {
	t.Helper()
	return Config{DataDir: t.TempDir(), ARFCN: DefaultARFCN, AutoRadio: true, Auth: "optional",
		SMPPSystemID: "meshsat-bridge", SMPPPassword: "pw12345", ShortCode: "1000", Subscribers: DefaultSubscribers, Operator: "kit-test"}
}

// The licence block is a constant: an ARFCN outside 861-885 never renders.
func TestConfigLicenceGuard(t *testing.T) {
	for _, a := range []int{860, 886, 4, 979, 0} {
		c := baseConfig(t)
		c.ARFCN = a
		if _, err := c.Render(); err == nil || !strings.Contains(err.Error(), "licensed") {
			t.Fatalf("ARFCN %d rendered: %v", a, err)
		}
	}
	for _, a := range []int{861, 873, 885} {
		c := baseConfig(t)
		c.ARFCN = a
		if _, err := c.Render(); err != nil {
			t.Fatalf("ARFCN %d refused: %v", a, err)
		}
	}
	if FreqMHz(873) != 1877.4 || FreqMHz(861) != 1875.0 {
		t.Fatalf("frequency %v %v", FreqMHz(873), FreqMHz(861))
	}
	c := baseConfig(t)
	c.SMPPPassword = "ninechars"
	if err := c.Validate(); err == nil {
		t.Fatal("a 9-character password must be refused")
	}
	c = baseConfig(t)
	c.Auth = "sometimes"
	if err := c.Validate(); err == nil {
		t.Fatal("auth must be optional or required")
	}
	subs, err := ParseSubscribers(" 001010000000009:9009, 001010000000008:8008 ")
	if err != nil || len(subs) != 2 || subs[1].MSISDN != "8008" {
		t.Fatalf("%v %v", subs, err)
	}
	if _, err := ParseSubscribers("nope"); err == nil {
		t.Fatal("IMSI:MSISDN required")
	}
}

func TestRenderedConfigs(t *testing.T) {
	c := baseConfig(t)
	dir, err := c.Render()
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	bsc := read("osmo-bsc.cfg")
	for _, want := range []string{"band DCS1800", "arfcn 873", "nominal power 23", "max_power_red 0", "bind 127.0.0.1 4342",
		"rach access-control-class 9 barred", "rach access-control-class 15 allowed", "rach emergency call allowed 0",
		"phys_chan_config CCCH+SDCCH4+CBCH", "gprs mode none", "allow-emergency deny"} {
		if !strings.Contains(bsc, want) {
			t.Errorf("osmo-bsc.cfg lacks %q", want)
		}
	}
	if strings.Count(bsc, "phys_chan_config TCH/F") != 7 {
		t.Error("seven TCH/F timeslots")
	}
	msc := read("osmo-msc.cfg")
	for _, want := range []string{"local-tcp-ip 127.0.0.1 2775", "policy closed", "esme meshsat-bridge", "password pw12345",
		"default-route", "no alert-notifications", "no dcs-transparent", "authentication optional", "no assign-tmsi"} {
		if !strings.Contains(msc, want) {
			t.Errorf("osmo-msc.cfg lacks %q", want)
		}
	}
	st, _ := os.Stat(filepath.Join(dir, "osmo-msc.cfg"))
	if st.Mode().Perm() != 0o600 {
		t.Errorf("osmo-msc.cfg holds the password and must be 0600, is %o", st.Mode().Perm())
	}
	bts := read("osmo-bts-trx.cfg")
	for _, want := range []string{"band 1800", "osmotrx ip local 192.168.2.10", "osmotrx ip remote 192.168.2.1", "oml remote-ip 127.0.0.1"} {
		if !strings.Contains(bts, want) {
			t.Errorf("osmo-bts-trx.cfg lacks %q", want)
		}
	}
	if !strings.Contains(read("osmo-cbc.cfg"), "unknown-peers accept") || !strings.Contains(read("osmo-stp.cfg"), "listen m3ua 2905") ||
		!strings.Contains(read("osmo-mgw.cfg"), "bind port 2427") || !strings.Contains(read("osmo-hlr.cfg"), "ussd route prefix *#100#") {
		t.Error("cbc, stp, mgw or hlr config")
	}
}

func TestParsers(t *testing.T) {
	bts := ParseBTS([]string{
		"BTS 0 is of osmo-bts type in band DCS1800, has CI 6969 LAC 1, BSIC 63 (NCC=7, BCC=7) and 1 TRX",
		"  Description: (null)", "  ARFCNs: 873", "  OML Link:", "  OML Link state: connected",
		"  Neighbor Cells: Automatic, ARFCNs: (none)", "  TRX 0", "    RSL link state: connected",
	})
	if bts.Band != "DCS1800" || bts.ARFCN != 873 || bts.OML != "connected" || bts.RSL != "connected" || len(bts.Raw) < 4 {
		t.Fatalf("%+v", bts)
	}
	phones := ParsePhones([]string{
		"    ID: 1", "    IMSI: 001010000000001", "    MSISDN: 1001", "    LAC: 1", "    Last seen: 2026-10-13 18:40:01",
		"    IMSI: 001010000000002", "    MSISDN: 2002",
	})
	if len(phones) != 2 || phones[0].MSISDN != "1001" || phones[0].LAC != 1 || phones[0].LastSeen == "" || phones[1].IMSI != "001010000000002" {
		t.Fatalf("%+v", phones)
	}
	if len(ParsePhones(nil)) != 0 {
		t.Fatal("no phones")
	}
	if vtyError([]string{"% No subscriber with IMSI x"}) == "" || vtyError([]string{"ok"}) != "" {
		t.Fatal("vty error detection")
	}
}

func TestFindPlutoInSysfs(t *testing.T) {
	root := t.TempDir()
	mk := func(dev, vid, pid string, iface string) {
		d := filepath.Join(root, dev)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "idVendor"), []byte(vid+"\n"), 0o644)
		os.WriteFile(filepath.Join(d, "idProduct"), []byte(pid+"\n"), 0o644)
		if iface != "" {
			os.MkdirAll(filepath.Join(d, dev+":1.0", "net", iface), 0o755)
		}
	}
	mk("1-1", "1a86", "7523", "")
	old := SysfsUSB
	SysfsUSB = root
	defer func() { SysfsUSB = old }()
	if p := FindPluto(); p.Present {
		t.Fatal("no Pluto yet")
	}
	mk("9-2", "0456", "b673", "enx00e022bde0dc")
	p := FindPluto()
	if !p.Present || p.State != "run" || p.USB != "9-2" || p.Iface != "enx00e022bde0dc" {
		t.Fatalf("%+v", p)
	}
	os.RemoveAll(filepath.Join(root, "9-2"))
	mk("9-3", "0456", "b674", "")
	if p := FindPluto(); !p.Present || p.State != "dfu" {
		t.Fatalf("dfu: %+v", p)
	}
}

type memTX struct {
	mu   sync.Mutex
	rows []TXRow
}

func (m *memTX) AppendTX(r TXRow) error {
	m.mu.Lock()
	m.rows = append(m.rows, r)
	m.mu.Unlock()
	return nil
}

// fakeDaemons puts sleeping stand-ins for the Osmocom daemons in a bin dir.
func fakeDaemons(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range append(append([]string{}, coreDaemons...), radioDaemon, "osmo-hlr-db-tool") {
		body := "#!/bin/sh\nexec sleep 300\n"
		if d == "osmo-hlr-db-tool" {
			body = "#!/bin/sh\ntouch \"$2\"\n"
		}
		if err := os.WriteFile(filepath.Join(dir, d), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The runner against fake daemons, a fake Pluto in sysfs, stubbed ip,
// SSH and VTY: core up, SIMs provisioned, the TX row before the radio,
// the closing row after, everything down when the SDR goes.
func TestRunnerLifecycle(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, "9-2")
	os.MkdirAll(filepath.Join(d, "9-2:1.0", "net", "usb0"), 0o755)
	os.WriteFile(filepath.Join(d, "idVendor"), []byte("0456"), 0o644)
	os.WriteFile(filepath.Join(d, "idProduct"), []byte("b673"), 0o644)
	oldSys, oldIP, oldSSH, oldVTY := SysfsUSB, ipCmd, plutoSSH, vtyFn
	SysfsUSB = root
	var ipCalls []string
	ipCmd = func(ctx context.Context, args ...string) error {
		ipCalls = append(ipCalls, strings.Join(args, " "))
		return nil
	}
	plutoSSH = func(ctx context.Context, command string) (string, error) {
		if strings.Contains(command, "/etc/meshsat-cell/build") {
			return "22c3733abcdef", nil
		}
		return "", nil
	}
	var vtyMu sync.Mutex
	var vtyCalls []string
	vtyFn = func(port int, command string) ([]string, error) {
		vtyMu.Lock()
		defer vtyMu.Unlock()
		vtyCalls = append(vtyCalls, command)
		if strings.Contains(command, "create") && strings.Contains(command, "001010000000001") {
			return []string{"% Subscriber already exists"}, nil
		}
		if strings.HasPrefix(command, "show bts") {
			return []string{"BTS 0 is of osmo-bts type in band DCS1800", "  ARFCNs: 873", "  OML Link state: connected"}, nil
		}
		if strings.HasPrefix(command, "show subscriber") {
			return []string{"    IMSI: 001010000000001", "    MSISDN: 1001"}, nil
		}
		return nil, nil
	}
	oldReach := reachableFn
	reachableFn = func(time.Duration) bool { return true }
	t.Cleanup(func() { SysfsUSB, ipCmd, plutoSSH, vtyFn, reachableFn = oldSys, oldIP, oldSSH, oldVTY, oldReach })

	cfg := baseConfig(t)
	cfg.BinDir = fakeDaemons(t)
	cfg.Subscribers = DefaultSubscribers[:2]
	cfg.Licence = "TEST-LICENCE"
	txlog := &memTX{}
	r, err := New(cfg, txlog)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := r.Up(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Down(ctx) })
	if len(ipCalls) != 3 || !strings.Contains(ipCalls[1], "192.168.2.10/32 dev usb0") || !strings.Contains(ipCalls[2], "192.168.2.1/32") {
		t.Fatalf("ip calls %v", ipCalls)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !r.RadioActive() {
		time.Sleep(50 * time.Millisecond)
	}
	st := r.Status()
	if !st.Core.Up || !st.Radio.Active || st.BTS == nil || st.BTS.OML != "connected" || len(st.Phones) != 1 || st.ARFCN[0] != 873 ||
		!st.SMPP.PasswordSet || st.SMPP.Port != SMPPPort || st.Mode != "bridge" || !st.AutoRadio || st.Pluto.Build != "22c3733abcde" {
		t.Fatalf("status %+v", st)
	}
	vtyMu.Lock()
	joined := strings.Join(vtyCalls, "|")
	vtyMu.Unlock()
	for _, want := range []string{"subscriber imsi 001010000000001 create", "update msisdn 1001", "subscriber imsi 001010000000002 create", "update msisdn 2002"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("hlr provisioning lacks %q in %s", want, joined)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "hlr.db")); err != nil {
		t.Fatal("hlr.db must be created before osmo-hlr starts")
	}
	txlog.mu.Lock()
	rows := append([]TXRow(nil), txlog.rows...)
	txlog.mu.Unlock()
	if len(rows) != 1 || rows[0].Event != "up" || rows[0].ARFCN != 873 || rows[0].FreqMHz != 1877.4 || rows[0].Licence != "TEST-LICENCE" ||
		!strings.Contains(rows[0].Purpose, "auto") || !strings.Contains(rows[0].Setting, "22c3733abcde") || rows[0].Operator != "kit-test" {
		t.Fatalf("tx rows %+v", rows)
	}

	// A second start is a no-op; a radio stop writes the closing row.
	if err := r.RadioStart(ctx, "again"); err != nil {
		t.Fatal(err)
	}
	if err := r.RadioStop(ctx); err != nil {
		t.Fatal(err)
	}
	if r.RadioActive() {
		t.Fatal("radio still active")
	}
	txlog.mu.Lock()
	n := len(txlog.rows)
	last := txlog.rows[len(txlog.rows)-1]
	txlog.mu.Unlock()
	if n != 2 || last.Event != "down" {
		t.Fatalf("closing row: %d rows, last %+v", n, last)
	}
	// A Pluto off the image: refused, logged as refused, no radio.
	plutoSSH = func(ctx context.Context, command string) (string, error) { return "", errors.New("no such file") }
	if err := r.RadioStart(ctx, "manual"); err == nil || !strings.Contains(err.Error(), "meshsat-cell image") {
		t.Fatalf("want the image refusal, got %v", err)
	}
	txlog.mu.Lock()
	last = txlog.rows[len(txlog.rows)-1]
	txlog.mu.Unlock()
	if last.Event != "refused" {
		t.Fatalf("refusal row %+v", last)
	}
	r.Down(ctx)
	if st := r.Status(); st.Core.Up || st.Radio.Active {
		t.Fatalf("after Down %+v", st)
	}
}

func TestRunnerWithoutPluto(t *testing.T) {
	old := SysfsUSB
	SysfsUSB = t.TempDir()
	defer func() { SysfsUSB = old }()
	cfg := baseConfig(t)
	cfg.BinDir = fakeDaemons(t)
	r, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Up(context.Background()); err == nil || !strings.Contains(err.Error(), "no Pluto") {
		t.Fatalf("want no Pluto, got %v", err)
	}
	if err := r.RadioStart(context.Background(), "x y z"); err == nil {
		t.Fatal("radio without the core")
	}
	if Available(cfg.BinDir) != true || Available(t.TempDir()) != false {
		t.Fatal("Available")
	}
}
