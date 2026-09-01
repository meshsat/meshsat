package oob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/keystore"
)

// Config is the operator configuration. Environment values are first-boot
// defaults only; the persisted system_config values win afterwards.
type Config struct {
	Enabled         bool
	ReplyBudgetHour int
	HostSocket      string
}

// Default values.
const (
	DefaultReplyBudgetHour = 12
	DefaultHostSocket      = "/run/meshsat-oob/agent.sock"
	MaxReplyChunks         = 4
	FollowUpDelay          = 30 * time.Second
	BootCounterBump        = 1 << 16
)

// system_config keys.
const (
	cfgEnabled     = "oob_enabled"
	cfgReplyBudget = "oob_reply_budget"
	cfgHostSocket  = "oob_host_socket"
)

// KeyProvider is the slice of the keystore the service uses.
type KeyProvider interface {
	GetKey(channelType, address string) ([]byte, int, error)
	StoreKey(channelType, address string, rawKey []byte) (int, error)
	RevokeKey(channelType, address string) error
	CreateBundleFromEntries(entries []keystore.BundleEntry) ([]byte, string, error)
}

// GatewayControl is the slice of the gateway manager the service uses.
type GatewayControl interface {
	StartGatewayInstance(ctx context.Context, instanceID string) error
	StopGatewayInstance(instanceID string) error
}

// Deps are the closures and interfaces the service needs from the rest of
// the bridge. The package imports none of engine, api, gateway or hub.
type Deps struct {
	DB        *database.DB
	Keys      KeyProvider
	Gateways  GatewayControl
	BearersUp func() map[string]bool // interface id -> up
	Host      *HostClient
	// Send queues text on a bearer to a bearer address through the delivery
	// ledger (class oob) and returns the delivery id.
	Send func(ctx context.Context, ifaceID, address, text string) (int64, error)
	// Audit appends to the signed audit chain.
	Audit func(eventType string, ifaceID *string, direction *string, deliveryID *int64, detail string)
	// Emit publishes an SSE event.
	Emit func(eventType string, data any)
	// ECDH resolves the material for the ECDH provisioning path.
	ECDH func(signerID string) (ECDHMaterial, error)
	// Actions are bridge-side reset actions: target name -> level -> action.
	Actions map[string]map[byte]Action
	// TriggerScan asks the device supervisor for an immediate USB scan.
	TriggerScan func()
	Status      StatusSources
	LocalAlias  string // default issuer alias for bundles
	Now         func() time.Time
}

// Service is the OOB management frame service.
type Service struct {
	mu       sync.RWMutex
	cfg      Config
	d        Deps
	locks    sync.Map // peer id -> *sync.Mutex
	buckets  map[uint16]*bucket
	rejected map[uint16]time.Time
	reverts  map[string]*time.Timer
	restart  func()
	agentVer string
}

