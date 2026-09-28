package transport

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestIsModemManager(t *testing.T) {
	for port, want := range map[string]bool{"modemmanager": true, "ModemManager": true, "mm": true, "mm://": true, "modemmanager://0": true, "auto": false, "": false, "/dev/ttyUSB2": false, "supervisor": false} {
		if got := IsModemManager(port); got != want {
			t.Errorf("IsModemManager(%q) = %v, want %v", port, got, want)
		}
	}
}

func TestQualityToBars(t *testing.T) {
	for pct, want := range map[int]int{-5: 0, 0: 0, 1: 1, 20: 1, 21: 2, 40: 2, 59: 3, 60: 3, 79: 4, 80: 4, 81: 5, 100: 5, 250: 5} {
		if got := qualityToBars(pct); got != want {
			t.Errorf("qualityToBars(%d) = %d, want %d", pct, got, want)
		}
	}
}

func TestMMSIMState(t *testing.T) {
	cases := []struct {
		state  int
		reason uint32
		sim    dbus.ObjectPath
		want   string
	}{
		{mmStateFailed, mmFailedSimMissing, "/", "NOT_INSERTED"}, // the bench PinePhone Pro, 28 Sep 2026
		{mmStateFailed, mmFailedSimError, "/", "SIM_ERROR"},
		{mmStateLocked, 0, "/org/freedesktop/ModemManager1/SIM/0", "PIN_REQUIRED"},
		{mmStateRegistered, 0, "/org/freedesktop/ModemManager1/SIM/0", "READY"},
		{mmStateEnabled, 0, "/", "NOT_INSERTED"},
		{3, 0, "", "NOT_INSERTED"},
	}
	for _, c := range cases {
		if got := mmSIMState(c.state, c.reason, c.sim); got != c.want {
			t.Errorf("mmSIMState(%d, %d, %q) = %q, want %q", c.state, c.reason, c.sim, got, c.want)
		}
	}
}

func TestMMRegistration(t *testing.T) {
	for reg, want := range map[uint32]string{0: "not_registered", 1: "registered_home", 2: "searching", 3: "denied", 4: "unknown", 5: "registered_roaming", 6: "registered_home", 7: "registered_roaming", 9: "registered_roaming", 42: "not_registered"} {
		if got := mmRegistration(reg); got != want {
			t.Errorf("mmRegistration(%d) = %q, want %q", reg, got, want)
		}
	}
}

func TestMMTechName(t *testing.T) {
	for mask, want := range map[uint32]string{0: "", 1 << 1: "2G", 1<<3 | 1<<4: "2G", 1 << 5: "3G", 1 << 9: "3G", 1 << 14: "LTE", 1<<14 | 1<<5: "LTE", 1 << 15: "5G", 1<<15 | 1<<14: "5G"} {
		if got := mmTechName(mask); got != want {
			t.Errorf("mmTechName(%#x) = %q, want %q", mask, got, want)
		}
	}
}

func TestMMTimestamp(t *testing.T) {
	if got := mmTimestamp("2026-09-28T14:05:11+02:00"); got != "2026-09-28T12:05:11Z" {
		t.Errorf("RFC 3339 with offset: got %q", got)
	}
	if got := mmTimestamp("2026-09-28T12:05:11Z"); got != "2026-09-28T12:05:11Z" {
		t.Errorf("UTC: got %q", got)
	}
	if got := mmTimestamp("2026-09-28T14:05:11+02"); got != "2026-09-28T12:05:11Z" {
		t.Errorf("hour-only offset, as some modems print it: got %q", got)
	}
	if got := mmTimestamp("garbage"); len(got) != len("2026-09-28T12:05:11Z") {
		t.Errorf("unparseable falls back to now: got %q", got)
	}
}

func TestUnpackQuality(t *testing.T) {
	pct, recent, err := unpackQuality(dbus.MakeVariant([]interface{}{uint32(63), true}))
	if err != nil || pct != 63 || !recent {
		t.Errorf("unpackQuality: pct %d recent %v err %v", pct, recent, err)
	}
	if _, _, err := unpackQuality(dbus.MakeVariant("no")); err == nil {
		t.Error("a string is not a SignalQuality")
	}
}

func TestMMCellInfo(t *testing.T) {
	info := mmCellInfo(map[string]dbus.Variant{
		"cell-type":   dbus.MakeVariant(uint32(5)),
		"serving":     dbus.MakeVariant(true),
		"operator-id": dbus.MakeVariant("20408"),
		"tac":         dbus.MakeVariant("1A2B"),
		"ci":          dbus.MakeVariant("00F1C2D3"),
		"rsrp":        dbus.MakeVariant(-98.4),
		"rsrq":        dbus.MakeVariant(-11.0),
	})
	if info.NetworkType != "LTE" || info.MCC != "204" || info.MNC != "08" || info.LAC != "1A2B" || info.CellID != "00F1C2D3" {
		t.Errorf("mmCellInfo: %+v", info)
	}
	if info.RSRP == nil || *info.RSRP != -98 || info.RSRQ == nil || *info.RSRQ != -11 {
		t.Errorf("mmCellInfo rsrp/rsrq: %+v", info)
	}
}

func TestMMFailedNameAndState(t *testing.T) {
	if mmFailedName(mmFailedSimMissing) != "no SIM in this modem" {
		t.Error("sim-missing wording")
	}
	if mmStateName(-1) != "failed" || mmStateName(8) != "registered" || mmStateName(99) != "99" {
		t.Error("state names")
	}
}

func TestMMCellTransportImplementsCellTransport(t *testing.T) {
	var _ CellTransport = NewMMCellTransport()
}
