package oob

import (
	"bytes"
	"testing"
)

func TestBuildArgs(t *testing.T) {
	cases := []struct {
		name    string
		cmd     byte
		spec    ArgSpec
		want    []byte
		wantErr bool
	}{
		{"ping_no_args", CmdPing, ArgSpec{Delay: 99}, nil, false},
		{"status_net_no_args", CmdStatusNet, ArgSpec{}, nil, false},
		{"restart_no_args", CmdRestart, ArgSpec{}, nil, false},
		{"reboot_default", CmdReboot, ArgSpec{}, EncodeRebootArgs(DefaultRebootDelay), false},
		{"reboot_value", CmdReboot, ArgSpec{Delay: 15}, EncodeRebootArgs(15), false},
		{"reboot_clamped", CmdReboot, ArgSpec{Delay: 99999}, EncodeRebootArgs(MaxRebootDelay), false},
		{"reset_default_level", CmdReset, ArgSpec{Target: "mesh"}, EncodeResetArgs(TargetMesh, LevelSoft), false},
		{"reset_level_3", CmdReset, ArgSpec{Target: "Cellular", Level: 3}, EncodeResetArgs(TargetCellular, LevelHard), false},
		{"reset_bad_level", CmdReset, ArgSpec{Target: "mesh", Level: 4}, nil, true},
		{"reset_unknown_target", CmdReset, ArgSpec{Target: "netplan"}, nil, true},
		{"bearer_off", CmdBearer, ArgSpec{Target: "aprs", State: "off"}, EncodeBearerArgs(TargetAPRS, 0), false},
		{"bearer_on_alias", CmdBearer, ArgSpec{Target: "aprs", State: "UP"}, EncodeBearerArgs(TargetAPRS, 1), false},
		{"bearer_bad_state", CmdBearer, ArgSpec{Target: "aprs", State: "maybe"}, nil, true},
		{"bearer_not_a_bearer", CmdBearer, ArgSpec{Target: "wifi", State: "off"}, nil, true},
		{"log_default_lines", CmdLog, ArgSpec{Unit: "docker"}, EncodeLogArgs(1, DefaultLogLines), false},
		{"log_clamped", CmdLog, ArgSpec{Unit: "docker", Lines: 500}, EncodeLogArgs(1, MaxLogLines), false},
		{"log_unknown_unit", CmdLog, ArgSpec{Unit: "sshd"}, nil, true},
		{"unknown_cmd", 0x55, ArgSpec{}, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := BuildArgs(c.cmd, c.spec)
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, c.wantErr)
			}
			if !bytes.Equal(got, c.want) {
				t.Fatalf("args %x want %x", got, c.want)
			}
		})
	}
}