// New creates the service. Call Start before use.
func New(cfg Config, d Deps) *Service {
	if cfg.ReplyBudgetHour <= 0 {
		cfg.ReplyBudgetHour = DefaultReplyBudgetHour
	}
	if cfg.HostSocket == "" && d.Host != nil && d.Host.Path != "" {
		cfg.HostSocket = d.Host.Path
	}
	if cfg.HostSocket == "" {
		cfg.HostSocket = DefaultHostSocket
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Host == nil {
		d.Host = NewHostClient(cfg.HostSocket)
	}
	return &Service{
		cfg:      cfg,
		d:        d,
		buckets:  map[uint16]*bucket{},
		rejected: map[uint16]time.Time{},
		reverts:  map[string]*time.Timer{},
	}
}

func (s *Service) now() time.Time { return s.d.Now() }

func (s *Service) logf(format string, args ...any) {
	log.Warn().Msg(fmt.Sprintf(format, args...))
}

// after runs fn after d. Tests replace the clock through Deps.Now only; the
// timer itself is real, which is fine for the two-second graces used here.
func (s *Service) after(d time.Duration, fn func()) {
	time.AfterFunc(d, fn)
}

// Start loads or seeds the persisted configuration, bumps every transmit
// counter (a database restored from an older backup must never rewind
// into nonces already used) and probes the host agent.
func (s *Service) Start(ctx context.Context) error {
	if s.d.DB == nil {
		return errors.New("oob: database required")
	}
	s.loadOrSeedConfig()
	if err := s.d.DB.BumpOOBTxCounters(BootCounterBump); err != nil {
		return err
	}
	if s.d.Host != nil && s.d.Host.Available() {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		ver, err := s.d.Host.Ping(pctx)
		cancel()
		if err == nil {
			s.mu.Lock()
			s.agentVer = ver
			s.mu.Unlock()
			log.Info().Str("version", ver).Str("socket", s.cfg.HostSocket).Msg("oob: host agent available")
		} else {
			log.Warn().Err(err).Msg("oob: host agent socket present but not answering")
		}
	} else {
		log.Info().Str("socket", s.cfg.HostSocket).Msg("oob: host agent not installed, host actions unavailable")
	}
	log.Info().Bool("enabled", s.cfg.Enabled).Int("reply_budget", s.cfg.ReplyBudgetHour).Msg("oob: management frames service started")
	return nil
}

func (s *Service) loadOrSeedConfig() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, err := s.d.DB.GetSystemConfig(cfgEnabled); err == nil {
		s.cfg.Enabled = v == "true"
	} else {
		_ = s.d.DB.SetSystemConfig(cfgEnabled, strconv.FormatBool(s.cfg.Enabled))
	}
	if v, err := s.d.DB.GetSystemConfig(cfgReplyBudget); err == nil {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.cfg.ReplyBudgetHour = n
		}
	} else {
		_ = s.d.DB.SetSystemConfig(cfgReplyBudget, strconv.Itoa(s.cfg.ReplyBudgetHour))
	}
	if v, err := s.d.DB.GetSystemConfig(cfgHostSocket); err == nil && v != "" {
		s.cfg.HostSocket = v
		if s.d.Host != nil {
			s.d.Host.Path = v
		}
	} else {
		_ = s.d.DB.SetSystemConfig(cfgHostSocket, s.cfg.HostSocket)
	}
}

// Config returns the current configuration.
func (s *Service) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// SetConfig persists and applies a new configuration.
func (s *Service) SetConfig(c Config) error {
	if c.ReplyBudgetHour <= 0 {
		c.ReplyBudgetHour = DefaultReplyBudgetHour
	}
	if c.HostSocket == "" {
		c.HostSocket = DefaultHostSocket
	}
	if s.d.DB != nil {
		if err := s.d.DB.SetSystemConfig(cfgEnabled, strconv.FormatBool(c.Enabled)); err != nil {
			return err
		}
		if err := s.d.DB.SetSystemConfig(cfgReplyBudget, strconv.Itoa(c.ReplyBudgetHour)); err != nil {
			return err
		}
		if err := s.d.DB.SetSystemConfig(cfgHostSocket, c.HostSocket); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.cfg = c
	if s.d.Host != nil {
		s.d.Host.Path = c.HostSocket
	}
	s.buckets = map[uint16]*bucket{}
	s.mu.Unlock()
	return nil
}

// SetRestartFunc installs the bridge restart hook (SIGTERM to self).
func (s *Service) SetRestartFunc(fn func()) {
	s.mu.Lock()
	s.restart = fn
	s.mu.Unlock()
}

// AgentStatus reports the host agent state.
func (s *Service) AgentStatus() (available bool, version, socket string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.d.Host != nil && s.d.Host.Available(), s.agentVer, s.cfg.HostSocket
}

func (s *Service) enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Enabled
}

