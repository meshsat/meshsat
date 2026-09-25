package routing

// IfaceManager: the runtime lifecycle of the dynamic Reticulum interface
// types (rnode, udp, auto, kiss), persisted in routing_ifaces and driven
// from Settings > Routing without a restart. Environment variables seed a
// first instance on a fresh database; after that the table is the truth.
// [MESHSAT-1350]

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/reticulum"
	"meshsat/internal/rnode"
)

// Dynamic interface types.
const (
	DynTypeRNode = "rnode"
	DynTypeUDP   = "udp"
	DynTypeAuto  = "auto"
	DynTypeKISS  = "kiss"
)

// DynTypes lists the types the manager knows, in UI order.
var DynTypes = []string{DynTypeRNode, DynTypeUDP, DynTypeAuto, DynTypeKISS}

// PacketSink is the processor side the manager plugs interfaces into.
type PacketSink interface {
	InjectReticulumPacket(packet []byte, sourceIface string)
	RegisterPacketSender(ifaceID string, fn func(ctx context.Context, data []byte) error)
	UnregisterPacketSender(ifaceID string)
}

// dynIface is what every managed instance exposes to the manager.
type dynIface interface {
	Start(ctx context.Context) error
	Stop()
	Send(ctx context.Context, packet []byte) error
	IsOnline() bool
}

// IfaceManagerConfig wires the manager.
type IfaceManagerConfig struct {
	DB         *database.DB
	Registry   *InterfaceRegistry
	Sink       PacketSink
	BLEAdapter string
	// OnRNodeSupervised is called for an RNode instance whose port is
	// "auto": main.go arms the device supervisor's RNode probe and routes
	// port found/lost callbacks to it. Nil disables auto ports.
	OnRNodeSupervised func(id string, r *RNodeInterface)
	// OnRNodeUnsupervised undoes the above when the instance stops.
	OnRNodeUnsupervised func(id string)
	// ExcludePort keeps the device supervisor off a KISS TNC's serial port.
	ExcludePort func(path string)
}

// DynIfaceStatus is the API view of one managed interface.
type DynIfaceStatus struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Enabled   bool            `json:"enabled"`
	Running   bool            `json:"running"`
	Online    bool            `json:"online"`
	Config    json.RawMessage `json:"config"`
	LastError string          `json:"last_error,omitempty"`
	Summary   string          `json:"summary"`
	Stats     any             `json:"stats,omitempty"`
	UpdatedAt string          `json:"updated_at,omitempty"`
}

type dynInstance struct {
	row     database.RoutingIface
	iface   dynIface
	ri      *ReticulumInterface
	running bool
	lastErr string
	summary string
	cancel  context.CancelFunc
}

// IfaceManager owns the dynamic interfaces.
type IfaceManager struct {
	cfg  IfaceManagerConfig
	mu   sync.Mutex
	inst map[string]*dynInstance
	ctx  context.Context
}

// NewIfaceManager creates the manager.
func NewIfaceManager(cfg IfaceManagerConfig) *IfaceManager {
	return &IfaceManager{cfg: cfg, inst: make(map[string]*dynInstance)}
}

