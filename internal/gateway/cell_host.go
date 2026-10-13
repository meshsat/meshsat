package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/cell"
	"meshsat/internal/database"
)

// HostCaller is the OOB host agent as the cell host needs it: one
// allowlisted action at a time on the kit's host. [MESHSAT-1578]
type HostCaller interface {
	Available() bool
	Call(ctx context.Context, action string, args map[string]any) (map[string]any, error)
}

// CellHost adopts the MeshSat 2G cell's SDR (a Pluto Nano on USB) and runs
// the cell on the kit's host through the OOB agent: when the Pluto appears
// the core (stp, hlr, mgw, msc, cbc, bsc; no emission) is started, the
// SMPP credentials come from the host's cell.env, cell_0 is seeded and
// bound; when it goes, the radio and the core are stopped and cell_0 is
// switched off. The radio (osmo-bts-trx) never starts by itself: an
// operator keys it up with a purpose, and the host refuses without the
// preflight and the site attestation (licence RDI-2610-00385). [MESHSAT-1578]
type CellHost struct {
	agent HostCaller
	db    *database.DB
	mgr   *Manager
	// local runs the cell inside the Bridge (the image carries the Osmocom
	// daemons); nil falls back to the kit's host package through the agent.
	local *cell.Runner

	mu        sync.Mutex
	port      string
	present   bool
	adoptedAt time.Time
	lastErr   string
	adopting  bool
}

// NewCellHost wires the host agent, the database and the gateway manager;
// local, when not nil, runs the cell inside the Bridge and the agent is
// not used.
func NewCellHost(agent HostCaller, db *database.DB, mgr *Manager, local *cell.Runner) *CellHost {
	return &CellHost{agent: agent, db: db, mgr: mgr, local: local}
}

// Local reports whether the cell runs inside the Bridge.
func (h *CellHost) Local() bool { return h.local != nil }

const cellInstance = "cell_0"

// ErrNoCellHost is returned when the host agent is not there.
var ErrNoCellHost = errors.New("cell: the host agent is not available (scripts/install-oob-agent.sh on the kit)")

// ErrNoCellKit is returned when the host has no cell package.
var ErrNoCellKit = errors.New("cell: the kit has no cell package (meshsat-cell kit/install-cell-kit.sh)")

func (h *CellHost) call(ctx context.Context, action string, args map[string]any, timeout time.Duration) (map[string]any, error) {
	if h.agent == nil || !h.agent.Available() {
		return nil, ErrNoCellHost
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := h.agent.Call(cctx, action, args)
	if err != nil {
		if strings.Contains(err.Error(), "cell not installed") {
			return nil, ErrNoCellKit
		}
		return nil, err
	}
	return res, nil
}

func (h *CellHost) setErr(err error) {
	h.mu.Lock()
	if err != nil {
		h.lastErr = err.Error()
	} else {
		h.lastErr = ""
	}
	h.mu.Unlock()
}

// OnPlutoConnected adopts the SDR: credentials from the host, cell_0 seeded
// and enabled, the core up, the gateway bound.
func (h *CellHost) OnPlutoConnected(ctx context.Context, port string) {
	h.mu.Lock()
	if h.adopting {
		h.mu.Unlock()
		return
	}
	h.adopting = true
	h.port = port
	h.present = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.adopting = false
		h.mu.Unlock()
	}()

	log.Info().Str("port", port).Msg("cell: the cell's SDR is on USB, adopting it")
	h.mgr.emitEvent("cell", "The cell's SDR (Pluto) is on USB, adopting it")

	if h.local != nil {
		h.adoptLocal(ctx)
		return
	}

	st, err := h.call(ctx, "cell_adopt", nil, 25*time.Second)
	if err != nil {
		h.setErr(err)
		log.Warn().Err(err).Msg("cell: the Pluto is on USB but the host could not adopt it")
		h.mgr.emitEvent("cell", "Pluto on USB, not adopted: "+err.Error())
		return
	}
	smpp, _ := st["smpp"].(map[string]any)
	if smpp == nil {
		h.setErr(errors.New("cell: the host's status has no smpp block"))
		return
	}
	if err := h.seedGateway(smpp); err != nil {
		h.setErr(err)
		log.Warn().Err(err).Msg("cell: cell_0 not seeded")
		return
	}
	if _, err := h.call(ctx, "cell_core", map[string]any{"op": "start"}, 60*time.Second); err != nil {
		h.setErr(err)
		log.Warn().Err(err).Msg("cell: the core did not start")
		h.mgr.emitEvent("cell", "Cell core did not start: "+err.Error())
		return
	}
	h.mgr.ensureLink("cell", true)
	if err := h.startGateway(ctx); err != nil {
		h.setErr(err)
		log.Warn().Err(err).Msg("cell: cell_0 did not start")
		return
	}
	h.mu.Lock()
	h.adoptedAt = time.Now()
	h.lastErr = ""
	h.mu.Unlock()
	log.Info().Msg("cell: adopted; the core is up and cell_0 is binding; the radio waits for an operator")
	h.mgr.emitEvent("cell", "Cell adopted: core up, cell_0 binding; the radio waits for an operator's purpose")
}

