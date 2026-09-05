package oob

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Origin describes who issued a command and over which bearer it arrived.
// PeerID 0 with Bearer "hub" is a Hub-originated command.
type Origin struct {
	PeerID   uint16
	Alias    string
	Role     string
	Bearer   string
	FromAddr string
}

// OriginHub is the bearer name for Hub-originated commands.
const OriginHub = "hub"

// RevertDelay is how long a self-severing BEARER off waits before the
// bridge brings the bearer back on its own.
const RevertDelay = 10 * time.Minute

// Action is a bridge-side reset action registered at wiring time.
type Action func(ctx context.Context) error

func (s *Service) peerLock(id uint16) *sync.Mutex {
	l, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	return l.(*sync.Mutex)
}

// otherBearerUp reports whether any bearer other than except is up.
func (s *Service) otherBearerUp(except string) bool {
	if s.d.BearersUp == nil {
		return false
	}
	for id, up := range s.d.BearersUp() {
		if up && id != except {
			return true
		}
	}
	return false
}

// severs reports whether a disruptive action on target would cut the
// bearer the command arrived on.
func severs(o Origin, t ResetTarget) bool {
	if o.Bearer == OriginHub {
		return false
	}
	return t.IfaceID != "" && t.IfaceID == o.Bearer
}

// Execute runs one command for an origin and returns the result. It never
// sends anything itself; the caller turns the result into a reply.
func (s *Service) Execute(ctx context.Context, o Origin, cmd byte, args []byte) Result {
	c, ok := CommandByCode(cmd)
	if !ok {
		return Result{Code: RCUnknownCmd}
	}
	if !RoleAllows(o.Role, c) {
		return Result{Code: RCDenied}
	}
	if len(args) < c.MinArgs || len(args) > c.MaxArgs {
		return Result{Code: RCBadArgs}
	}
	lock := s.peerLock(o.PeerID)
	if !lock.TryLock() {
		return Result{Code: RCBusy}
	}
	defer lock.Unlock()

	switch cmd {
	case CmdPing:
		return Result{Code: RCOK, Body: s.pingBody()}
	case CmdStatusNet:
		return Result{Code: RCOK, Body: s.statusNetBody(ctx)}
	case CmdLog:
		return s.execLog(ctx, args)
	case CmdReboot:
		return s.execReboot(ctx, o, args)
	case CmdRestart:
		return s.execRestart(o)
	case CmdReset:
		return s.execReset(ctx, o, args)
	case CmdBearer:
		return s.execBearer(ctx, o, args)
	}
	return Result{Code: RCUnknownCmd}
}

func (s *Service) execLog(ctx context.Context, args []byte) Result {
	unit, lines, err := ParseLogArgs(args)
	if err != nil {
		return Result{Code: RCBadArgs}
	}
	name, ok := LogUnitByIndex(unit)
	if !ok {
		return Result{Code: RCBadArgs, Body: "unit"}
	}
	if s.d.Host == nil || !s.d.Host.Available() {
		return Result{Code: RCUnavailable, Body: "no host agent"}
	}
	res, err := s.d.Host.Call(ctx, "journal_tail", map[string]any{"unit": name, "lines": int(lines)})
	if err != nil {
		return Result{Code: RCAgentError, Body: trim(err.Error(), 60)}
	}
	raw, _ := res["lines"].([]any)
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		line := fmt.Sprint(l)
		// Drop the ISO timestamp and host name; keep HH:MM:SS and the message.
		if i := strings.Index(line, " "); i > 0 && i < 40 {
			if len(line) > 11 && line[10] == 'T' {
				line = line[11:19] + line[i:]
			}
		}
		out = append(out, trim(line, 60))
	}
	return Result{Code: RCOK, Body: strings.Join(out, "\n")}
}

