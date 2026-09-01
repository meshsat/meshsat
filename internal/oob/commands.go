package oob

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Command codes. See spec section 6.
const (
	CmdPing      byte = 0x01
	CmdReboot    byte = 0x02
	CmdRestart   byte = 0x03
	CmdReset     byte = 0x04
	CmdBearer    byte = 0x05
	CmdLog       byte = 0x06
	CmdStatusNet byte = 0x07
)

// Peer roles.
const (
	RoleReadonly = "readonly"
	RoleControl  = "control"
)

// Command describes one allowlisted command.
type Command struct {
	Code    byte   `json:"code"`
	Name    string `json:"name"`
	MinRole string `json:"min_role"`
	MinArgs int    `json:"min_args"`
	MaxArgs int    `json:"max_args"`
}

// Commands is the fixed allowlist. Nothing outside it is ever executed.
var Commands = []Command{
	{CmdPing, "PING", RoleReadonly, 0, 0},
	{CmdReboot, "REBOOT", RoleControl, 0, 2},
	{CmdRestart, "RESTART", RoleControl, 0, 0},
	{CmdReset, "RESET", RoleControl, 2, 2},
	{CmdBearer, "BEARER", RoleControl, 2, 2},
	{CmdLog, "LOG", RoleReadonly, 1, 2},
	{CmdStatusNet, "STATUS-NET", RoleReadonly, 0, 0},
}

// CommandByCode looks a command up by wire code.
func CommandByCode(code byte) (Command, bool) {
	for _, c := range Commands {
		if c.Code == code {
			return c, true
		}
	}
	return Command{}, false
}

// CommandByName looks a command up by name, case-insensitively, accepting
// "STATUS_NET" and "status-net" alike.
func CommandByName(name string) (Command, bool) {
	n := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(name), "_", "-"))
	for _, c := range Commands {
		if c.Name == n {
			return c, true
		}
	}
	return Command{}, false
}

// RoleAllows reports whether a peer with role may run cmd.
func RoleAllows(role string, cmd Command) bool {
	if cmd.MinRole == RoleReadonly {
		return role == RoleReadonly || role == RoleControl
	}
	return role == RoleControl
}

// ResultCode is the first byte of every reply's args.
type ResultCode byte

// Result codes. See spec section 6.
const (
	RCOK           ResultCode = 0
	RCDenied       ResultCode = 1
	RCBadArgs      ResultCode = 2
	RCUnavailable  ResultCode = 3
	RCRefused      ResultCode = 4
	RCAgentError   ResultCode = 5
	RCUnknownCmd   ResultCode = 6
	RCBusy         ResultCode = 7
	RCKeyExhausted ResultCode = 8
)

func (rc ResultCode) String() string {
	switch rc {
	case RCOK:
		return "ok"
	case RCDenied:
		return "denied"
	case RCBadArgs:
		return "bad_args"
	case RCUnavailable:
		return "unavailable"
	case RCRefused:
		return "refused"
	case RCAgentError:
		return "agent_error"
	case RCUnknownCmd:
		return "unknown_cmd"
	case RCBusy:
		return "busy"
	case RCKeyExhausted:
		return "key_exhausted"
	}
	return fmt.Sprintf("rc%d", byte(rc))
}

// Result is what the executor returns for one command.
type Result struct {
	Code     ResultCode
	Body     string
	FollowUp bool // send an unsolicited STATUS-NET style reply 30 s later
}

// Argument limits.
const (
	DefaultRebootDelay = 10
	MaxRebootDelay     = 3600
	DefaultLogLines    = 10
	MaxLogLines        = 20
	MinLevel           = 1
	MaxLevel           = 3
)

var errArgs = errors.New("oob: bad args")

// EncodeRebootArgs encodes a REBOOT delay in seconds.
func EncodeRebootArgs(delay uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, delay)
	return b
}

// ParseRebootArgs returns the delay; absent args mean the default, values
// above the maximum are clamped.
func ParseRebootArgs(args []byte) (uint16, error) {
	switch len(args) {
	case 0:
		return DefaultRebootDelay, nil
	case 2:
		d := binary.BigEndian.Uint16(args)
		return min(d, MaxRebootDelay), nil
	}
	return 0, errArgs
}

// EncodeResetArgs encodes a RESET target and level.
func EncodeResetArgs(target, level byte) []byte { return []byte{target, level} }

// ParseResetArgs validates a RESET request.
func ParseResetArgs(args []byte) (target, level byte, err error) {
	if len(args) != 2 {
		return 0, 0, errArgs
	}
	if args[1] < MinLevel || args[1] > MaxLevel {
		return 0, 0, errArgs
	}
	return args[0], args[1], nil
}

// EncodeBearerArgs encodes a BEARER target and state (0 off, 1 on).
func EncodeBearerArgs(target, state byte) []byte { return []byte{target, state} }

// ParseBearerArgs validates a BEARER request.
func ParseBearerArgs(args []byte) (target, state byte, err error) {
	if len(args) != 2 || args[1] > 1 {
		return 0, 0, errArgs
	}
	return args[0], args[1], nil
}

// EncodeLogArgs encodes a LOG unit index and line count.
func EncodeLogArgs(unit, lines byte) []byte { return []byte{unit, lines} }