// adoptLocal runs the cell inside the Bridge: the Pluto's network, the
// daemons, the SIMs, the radio when auto, then cell_0 bound to the local
// SMPP server with the generated password.
func (h *CellHost) adoptLocal(ctx context.Context) {
	cfg := h.local.Config()
	if err := h.local.Up(ctx); err != nil {
		h.setErr(err)
		log.Warn().Err(err).Msg("cell: the cell did not come up")
		h.mgr.emitEvent("cell", "Cell not up: "+err.Error())
		return
	}
	setCellHostPassword(h.local.Password())
	smpp := map[string]any{"bind": "127.0.0.1", "port": cell.SMPPPort, "system_id": cfg.SMPPSystemID,
		"short_code": cfg.ShortCode, "password": h.local.Password()}
	if err := h.seedGateway(smpp); err != nil {
		h.setErr(err)
		log.Warn().Err(err).Msg("cell: cell_0 not seeded")
		return
	}
	h.mgr.ensureLink("cell", true)
	if err := h.startGateway(ctx); err != nil {
		h.setErr(err)
		log.Warn().Err(err).Msg("cell: cell_0 did not start")
		return
	}
	h.mu.Lock()
	h.adoptedAt = time.Now()
	h.lastErr = ""
	h.mu.Unlock()
	radio := "the radio waits for an operator"
	if h.local.RadioActive() {
		radio = "the radio is on the air"
	} else if cfg.AutoRadio {
		radio = "the radio did not key up (see the cell status)"
	}
	log.Info().Msg("cell: adopted; core up, cell_0 binding; " + radio)
	h.mgr.emitEvent("cell", "Cell adopted: core up, cell_0 binding; "+radio)
}