func (s *Service) execReboot(ctx context.Context, o Origin, args []byte) Result {
	delay, err := ParseRebootArgs(args)
	if err != nil {
		return Result{Code: RCBadArgs}
	}
	if o.Bearer != OriginHub && !s.otherBearerUp(o.Bearer) {
		return Result{Code: RCRefused, Body: "sole bearer"}
	}
	if s.d.Host == nil || !s.d.Host.Available() {
		return Result{Code: RCUnavailable, Body: "no host agent"}
	}
	// The ACK is queued first; the agent is asked after a short grace so
	// the reply has left the ledger before the host goes down.
	host := s.d.Host
	s.after(2*time.Second, func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := host.Call(cctx, "reboot", map[string]any{"delay": int(delay)}); err != nil {
			s.logf("oob: reboot request failed: %v", err)
		}
	})
	return Result{Code: RCOK, Body: fmt.Sprintf("rb%ds", delay)}
}

func (s *Service) execRestart(o Origin) Result {
	if o.Bearer != OriginHub && !s.otherBearerUp(o.Bearer) {
		return Result{Code: RCRefused, Body: "sole bearer"}
	}
	s.mu.RLock()
	restart := s.restart
	s.mu.RUnlock()
	if restart == nil {
		return Result{Code: RCUnavailable, Body: "no restart hook"}
	}
	s.after(2*time.Second, restart)
	return Result{Code: RCOK, Body: "rs2s"}
}

func (s *Service) execReset(ctx context.Context, o Origin, args []byte) Result {
	code, level, err := ParseResetArgs(args)
	if err != nil {
		return Result{Code: RCBadArgs}
	}
	t, ok := TargetByCode(code)
	if !ok {
		return Result{Code: RCBadArgs, Body: "target"}
	}
	switch t.Code {
	case TargetBridge:
		if level != LevelSoft {
			return Result{Code: RCUnavailable}
		}
		return s.execRestart(o)
	case TargetHost:
		if level != LevelHard {
			return Result{Code: RCUnavailable}
		}
		return s.execReboot(ctx, o, nil)
	}
	if level == LevelHard && severs(o, t) && !s.otherBearerUp(o.Bearer) {
		return Result{Code: RCRefused, Body: "sole bearer"}
	}

	name := fmt.Sprintf("%s L%d", t.Name, level)
	if act := s.action(t.Name, level); act != nil {
		if err := act(ctx); err != nil {
			return Result{Code: RCAgentError, Body: trim(name+" "+err.Error(), 60)}
		}
	} else if t.Kind == KindInterface && level == LevelSoft && t.IfaceID != "" && s.d.Gateways != nil {
		if err := s.restartGateway(ctx, t.IfaceID); err != nil {
			return Result{Code: RCUnavailable, Body: trim(name+" "+err.Error(), 60)}
		}
	} else if host := t.HostActions[level]; host != "" {
		if s.d.Host == nil || !s.d.Host.Available() {
			return Result{Code: RCUnavailable, Body: "no host agent"}
		}
		action, arg := SplitHostAction(host)
		hargs := map[string]any{}
		if arg != "" {
			hargs["device"] = arg
			hargs["unit"] = arg
		}
		if _, err := s.d.Host.Call(ctx, action, hargs); err != nil {
			return Result{Code: RCAgentError, Body: trim(err.Error(), 60)}
		}
	} else {
		return Result{Code: RCUnavailable, Body: name}
	}
	body := name + " ok"
	if level == LevelHard && t.Kind == KindInterface && t.IfaceID != "" && s.d.Gateways != nil {
		iface := t.IfaceID
		s.after(hardResetRestartDelay, func() {
			rctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if err := s.restartGateway(rctx, iface); err != nil && !restartAlreadyUnderway(err) {
				s.logf("oob: restart of %s after hard reset failed: %v", iface, err)
			}
		})
		body += fmt.Sprintf(" rs%ds", int(hardResetRestartDelay/time.Second))
	}
	if level == LevelHard && s.d.TriggerScan != nil {
		s.d.TriggerScan()
	}
	return Result{Code: RCOK, Body: body, FollowUp: t.FollowUp}
}

// hardResetRestartDelay is how long after a level-3 reset of an interface the
// executor restarts that interface's gateway itself. A USB power cycle holds
// VBUS off for 3 s and the device needs a few more to enumerate; when it comes
// back under the same tty name the device supervisor sees no change, and the
// transport's own "disconnected" event can sit behind a backlog of queued
// packets (parallax, 5 Sep 2026: the reopen came 4 min 43 s after the cut), so
// the restart is scheduled here rather than hoped for. Tests shorten it.
// [MESHSAT-786]
var hardResetRestartDelay = 10 * time.Second