// ValidateDynConfig parses and checks a type's JSON config, returning the
// normalised JSON. It is what the API calls before saving.
func ValidateDynConfig(ifType string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	switch ifType {
	case DynTypeRNode:
		var c RNodeInterfaceConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("rnode config: %w", err)
		}
		if c.Port == "" {
			c.Port = "auto"
		}
		if c.Preset != "" {
			p := rnode.PresetByID(c.Preset)
			if p == nil {
				return nil, fmt.Errorf("unknown preset %q", c.Preset)
			}
			if c.Params.Frequency == 0 {
				c.Params.Frequency = p.Frequency
			}
			if c.Params.Bandwidth == 0 {
				c.Params.Bandwidth = p.Bandwidth
			}
			if c.Params.SF == 0 {
				c.Params.SF = p.SF
			}
			if c.Params.CR == 0 {
				c.Params.CR = p.CR
			}
			if c.Params.TXPower == 0 {
				c.Params.TXPower = p.TXPower
			}
		}
		if err := c.Params.Validate(); err != nil {
			return nil, err
		}
		if c.IDCallsign != "" && c.IDInterval == 0 {
			c.IDInterval = 10 * time.Minute
		}
		return json.Marshal(c)
	case DynTypeUDP:
		var c UDPInterfaceConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("udp config: %w", err)
		}
		if c.Device == "" && c.ForwardAddr == "" {
			return nil, fmt.Errorf("udp: a device or a forward address is required")
		}
		if c.Device == "wlan0" {
			return nil, fmt.Errorf("udp: wlan0 is the management path and may not carry a Reticulum interface")
		}
		return json.Marshal(c)
	case DynTypeAuto:
		var c AutoInterfaceConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("auto config: %w", err)
		}
		if c.GroupID == "" {
			c.GroupID = AutoDefaultGroup
		}
		if len(c.Devices) == 0 {
			c.Devices = []string{"eth0"}
		}
		for _, d := range c.Devices {
			if d == "wlan0" {
				return nil, fmt.Errorf("auto: wlan0 is the management path and may not carry a Reticulum interface")
			}
		}
		return json.Marshal(c)
	case DynTypeKISS:
		var c KISSInterfaceConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("kiss config: %w", err)
		}
		if c.Port == "" {
			return nil, fmt.Errorf("kiss: port required (/dev/... or tcp://host:port)")
		}
		return json.Marshal(c)
	}
	return nil, fmt.Errorf("unknown interface type %q", ifType)
}

// Load reads the table and starts every enabled instance.
func (m *IfaceManager) Load(ctx context.Context) error {
	m.ctx = ctx
	rows, err := m.cfg.DB.ListRoutingIfaces()
	if err != nil {
		return err
	}
	for _, r := range rows {
		m.mu.Lock()
		m.inst[r.ID] = &dynInstance{row: r}
		m.mu.Unlock()
		if r.Enabled {
			if err := m.start(r.ID); err != nil {
				log.Warn().Err(err).Str("iface", r.ID).Msg("ifacemgr: start failed")
			}
		}
	}
	return nil
}

// Seed creates an instance from environment defaults when the table holds
// no instance of that type yet. Returns the id, or "" when nothing was done.
func (m *IfaceManager) Seed(ifType string, cfg any) (string, error) {
	rows, err := m.cfg.DB.ListRoutingIfaces()
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		if r.Type == ifType {
			return "", nil
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	id, err := m.Create(ifType, raw, true)
	if err != nil {
		return "", err
	}
	log.Info().Str("iface", id).Msg("ifacemgr: seeded from environment; edit it under Settings > Routing from now on")
	return id, nil
}

// Create validates, persists and (if enabled) starts a new instance.
func (m *IfaceManager) Create(ifType string, raw json.RawMessage, enabled bool) (string, error) {
	norm, err := ValidateDynConfig(ifType, raw)
	if err != nil {
		return "", err
	}
	id, err := m.cfg.DB.NextRoutingIfaceID(ifType)
	if err != nil {
		return "", err
	}
	if m.cfg.Registry != nil && m.cfg.Registry.Get(id) != nil {
		return "", fmt.Errorf("interface id %s is already registered", id)
	}
	row := database.RoutingIface{ID: id, Type: ifType, Enabled: enabled, Config: string(norm)}
	if err := m.cfg.DB.SaveRoutingIface(row); err != nil {
		return "", err
	}
	m.mu.Lock()
	m.inst[id] = &dynInstance{row: row}
	m.mu.Unlock()
	if enabled {
		if err := m.start(id); err != nil {
			return id, err
		}
	}
	return id, nil
}

// Update replaces an instance's config and enabled flag and restarts it.
func (m *IfaceManager) Update(id string, raw json.RawMessage, enabled bool) error {
	m.mu.Lock()
	in, ok := m.inst[id]
	m.mu.Unlock()
	if !ok {
		return database.ErrRoutingIfaceNotFound
	}
	if raw == nil {
		raw = json.RawMessage(in.row.Config)
	}
	norm, err := ValidateDynConfig(in.row.Type, raw)
	if err != nil {
		return err
	}
	m.stop(id)
	row := in.row
	row.Enabled = enabled
	row.Config = string(norm)
	if err := m.cfg.DB.SaveRoutingIface(row); err != nil {
		return err
	}
	m.mu.Lock()
	in.row = row
	m.mu.Unlock()
	if enabled {
		return m.start(id)
	}
	return nil
}

// Restart stops and starts an instance.
func (m *IfaceManager) Restart(id string) error {
	m.mu.Lock()
	in, ok := m.inst[id]
	m.mu.Unlock()
	if !ok {
		return database.ErrRoutingIfaceNotFound
	}
	m.stop(id)
	if !in.row.Enabled {
		return fmt.Errorf("%s is disabled", id)
	}
	return m.start(id)
}

// Delete stops and removes an instance.
func (m *IfaceManager) Delete(id string) error {
	m.stop(id)
	if err := m.cfg.DB.DeleteRoutingIface(id); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.inst, id)
	m.mu.Unlock()
	return nil
}