// bucket is a per-peer token bucket: capacity = hourly budget, refilling
// at budget per hour.
type bucket struct {
	tokens float64
	last   time.Time
}

func (s *Service) takeReplyToken(peer uint16) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	capacity := float64(s.cfg.ReplyBudgetHour)
	b, ok := s.buckets[peer]
	if !ok {
		b = &bucket{tokens: capacity, last: now}
		s.buckets[peer] = b
	}
	elapsed := now.Sub(b.last).Hours()
	if elapsed > 0 {
		b.tokens += elapsed * capacity
		if b.tokens > capacity {
			b.tokens = capacity
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// auditRejectOnce audits at most one rejection per peer per hour so a
// flood cannot bloat the signed chain.
func (s *Service) auditRejectOnce(peer uint16, bearer, reason string) {
	s.mu.Lock()
	last, ok := s.rejected[peer]
	now := s.now()
	if ok && now.Sub(last) < time.Hour {
		s.mu.Unlock()
		return
	}
	s.rejected[peer] = now
	s.mu.Unlock()
	if s.d.Audit != nil {
		dir := "ingress"
		detail, _ := json.Marshal(map[string]any{"peer_id": peer, "reason": reason})
		s.d.Audit("oob_reject", &bearer, &dir, nil, string(detail))
	}
}

func (s *Service) reject(peer uint16, bearer, fromAddr, reason string, h Header) {
	Global.IncFrame(reason)
	if s.d.DB != nil {
		_, _ = s.d.DB.InsertOOBLog(&database.OOBLogEntry{
			PeerID: peer, Direction: "in", Kind: "reject", Bearer: bearer, FromAddr: fromAddr,
			Cmd: int(h.Cmd), Counter: h.Counter, Result: reason,
		})
	}
	s.auditRejectOnce(peer, bearer, reason)
	log.Debug().Uint16("peer", peer).Str("bearer", bearer).Str("reason", reason).Msg("oob: frame rejected")
}

// HandleInbound is the classifier hook. It returns true when text carried a
// management frame (valid or not) so the caller drops it from the normal
// message flow; false means "not a frame, carry on".
func (s *Service) HandleInbound(ctx context.Context, ifaceID, fromAddr, text string) bool {
	wire, ok := ExtractFrame(text)
	if !ok {
		return false
	}
	h, err := ParseHeader(wire)
	if err != nil {
		return false
	}
	if !s.enabled() {
		Global.IncFrame("disabled")
		log.Debug().Str("bearer", ifaceID).Msg("oob: frame received while disabled, dropped")
		return true
	}
	if s.d.DB == nil {
		return true
	}
	peer, err := s.d.DB.GetOOBPeer(h.PeerID)
	if err != nil {
		s.reject(h.PeerID, ifaceID, fromAddr, "unknown_peer", h)
		return true
	}
	if !peer.Enabled {
		s.reject(h.PeerID, ifaceID, fromAddr, "disabled_peer", h)
		return true
	}
	w := Window{High: peer.RxHigh, Bitmap: peer.RxWindow}
	if !w.Check(h.Counter) {
		s.reject(h.PeerID, ifaceID, fromAddr, "replay", h)
		return true
	}
	key, err := s.peerKey(peer)
	if err != nil {
		s.reject(h.PeerID, ifaceID, fromAddr, "no_key", h)
		return true
	}
	frame, err := Open(wire, key, Role(peer.LocalRole).Other())
	if err != nil {
		s.reject(h.PeerID, ifaceID, fromAddr, "bad_tag", h)
		return true
	}
	w.Mark(frame.Counter)
	if err := s.d.DB.SaveOOBRxWindow(peer.PeerID, w.High, w.Bitmap); err != nil {
		log.Error().Err(err).Msg("oob: persist replay window")
	}
	Global.IncFrame("accepted")

	if frame.Reply {
		s.handleReply(peer, ifaceID, fromAddr, frame)
		return true
	}

	_, _ = s.d.DB.InsertOOBLog(&database.OOBLogEntry{
		PeerID: peer.PeerID, Direction: "in", Kind: "request", Bearer: ifaceID, FromAddr: fromAddr,
		Cmd: int(frame.Cmd), Counter: frame.Counter,
	})
	go s.run(peer, ifaceID, fromAddr, frame)
	return true
}

func (s *Service) handleReply(peer *database.OOBPeer, ifaceID, fromAddr string, frame Frame) {
	ra, err := ParseReplyArgs(frame.Args)
	result := "malformed"
	detail := ""
	if err == nil {
		result = ra.RC.String()
		detail, _ = func() (string, error) {
			b, e := json.Marshal(map[string]any{"req_counter_lo": ra.ReqCounterLo, "seq": ra.Seq, "total": ra.Total, "body": string(ra.Body)})
			return string(b), e
		}()
	}
	_, _ = s.d.DB.InsertOOBLog(&database.OOBLogEntry{
		PeerID: peer.PeerID, Direction: "in", Kind: "reply", Bearer: ifaceID, FromAddr: fromAddr,
		Cmd: int(frame.Cmd), Counter: frame.Counter, Result: result, Detail: detail,
	})
	if s.d.Emit != nil {
		s.d.Emit("oob_reply", map[string]any{
			"peer_id": peer.PeerID, "alias": peer.Alias, "bearer": ifaceID, "cmd": frame.Cmd,
			"counter": frame.Counter, "result": result, "detail": detail,
		})
	}
}

func cmdName(cmd byte) string {
	if c, ok := CommandByCode(cmd); ok {
		return c.Name
	}
	return fmt.Sprintf("0x%02x", cmd)
}

// run executes a request and sends the reply.
func (s *Service) run(peer *database.OOBPeer, ifaceID, fromAddr string, frame Frame) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	o := Origin{PeerID: peer.PeerID, Alias: peer.Alias, Role: peer.Role, Bearer: ifaceID, FromAddr: fromAddr}
	res := s.Execute(ctx, o, frame.Cmd, frame.Args)
	s.recordCommand(o, frame.Cmd, frame.Counter, res)
	if frame.NoReply {
		return
	}
	s.reply(ctx, peer, ifaceID, fromAddr, frame, res)
	if res.FollowUp && res.Code == RCOK {
		s.after(FollowUpDelay, func() {
			fctx, fcancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer fcancel()
			s.reply(fctx, peer, ifaceID, fromAddr, frame, Result{Code: RCOK, Body: s.statusNetBody(fctx)})
		})
	}
}

func (s *Service) recordCommand(o Origin, cmd byte, counter uint32, res Result) {
	name := cmdName(cmd)
	Global.IncCommand(name, res.Code.String())
	detail, _ := json.Marshal(map[string]any{
		"peer_id": o.PeerID, "alias": o.Alias, "bearer": o.Bearer, "from": o.FromAddr,
		"cmd": name, "counter": counter, "result": res.Code.String(), "body": res.Body,
	})
	if s.d.DB != nil {
		_, _ = s.d.DB.InsertOOBLog(&database.OOBLogEntry{
			PeerID: o.PeerID, Direction: "in", Kind: "request", Bearer: o.Bearer, FromAddr: o.FromAddr,
			Cmd: int(cmd), Counter: counter, Result: res.Code.String(), Detail: string(detail),
		})
	}
	if s.d.Audit != nil {
		bearer := o.Bearer
		dir := "ingress"
		s.d.Audit("oob_command", &bearer, &dir, nil, string(detail))
	}
	if s.d.Emit != nil {
		s.d.Emit("oob_command", json.RawMessage(detail))
	}
	log.Info().Str("cmd", name).Uint16("peer", o.PeerID).Str("bearer", o.Bearer).Str("result", res.Code.String()).Msg("oob: command executed")
}

// reply seals and queues the reply frames for res on the arriving bearer.
func (s *Service) reply(ctx context.Context, peer *database.OOBPeer, ifaceID, fromAddr string, req Frame, res Result) {
	if s.d.Send == nil {
		return
	}
	key, err := s.peerKey(peer)
	if err != nil {
		return
	}
	bodyBudget := BearerBudget(ifaceID) - ReplyHeaderLen
	// Only LOG and STATUS-NET are worth several frames; every other reply
	// is one frame with the body truncated to the bearer budget, so a PING
	// over APRS costs one token and keeps the head of the status line.
	maxChunks := 1
	if req.Cmd == CmdLog || req.Cmd == CmdStatusNet {
		maxChunks = MaxReplyChunks
	}
	chunks := chunk(res.Body, bodyBudget, maxChunks)
	if len(chunks) == 0 {
		chunks = []string{""}
	}
	total := byte(len(chunks))
	for i, c := range chunks {
		if !s.takeReplyToken(peer.PeerID) {
			log.Warn().Uint16("peer", peer.PeerID).Msg("oob: reply budget exhausted, reply dropped")
			_, _ = s.d.DB.InsertOOBLog(&database.OOBLogEntry{
				PeerID: peer.PeerID, Direction: "out", Kind: "reply", Bearer: ifaceID, FromAddr: fromAddr,
				Cmd: int(req.Cmd), Counter: 0, Result: "budget",
			})
			return
		}
		counter, err := s.d.DB.NextOOBTxCounter(peer.PeerID)
		if err != nil || counter >= MaxCounter {
			log.Error().Err(err).Msg("oob: transmit counter unavailable or exhausted")
			return
		}
		wire, err := Seal(Frame{
			Enc:     req.Enc,
			Reply:   true,
			PeerID:  peer.PeerID,
			Counter: counter,
			Cmd:     req.Cmd,
			Args:    EncodeReplyArgs(ReplyArgs{RC: res.Code, ReqCounterLo: uint16(req.Counter), Seq: byte(i + 1), Total: total, Body: []byte(c)}),
		}, key, Role(peer.LocalRole))
		if err != nil {
			log.Error().Err(err).Msg("oob: seal reply")
			return
		}
		delID, err := s.d.Send(ctx, ifaceID, fromAddr, Encode(wire))
		entry := &database.OOBLogEntry{
			PeerID: peer.PeerID, Direction: "out", Kind: "reply", Bearer: ifaceID, FromAddr: fromAddr,
			Cmd: int(req.Cmd), Counter: counter, Result: res.Code.String(),
		}
		if err != nil {
			entry.Result = "send_error"
			entry.Detail = err.Error()
		} else {
			entry.DeliveryID = &delID
		}
		_, _ = s.d.DB.InsertOOBLog(entry)
	}
}

// SendRequest originates a command to a peer over a bearer.
type SendRequest struct {
	PeerID  uint16
	Via     string // interface id
	Cmd     byte
	Args    []byte
	NoReply bool
	Encrypt *bool // nil = the peer's per-bearer policy
}

// SendResult reports what was queued.
type SendResult struct {
	DeliveryID int64  `json:"delivery_id"`
	Counter    uint32 `json:"counter"`
	Text       string `json:"text"`
	Address    string `json:"address,omitempty"`
}

// Send frames a request and queues it through the delivery ledger.
func (s *Service) Send(ctx context.Context, req SendRequest) (SendResult, error) {
	if !s.enabled() {
		return SendResult{}, errors.New("oob: disabled")
	}
	if s.d.Send == nil {
		return SendResult{}, errors.New("oob: no send path")
	}
	if _, ok := CommandByCode(req.Cmd); !ok {
		return SendResult{}, errors.New("oob: unknown command")
	}
	if len(req.Args) > MaxArgs {
		return SendResult{}, ErrArgsLen
	}
	if req.Via == "" {
		return SendResult{}, errors.New("oob: bearer required")
	}
	peer, err := s.d.DB.GetOOBPeer(req.PeerID)
	if err != nil {
		return SendResult{}, err
	}
	key, err := s.peerKey(peer)
	if err != nil {
		return SendResult{}, err
	}
	addr := resolveAddress(peer, req.Via)
	enc := wantsEnc(peer, req.Via)
	if req.Encrypt != nil {
		enc = *req.Encrypt
	}
	counter, err := s.d.DB.NextOOBTxCounter(peer.PeerID)
	if err != nil {
		return SendResult{}, err
	}
	if counter >= MaxCounter {
		return SendResult{}, errors.New("oob: key exhausted, rotate the peer key")
	}
	wire, err := Seal(Frame{Enc: enc, NoReply: req.NoReply, PeerID: peer.PeerID, Counter: counter, Cmd: req.Cmd, Args: req.Args}, key, Role(peer.LocalRole))
	if err != nil {
		return SendResult{}, err
	}
	text := Encode(wire)
	delID, err := s.d.Send(ctx, req.Via, addr, text)
	entry := &database.OOBLogEntry{
		PeerID: peer.PeerID, Direction: "out", Kind: "request", Bearer: req.Via, FromAddr: addr,
		Cmd: int(req.Cmd), Counter: counter, Result: "queued",
	}
	if err != nil {
		entry.Result = "send_error"
		entry.Detail = err.Error()
		_, _ = s.d.DB.InsertOOBLog(entry)
		return SendResult{}, err
	}
	entry.DeliveryID = &delID
	_, _ = s.d.DB.InsertOOBLog(entry)
	return SendResult{DeliveryID: delID, Counter: counter, Text: text, Address: addr}, nil
}

// ExecuteLocal runs a command for a local or Hub origin (peer id 0) with
// the same executor, log and audit path as a frame.
func (s *Service) ExecuteLocal(ctx context.Context, o Origin, cmd byte, args []byte) Result {
	if o.Bearer == "" {
		o.Bearer = OriginHub
	}
	if o.Role == "" {
		o.Role = RoleControl
	}
	res := s.Execute(ctx, o, cmd, args)
	s.recordCommand(o, cmd, 0, res)
	return res
}

// TargetInfo is the API view of one reset target with its usable levels.
type TargetInfo struct {
	Code    byte   `json:"code"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	IfaceID string `json:"iface_id,omitempty"`
	Levels  []int  `json:"levels"` // ints, not bytes: a []byte would marshal as base64
	Bearer  bool   `json:"bearer"` // usable with BEARER
}

// TargetsInfo lists the targets and the levels this kit can act on.
func (s *Service) TargetsInfo() []TargetInfo {
	agent := s.d.Host != nil && s.d.Host.Available()
	out := make([]TargetInfo, 0, len(Targets))
	for _, t := range Targets {
		info := TargetInfo{Code: t.Code, Name: t.Name, Kind: t.Kind.String(), IfaceID: t.IfaceID, Bearer: t.IfaceID != "" && s.d.Gateways != nil, Levels: []int{}}
		for level := byte(MinLevel); level <= MaxLevel; level++ {
			switch {
			case t.Code == TargetBridge:
				if level == LevelSoft && s.restart != nil {
					info.Levels = append(info.Levels, int(level))
				}
			case t.Code == TargetHost:
				if level == LevelHard && agent {
					info.Levels = append(info.Levels, int(level))
				}
			case s.action(t.Name, level) != nil:
				info.Levels = append(info.Levels, int(level))
			case t.Kind == KindInterface && level == LevelSoft && t.IfaceID != "" && s.d.Gateways != nil:
				info.Levels = append(info.Levels, int(level))
			case t.HostActions[level] != "" && agent:
				info.Levels = append(info.Levels, int(level))
			}
		}
		out = append(out, info)
	}
	return out
}