// ParseLogArgs validates a LOG request; a missing line count means the
// default and values are clamped to 1..MaxLogLines.
func ParseLogArgs(args []byte) (unit, lines byte, err error) {
	switch len(args) {
	case 1:
		return args[0], DefaultLogLines, nil
	case 2:
		l := min(max(args[1], 1), MaxLogLines)
		return args[0], l, nil
	}
	return 0, 0, errArgs
}

// ReplyHeaderLen is the fixed prefix of every reply's args.
const ReplyHeaderLen = 5

// ReplyArgs is the decoded args of a reply frame.
type ReplyArgs struct {
	RC           ResultCode
	ReqCounterLo uint16 // low 16 bits of the request counter
	Seq          byte   // chunk number, 1-based
	Total        byte   // chunk count
	Body         []byte
}

// EncodeReplyArgs lays out [rc:1][req_counter_lo16:2][seq:1][total:1][body].
func EncodeReplyArgs(r ReplyArgs) []byte {
	out := make([]byte, ReplyHeaderLen, ReplyHeaderLen+len(r.Body))
	out[0] = byte(r.RC)
	binary.BigEndian.PutUint16(out[1:3], r.ReqCounterLo)
	out[3] = r.Seq
	out[4] = r.Total
	return append(out, r.Body...)
}

// ParseReplyArgs decodes a reply's args.
func ParseReplyArgs(args []byte) (ReplyArgs, error) {
	if len(args) < ReplyHeaderLen {
		return ReplyArgs{}, errArgs
	}
	return ReplyArgs{
		RC:           ResultCode(args[0]),
		ReqCounterLo: binary.BigEndian.Uint16(args[1:3]),
		Seq:          args[3],
		Total:        args[4],
		Body:         append([]byte{}, args[ReplyHeaderLen:]...),
	}, nil
}

// ArgSpec is the operator-facing (API, Hub) form of command arguments.
// Names, never bytes, cross those surfaces; BuildArgs turns them into the
// wire form.
type ArgSpec struct {
	Delay  int    `json:"delay,omitempty"`  // REBOOT seconds
	Target string `json:"target,omitempty"` // RESET, BEARER target name
	Level  int    `json:"level,omitempty"`  // RESET level 1..3
	State  string `json:"state,omitempty"`  // BEARER on|off
	Unit   string `json:"unit,omitempty"`   // LOG unit name
	Lines  int    `json:"lines,omitempty"`  // LOG line count
}

// BuildArgs encodes an ArgSpec for a command.
func BuildArgs(cmd byte, a ArgSpec) ([]byte, error) {
	switch cmd {
	case CmdPing, CmdRestart, CmdStatusNet:
		return nil, nil
	case CmdReboot:
		d := a.Delay
		if d <= 0 {
			d = DefaultRebootDelay
		}
		d = min(d, MaxRebootDelay)
		return EncodeRebootArgs(uint16(d)), nil
	case CmdReset:
		t, ok := TargetByName(a.Target)
		if !ok {
			return nil, fmt.Errorf("oob: unknown target %q", a.Target)
		}
		level := a.Level
		if level == 0 {
			level = int(LevelSoft)
		}
		if level < MinLevel || level > MaxLevel {
			return nil, fmt.Errorf("oob: level must be %d..%d", MinLevel, MaxLevel)
		}
		return EncodeResetArgs(t.Code, byte(level)), nil
	case CmdBearer:
		t, ok := TargetByName(a.Target)
		if !ok || t.IfaceID == "" {
			return nil, fmt.Errorf("oob: %q is not a bearer target", a.Target)
		}
		switch strings.ToLower(strings.TrimSpace(a.State)) {
		case "on", "1", "true", "up":
			return EncodeBearerArgs(t.Code, 1), nil
		case "off", "0", "false", "down":
			return EncodeBearerArgs(t.Code, 0), nil
		}
		return nil, fmt.Errorf("oob: state must be on or off")
	case CmdLog:
		idx, ok := LogUnitIndex(strings.TrimSpace(a.Unit))
		if !ok {
			return nil, fmt.Errorf("oob: unknown log unit %q", a.Unit)
		}
		lines := a.Lines
		if lines <= 0 {
			lines = DefaultLogLines
		}
		lines = min(lines, MaxLogLines)
		return EncodeLogArgs(idx, byte(lines)), nil
	}
	return nil, errors.New("oob: unknown command")
}

// LogUnits is the fixed table of journal units LOG may read, indexed by the
// unit byte. It mirrors the host agent's allowlist; the agent is the second
// gate.
var LogUnits = []string{
	"meshsat-oob-agent",
	"docker",
	"netplan-wpa-wlan0",
	"systemd-networkd",
	"x1202-monitor",
	"meshsat-mgmt-keepalive",
	"meshsat-p2p-link",
	"bluetooth",
}

// LogUnitByIndex resolves a unit byte.
func LogUnitByIndex(i byte) (string, bool) {
	if int(i) >= len(LogUnits) {
		return "", false
	}
	return LogUnits[i], true
}

// LogUnitIndex resolves a unit name to its byte.
func LogUnitIndex(name string) (byte, bool) {
	for i, u := range LogUnits {
		if u == name {
			return byte(i), true
		}
	}
	return 0, false
}