// Get returns one instance's status.
func (m *IfaceManager) Get(id string) (*DynIfaceStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.inst[id]
	if !ok {
		return nil, database.ErrRoutingIfaceNotFound
	}
	s := m.status(in)
	return &s, nil
}

// List returns every instance's status, ordered by id.
func (m *IfaceManager) List() []DynIfaceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DynIfaceStatus, 0, len(m.inst))
	for _, in := range m.inst {
		out = append(out, m.status(in))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// RNode returns the RNode instance behind an id (for the supervisor hooks).
func (m *IfaceManager) RNode(id string) *RNodeInterface {
	m.mu.Lock()
	defer m.mu.Unlock()
	if in, ok := m.inst[id]; ok {
		if r, ok := in.iface.(*RNodeInterface); ok {
			return r
		}
	}
	return nil
}

// StopAll stops every running instance (shutdown).
func (m *IfaceManager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.inst))
	for id := range m.inst {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.stop(id)
	}
}

func (m *IfaceManager) status(in *dynInstance) DynIfaceStatus {
	s := DynIfaceStatus{
		ID: in.row.ID, Type: in.row.Type, Enabled: in.row.Enabled, Running: in.running,
		Config: json.RawMessage(in.row.Config), LastError: in.lastErr, Summary: in.summary, UpdatedAt: in.row.UpdatedAt,
	}
	if in.iface != nil && in.running {
		s.Online = in.iface.IsOnline()
		switch x := in.iface.(type) {
		case *RNodeInterface:
			st := x.Stats()
			s.Stats = map[string]any{"rnode": st, "port": x.Port(), "bitrate": x.Bitrate()}
			if le := x.LastError(); le != "" {
				s.LastError = le
			}
		case *UDPInterface:
			rx, tx := x.Counters()
			s.Stats = map[string]any{"rx_bytes": rx, "tx_bytes": tx}
		case *AutoInterface:
			s.Stats = map[string]any{"peers": x.Peers(), "peer_count": x.PeerCount(), "group": x.GroupHex()}
		case *KISSInterface:
			rx, tx := x.Counters()
			s.Stats = map[string]any{"rx_bytes": rx, "tx_bytes": tx}
			if le := x.LastError(); le != "" {
				s.LastError = le
			}
		}
	}
	return s
}

