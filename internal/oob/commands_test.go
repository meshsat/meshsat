package oob

import (
	"bytes"
	"testing"
)

func TestCommandTable(t *testing.T) {
	cases := []struct {
		name    string
		lookup  string
		code    byte
		minRole string
	}{
		{"ping", "PING", CmdPing, RoleReadonly},
		{"reboot_lower", "reboot", CmdReboot, RoleControl},
		{"status_net_underscore", "status_net", CmdStatusNet, RoleReadonly},
		{"status_net_dash", "STATUS-NET", CmdStatusNet, RoleReadonly},
		{"log_padded", " log ", CmdLog, RoleReadonly},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd, ok := CommandByName(c.lookup)
			if !ok || cmd.Code != c.code || cmd.MinRole != c.minRole {
				t.Fatalf("lookup %q: ok=%v cmd=%+v", c.lookup, ok, cmd)
			}
			back, ok := CommandByCode(c.code)
			if !ok || back.Name != cmd.Name {
				t.Fatalf("code %#x: %+v", c.code, back)
			}
		})
	}
	if _, ok := CommandByName("SHELL"); ok {
		t.Fatal("SHELL must never resolve")
	}
	if _, ok := CommandByCode(0x7F); ok {
		t.Fatal("0x7F must not be a command")
	}
	ping, _ := CommandByCode(CmdPing)
	reboot, _ := CommandByCode(CmdReboot)
	if !RoleAllows(RoleReadonly, ping) || RoleAllows(RoleReadonly, reboot) || !RoleAllows(RoleControl, reboot) || RoleAllows("", ping) {
		t.Fatal("role matrix wrong")
	}
}

func TestArgCodecs(t *testing.T) {
	t.Run("reboot", func(t *testing.T) {
		if d, err := ParseRebootArgs(nil); err != nil || d != DefaultRebootDelay {
			t.Fatalf("default: %d %v", d, err)
		}
		if d, err := ParseRebootArgs(EncodeRebootArgs(15)); err != nil || d != 15 {
			t.Fatalf("15: %d %v", d, err)
		}
		if d, err := ParseRebootArgs(EncodeRebootArgs(9000)); err != nil || d != MaxRebootDelay {
			t.Fatalf("clamp: %d %v", d, err)
		}
		if _, err := ParseRebootArgs([]byte{1}); err == nil {
			t.Fatal("odd length accepted")
		}
	})
	t.Run("reset", func(t *testing.T) {
		tg, lv, err := ParseResetArgs(EncodeResetArgs(TargetMesh, LevelDevice))
		if err != nil || tg != TargetMesh || lv != LevelDevice {
			t.Fatalf("%d %d %v", tg, lv, err)
		}
		for _, bad := range [][]byte{nil, {TargetMesh}, {TargetMesh, 0}, {TargetMesh, 4}, {1, 2, 3}} {
			if _, _, err := ParseResetArgs(bad); err == nil {
				t.Fatalf("accepted %v", bad)
			}
		}
	})
	t.Run("bearer", func(t *testing.T) {
		tg, st, err := ParseBearerArgs(EncodeBearerArgs(TargetAPRS, 0))
		if err != nil || tg != TargetAPRS || st != 0 {
			t.Fatalf("%d %d %v", tg, st, err)
		}
		if _, _, err := ParseBearerArgs([]byte{TargetAPRS, 2}); err == nil {
			t.Fatal("state 2 accepted")
		}
	})
	t.Run("log", func(t *testing.T) {
		u, l, err := ParseLogArgs([]byte{1})
		if err != nil || u != 1 || l != DefaultLogLines {
			t.Fatalf("%d %d %v", u, l, err)
		}
		if _, l, _ := ParseLogArgs(EncodeLogArgs(1, 0)); l != 1 {
			t.Fatalf("clamp low: %d", l)
		}
		if _, l, _ := ParseLogArgs(EncodeLogArgs(1, 200)); l != MaxLogLines {
			t.Fatalf("clamp high: %d", l)
		}
		if _, _, err := ParseLogArgs(nil); err == nil {
			t.Fatal("empty accepted")
		}
		name, ok := LogUnitByIndex(1)
		if !ok || name != "docker" {
			t.Fatalf("unit 1: %q %v", name, ok)
		}
		if idx, ok := LogUnitIndex("docker"); !ok || idx != 1 {
			t.Fatalf("index docker: %d %v", idx, ok)
		}
		if _, ok := LogUnitByIndex(200); ok {
			t.Fatal("unit 200 resolved")
		}
	})
	t.Run("reply", func(t *testing.T) {
		in := ReplyArgs{RC: RCRefused, ReqCounterLo: 0xBEEF, Seq: 2, Total: 3, Body: []byte("x")}
		enc := EncodeReplyArgs(in)
		if len(enc) != ReplyHeaderLen+1 {
			t.Fatalf("len %d", len(enc))
		}
		out, err := ParseReplyArgs(enc)
		if err != nil || out.RC != in.RC || out.ReqCounterLo != in.ReqCounterLo || out.Seq != 2 || out.Total != 3 || !bytes.Equal(out.Body, in.Body) {
			t.Fatalf("%+v %v", out, err)
		}
		if _, err := ParseReplyArgs([]byte{0, 1}); err == nil {
			t.Fatal("short reply accepted")
		}
		if RCKeyExhausted.String() != "key_exhausted" || ResultCode(200).String() != "rc200" {
			t.Fatal("result code names")
		}
	})
}

func TestTargets(t *testing.T) {
	seen := map[byte]bool{}
	for _, tg := range Targets {
		if seen[tg.Code] {
			t.Fatalf("duplicate target code %#x", tg.Code)
		}
		seen[tg.Code] = true
		byName, ok := TargetByName(tg.Name)
		if !ok || byName.Code != tg.Code {
			t.Fatalf("by name %s", tg.Name)
		}
		byCode, ok := TargetByCode(tg.Code)
		if !ok || byCode.Name != tg.Name {
			t.Fatalf("by code %#x", tg.Code)
		}
		if tg.IfaceID != "" {
			byIface, ok := TargetByIface(tg.IfaceID)
			if !ok || byIface.Code != tg.Code {
				t.Fatalf("by iface %s", tg.IfaceID)
			}
		}
	}
	if _, ok := TargetByName("netplan"); ok {
		t.Fatal("netplan must never be a target")
	}
	aprs, _ := TargetByName("APRS")
	if aprs.IfaceID != "aprs_0" {
		t.Fatalf("aprs iface id %q (ax25_0 is the routing interface, not the gateway)", aprs.IfaceID)
	}
	wifi, _ := TargetByCode(TargetWiFi)
	if wifi.HostActions[LevelSoft] != "wifi_reassociate" || wifi.HostActions[LevelHard] != "" {
		t.Fatalf("wifi host actions %v", wifi.HostActions)
	}
	if a, arg := SplitHostAction("usb_rebind:aioc"); a != "usb_rebind" || arg != "aioc" {
		t.Fatalf("split %q %q", a, arg)
	}
	if a, arg := SplitHostAction("ping"); a != "ping" || arg != "" {
		t.Fatalf("split %q %q", a, arg)
	}
	if KindInterface.String() != "interface" || TargetKind(9).String() != "unknown" {
		t.Fatal("kind names")
	}
}