// seedGateway writes cell_0 from the host's settings when there is no row,
// fills an empty host on an existing row, and enables it. The password
// stays out of the database: the gateway takes it from the host at start.
func (h *CellHost) seedGateway(smpp map[string]any) error {
	cfg := DefaultCellConfig()
	enabledBefore := false
	if row, err := h.db.GetGatewayConfigByInstance(cellInstance); err == nil && row != nil {
		if parsed, err := ParseCellConfig(row.Config); err == nil {
			cfg = *parsed
		}
		enabledBefore = row.Enabled
	}
	str := func(k string) string { v, _ := smpp[k].(string); return strings.TrimSpace(v) }
	num := func(k string) int {
		switch v := smpp[k].(type) {
		case float64:
			return int(v)
		case int:
			return v
		}
		return 0
	}
	if cfg.Host == "" {
		cfg.Host = str("bind")
	}
	if cfg.Host == "" || cfg.Host == "0.0.0.0" {
		cfg.Host = "127.0.0.1"
	}
	if p := num("port"); p > 0 && cfg.Port == DefaultCellConfig().Port {
		cfg.Port = p
	}
	if v := str("system_id"); v != "" && cfg.SystemID == DefaultCellConfig().SystemID {
		cfg.SystemID = v
	}
	if v := str("short_code"); v != "" && cfg.ShortCode == DefaultCellConfig().ShortCode {
		cfg.ShortCode = v
	}
	if pw := str("password"); pw != "" {
		setCellHostPassword(pw)
	} else if cfg.Password == "" {
		return errors.New("cell: the host has no SMPP password in cell.env")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := h.db.SaveGatewayConfigInstance("cell", cellInstance, true, string(raw)); err != nil {
		return fmt.Errorf("cell: save cell_0: %w", err)
	}
	if !enabledBefore {
		log.Info().Str("host", cfg.Host).Int("port", cfg.Port).Str("short_code", cfg.ShortCode).Msg("cell: cell_0 seeded from the host")
	}
	return nil
}

func (h *CellHost) startGateway(ctx context.Context) error {
	h.mgr.mu.RLock()
	gw, running := h.mgr.running[cellInstance]
	h.mgr.mu.RUnlock()
	if running && gw != nil {
		return nil
	}
	if running {
		return nil // a start is in flight
	}
	return h.mgr.StartGatewayInstance(ctx, cellInstance)
}

// OnPlutoRemoved lets the SDR go: radio and core down, cell_0 off.
func (h *CellHost) OnPlutoRemoved(ctx context.Context, port string) {
	h.mu.Lock()
	h.present = false
	h.port = ""
	h.mu.Unlock()
	log.Warn().Str("port", port).Msg("cell: the cell's SDR left the USB bus; radio and core down")
	h.mgr.emitEvent("cell", "The cell's SDR left: radio and core stopped, cell_0 off")
	if h.local != nil {
		h.local.Down(ctx)
	} else {
		if _, err := h.call(ctx, "cell_radio", map[string]any{"op": "stop"}, 60*time.Second); err != nil && !errors.Is(err, ErrNoCellKit) {
			log.Warn().Err(err).Msg("cell: radio stop after the Pluto left")
		}
		if _, err := h.call(ctx, "cell_core", map[string]any{"op": "stop"}, 60*time.Second); err != nil && !errors.Is(err, ErrNoCellKit) {
			log.Warn().Err(err).Msg("cell: core stop after the Pluto left")
		}
	}
	if err := h.mgr.StopGatewayInstance(cellInstance); err != nil {
		log.Debug().Err(err).Msg("cell: cell_0 was not running")
	}
	if row, err := h.db.GetGatewayConfigByInstance(cellInstance); err == nil && row != nil && row.Enabled {
		h.db.SaveGatewayConfigInstance("cell", cellInstance, false, row.Config)
	}
}

// Shutdown keys the radio down and stops the core when the Bridge stops:
// the daemons are its children and an emission never outlives the process
// that logged it.
func (h *CellHost) Shutdown(ctx context.Context) {
	if h.local != nil && h.local.IsUp() {
		h.local.Down(ctx)
	}
}

// HostStatus is what the host reports plus what the bridge knows.
type HostStatus struct {
	Agent     bool       `json:"agent"`      // the host agent answers
	Installed bool       `json:"installed"`  // the kit has the cell package
	PlutoPort string     `json:"pluto_port"` // the console tty the bridge saw the SDR on
	Present   bool       `json:"present"`    // the SDR is on USB (the bridge's view)
	AdoptedAt *time.Time `json:"adopted_at,omitempty"`
	LastError string     `json:"last_error,omitempty"`
	TXLogRows int        `json:"tx_log_rows"`
	Host      any        `json:"host,omitempty"` // the cell's status: pluto, core, radio, bts, phones, licence (cell.Status, or cellctl's JSON)
	Local     bool       `json:"local"`          // the daemons run inside the Bridge
}

// Status asks the host and merges the bridge's own view.
func (h *CellHost) Status(ctx context.Context) HostStatus {
	h.mu.Lock()
	st := HostStatus{PlutoPort: h.port, Present: h.present, LastError: h.lastErr}
	if !h.adoptedAt.IsZero() {
		t := h.adoptedAt
		st.AdoptedAt = &t
	}
	h.mu.Unlock()
	if h.local != nil {
		st.Local, st.Installed, st.Agent = true, true, true
		if n, err := h.db.CountCellTX(); err == nil {
			st.TXLogRows = n
		}
		st.Host = h.local.Status()
		return st
	}
	st.Agent = h.agent != nil && h.agent.Available()
	if !st.Agent {
		return st
	}
	res, err := h.call(ctx, "cell_status", nil, 25*time.Second)
	switch {
	case errors.Is(err, ErrNoCellKit):
		st.Installed = false
	case err != nil:
		if st.LastError == "" {
			st.LastError = err.Error()
		}
	default:
		st.Installed = true
		delete(res, "ok")
		st.Host = res
	}
	return st
}

// Radio keys the radio up ("start", with the purpose the TX log records)
// or down ("stop"). The host runs the preflight and refuses with its reason.
func (h *CellHost) Radio(ctx context.Context, op, purpose string) (map[string]any, error) {
	switch op {
	case "start":
		purpose = strings.TrimSpace(purpose)
		if len(purpose) < 3 {
			return nil, errors.New("cell: a purpose for the TX log is required to key the radio up")
		}
		if h.local != nil {
			if err := h.local.RadioStart(ctx, purpose); err != nil {
				h.mgr.emitEvent("cell", "Radio NOT keyed up: "+err.Error())
				return nil, err
			}
			h.mgr.emitEvent("cell", "Radio keyed up: "+purpose)
			return map[string]any{"radio": "up", "arfcn": []int{h.local.Config().ARFCN}, "licence": h.local.Config().Licence}, nil
		}
		res, err := h.call(ctx, "cell_radio", map[string]any{"op": "start", "purpose": purpose}, 90*time.Second)
		if err != nil {
			h.mgr.emitEvent("cell", "Radio NOT keyed up: "+err.Error())
			return nil, err
		}
		h.mgr.emitEvent("cell", "Radio keyed up: "+purpose)
		return res, nil
	case "stop":
		if h.local != nil {
			if err := h.local.RadioStop(ctx); err != nil {
				return nil, err
			}
			h.mgr.emitEvent("cell", "Radio keyed down")
			return map[string]any{"radio": "down"}, nil
		}
		res, err := h.call(ctx, "cell_radio", map[string]any{"op": "stop"}, 60*time.Second)
		if err != nil {
			return nil, err
		}
		h.mgr.emitEvent("cell", "Radio keyed down")
		return res, nil
	}
	return nil, fmt.Errorf("cell: unknown radio action %q (start or stop)", op)
}

// Core starts or stops the core without the radio.
func (h *CellHost) Core(ctx context.Context, op string) (map[string]any, error) {
	if op != "start" && op != "stop" {
		return nil, fmt.Errorf("cell: unknown core action %q (start or stop)", op)
	}
	if h.local != nil {
		if op == "stop" {
			h.local.Down(ctx)
			return map[string]any{"core": "down"}, nil
		}
		if err := h.local.UpCore(ctx); err != nil {
			return nil, err
		}
		setCellHostPassword(h.local.Password())
		if err := h.startGateway(ctx); err != nil {
			log.Warn().Err(err).Msg("cell: cell_0 did not start with the core")
		}
		return map[string]any{"core": "up"}, nil
	}
	res, err := h.call(ctx, "cell_core", map[string]any{"op": op}, 60*time.Second)
	if err != nil {
		return nil, err
	}
	if op == "start" {
		if err := h.startGateway(ctx); err != nil {
			log.Warn().Err(err).Msg("cell: cell_0 did not start with the core")
		}
	}
	return res, nil
}

// --- the password the host hands over, kept in memory only ---

var (
	cellHostPasswordMu sync.Mutex
	cellHostPassword   string
)

func setCellHostPassword(pw string) {
	cellHostPasswordMu.Lock()
	cellHostPassword = pw
	cellHostPasswordMu.Unlock()
}

// CellHostPassword is the SMPP password the host handed over at adoption,
// empty when none did.
func CellHostPassword() string {
	cellHostPasswordMu.Lock()
	defer cellHostPasswordMu.Unlock()
	return cellHostPassword
}

// emitEvent is the manager's SSE emitter, nil-safe.
func (m *Manager) emitEvent(eventType, message string) {
	if m != nil && m.onEventEmit != nil {
		m.onEventEmit(eventType, message)
	}
}
