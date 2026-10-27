package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// The workspace's Private Reticulum network, kept by the Hub (MESHSAT-1715;
// the Hub's docs/bridge-hub-protocol.md "Reticulum access"). The Hub says on
// rns/access what the workspace's network name and passphrase are, or "none";
// this kit keeps ONE tcp_rns row marked managed=hub to match. The row is an
// access-coded row like any other (coded.go): a leaf that carries only this
// kit's own traffic, kept out of the legacy layer.
//
// Only the code comes from the Hub's word. The host and port are this kit's
// own setting (MESHSAT_HUB_RNS_ENDPOINT) and TLS is off, as for Sideband: an
// access message for another endpoint is not applied, so a forged or wrong
// message cannot point the row, and frames tagged with the code, elsewhere.

// errManagedByHub refuses the Hub's mark from anyone but the Hub's own path:
// a person's row marked so would be removed by the Hub's next "none".
var errManagedByHub = errors.New("tcp_rns: \"managed\": \"hub\" is set by the Hub only")

func markedManaged(raw json.RawMessage) bool {
	var c TCPRNSConfig
	return json.Unmarshal(raw, &c) == nil && strings.TrimSpace(c.Managed) != ""
}

// keepTCPRNSManaged returns next with the stored managed mark when next has
// none: an edit through the API never turns the Hub's row into a person's.
func keepTCPRNSManaged(stored, next json.RawMessage) json.RawMessage {
	var old, nw TCPRNSConfig
	if json.Unmarshal(stored, &old) != nil || json.Unmarshal(next, &nw) != nil {
		return next
	}
	if old.Managed == "" || nw.Managed != "" {
		return next
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(next, &m) != nil {
		return next
	}
	m["managed"], _ = json.Marshal(old.Managed)
	out, err := json.Marshal(m)
	if err != nil {
		return next
	}
	return out
}

// ReconcileManaged makes the Hub's row match desired, or removes it when
// desired is nil. One row: an existing managed row is kept (any other
// managed row goes), else a coded row to the same host, port and network is
// adopted, else one is created. It answers what it did.
func (m *IfaceManager) ReconcileManaged(desired *TCPRNSConfig) (string, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	type row struct {
		id      string
		cfg     TCPRNSConfig
		enabled bool
	}
	var managed, adoptable []row
	m.mu.Lock()
	for id, in := range m.inst {
		if in.row.Type != DynTypeTCPRNS {
			continue
		}
		var c TCPRNSConfig
		if json.Unmarshal([]byte(in.row.Config), &c) != nil {
			continue
		}
		r := row{id: id, cfg: c, enabled: in.row.Enabled}
		switch {
		case c.Managed == TCPRNSManagedHub:
			managed = append(managed, r)
		case desired != nil && c.Coded() && c.Host == desired.Host && c.port() == desired.port() && c.IFACNetname == desired.IFACNetname:
			adoptable = append(adoptable, r)
		}
	}
	m.mu.Unlock()
	sort.Slice(managed, func(i, j int) bool { return managed[i].id < managed[j].id })
	sort.Slice(adoptable, func(i, j int) bool { return adoptable[i].id < adoptable[j].id })

	if desired == nil {
		if len(managed) == 0 {
			return "none", nil
		}
		for _, r := range managed {
			if err := m.delete(r.id); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("removed %d", len(managed)), nil
	}
	want := *desired
	want.TLS = false
	want.Managed = TCPRNSManagedHub
	raw, err := json.Marshal(want)
	if err != nil {
		return "", err
	}
	var keep *row
	if len(managed) > 0 {
		keep = &managed[0]
		for _, r := range managed[1:] {
			if err := m.delete(r.id); err != nil {
				return "", err
			}
		}
	} else if len(adoptable) > 0 {
		keep = &adoptable[0]
	}
	if keep == nil {
		id, err := m.create(DynTypeTCPRNS, raw, true)
		if err != nil {
			return "", err
		}
		return "created " + id, nil
	}
	if keep.cfg == want && keep.enabled {
		return "unchanged " + keep.id, nil
	}
	if err := m.update(keep.id, raw, true); err != nil {
		return "", err
	}
	return "updated " + keep.id, nil
}

// managedOnline reports whether the Hub's row exists and is online.
func (m *IfaceManager) managedOnline() (exists, online bool) {
	for _, st := range m.List() {
		if st.Type != DynTypeTCPRNS {
			continue
		}
		var c TCPRNSConfig
		if json.Unmarshal(st.Config, &c) == nil && c.Managed == TCPRNSManagedHub {
			return true, st.Online
		}
	}
	return false, false
}

// HubAccessConfig wires the kit's side of the workspace's Private network.
type HubAccessConfig struct {
	Ifaces *IfaceManager
	// Enabled is MESHSAT_HUB_RNS_JOIN: false never joins and removes the row.
	Enabled bool
	// Endpoint is MESHSAT_HUB_RNS_ENDPOINT, the host:port the row connects to.
	Endpoint string
	// NodeRunning is false when the kit's Reticulum node is off: a coded row
	// then carries nothing, so none is kept.
	NodeRunning bool
	// Ask publishes rns/join; false when it did not go out.
	Ask func() bool
	// Every is how often the kit asks while it has an answer (6 h).
	Every time.Duration
}

// HubAccess applies the Hub's rns/access and keeps asking until it has one.
type HubAccess struct {
	cfg  HubAccessConfig
	host string
	port int

	mu           sync.Mutex
	told         time.Time // the last access applied; zero until one is
	nextAsk      time.Time
	backoff      time.Duration
	offlineTicks int
}

const (
	hubAccessMinBackoff = 15 * time.Second
	hubAccessMaxBackoff = 10 * time.Minute
	hubAccessTick       = 15 * time.Second
)

// NewHubAccess checks the endpoint.
func NewHubAccess(cfg HubAccessConfig) (*HubAccess, error) {
	host, p, err := net.SplitHostPort(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("hub rns endpoint %q: %w", cfg.Endpoint, err)
	}
	port, err := strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 || host == "" {
		return nil, fmt.Errorf("hub rns endpoint %q is not host:port", cfg.Endpoint)
	}
	if cfg.Every <= 0 {
		cfg.Every = 6 * time.Hour
	}
	return &HubAccess{cfg: cfg, host: host, port: port}, nil
}

func (h *HubAccess) active() bool { return h.cfg.Enabled && h.cfg.NodeRunning && h.cfg.Ifaces != nil }

// Start removes the Hub's row when the kit may not join, else keeps asking.
func (h *HubAccess) Start(ctx context.Context) {
	if h.cfg.Ifaces == nil {
		return
	}
	if !h.active() {
		if res, err := h.cfg.Ifaces.ReconcileManaged(nil); err != nil {
			log.Warn().Err(err).Msg("rns: the Hub's row could not be removed")
		} else if res != "none" {
			log.Info().Str("result", res).Bool("join_enabled", h.cfg.Enabled).Bool("node_running", h.cfg.NodeRunning).
				Msg("rns: the Hub's row removed: this kit does not join its workspace's Private network")
		}
		return
	}
	go h.run(ctx)
}

// OnConnect is called once rns/access is subscribed on a new connection: the
// kit asks at once and again with backoff until it has an answer.
func (h *HubAccess) OnConnect() {
	if !h.active() {
		return
	}
	h.mu.Lock()
	h.told = time.Time{}
	h.backoff = 0
	h.nextAsk = time.Time{}
	h.mu.Unlock()
	h.tick(time.Now())
}

func (h *HubAccess) run(ctx context.Context) {
	t := time.NewTicker(hubAccessTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			h.tick(now)
		}
	}
}

// tick asks when no answer came yet (with backoff), when the row has been
// offline for two ticks (a rotation closes it), and every cfg.Every.
func (h *HubAccess) tick(now time.Time) {
	exists, online := h.cfg.Ifaces.managedOnline()
	h.mu.Lock()
	if exists && !online {
		h.offlineTicks++
	} else {
		h.offlineTicks = 0
	}
	due := false
	switch {
	case h.told.IsZero() || h.offlineTicks >= 2:
		due = !now.Before(h.nextAsk)
	case now.Sub(h.told) >= h.cfg.Every:
		due = true
	}
	if due {
		h.backoff *= 2
		if h.backoff < hubAccessMinBackoff {
			h.backoff = hubAccessMinBackoff
		}
		if h.backoff > hubAccessMaxBackoff {
			h.backoff = hubAccessMaxBackoff
		}
		h.nextAsk = now.Add(h.backoff)
	}
	h.mu.Unlock()
	if due && h.cfg.Ask != nil {
		h.cfg.Ask()
	}
}

// hubAccessMsg is the rns/access payload.
type hubAccessMsg struct {
	V              int    `json:"v"`
	Mode           string `json:"mode"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	IFACNetname    string `json:"ifac_netname"`
	IFACPassphrase string `json:"ifac_passphrase"`
}

// Apply takes one rns/access payload. It never logs the passphrase.
func (h *HubAccess) Apply(payload []byte) {
	if !h.active() {
		return
	}
	var a hubAccessMsg
	if err := json.Unmarshal(payload, &a); err != nil || a.V != 1 {
		log.Warn().Int("v", a.V).Msg("rns: an access message the kit does not read, ignored")
		return
	}
	var desired *TCPRNSConfig
	switch a.Mode {
	case "none":
	case "private":
		if a.Host != h.host || a.Port != h.port {
			log.Warn().Str("host", a.Host).Int("port", a.Port).Str("endpoint", net.JoinHostPort(h.host, strconv.Itoa(h.port))).
				Msg("rns: access for another endpoint than this kit's, ignored")
			return
		}
		c := TCPRNSConfig{Host: h.host, Port: h.port, IFACNetname: a.IFACNetname, IFACPassphrase: a.IFACPassphrase, Managed: TCPRNSManagedHub}
		raw, _ := json.Marshal(c)
		norm, err := ValidateDynConfig(DynTypeTCPRNS, raw)
		if err != nil || json.Unmarshal(norm, &c) != nil {
			log.Warn().Err(err).Msg("rns: an access message with an unusable code, ignored")
			return
		}
		desired = &c
	default:
		log.Warn().Str("mode", a.Mode).Msg("rns: an access message with an unknown mode, ignored")
		return
	}
	res, err := h.cfg.Ifaces.ReconcileManaged(desired)
	if err != nil {
		log.Warn().Err(err).Str("mode", a.Mode).Msg("rns: the Hub's row could not be set; the kit asks again")
		return
	}
	h.mu.Lock()
	h.told = time.Now()
	h.backoff = 0
	h.offlineTicks = 0
	h.mu.Unlock()
	ev := log.Info().Str("mode", a.Mode).Str("result", res)
	if desired != nil {
		ev = ev.Str("network", desired.IFACNetname)
	}
	ev.Msg("rns: the workspace's Private network as the Hub says")
}