func (m *IfaceManager) build(row database.RoutingIface) (dynIface, reticulum.InterfaceType, int, string, error) {
	sink := m.cfg.Sink
	cb := func(packet []byte) {
		if sink != nil {
			sink.InjectReticulumPacket(packet, row.ID)
		}
	}
	switch row.Type {
	case DynTypeRNode:
		var c RNodeInterfaceConfig
		if err := json.Unmarshal([]byte(row.Config), &c); err != nil {
			return nil, "", 0, "", err
		}
		c.Name = row.ID
		if c.BLEAdapter == "" {
			c.BLEAdapter = m.cfg.BLEAdapter
		}
		sum := fmt.Sprintf("%s, %.3f MHz, BW %d kHz, SF%d, CR%d, %d dBm", c.Port, float64(c.Params.Frequency)/1e6, c.Params.Bandwidth/1000, c.Params.SF, c.Params.CR, c.Params.TXPower)
		return NewRNodeInterface(c, cb), reticulum.IfaceRNode, reticulum.MTU, sum, nil
	case DynTypeUDP:
		var c UDPInterfaceConfig
		if err := json.Unmarshal([]byte(row.Config), &c); err != nil {
			return nil, "", 0, "", err
		}
		c.Name = row.ID
		sum := strings.TrimSpace(fmt.Sprintf("%s listen %s forward %s", c.Device, c.ListenAddr, c.ForwardAddr))
		return NewUDPInterface(c, cb), reticulum.IfaceUDP, UDPHWMTU, sum, nil
	case DynTypeAuto:
		var c AutoInterfaceConfig
		if err := json.Unmarshal([]byte(row.Config), &c); err != nil {
			return nil, "", 0, "", err
		}
		c.Name = row.ID
		sum := fmt.Sprintf("group %s on %s", c.GroupID, strings.Join(c.Devices, ", "))
		return NewAutoInterface(c, cb), reticulum.IfaceAuto, AutoHWMTU, sum, nil
	case DynTypeKISS:
		var c KISSInterfaceConfig
		if err := json.Unmarshal([]byte(row.Config), &c); err != nil {
			return nil, "", 0, "", err
		}
		c.Name = row.ID
		if m.cfg.ExcludePort != nil && !strings.HasPrefix(strings.ToLower(c.Port), "tcp://") {
			m.cfg.ExcludePort(c.Port)
		}
		sum := fmt.Sprintf("%s, %d baud", c.Port, c.Baud)
		return NewKISSInterface(c, cb), reticulum.IfaceKISS, KISSHWMTU, sum, nil
	}
	return nil, "", 0, "", fmt.Errorf("unknown interface type %q", row.Type)
}

func (m *IfaceManager) start(id string) error {
	m.mu.Lock()
	in, ok := m.inst[id]
	if !ok {
		m.mu.Unlock()
		return database.ErrRoutingIfaceNotFound
	}
	if in.running {
		m.mu.Unlock()
		return nil
	}
	row := in.row
	m.mu.Unlock()

	iface, itype, mtu, summary, err := m.build(row)
	if err != nil {
		m.setErr(id, err)
		return err
	}
	base := m.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	if err := iface.Start(ctx); err != nil {
		cancel()
		m.setErr(id, err)
		return err
	}
	if m.cfg.Sink != nil {
		m.cfg.Sink.RegisterPacketSender(id, iface.Send)
	}
	var ri *ReticulumInterface
	if m.cfg.Registry != nil {
		ri = NewReticulumInterface(id, itype, mtu, iface.Send)
		ri.SetOnlineFunc(iface.IsOnline)
		if r, ok := iface.(*RNodeInterface); ok {
			ri.SetBitrateFunc(r.Bitrate)
		}
		m.cfg.Registry.Register(ri)
	}
	if r, ok := iface.(*RNodeInterface); ok && r.NeedsSupervisor() && m.cfg.OnRNodeSupervised != nil {
		m.cfg.OnRNodeSupervised(id, r)
	}
	m.mu.Lock()
	in.iface = iface
	in.ri = ri
	in.running = true
	in.lastErr = ""
	in.summary = summary
	in.cancel = cancel
	m.mu.Unlock()
	log.Info().Str("iface", id).Str("type", row.Type).Str("summary", summary).Msg("ifacemgr: interface started")
	return nil
}

func (m *IfaceManager) stop(id string) {
	m.mu.Lock()
	in, ok := m.inst[id]
	if !ok || !in.running {
		m.mu.Unlock()
		return
	}
	iface, cancel := in.iface, in.cancel
	in.running = false
	in.iface = nil
	in.ri = nil
	in.cancel = nil
	m.mu.Unlock()

	if r, ok := iface.(*RNodeInterface); ok && r.NeedsSupervisor() && m.cfg.OnRNodeUnsupervised != nil {
		m.cfg.OnRNodeUnsupervised(id)
	}
	if m.cfg.Registry != nil {
		m.cfg.Registry.Unregister(id)
	}
	if m.cfg.Sink != nil {
		m.cfg.Sink.UnregisterPacketSender(id)
	}
	iface.Stop()
	if cancel != nil {
		cancel()
	}
	log.Info().Str("iface", id).Msg("ifacemgr: interface stopped")
}

func (m *IfaceManager) setErr(id string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if in, ok := m.inst[id]; ok {
		in.lastErr = err.Error()
	}
}