// restartAlreadyUnderway recognises the gateway manager telling us the
// instance is being started or already runs again: after a USB power cycle
// the device supervisor restarts an instance itself when the device returns
// under a new tty name, and our scheduled restart must then stand down.
func restartAlreadyUnderway(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "is starting") || strings.Contains(msg, "already running")
}

func (s *Service) execBearer(ctx context.Context, o Origin, args []byte) Result {
	code, state, err := ParseBearerArgs(args)
	if err != nil {
		return Result{Code: RCBadArgs}
	}
	t, ok := TargetByCode(code)
	if !ok || t.IfaceID == "" {
		return Result{Code: RCBadArgs, Body: "target"}
	}
	if s.d.Gateways == nil {
		return Result{Code: RCUnavailable}
	}
	if state == 1 {
		s.cancelRevert(t.IfaceID)
		if err := s.startBearer(ctx, t.IfaceID); err != nil {
			return Result{Code: RCUnavailable, Body: trim(err.Error(), 60)}
		}
		return Result{Code: RCOK, Body: t.Name + " on"}
	}
	body := t.Name + " off"
	if severs(o, t) {
		iface := t.IfaceID
		s.armRevert(iface, RevertDelay, func() {
			rctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := s.startBearer(rctx, iface); err != nil {
				s.logf("oob: revert of %s failed: %v", iface, err)
			}
		})
		body += " rv10m"
	}
	if err := s.stopBearer(t.IfaceID); err != nil {
		return Result{Code: RCUnavailable, Body: trim(err.Error(), 60)}
	}
	return Result{Code: RCOK, Body: body}
}

func (s *Service) action(target string, level byte) Action {
	if s.d.Actions == nil {
		return nil
	}
	return s.d.Actions[target][level]
}

func (s *Service) restartGateway(ctx context.Context, ifaceID string) error {
	if err := s.d.Gateways.StopGatewayInstance(ifaceID); err != nil {
		s.logf("oob: stop %s before restart: %v", ifaceID, err)
	}
	return s.d.Gateways.StartGatewayInstance(ctx, ifaceID)
}

func (s *Service) startBearer(ctx context.Context, ifaceID string) error {
	s.setInterfaceEnabled(ifaceID, true)
	return s.d.Gateways.StartGatewayInstance(ctx, ifaceID)
}

func (s *Service) stopBearer(ifaceID string) error {
	s.setInterfaceEnabled(ifaceID, false)
	return s.d.Gateways.StopGatewayInstance(ifaceID)
}

// setInterfaceEnabled mirrors the operator-visible enabled flag so the
// interface manager stops or starts the delivery worker to match.
func (s *Service) setInterfaceEnabled(ifaceID string, enabled bool) {
	if s.d.DB == nil {
		return
	}
	iface, err := s.d.DB.GetInterface(ifaceID)
	if err != nil {
		return
	}
	if iface.Enabled == enabled {
		return
	}
	iface.Enabled = enabled
	if err := s.d.DB.UpdateInterface(iface); err != nil {
		s.logf("oob: update interface %s: %v", ifaceID, err)
	}
}

// armRevert schedules fn after d, replacing any pending revert for the
// same interface.
func (s *Service) armRevert(ifaceID string, d time.Duration, fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.reverts[ifaceID]; ok {
		t.Stop()
	}
	s.reverts[ifaceID] = time.AfterFunc(d, func() {
		s.mu.Lock()
		delete(s.reverts, ifaceID)
		s.mu.Unlock()
		fn()
	})
}

func (s *Service) cancelRevert(ifaceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.reverts[ifaceID]; ok {
		t.Stop()
		delete(s.reverts, ifaceID)
	}
}

func (s *Service) cancelReverts() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, t := range s.reverts {
		t.Stop()
		delete(s.reverts, id)
	}
}

// PendingReverts lists interfaces with an armed revert timer.
func (s *Service) PendingReverts() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.reverts))
	for id := range s.reverts {
		out = append(out, id)
	}
	return out
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
