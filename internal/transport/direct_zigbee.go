package transport

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"go.bug.st/serial"

	"meshsat/internal/database"
)

// Known ZigBee coordinator VID:PID pairs.
// Note: CP210x (10c4:ea60) and CH343 (1a86:55d4) overlap with Meshtastic —
// protocol probing is required to disambiguate.
var knownZigBeeVIDPIDs = map[string]bool{
	"10c4:ea60": true, // CP210x (SONOFF ZBDongle-P, CC2652P)
	"1a86:55d4": true, // CH9102 (SONOFF ZBDongle-E, EFR32MG21)
	"10c4:8a2a": true, // CP2102N (ConBee II, CC2538+CC2592)
	"0451:16a8": true, // TI CC2531 (older ZigBee stick)
	"1cf1:0030": true, // dresden elektronik ConBee/RaspBee
}

// ZigBeeDevice holds information about a paired ZigBee device.
type ZigBeeDevice struct {
	ShortAddr    uint16    `json:"short_addr"`
	IEEEAddr     string    `json:"ieee_addr"` // hex-encoded 8-byte IEEE address
	Alias        string    `json:"alias"`     // user-given name; falls back to "" until renamed [MESHSAT-509]
	Manufacturer string    `json:"manufacturer,omitempty"`
	Model        string    `json:"model,omitempty"`
	Endpoint     byte      `json:"endpoint"`
	LQI          byte      `json:"lqi"`
	LastSeen     time.Time `json:"last_seen"`
	Temperature  *float64  `json:"temperature,omitempty"` // Celsius (from cluster 0x0402) [MESHSAT-511]
	Humidity     *float64  `json:"humidity,omitempty"`    // percent (from cluster 0x0405) [MESHSAT-511]
	BatteryPct   int       `json:"battery_pct"`           // 0-100, -1 = unknown (from cluster 0x0001 attr 0x0021) [MESHSAT-509]
	OnOff        int       `json:"onoff"`                 // 0/1, -1 = unknown (from cluster 0x0006) [MESHSAT-509]
	ZoneStatus   int       `json:"zone_status"`           // -1 unknown, otherwise 16-bit IAS Zone bitmask [MESHSAT-509]
}

// ZigBeeEvent is emitted when data arrives from a ZigBee device.
type ZigBeeEvent struct {
	Type        string       `json:"type"` // "data", "join", "leave", "temperature", "humidity", "onoff", "battery", "ias_zone"
	Device      ZigBeeDevice `json:"device"`
	ClusterID   uint16       `json:"cluster_id"`
	Data        []byte       `json:"data"`
	Timestamp   time.Time    `json:"timestamp"`
	Temperature *float64     `json:"temperature,omitempty"` // decoded Celsius [MESHSAT-511]
	Humidity    *float64     `json:"humidity,omitempty"`    // decoded percent [MESHSAT-511]
	BatteryPct  *int         `json:"battery_pct,omitempty"` // decoded 0-100 [MESHSAT-509]
	OnOff       *bool        `json:"onoff,omitempty"`       // decoded boolean [MESHSAT-509]
	ZoneStatus  *ZoneStatus  `json:"zone_status,omitempty"` // IAS Zone status flags [MESHSAT-509]
}

// ZoneStatus is the decoded IAS Zone Status Change Notification (cluster
// 0x0500, cmd 0x00). The Raw field holds the 16-bit bitmask straight off
// the wire; the named booleans are convenience accessors. [MESHSAT-509]
//
// Bit map (ZCL 8.2.2.4.1):
//
//	0x0001 Alarm1          — primary alarm (motion / contact open / leak detected)
//	0x0002 Alarm2          — secondary alarm
//	0x0004 Tamper          — device housing has been opened
//	0x0008 BatteryLow      — battery below threshold
//	0x0010 SupervisionRpts — device supports supervision reporting
//	0x0020 RestoreRpts     — device supports restore reporting
//	0x0040 Trouble         — sensor not operating correctly
//	0x0080 ACMainsFault    — sensor's AC mains supply has failed
//	0x0100 TestMode        — sensor in test mode
//	0x0200 BatteryDefect   — battery defect detected
type ZoneStatus struct {
	Raw           uint16 `json:"raw"`
	Alarm1        bool   `json:"alarm1"` // motion / contact / leak
	Alarm2        bool   `json:"alarm2"`
	Tamper        bool   `json:"tamper"`
	BatteryLow    bool   `json:"battery_low"`
	Trouble       bool   `json:"trouble"`
	ACMainsFault  bool   `json:"ac_mains_fault"`
	TestMode      bool   `json:"test_mode"`
	BatteryDefect bool   `json:"battery_defect"`
	// Triggered is true if any user-visible alarm bit is set
	// (Alarm1 or Alarm2 or Tamper). Used to drive the UI badge.
	Triggered bool `json:"triggered"`
}

func decodeZoneStatus(raw uint16) ZoneStatus {
	zs := ZoneStatus{
		Raw:           raw,
		Alarm1:        raw&0x0001 != 0,
		Alarm2:        raw&0x0002 != 0,
		Tamper:        raw&0x0004 != 0,
		BatteryLow:    raw&0x0008 != 0,
		Trouble:       raw&0x0040 != 0,
		ACMainsFault:  raw&0x0080 != 0,
		TestMode:      raw&0x0100 != 0,
		BatteryDefect: raw&0x0200 != 0,
	}
	zs.Triggered = zs.Alarm1 || zs.Alarm2 || zs.Tamper
	return zs
}

// ZCL cluster IDs we decode [MESHSAT-509, MESHSAT-511]
const (
	ZCLClusterPowerCfg    = 0x0001 // PowerConfiguration — battery percent
	ZCLClusterOnOff       = 0x0006 // On/Off — switch/light state
	ZCLClusterLevelCtrl   = 0x0008 // Level Control — dimmer brightness
	ZCLClusterColorCtrl   = 0x0300 // Color Control — RGB / color temperature
	ZCLClusterIASZone     = 0x0500 // IAS Zone — door/motion/leak sensors
	ZCLClusterTemperature = 0x0402
	ZCLClusterHumidity    = 0x0405
	ZCLAttrBatteryPercent = 0x0021 // PowerConfiguration cluster
	ZCLAttrOnOffState     = 0x0000 // OnOff cluster
	ZCLAttrMeasuredValue  = 0x0000 // Temp/Humidity clusters
	ZCLAttrZoneStatus     = 0x0002 // IAS Zone — current zone status (uint16)
)

// ZigBeeStore is the persistence interface implemented by *database.DB. The
// transport keeps it nullable so unit tests can run without a sqlite handle.
type ZigBeeStore interface {
	UpsertZigBeeDevice(d *databaseZigBeeDevice) error
	InsertZigBeeSensorReading(r *databaseZigBeeSensorReading) error
	ListZigBeeDevices() ([]databaseZigBeeDevice, error)
	SetZigBeeDeviceAlias(ieeeAddr, alias string) error
}

// Aliased here to avoid a hard import in test mocks. The concrete type is
// database.ZigBeeDevice / database.ZigBeeSensorReading.
type (
	databaseZigBeeDevice        = database.ZigBeeDevice
	databaseZigBeeSensorReading = database.ZigBeeSensorReading
)

// DirectZigBeeTransport manages a CC2652P Z-Stack coordinator over serial.
type DirectZigBeeTransport struct {
	mu          sync.Mutex
	port        serial.Port
	portName    string
	running     bool
	cancelFn    context.CancelFunc
	devices     map[uint16]*ZigBeeDevice // shortAddr → device
	coordState  byte                     // ZNP device state
	transID     byte                     // incrementing transaction ID
	subscribers []chan ZigBeeEvent
	subMu       sync.RWMutex

	// serialMu guards synchronous ZNP request/response exchanges.
	// Both PermitJoin and Send lock this to prevent the readLoop from
	// stealing their SRSP responses. The readLoop also locks it for each
	// Read call so it yields during synchronous commands. [MESHSAT-510]
	serialMu sync.Mutex

	// State-change waiters — pattern borrowed from zigbee-herdsman:
	// register a waiter BEFORE sending a command that triggers an AREQ,
	// then await the waiter with a timeout. Used by initCoordinator to
	// wait for ZDO_STATE_CHANGE_IND state=0x09 (DEV_ZB_COORD) after
	// ZDO_STARTUP_FROM_APP, which is what zigbee-herdsman does.
	stateWaitersMu sync.Mutex
	stateWaiters   []chan byte

	// Reset-recovery: when the coordinator emits an unsolicited
	// SYS_RESET_IND (watchdog, hard fault, external reset), the network
	// is gone. reinitPending is set by handleFrame and consumed by the
	// reinitLoop goroutine, which reruns initCoordinator under serialMu
	// to bring the network back up without restarting the gateway.
	reinitPending chan struct{}

	// Permit-join state
	permitJoinEnd time.Time // when permit-join expires (zero = not active)

	// Firmware info (populated after init)
	FirmwareVersion string

	// db is the persistence backend. nil-safe: when unset (e.g. unit tests
	// that exercise only the protocol layer), the transport keeps everything
	// in memory and skips DB writes. Set via SetStore() before Start().
	db ZigBeeStore

	// readLoopIterCount increments on every readLoop iteration regardless
	// of whether bytes were received. The watchdog samples it twice across
	// stuckReadThreshold; if it hasn't advanced, readLoop is stuck inside
	// read(2) (the cp210x wedge bug — Select reports readable + read blocks
	// on VMIN=1) and we USBDEVFS_RESET the port to force read(2) to return
	// EBADF and release serialMu.
	//
	// An earlier version of this watchdog checked "last byte received time"
	// instead of iteration count, and fell back to a TryLock contention
	// check to gate the reset. That was wrong on two counts: (a) idle
	// networks legitimately go minutes between bytes, and (b) readLoop
	// holds serialMu nearly continuously (releases only briefly between
	// iterations) so TryLock always reports contention. The combined effect
	// was that the watchdog fired on every quiet network, which on parallax
	// killed coordinator init in a loop. The iteration counter is the
	// simpler and correct signal — readLoop ALWAYS advances when healthy,
	// even when reading 0 bytes. [MESHSAT-509]
	readLoopIterCount atomic.Uint64
}

// stuckReadThreshold is how long readLoop can sit in read(2) without
// receiving any bytes before the watchdog declares the cp210x driver
// wedged and triggers USBDEVFS_RESET. Long enough that idle networks
// (no traffic, no scheduled reports) don't trigger spurious resets, short
// enough that real wedges unstick within a UI poll cycle.
const stuckReadThreshold = 90 * time.Second

// NewDirectZigBeeTransport creates a new ZigBee transport.
func NewDirectZigBeeTransport() *DirectZigBeeTransport {
	return &DirectZigBeeTransport{
		devices:       make(map[uint16]*ZigBeeDevice),
		reinitPending: make(chan struct{}, 1),
	}
}

// SetStore wires a persistence backend (typically *database.DB). Must be
// called before Start() so the device-cache hydration runs at boot.
func (z *DirectZigBeeTransport) SetStore(s ZigBeeStore) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.db = s
}

// hydrateFromStore loads previously-paired devices from the DB into the
// in-memory map so the gateway can serve /api/zigbee/devices and resolve
// short→IEEE bindings immediately on startup, before any device sends a
// fresh announce. Called once from Start().
func (z *DirectZigBeeTransport) hydrateFromStore() {
	if z.db == nil {
		return
	}
	devs, err := z.db.ListZigBeeDevices()
	if err != nil {
		log.Warn().Err(err).Msg("zigbee: hydrate from DB failed")
		return
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	for _, d := range devs {
		short := uint16(d.ShortAddr)
		dev := &ZigBeeDevice{
			ShortAddr:    short,
			IEEEAddr:     d.IEEEAddr,
			Alias:        d.Alias,
			Manufacturer: d.Manufacturer,
			Model:        d.Model,
			Endpoint:     byte(d.Endpoint),
			LQI:          byte(d.LQI),
			Temperature:  d.LastTemp,
			Humidity:     d.LastHumidity,
			BatteryPct:   d.BatteryPct,
			OnOff:        d.LastOnOff,
			ZoneStatus:   d.LastZoneStatus,
		}
		if t, err := time.Parse("2006-01-02 15:04:05", d.LastSeen); err == nil {
			dev.LastSeen = t
		}
		z.devices[short] = dev
	}
	log.Info().Int("count", len(devs)).Msg("zigbee: hydrated paired devices from DB")
}

// persistDevice writes the in-memory device record to the DB. Nil-safe.
// Caller must hold z.mu (we read pointer fields under the lock implicitly).
func (z *DirectZigBeeTransport) persistDevice(dev *ZigBeeDevice) {
	if z.db == nil || dev == nil {
		return
	}
	rec := &database.ZigBeeDevice{
		IEEEAddr:       dev.IEEEAddr,
		ShortAddr:      int(dev.ShortAddr),
		Alias:          dev.Alias,
		Manufacturer:   dev.Manufacturer,
		Model:          dev.Model,
		Endpoint:       int(dev.Endpoint),
		LQI:            int(dev.LQI),
		BatteryPct:     dev.BatteryPct,
		LastTemp:       dev.Temperature,
		LastHumidity:   dev.Humidity,
		LastOnOff:      dev.OnOff,
		LastZoneStatus: dev.ZoneStatus,
	}
	if rec.IEEEAddr == "" {
		// Some devices report sensor data before sending their announce frame
		// — we'd rather wait for the IEEE binding than create a row keyed on
		// an empty string (which would collide across all such devices).
		return
	}
	if err := z.db.UpsertZigBeeDevice(rec); err != nil {
		log.Warn().Err(err).Str("ieee", dev.IEEEAddr).Msg("zigbee: persist device failed")
	}
}

// recordReading appends one row to the sensor time-series. Nil-safe.
func (z *DirectZigBeeTransport) recordReading(ieeeAddr string, cluster, attr uint16, valueNum *float64, valueText, unit string, lqi byte) {
	if z.db == nil || ieeeAddr == "" {
		return
	}
	r := &database.ZigBeeSensorReading{
		IEEEAddr:  ieeeAddr,
		Cluster:   int(cluster),
		Attribute: int(attr),
		ValueNum:  valueNum,
		ValueText: valueText,
		Unit:      unit,
		LQI:       int(lqi),
	}
	if err := z.db.InsertZigBeeSensorReading(r); err != nil {
		log.Warn().Err(err).Str("ieee", ieeeAddr).Msg("zigbee: persist reading failed")
	}
}

// reopenPort (re)opens the serial port named by z.portName. Closes any
// existing port first. Used by reinitLoop to escape the CP210x driver's
// "stuck read" state after the CC2652P does an unsolicited hard reset —
// without a close/reopen, read(2) on the surviving fd can block for
// minutes despite SetReadTimeout. Caller must hold z.serialMu.
func (z *DirectZigBeeTransport) reopenPort() error {
	z.mu.Lock()
	portName := z.portName
	old := z.port
	z.mu.Unlock()

	if old != nil {
		_ = old.Close()
	}

	mode := &serial.Mode{
		BaudRate: 115200,
		DataBits: 8,
		StopBits: serial.OneStopBit,
		Parity:   serial.NoParity,
	}
	p, err := serial.Open(portName, mode)
	if err != nil {
		return fmt.Errorf("reopen %s: %w", portName, err)
	}

	// Drain any stale data from the kernel buffer and give the CC2652P
	// a beat to finish its power-up sequence.
	p.SetReadTimeout(200 * time.Millisecond)
	drain := make([]byte, 256)
	for {
		n, _ := p.Read(drain)
		if n == 0 {
			break
		}
	}
	time.Sleep(200 * time.Millisecond)

	z.mu.Lock()
	z.port = p
	z.mu.Unlock()
	log.Debug().Str("port", portName).Msg("zigbee: serial port reopened")
	return nil
}

// watchStateChange registers a listener that receives the next
// ZDO_STATE_CHANGE_IND byte(s). The caller must call unsub when done.
// Implementation note: this is the Go equivalent of zigbee-herdsman's
// znp.waitFor(AREQ, ZDO, "stateChangeInd", ..., 9, 60000) pattern — register
// BEFORE sending the startup command, otherwise the state change can arrive
// before the waiter is set up and the coordinator is stuck in an unknown
// state from our perspective.
func (z *DirectZigBeeTransport) watchStateChange() (<-chan byte, func()) {
	ch := make(chan byte, 8)
	z.stateWaitersMu.Lock()
	z.stateWaiters = append(z.stateWaiters, ch)
	z.stateWaitersMu.Unlock()
	unsub := func() {
		z.stateWaitersMu.Lock()
		defer z.stateWaitersMu.Unlock()
		for i, c := range z.stateWaiters {
			if c == ch {
				z.stateWaiters = append(z.stateWaiters[:i], z.stateWaiters[i+1:]...)
				break
			}
		}
	}
	return ch, unsub
}

// notifyStateChange fans a new device-state byte out to all active waiters.
func (z *DirectZigBeeTransport) notifyStateChange(state byte) {
	z.stateWaitersMu.Lock()
	defer z.stateWaitersMu.Unlock()
	for _, ch := range z.stateWaiters {
		select {
		case ch <- state:
		default:
		}
	}
}

// IsReady reports whether the coordinator is in DEV_ZB_COORD state —
// the only state where ZDO requests like PERMIT_JOIN will succeed.
func (z *DirectZigBeeTransport) IsReady() bool {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.running && z.coordState == ZNPDevStateCoord
}

// CoordState returns the current cached device state (0x00..0x09).
func (z *DirectZigBeeTransport) CoordState() byte {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.coordState
}

// Subscribe returns a channel that receives ZigBee events.
func (z *DirectZigBeeTransport) Subscribe() chan ZigBeeEvent {
	z.subMu.Lock()
	defer z.subMu.Unlock()
	ch := make(chan ZigBeeEvent, 32)
	z.subscribers = append(z.subscribers, ch)
	return ch
}

// emit sends an event to all subscribers.
func (z *DirectZigBeeTransport) emit(evt ZigBeeEvent) {
	z.subMu.RLock()
	defer z.subMu.RUnlock()
	for _, ch := range z.subscribers {
		select {
		case ch <- evt:
		default:
		}
	}
}

// Start opens the serial port and initializes the Z-Stack coordinator.
//
// We intentionally release z.mu around initCoordinator — init may take 60s
// waiting for DEV_ZB_COORD and it needs to update z.coordState via z.mu
// while running. Holding z.mu across that call would deadlock. The
// serialMu + the "first-caller" guard below are what actually guarantee
// mutual exclusion on Start.
func (z *DirectZigBeeTransport) Start(ctx context.Context, portName string) error {
	z.mu.Lock()
	if z.running {
		z.mu.Unlock()
		return fmt.Errorf("zigbee transport already running")
	}

	mode := &serial.Mode{
		BaudRate: 115200,
		DataBits: 8,
		StopBits: serial.OneStopBit,
		Parity:   serial.NoParity,
	}

	p, err := serial.Open(portName, mode)
	if err != nil {
		z.mu.Unlock()
		return fmt.Errorf("open zigbee serial %s: %w", portName, err)
	}

	// Drain stale data from serial buffer — ProbeZNP may have left residual
	// bytes from the identification probe. Without this drain, initCoordinator's
	// SYS_PING response may contain stale probe data mixed in. [MESHSAT-403]
	p.SetReadTimeout(200 * time.Millisecond)
	drain := make([]byte, 256)
	for {
		n, _ := p.Read(drain)
		if n == 0 {
			break
		}
	}
	// Settle delay — give the CC2652P time to finish processing any residual
	// probe data before we send the first real command. [MESHSAT-403]
	time.Sleep(100 * time.Millisecond)

	z.port = p
	z.portName = portName

	// Release z.mu before running initCoordinator — init may take up to
	// 60s waiting for DEV_ZB_COORD and it takes z.mu internally to update
	// z.coordState. Holding z.mu across that call would deadlock.
	z.mu.Unlock()

	// Initialize coordinator without z.mu held. We pass ctx so the 60s
	// DEV_ZB_COORD wait can abort early if the caller cancels.
	if err := z.initCoordinator(ctx); err != nil {
		p.Close()
		z.mu.Lock()
		z.port = nil
		z.mu.Unlock()
		return fmt.Errorf("init coordinator: %w", err)
	}

	z.mu.Lock()
	ctx, z.cancelFn = context.WithCancel(ctx)
	z.running = true
	firmware := z.FirmwareVersion
	state := z.coordState
	z.mu.Unlock()

	go z.readLoop(ctx)
	go z.reinitLoop(ctx)
	go z.periodicRefreshLoop(ctx) // [MESHSAT-509] keep sensor values fresh
	// stuckReadWatchdog is intentionally NOT started — see the note on the
	// function for context. The cp210x driver wedges on these specific
	// SONOFF dongles every ~2-3 min regardless of traffic, and the
	// USBDEVFS_RESET recovery races with the device-supervisor → gateway-
	// manager rebind, leaving the gateway in a permanent down state. Until
	// the rebind race is fixed, the watchdog does more harm than good —
	// without it, the operator can pair / drive the gateway during the
	// initial healthy window after each container restart. [MESHSAT-509]

	// Hydrate the in-memory device cache from DB so the API and any
	// dashboard widget see previously-paired devices immediately, even
	// before they re-announce. [MESHSAT-509]
	z.hydrateFromStore()

	log.Info().Str("port", portName).Str("firmware", firmware).
		Str("coord_state", ZNPDevStateName(state)).
		Msg("zigbee: coordinator started")
	return nil
}

// Stop shuts down the transport.
func (z *DirectZigBeeTransport) Stop() {
	z.mu.Lock()
	defer z.mu.Unlock()

	if !z.running {
		return
	}
	z.running = false
	if z.cancelFn != nil {
		z.cancelFn()
	}
	if z.port != nil {
		z.port.Close()
	}
	log.Info().Msg("zigbee: transport stopped")
}

// IsRunning returns true if the transport is active.
func (z *DirectZigBeeTransport) IsRunning() bool {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.running
}

// GetDevices returns all known paired devices.
func (z *DirectZigBeeTransport) GetDevices() []ZigBeeDevice {
	z.mu.Lock()
	defer z.mu.Unlock()
	devs := make([]ZigBeeDevice, 0, len(z.devices))
	for _, d := range z.devices {
		devs = append(devs, *d)
	}
	return devs
}

// SendReadAttributes sends a ZCL Read Attributes (cmd 0x00, profile-wide)
// for one or more attribute IDs on a single cluster. Sleepy end-devices
// like the Tuya temp/humidity sensor only push Report Attributes on their
// internal cycle (every 30 min by default for Tuya), so we use Read
// Attributes to force the device to respond with current values right now
// — typically arrives within ~5s once the device polls the coordinator.
//
// The response comes back through the normal AF_INCOMING_MSG path as ZCL
// cmd 0x01 (Read Attributes Response) and decodes through zclReportHeader,
// which has special-case handling for the extra status byte cmd 0x01 puts
// before the data type. [MESHSAT-509]
func (z *DirectZigBeeTransport) SendReadAttributes(dstAddr uint16, dstEP byte, clusterID uint16, attrIDs ...uint16) error {
	if len(attrIDs) == 0 {
		return fmt.Errorf("no attribute ids supplied")
	}
	z.mu.Lock()
	z.transID++
	tsn := z.transID
	z.mu.Unlock()
	// ZCL frame: [FCF=0x00 profile-wide, server→client] [TSN] [Cmd=0x00 ReadAttr]
	// [AttrID(LE)...]  — packed LE pairs.
	zcl := make([]byte, 3, 3+2*len(attrIDs))
	zcl[0] = 0x00
	zcl[1] = tsn
	zcl[2] = 0x00
	for _, a := range attrIDs {
		zcl = append(zcl, byte(a), byte(a>>8))
	}
	return z.Send(dstAddr, dstEP, clusterID, zcl)
}

// RefreshDeviceSensors triggers Read Attributes for the temperature,
// humidity, and battery clusters on a device. Used after a join announce
// (so paired devices' values populate immediately rather than after the
// device's next scheduled report) and from the device-detail "Refresh"
// button. Endpoint defaults to 1 if the cached value is 0.
//
// **Reads are issued sequentially with 4s gaps**: Z-Stack's indirect message
// buffer for sleepy children holds messages with a default ~7.68s TTL, and
// a Tuya end-device polls roughly every 7s. Sending all three reads back-
// to-back caused 2 of 3 to TTL-expire before the device polled again
// (observed live on tesseract: only the temperature read got a response).
// Spacing them 4s apart gives each read its own poll window. Total runtime
// is ~12s which is fine — this runs in a goroutine.
func (z *DirectZigBeeTransport) RefreshDeviceSensors(shortAddr uint16) {
	z.mu.Lock()
	dev, ok := z.devices[shortAddr]
	if !ok {
		z.mu.Unlock()
		return
	}
	ep := dev.Endpoint
	if ep == 0 {
		ep = 1
	}
	z.mu.Unlock()

	reads := []struct {
		cluster uint16
		attr    uint16
		label   string
	}{
		{ZCLClusterTemperature, ZCLAttrMeasuredValue, "temperature"},
		{ZCLClusterHumidity, ZCLAttrMeasuredValue, "humidity"},
		{ZCLClusterPowerCfg, ZCLAttrBatteryPercent, "battery"},
	}
	for i, r := range reads {
		if i > 0 {
			time.Sleep(4 * time.Second)
		}
		if err := z.SendReadAttributes(shortAddr, ep, r.cluster, r.attr); err != nil {
			log.Debug().Err(err).Str("kind", r.label).Uint16("addr", shortAddr).
				Msg("zigbee: refresh read failed (sleepy device, will retry next cycle)")
		}
	}
}

// stuckReadWatchdog detects the cp210x "Select-readable-but-read-blocks"
// wedge condition and recovers it by USBDEVFS_RESET. Runs every
// stuckReadThreshold/3 — three samples spans roughly the threshold, which
// gives the operator a tight enough recovery window without churning
// through resets on transient slow reads.
//
// Detection: readLoopIterCount advances on EVERY return from port.Read,
// including 500ms-timeout-with-zero-bytes. So as long as readLoop is
// healthy, the counter ticks ~2/sec on an idle network and faster when
// data flows. If the counter has not advanced across two consecutive
// samples (≥ stuckReadThreshold), readLoop is wedged inside the read(2)
// syscall (driver bug — VMIN=1 with no data, no timeout honored) and we
// USBDEVFS_RESET the port to force read(2) to return EBADF.
//
// Cost of a false positive (extremely unlikely with iteration tracking):
// one USB reset (~2s dropped traffic) + the device-supervisor reattaches
// the gateway. [MESHSAT-509]
func (z *DirectZigBeeTransport) stuckReadWatchdog(ctx context.Context) {
	checkInterval := stuckReadThreshold / 3
	if checkInterval < 10*time.Second {
		checkInterval = 10 * time.Second
	}
	// Wait one threshold-worth before the first check so init has time to
	// run and readLoop ticks at least once.
	select {
	case <-ctx.Done():
		return
	case <-time.After(stuckReadThreshold):
	}

	prev := z.readLoopIterCount.Load()
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	stuckSince := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		cur := z.readLoopIterCount.Load()
		if cur != prev {
			// Healthy: readLoop is iterating.
			prev = cur
			stuckSince = time.Time{}
			continue
		}
		// Counter is stuck. Track when we first noticed; only fire if it
		// stays stuck for the full threshold.
		if stuckSince.IsZero() {
			stuckSince = time.Now()
			continue
		}
		if time.Since(stuckSince) < stuckReadThreshold {
			continue
		}

		z.mu.Lock()
		portName := z.portName
		z.mu.Unlock()
		if portName == "" {
			stuckSince = time.Time{}
			continue
		}

		log.Warn().Dur("stuck", time.Since(stuckSince)).Uint64("iter", cur).Str("port", portName).
			Msg("zigbee: readLoop stuck inside read(2) — USBDEVFS_RESET to unwedge cp210x driver")
		usbResetSerialDevice("zigbee-watchdog", portName)
		stuckSince = time.Time{}
		// Give the kernel a beat to re-enumerate before the next check;
		// also resync prev so we don't fire again on the post-reset gap
		// while the new tty appears.
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
		prev = z.readLoopIterCount.Load()
	}
}

// periodicRefreshLoop wakes every interval and runs RefreshDeviceSensors
// for every paired device. Keeps the device-manager UI showing reasonably-
// current values without the user having to mash the "Refresh now" button,
// and bridges the gap before sleepy devices' natural 30-min report cycle.
// Interval defaults to 5 minutes — short enough to feel live, long enough
// to not exhaust battery (~12 mac polls per device per refresh).
func (z *DirectZigBeeTransport) periodicRefreshLoop(ctx context.Context) {
	const interval = 5 * time.Minute
	// Initial delay so we don't pile reads on top of the announce-triggered
	// refresh during the first ~30s after Start.
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		z.mu.Lock()
		shorts := make([]uint16, 0, len(z.devices))
		for s, d := range z.devices {
			if d.IEEEAddr == "" {
				continue // skip half-known devices; they'll show up properly after the next announce
			}
			shorts = append(shorts, s)
		}
		z.mu.Unlock()
		for _, s := range shorts {
			z.RefreshDeviceSensors(s)
			// Small gap between devices so we don't flood the chip.
			time.Sleep(2 * time.Second)
		}
		timer.Reset(interval)
	}
}

// SendOnOffCommand sends a ZCL OnOff cluster (0x0006) command to a device.
// `cmd` is one of "on" / "off" / "toggle". The frame uses ZCL FCF = 0x01
// (cluster-specific, client→server) and a fresh transaction sequence number.
// Used by the device-manager UI's command panel. [MESHSAT-509]
func (z *DirectZigBeeTransport) SendOnOffCommand(dstAddr uint16, dstEP byte, cmd string) error {
	var cmdByte byte
	switch cmd {
	case "off":
		cmdByte = 0x00
	case "on":
		cmdByte = 0x01
	case "toggle":
		cmdByte = 0x02
	default:
		return fmt.Errorf("unknown onoff command %q (want on/off/toggle)", cmd)
	}
	z.mu.Lock()
	z.transID++
	tsn := z.transID
	z.mu.Unlock()
	// ZCL frame: [FCF=0x01 cluster-specific] [TSN] [Cmd]
	zcl := []byte{0x01, tsn, cmdByte}
	return z.Send(dstAddr, dstEP, ZCLClusterOnOff, zcl)
}

// SendLevelCommand sends a ZCL Level Control (0x0008) Move-To-Level-With-OnOff
// (cmd 0x04). Level is clamped to 0-254 (255 = "use previous value" in the
// spec, which most devices treat as "do nothing"). transitionDeciseconds is
// the fade duration (in 1/10s units) — 0 = instant, 0xFFFF = "use device
// default". [MESHSAT-509]
//
// We use the *WithOnOff variant rather than plain Move-To-Level (0x00)
// because users expect "set brightness 0" to also turn the light off, and
// "set brightness 50% on a dark light" to also turn it on. The plain
// variant changes the level register but doesn't toggle OnOff state, which
// is rarely what anyone wants from a UI slider.
func (z *DirectZigBeeTransport) SendLevelCommand(dstAddr uint16, dstEP byte, level byte, transitionDeciseconds uint16) error {
	if level > 254 {
		level = 254
	}
	z.mu.Lock()
	z.transID++
	tsn := z.transID
	z.mu.Unlock()
	// ZCL frame: [FCF=0x01] [TSN] [Cmd=0x04 MoveToLevelWithOnOff] [Level] [Transition LE]
	zcl := []byte{
		0x01, tsn, 0x04,
		level,
		byte(transitionDeciseconds), byte(transitionDeciseconds >> 8),
	}
	return z.Send(dstAddr, dstEP, ZCLClusterLevelCtrl, zcl)
}

// SendColorCommand sends a ZCL Color Control (0x0300) Move-To-Color
// (cmd 0x07) using the CIE xy color space. x and y are uint16 values
// scaled 0..65279 (= xy 0.0..1.0). [MESHSAT-509]
//
// Move-To-Color is the most-portable color command — Move-To-Hue and
// Move-To-Saturation depend on the device implementing the optional Hue/Sat
// attribute set, while every spec-compliant color light supports xy.
// Callers that have hue/sat handy should convert to xy before sending.
func (z *DirectZigBeeTransport) SendColorCommand(dstAddr uint16, dstEP byte, x, y uint16, transitionDeciseconds uint16) error {
	z.mu.Lock()
	z.transID++
	tsn := z.transID
	z.mu.Unlock()
	zcl := []byte{
		0x01, tsn, 0x07, // FCF, TSN, Cmd=0x07 Move-To-Color
		byte(x), byte(x >> 8),
		byte(y), byte(y >> 8),
		byte(transitionDeciseconds), byte(transitionDeciseconds >> 8),
	}
	return z.Send(dstAddr, dstEP, ZCLClusterColorCtrl, zcl)
}

// SendColorTempCommand sends ZCL Color Control Move-To-Color-Temperature
// (cmd 0x0a). `mireds` = 1e6 / Kelvin — typical range is 153 (6500K cool)
// to 500 (2000K warm). Used for tunable-white bulbs.
func (z *DirectZigBeeTransport) SendColorTempCommand(dstAddr uint16, dstEP byte, mireds uint16, transitionDeciseconds uint16) error {
	z.mu.Lock()
	z.transID++
	tsn := z.transID
	z.mu.Unlock()
	zcl := []byte{
		0x01, tsn, 0x0a, // FCF, TSN, Cmd=0x0a Move-To-Color-Temperature
		byte(mireds), byte(mireds >> 8),
		byte(transitionDeciseconds), byte(transitionDeciseconds >> 8),
	}
	return z.Send(dstAddr, dstEP, ZCLClusterColorCtrl, zcl)
}

// ForgetDevice removes a device from the in-memory cache. Used by the
// "unpair" UI button after the DB row has been cleared. Best-effort: we
// also send ZDO_MGMT_LEAVE_REQ to evict the device from the Z-Stack
// network so it doesn't silently rejoin. Failures (chip not ready, device
// already gone, leave SRSP non-zero) are logged but don't block local
// state cleanup — the user clicked "Forget", they want it gone from the
// UI even if the radio command misfires. [MESHSAT-509]
func (z *DirectZigBeeTransport) ForgetDevice(shortAddr uint16) {
	z.mu.Lock()
	dev, ok := z.devices[shortAddr]
	delete(z.devices, shortAddr)
	z.mu.Unlock()
	if !ok || dev.IEEEAddr == "" {
		return
	}
	// Decode the cached IEEE hex back to 8 bytes for the leave frame.
	// IEEEAddr is the big-endian human form (set by handleDeviceAnnounce);
	// MgmtLeaveReq wants little-endian on the wire.
	ieeeBE, err := hexDecodeIEEE(dev.IEEEAddr)
	if err != nil {
		log.Warn().Err(err).Str("ieee", dev.IEEEAddr).Msg("zigbee: forget — bad IEEE, skipping leave req")
		return
	}
	var ieeeLE [8]byte
	for i := 0; i < 8; i++ {
		ieeeLE[i] = ieeeBE[7-i]
	}
	go func() {
		// Run on its own goroutine so the API handler returns immediately.
		// Take serialMu so we don't race the readLoop.
		z.serialMu.Lock()
		defer z.serialMu.Unlock()
		if err := z.sendFrame(BuildMgmtLeaveReq(shortAddr, ieeeLE)); err != nil {
			log.Warn().Err(err).Uint16("addr", shortAddr).Msg("zigbee: leave req send failed")
			return
		}
		// Best-effort wait for the SRSP — don't care about the body, just
		// that the chip accepted the frame.
		if _, err := z.readCmdFrameTimeout(CmdZDOMgmtLeaveRsp, 2*time.Second); err != nil {
			log.Debug().Err(err).Msg("zigbee: leave req SRSP timeout (proceeding)")
		}
		log.Info().Uint16("addr", shortAddr).Str("ieee", dev.IEEEAddr).
			Msg("zigbee: ZDO_MGMT_LEAVE_REQ sent")
	}()
}

// hexDecodeIEEE turns "38b15462626ff512" (16 hex chars, big-endian human
// form) into an 8-byte big-endian array.
func hexDecodeIEEE(s string) ([8]byte, error) {
	var out [8]byte
	if len(s) != 16 {
		return out, fmt.Errorf("ieee hex: expected 16 chars, got %d", len(s))
	}
	for i := 0; i < 8; i++ {
		var b byte
		for j := 0; j < 2; j++ {
			c := s[i*2+j]
			var nib byte
			switch {
			case c >= '0' && c <= '9':
				nib = c - '0'
			case c >= 'a' && c <= 'f':
				nib = c - 'a' + 10
			case c >= 'A' && c <= 'F':
				nib = c - 'A' + 10
			default:
				return out, fmt.Errorf("ieee hex: bad char %q at index %d", c, i*2+j)
			}
			b = (b << 4) | nib
		}
		out[i] = b
	}
	return out, nil
}

// Send sends data to a specific ZigBee device endpoint. Returns an error
// if the coordinator isn't in DEV_ZB_COORD state — sending AF_DATA_REQUESTs
// to a pre-coord network burns the serial bus and logs noise without any
// chance of delivery.
func (z *DirectZigBeeTransport) Send(dstAddr uint16, dstEP byte, clusterID uint16, data []byte) error {
	z.mu.Lock()
	if !z.running {
		z.mu.Unlock()
		return fmt.Errorf("zigbee transport not running")
	}
	if z.coordState != ZNPDevStateCoord {
		state := z.coordState
		z.mu.Unlock()
		return fmt.Errorf("zigbee coordinator not ready (state=%s)", ZNPDevStateName(state))
	}
	z.transID++
	tid := z.transID
	z.mu.Unlock()

	z.serialMu.Lock()
	defer z.serialMu.Unlock()
	frame := BuildAFDataReq(dstAddr, dstEP, 1, clusterID, tid, data)
	return z.sendFrame(frame)
}

// PermitJoin sends ZDO_MGMT_PERMIT_JOIN_REQ to open the network for pairing.
// duration is clamped to 1-254 seconds. Use 0 to close the network.
//
// The coordinator must be in DEV_ZB_COORD state (0x09) or the NWK layer
// will reject the request with ZNwkInvalidRequest (0xC2). We check that
// up front so the operator gets a friendly "network not ready" message
// instead of a raw status code. [MESHSAT-510]
func (z *DirectZigBeeTransport) PermitJoin(durationSec byte) error {
	z.mu.Lock()
	if !z.running {
		z.mu.Unlock()
		return fmt.Errorf("zigbee transport not running")
	}
	state := z.coordState
	z.mu.Unlock()

	if state != ZNPDevStateCoord {
		return fmt.Errorf("coordinator not ready (state=%s) — network is still forming, try again in a few seconds",
			ZNPDevStateName(state))
	}

	// Lock serial to prevent readLoop from stealing our response
	z.serialMu.Lock()
	defer z.serialMu.Unlock()

	// Re-check state under the serial lock — a SYS_RESET_IND could have
	// arrived between the Lock above and now.
	z.mu.Lock()
	state = z.coordState
	z.mu.Unlock()
	if state != ZNPDevStateCoord {
		return fmt.Errorf("coordinator not ready (state=%s) — network reset, try again",
			ZNPDevStateName(state))
	}

	frame := BuildMgmtPermitJoinReq(durationSec)
	if err := z.sendFrame(frame); err != nil {
		return fmt.Errorf("permit join send: %w", err)
	}

	// Read frames until we get the SRSP, skipping unsolicited AREQs
	// (SYS_RESET_IND, ZDO_STATE_CHANGE_IND, etc.) that may arrive first.
	resp, err := z.readCmdFrameTimeout(CmdZDOMgmtPermitJoinRsp, 5*time.Second)
	if err != nil {
		return fmt.Errorf("permit join response: %w", err)
	}
	if len(resp.Data) > 0 && resp.Data[0] != ZStatusSuccess {
		return fmt.Errorf("permit join rejected: %s (0x%02x)",
			ZNPStatusString(resp.Data[0]), resp.Data[0])
	}

	z.mu.Lock()
	if durationSec > 0 {
		z.permitJoinEnd = time.Now().Add(time.Duration(durationSec) * time.Second)
		log.Info().Uint8("duration_sec", durationSec).Msg("zigbee: permit join opened")
	} else {
		z.permitJoinEnd = time.Time{}
		log.Info().Msg("zigbee: permit join closed")
	}
	z.mu.Unlock()

	return nil
}

// PermitJoinRemaining returns the seconds remaining on the permit-join window.
// Returns 0 if permit-join is not active.
func (z *DirectZigBeeTransport) PermitJoinRemaining() int {
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.permitJoinEnd.IsZero() {
		return 0
	}
	rem := time.Until(z.permitJoinEnd)
	if rem <= 0 {
		z.permitJoinEnd = time.Time{}
		return 0
	}
	return int(rem.Seconds())
}

// ---- Internal ----

// initCoordinator brings the Z-Stack coordinator to the operational
// DEV_ZB_COORD state. The flow mirrors zigbee-herdsman's ZnpAdapterManager:
//
//  1. SYS_PING (verify ZNP is alive)
//  2. SYS_VERSION (record firmware string)
//  3. AF_REGISTER endpoint 1 (HA profile with temp/humidity clusters)
//  4. UTIL_GET_DEVICE_INFO — check whether the coordinator is already up
//  5. If not in DEV_ZB_COORD: register a state-change waiter, send
//     ZDO_STARTUP_FROM_APP, then block until the waiter delivers state=0x09
//     (or timeout). This is the key fix for MESHSAT-510: without it, the
//     SRSP for startup arrives in a few ms but the NWK layer takes up to
//     60 s to finish forming/rejoining, and ZDO requests (including
//     MGMT_PERMIT_JOIN_REQ) return ZNwkInvalidRequest (0xC2) until then.
//
// Callers must hold z.serialMu or only run this before readLoop starts.
// Re-entry via reinitLoop holds serialMu for the full duration.
//
// ctx lets a slow init (the 60s DEV_ZB_COORD wait) abort cleanly when the
// caller cancels — Start() passes its own ctx here so Stop() aborts.
// reinitLoop passes its own ctx for the same reason.
func (z *DirectZigBeeTransport) initCoordinator(ctx context.Context) error {
	// 1. SYS_PING
	if err := z.sendFrame(BuildSysPing()); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	resp, err := z.readCmdFrameTimeout(CmdSysPingRsp, 2*time.Second)
	if err != nil {
		return fmt.Errorf("ping response: %w", err)
	}
	_ = resp
	log.Debug().Msg("zigbee: SYS_PING OK")

	// 2. SYS_VERSION
	if err := z.sendFrame(BuildSysVersion()); err != nil {
		return fmt.Errorf("version req: %w", err)
	}
	resp, err = z.readCmdFrameTimeout(CmdSysVersionRsp, 2*time.Second)
	if err != nil {
		log.Warn().Err(err).Msg("zigbee: SYS_VERSION failed (continuing)")
	} else if info, err := ParseSysVersionRsp(resp.Data); err == nil {
		z.FirmwareVersion = fmt.Sprintf("Z-Stack %d.%d.%d (product=%d)",
			info.MajorRel, info.MinorRel, info.MaintRel, info.Product)
	}

	// 3. AF_REGISTER endpoint 1 (HA profile 0x0104, config-tool device 0x0005).
	// If endpoint 1 is already registered (status ZApsDuplicateEntry=0xB8 on
	// restore), that's fine — we continue. This matches zigbee-herdsman's
	// "check active endpoints, register only if missing" logic.
	afReg := BuildAFRegister(1, 0x0104, 0x0005,
		[]uint16{0x0000, 0x0003, 0x0006, 0x0008, 0x0402, 0x0405}, // Basic, Identify, OnOff, Level, Temp, Humidity
		[]uint16{0x0000, 0x0003, 0x0006, 0x0008},
	)
	if err := z.sendFrame(afReg); err != nil {
		return fmt.Errorf("AF register: %w", err)
	}
	resp, err = z.readCmdFrameTimeout(CmdAFRegisterRsp, 2*time.Second)
	if err != nil {
		log.Warn().Err(err).Msg("zigbee: AF_REGISTER response missing (continuing)")
	} else if len(resp.Data) > 0 && resp.Data[0] != ZStatusSuccess &&
		resp.Data[0] != ZStatusApsDuplicateEntry {
		log.Warn().Uint8("status", resp.Data[0]).
			Str("meaning", ZNPStatusString(resp.Data[0])).
			Msg("zigbee: AF_REGISTER returned non-success")
	}

	// 4. Check current device state via UTIL_GET_DEVICE_INFO. If the
	// coordinator is already in DEV_ZB_COORD, we can skip ZDO_STARTUP and
	// go straight to operational — avoids retransmitting startup on a
	// re-init after soft reset.
	currentState := byte(0xFF)
	if err := z.sendFrame(BuildUtilGetDeviceInfo()); err == nil {
		if resp, err := z.readCmdFrameTimeout(CmdUtilGetDeviceInfoRsp, 2*time.Second); err == nil {
			if info, perr := ParseDeviceInfo(resp.Data); perr == nil {
				currentState = info.DeviceState
				z.mu.Lock()
				z.coordState = info.DeviceState
				z.mu.Unlock()
				log.Debug().Str("state", ZNPDevStateName(info.DeviceState)).
					Msg("zigbee: current device state")
			}
		}
	}

	if currentState == ZNPDevStateCoord {
		log.Info().Msg("zigbee: coordinator already in DEV_ZB_COORD, skipping startup")
		return nil
	}

	// 5. Register a state-change waiter BEFORE sending startup, then
	// send ZDO_STARTUP_FROM_APP and await DEV_ZB_COORD (0x09). Anything
	// other than 0x09 in the meantime (INIT → NWK_DISC → COORD_STARTING)
	// is just progress reporting — we keep waiting.
	waiter, unsub := z.watchStateChange()
	defer unsub()

	if err := z.sendFrame(BuildZDOStartup()); err != nil {
		return fmt.Errorf("ZDO startup: %w", err)
	}
	resp, err = z.readCmdFrameTimeout(CmdZDOStartupFromAppRsp, 5*time.Second)
	if err != nil {
		return fmt.Errorf("ZDO startup response: %w", err)
	}
	if len(resp.Data) > 0 {
		switch resp.Data[0] {
		case 0:
			log.Info().Msg("zigbee: ZDO_STARTUP status=0 (restored from NV)")
		case 1:
			log.Info().Msg("zigbee: ZDO_STARTUP status=1 (new network started)")
		case 2:
			// status=2 = NOT_INITIALIZED (no NV state and cannot commission).
			// zigbee-herdsman treats FAILURE here as tolerable and waits for
			// the state change anyway — the stack may still transition to
			// coord. We do the same.
			log.Warn().Msg("zigbee: ZDO_STARTUP status=2 (not initialized) — waiting for state change anyway")
		}
	}

	// Wait for DEV_ZB_COORD via two parallel signals:
	//   (a) ZDO_STATE_CHANGE_IND AREQs pushed through the waiter by
	//       handleFrame (zigbee-herdsman's primary path), and
	//   (b) periodic UTIL_GET_DEVICE_INFO polls.
	//
	// (b) is necessary because some Z-Stack 2.7.1 SmartRF06 firmwares
	// ship with ZCD_NV_ZDO_DIRECT_CB=0 by default, and without that NV
	// bit the stack never emits ZDO_STATE_CHANGE_IND AREQs. Observed on
	// the SONOFF ZBDongle-P currently on parallax01 (ZDO_STARTUP returns
	// status=0 "restored from NV" but no state-change AREQ ever arrives).
	// The poll closes the gap without us having to write NV.
	log.Debug().Msg("zigbee: waiting for DEV_ZB_COORD (via AREQ waiter + UTIL_GET_DEVICE_INFO poll)")
	deadline := time.Now().Add(60 * time.Second)
	nextPoll := time.Now().Add(1 * time.Second)
	kickedBDB := false
	bdbKickAt := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining > 2*time.Second {
			remaining = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("init cancelled: %w", ctx.Err())
		case st, ok := <-waiter:
			if !ok {
				return fmt.Errorf("state waiter closed unexpectedly")
			}
			log.Debug().Str("state", ZNPDevStateName(st)).Msg("zigbee: state transition during init")
			if st == ZNPDevStateCoord {
				return nil
			}
		case <-time.After(remaining):
			// Keep the serial bus warm by draining frames that arrived
			// while we were waiting — each decoded AREQ feeds the state
			// waiter via handleFrame when applicable.
			_, _ = z.drainFrame(100 * time.Millisecond)
		}
		if time.Now().After(nextPoll) {
			nextPoll = time.Now().Add(2 * time.Second)
			if err := z.sendFrame(BuildUtilGetDeviceInfo()); err == nil {
				if resp, err := z.readCmdFrameTimeout(CmdUtilGetDeviceInfoRsp, 1*time.Second); err == nil {
					if info, perr := ParseDeviceInfo(resp.Data); perr == nil {
						z.mu.Lock()
						z.coordState = info.DeviceState
						z.mu.Unlock()
						if info.DeviceState == ZNPDevStateCoord {
							log.Info().Msg("zigbee: DEV_ZB_COORD reached (via poll)")
							return nil
						}
						log.Debug().Str("state", ZNPDevStateName(info.DeviceState)).
							Msg("zigbee: init poll — still forming")
					}
				}
			}
		}
		// Fallback: if we're stuck in HOLD for 10+ seconds after ZDO_STARTUP,
		// the BDB layer hasn't been kicked into forming the network. Issue
		// an APP_CNF_BDB_START_COMMISSIONING with NETWORK_FORMATION mode —
		// zigbee-herdsman does this explicitly for Z-Stack 3.0.x/3.x.0 new
		// networks. On a dongle with a restored-but-inactive NIB, this
		// restarts commissioning and should bring the state up.
		if !kickedBDB && time.Now().After(bdbKickAt) && z.CoordState() != ZNPDevStateCoord {
			log.Info().Msg("zigbee: state still HOLD after 10s — kicking BDB_START_COMMISSIONING (mode=formation)")
			kickedBDB = true
			if err := z.sendFrame(BuildBdbStartCommissioning(BDBModeNetworkFormation)); err != nil {
				log.Warn().Err(err).Msg("zigbee: BDB commissioning kick send failed")
			} else if rsp, rerr := z.readCmdFrameTimeout(CmdAppCnfBdbStartCommissioningRsp, 2*time.Second); rerr != nil {
				log.Warn().Err(rerr).Msg("zigbee: BDB commissioning kick SRSP timeout")
			} else if len(rsp.Data) > 0 && rsp.Data[0] != ZStatusSuccess {
				log.Warn().Uint8("status", rsp.Data[0]).
					Str("meaning", ZNPStatusString(rsp.Data[0])).
					Msg("zigbee: BDB commissioning kick returned non-success")
			}
		}
	}
	return fmt.Errorf("timed out waiting for DEV_ZB_COORD (last state=%s)",
		ZNPDevStateName(z.CoordState()))
}

// readCmdFrameTimeout reads frames until it sees the expected command or
// times out. AREQs encountered in the meantime are fed through handleFrame
// so side effects (state changes, device-announce, incoming messages) are
// still processed during the synchronous init/permit-join flows.
func (z *DirectZigBeeTransport) readCmdFrameTimeout(want [2]byte, timeout time.Duration) (ZNPFrame, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		frame, err := z.readFrameTimeout(time.Until(deadline))
		if err != nil {
			return ZNPFrame{}, err
		}
		if frame.IsCmd(want) {
			return frame, nil
		}
		// Not the expected response — route it through the normal
		// handler so state changes and incoming-message events aren't
		// lost. handleFrame also feeds state-change waiters.
		z.handleFrame(frame)
	}
	return ZNPFrame{}, fmt.Errorf("timeout waiting for cmd 0x%02x%02x", want[0], want[1])
}

// drainFrame tries to read one frame within the given timeout and feeds it
// through handleFrame. Used by initCoordinator while blocked on the state
// waiter so AREQs emitted during network formation are consumed.
func (z *DirectZigBeeTransport) drainFrame(timeout time.Duration) (ZNPFrame, error) {
	frame, err := z.readFrameTimeout(timeout)
	if err != nil {
		return ZNPFrame{}, err
	}
	z.handleFrame(frame)
	return frame, nil
}

func (z *DirectZigBeeTransport) sendFrame(f ZNPFrame) error {
	encoded, err := EncodeZNP(f)
	if err != nil {
		return err
	}
	z.mu.Lock()
	port := z.port
	z.mu.Unlock()
	if port == nil {
		return fmt.Errorf("zigbee port closed")
	}
	_, err = port.Write(encoded)
	return err
}

func (z *DirectZigBeeTransport) readFrameTimeout(timeout time.Duration) (ZNPFrame, error) {
	z.mu.Lock()
	port := z.port
	z.mu.Unlock()
	if port == nil {
		return ZNPFrame{}, fmt.Errorf("zigbee port closed")
	}
	port.SetReadTimeout(timeout)
	buf := make([]byte, 256)
	var accumulated []byte

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		n, err := port.Read(buf)
		if n > 0 {
			accumulated = append(accumulated, buf[:n]...)
			frame, _, err := DecodeZNP(accumulated)
			if err == nil {
				return frame, nil
			}
		}
		if err != nil && len(accumulated) > 0 {
			continue
		}
	}
	return ZNPFrame{}, fmt.Errorf("read timeout after %v", timeout)
}

func (z *DirectZigBeeTransport) readLoop(ctx context.Context) {
	buf := make([]byte, 512)
	var accumulated []byte

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// serialMu prevents this from running during synchronous ZNP
		// commands (PermitJoin, Send). Short lock duration — just the
		// Read call + frame processing. [MESHSAT-510]
		z.serialMu.Lock()
		z.mu.Lock()
		port := z.port
		z.mu.Unlock()
		if port == nil {
			// reinitLoop briefly nils z.port between close and reopen to
			// break a stuck read(2). Yield and retry.
			z.serialMu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		port.SetReadTimeout(500 * time.Millisecond)
		n, err := port.Read(buf)
		// Mark a healthy heartbeat on EVERY return from Read, not just on
		// successful byte reads. A 0-byte timeout-return means the port is
		// alive — bytes are absent but the syscall came back. Only a
		// truly-stuck Read (cp210x wedge) keeps us in syscall.
		z.readLoopIterCount.Add(1)
		if n > 0 {
			accumulated = append(accumulated, buf[:n]...)
			z.processAccumulated(&accumulated)
		}
		z.serialMu.Unlock()
		if err != nil {
			// Port may have been closed by reinitLoop — back off briefly
			// to avoid spinning. Next iteration will pick up the new port.
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
	}
}

func (z *DirectZigBeeTransport) processAccumulated(buf *[]byte) {
	for len(*buf) >= znpMinFrameLen {
		frame, consumed, err := DecodeZNP(*buf)
		if err != nil {
			if consumed > 0 {
				*buf = (*buf)[consumed:]
				continue
			}
			break // incomplete frame, wait for more data
		}
		*buf = (*buf)[consumed:]
		z.handleFrame(frame)
	}
}

func (z *DirectZigBeeTransport) handleFrame(f ZNPFrame) {
	switch {
	case f.IsCmd(CmdAFIncomingMsg):
		z.handleIncomingMsg(f)
	case f.IsCmd(CmdZDOStateChangeInd):
		if len(f.Data) > 0 {
			st := f.Data[0]
			z.mu.Lock()
			z.coordState = st
			z.mu.Unlock()
			log.Info().Str("state", ZNPDevStateName(st)).
				Uint8("raw", st).Msg("zigbee: coordinator state changed")
			z.notifyStateChange(st)
		}
	case f.IsCmd(CmdZDOEndDeviceAnnceInd):
		z.handleDeviceAnnounce(f)
	case f.IsCmd(CmdZDOPermitJoinInd):
		if len(f.Data) > 0 {
			dur := f.Data[0]
			log.Info().Uint8("duration", dur).Msg("zigbee: permit join indication")
			z.mu.Lock()
			if dur == 0 {
				z.permitJoinEnd = time.Time{}
			}
			z.mu.Unlock()
		}
	case f.IsCmd(CmdAppCnfBdbCommissioningNotif):
		// Data: status(1), commissioningMode(1), remainingMode(1)
		if len(f.Data) >= 3 {
			log.Info().
				Str("status", BdbCommissioningStatus(f.Data[0])).
				Uint8("status_raw", f.Data[0]).
				Uint8("mode", f.Data[1]).
				Uint8("remaining", f.Data[2]).
				Msg("zigbee: BDB commissioning notification")
		}
	case f.IsCmd(CmdSysResetInd):
		// The coordinator has rebooted on us — watchdog, external reset,
		// or DTR/RTS glitch from another process opening our serial port.
		// Mark the network as down and schedule an async re-init. The
		// reinitLoop goroutine will grab serialMu and rerun
		// initCoordinator to bring DEV_ZB_COORD back.
		reason := byte(0xFF)
		if info, err := ParseSysResetInd(f.Data); err == nil {
			reason = info.Reason
			log.Warn().Str("reason", ZNPResetReasonName(info.Reason)).
				Uint8("major", info.MajorRel).Uint8("minor", info.MinorRel).
				Uint8("maint", info.HwRev).Msg("zigbee: coordinator reset — scheduling re-init")
		} else {
			log.Warn().Str("frame", f.String()).Msg("zigbee: malformed SYS_RESET_IND — scheduling re-init")
		}
		z.mu.Lock()
		z.coordState = ZNPDevStateHold
		z.permitJoinEnd = time.Time{}
		z.mu.Unlock()
		z.notifyStateChange(ZNPDevStateHold)
		select {
		case z.reinitPending <- struct{}{}:
		default:
			// Re-init already pending — coalesce.
		}
		_ = reason
	default:
		log.Debug().Str("frame", f.String()).Msg("zigbee: unhandled frame")
	}
}

// reinitLoop consumes reinitPending and reruns initCoordinator under
// serialMu when the coordinator has reset itself. This keeps the gateway
// process alive across firmware resets (watchdog, fault, or external DTR
// glitches) without needing a restart of the meshsat container.
func (z *DirectZigBeeTransport) reinitLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-z.reinitPending:
		}

		// Brief settle delay — the CC2652P needs ~1 s after reset before
		// it will accept SYS_PING reliably.
		select {
		case <-ctx.Done():
			return
		case <-time.After(1500 * time.Millisecond):
		}

		log.Info().Msg("zigbee: re-initialising coordinator after reset")

		// Close the existing serial fd BEFORE taking serialMu. After an
		// unsolicited CC2652P hard reset, the CP210x kernel driver can
		// leave readLoop's blocking read(2) syscall stuck indefinitely
		// despite SetReadTimeout — that means readLoop keeps serialMu
		// forever and we can never acquire it. Closing the fd forces
		// readLoop's Read to return EBADF, it releases serialMu, and we
		// can proceed. [MESHSAT-510]
		z.mu.Lock()
		old := z.port
		z.port = nil
		portName := z.portName
		z.mu.Unlock()
		if old != nil {
			_ = old.Close()
		}

		// USBDEVFS_RESET on the underlying USB device. Closing+reopening the
		// fd alone is not enough: a goroutine dump from tesseract01 showed
		// read(2) on a freshly-opened fd blocking for 4+ minutes after the
		// CC2652P unsolicited power-up reset. go.bug.st/serial.Read uses
		// Select() for the timeout,
		// then unix.Read with VMIN=1, VTIME=0 — so when the cp210x driver
		// reports the fd readable but no data is actually available, read(2)
		// blocks waiting for the first byte that never comes. A USB-level
		// reset clears the driver state and forces re-enumeration. The IMT
		// transport uses the same pattern to recover the FT234XD on the 9704.
		if portName != "" {
			usbResetSerialDevice("zigbee", portName)
			// Give the kernel a beat to re-enumerate the device before we
			// try to open it again. cp210x typically reappears within ~1s.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}

		z.serialMu.Lock()

		// Now reopen the port and run init under the lock.
		if err := z.reopenPort(); err != nil {
			log.Error().Err(err).Msg("zigbee: reopen port before re-init failed")
			z.serialMu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			continue
		}

		err := z.initCoordinator(ctx)
		z.serialMu.Unlock()

		if err != nil {
			log.Error().Err(err).Msg("zigbee: re-init failed, will retry on next reset")
			// Back off a few seconds before allowing another re-init
			// attempt — prevents busy-loop if something is wrong.
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			continue
		}
		log.Info().Str("state", ZNPDevStateName(z.CoordState())).
			Msg("zigbee: re-init completed")
	}
}

func (z *DirectZigBeeTransport) handleIncomingMsg(f ZNPFrame) {
	msg, err := ParseAFIncomingMsg(f.Data)
	if err != nil {
		log.Warn().Err(err).Msg("zigbee: parse incoming msg failed")
		return
	}

	z.mu.Lock()
	dev, ok := z.devices[msg.SrcAddr]
	if !ok {
		dev = &ZigBeeDevice{ShortAddr: msg.SrcAddr, Endpoint: msg.SrcEP, BatteryPct: -1, OnOff: -1, ZoneStatus: -1}
		z.devices[msg.SrcAddr] = dev
	}
	if dev.Endpoint == 0 {
		dev.Endpoint = msg.SrcEP
	}
	dev.LQI = msg.LQI
	dev.LastSeen = time.Now()

	// Decode ZCL Report Attributes for known clusters [MESHSAT-509, MESHSAT-511]
	var (
		temperature   *float64
		humidity      *float64
		battery       *int
		onoff         *bool
		zoneStatusPtr *ZoneStatus
		evtType       = "data"
		valueNum      *float64
		valueText     string
		unit          string
		attrID        uint16
	)

	switch msg.ClusterID {
	case ZCLClusterTemperature:
		if attr, v, ok := decodeZCLInt16Report(msg.Data); ok {
			t := float64(v) / 100.0 // ZCL temperature: 0.01 °C
			temperature = &t
			dev.Temperature = &t
			evtType = "temperature"
			valueNum = &t
			unit = "°C"
			attrID = attr
			log.Info().Uint16("src", msg.SrcAddr).Float64("celsius", t).Msg("zigbee: temperature reading")
		}
	case ZCLClusterHumidity:
		if attr, v, ok := decodeZCLUint16Report(msg.Data); ok {
			// Spec (ZCL 4.7.2.1.1): MeasuredValue = 100 × %RH (units of
			// 0.01%). A handful of Tuya firmwares (TS0201 variants, some
			// _TZ3000_ / _TZB210_ models) ignore the spec and report in
			// units of 0.1% instead — raw=475 becomes 4.75% with the
			// spec scale, but is actually 47.5% (the Tuya scale).
			//
			// Heuristic: a true indoor humidity below 10% is extremely
			// rare (heated-winter air settles around 15-25%, deserts go
			// down to ~5% but those don't run indoor sensors). If the
			// /100 decode gives < 10%, assume the device is on the Tuya
			// /10 scale and apply that instead. Worst case for a genuinely
			// arid environment: we report 10× too high; user can override
			// with a per-device scale field once we add one. [MESHSAT-509]
			h := float64(v) / 100.0
			scaledRaw := false
			if h < 10.0 {
				h = float64(v) / 10.0
				scaledRaw = true
			}
			humidity = &h
			dev.Humidity = &h
			evtType = "humidity"
			valueNum = &h
			unit = "%"
			attrID = attr
			log.Info().Uint16("src", msg.SrcAddr).Uint16("raw", v).Float64("percent", h).
				Bool("tuya_scale", scaledRaw).Msg("zigbee: humidity reading")
		}
	case ZCLClusterPowerCfg:
		// BatteryPercentageRemaining (attr 0x0021) is reported in half-percent
		// units (200 = 100%). We store the user-facing 0-100 value.
		if attr, v, ok := decodeZCLUint8Report(msg.Data); ok && attr == ZCLAttrBatteryPercent {
			pct := int(v) / 2
			battery = &pct
			dev.BatteryPct = pct
			evtType = "battery"
			fv := float64(pct)
			valueNum = &fv
			unit = "%"
			attrID = attr
			log.Info().Uint16("src", msg.SrcAddr).Int("percent", pct).Msg("zigbee: battery reading")
		}
	case ZCLClusterOnOff:
		// OnOff state (attr 0x0000) is a single boolean byte after the ZCL
		// Report header — same frame layout as the uint8 path.
		if attr, v, ok := decodeZCLUint8Report(msg.Data); ok && attr == ZCLAttrOnOffState {
			b := v != 0
			onoff = &b
			if b {
				dev.OnOff = 1
				valueText = "on"
			} else {
				dev.OnOff = 0
				valueText = "off"
			}
			evtType = "onoff"
			attrID = attr
			log.Info().Uint16("src", msg.SrcAddr).Bool("on", b).Msg("zigbee: onoff state")
		}
	case ZCLClusterIASZone:
		// Zone Status Change Notification: cluster-specific (FCF=0x09),
		// cmd=0x00, payload = ZoneStatus(2 LE) + ExtendedStatus(1) +
		// ZoneID(1) + Delay(2 LE). We only care about ZoneStatus. Also
		// handle the attribute-report path (attr 0x0002, ZoneStatus uint16)
		// which some devices use for periodic status echoes.
		if zs, attr, ok := decodeIASZoneStatus(msg.Data); ok {
			zonePtr := zs
			zoneStatusPtr = &zonePtr
			dev.ZoneStatus = int(zs.Raw)
			// Surface battery-low via the battery cluster path too so the
			// shared "low battery" widget shows up even for devices that
			// don't implement cluster 0x0001.
			if zs.BatteryLow && dev.BatteryPct < 0 {
				dev.BatteryPct = 1 // "warning" sentinel — 0 means fully drained
			}
			evtType = "ias_zone"
			attrID = attr
			fv := float64(zs.Raw)
			valueNum = &fv
			valueText = iasZoneText(&zs)
			log.Info().Uint16("src", msg.SrcAddr).Uint16("raw", zs.Raw).
				Bool("triggered", zs.Triggered).Bool("alarm1", zs.Alarm1).
				Bool("tamper", zs.Tamper).Bool("battery_low", zs.BatteryLow).
				Msg("zigbee: IAS zone status")
		}
	}
	devSnapshot := *dev
	z.mu.Unlock()

	log.Debug().
		Uint16("src", msg.SrcAddr).
		Uint16("cluster", msg.ClusterID).
		Uint8("lqi", msg.LQI).
		Int("len", len(msg.Data)).
		Msg("zigbee: incoming data")

	// Persist outside the mutex — DB writes can block on disk and we don't
	// want to stall readLoop frame processing.
	z.persistDevice(&devSnapshot)
	if evtType != "data" {
		z.recordReading(devSnapshot.IEEEAddr, msg.ClusterID, attrID, valueNum, valueText, unit, msg.LQI)
	}

	z.emit(ZigBeeEvent{
		Type:        evtType,
		Device:      devSnapshot,
		ClusterID:   msg.ClusterID,
		Data:        msg.Data,
		Timestamp:   time.Now(),
		Temperature: temperature,
		Humidity:    humidity,
		BatteryPct:  battery,
		OnOff:       onoff,
		ZoneStatus:  zoneStatusPtr,
	})
}

// decodeIASZoneStatus pulls the zone status off either:
//
//	cmd 0x00 (Zone Status Change Notification) — the device-pushed alarm.
//	  Frame: FCF=0x09, TSN, Cmd=0x00, Status(2 LE), ExtStatus(1), ZoneID(1), Delay(2 LE)
//
//	or the attribute-report path on attr 0x0002 (ZoneStatus uint16):
//	  Frame: FCF, TSN, [Cmd=0x0a optional], AttrID(2 LE)=0x0002, DT=0x21, Val(2 LE)
//
// Returns (zs, attrID, true) on success. attrID is 0xFFFF for the cmd 0x00
// path so the persisted reading is distinguishable from an attr report.
func decodeIASZoneStatus(data []byte) (ZoneStatus, uint16, bool) {
	if len(data) < 5 {
		return ZoneStatus{}, 0, false
	}
	cmd := data[2]
	// Path 1: Zone Status Change Notification (cluster-specific cmd 0x00).
	// FCF bit 0 must be 1 (cluster-specific) — typical FCF is 0x09 or 0x19.
	if cmd == 0x00 && data[0]&0x01 != 0 && len(data) >= 5 {
		raw := uint16(data[3]) | uint16(data[4])<<8
		return decodeZoneStatus(raw), 0xFFFF, true
	}
	// Path 2: attribute report for attr 0x0002 ZoneStatus (uint16).
	if attr, val, ok := decodeZCLUint16Report(data); ok && attr == ZCLAttrZoneStatus {
		return decodeZoneStatus(val), attr, true
	}
	return ZoneStatus{}, 0, false
}

// iasZoneText turns a ZoneStatus into a short human-readable token used for
// the value_text column + UI badge. Multiple flags are joined with "+".
func iasZoneText(zs *ZoneStatus) string {
	if zs == nil {
		return ""
	}
	var parts []string
	if zs.Alarm1 {
		parts = append(parts, "alarm1")
	}
	if zs.Alarm2 {
		parts = append(parts, "alarm2")
	}
	if zs.Tamper {
		parts = append(parts, "tamper")
	}
	if zs.BatteryLow {
		parts = append(parts, "battery_low")
	}
	if zs.Trouble {
		parts = append(parts, "trouble")
	}
	if zs.ACMainsFault {
		parts = append(parts, "ac_fault")
	}
	if zs.TestMode {
		parts = append(parts, "test")
	}
	if zs.BatteryDefect {
		parts = append(parts, "battery_defect")
	}
	if len(parts) == 0 {
		return "clear"
	}
	return strings.Join(parts, "+")
}

// ZCL Report Attributes frame layout (cmd 0x0a):
//
//	byte 0      Frame Control (FCF)
//	byte 1      Transaction Sequence Number
//	byte 2      Command identifier (we only handle 0x0a Report Attributes)
//	bytes 3-4   Attribute ID (LE)
//	byte 5      Data type
//	bytes 6+    Value (length depends on data type)
//
// The legacy decoders below assumed the command byte was absent (offset 5
// for the value), which works on devices that send a "stripped" report —
// but the Tuya temp/humidity sensor on the field kits sends the full frame
// with the command byte at offset 2. The unified helpers handle both by
// detecting the data type byte. [MESHSAT-509, MESHSAT-511]

// decodeZCLInt16Report returns the attribute ID + signed 16-bit value from
// a ZCL Report Attributes frame. Used for Temperature (cluster 0x0402, attr
// 0x0000, datatype 0x29 int16, units 0.01 °C).
func decodeZCLInt16Report(data []byte) (uint16, int16, bool) {
	off, attr, dt, ok := zclReportHeader(data)
	if !ok || (dt != 0x29 && dt != 0x21) || len(data) < off+2 {
		return 0, 0, false
	}
	val := int16(data[off]) | int16(data[off+1])<<8
	return attr, val, true
}

// decodeZCLUint16Report returns the attribute ID + unsigned 16-bit value.
// Used for Humidity (cluster 0x0405, attr 0x0000, datatype 0x21 uint16).
func decodeZCLUint16Report(data []byte) (uint16, uint16, bool) {
	off, attr, dt, ok := zclReportHeader(data)
	if !ok || (dt != 0x21 && dt != 0x29) || len(data) < off+2 {
		return 0, 0, false
	}
	val := uint16(data[off]) | uint16(data[off+1])<<8
	return attr, val, true
}

// decodeZCLUint8Report returns the attribute ID + unsigned 8-bit value.
// Used for Battery percent (cluster 0x0001, attr 0x0021, datatype 0x20)
// and OnOff state (cluster 0x0006, attr 0x0000, datatype 0x10).
func decodeZCLUint8Report(data []byte) (uint16, uint8, bool) {
	off, attr, dt, ok := zclReportHeader(data)
	if !ok || (dt != 0x20 && dt != 0x10) || len(data) < off+1 {
		return 0, 0, false
	}
	return attr, data[off], true
}

// zclReportHeader walks the variable-length ZCL Report Attributes header
// and returns the byte offset of the first value byte plus the attribute ID
// and data type. Handles both "with command byte" (most devices) and
// "without command byte" (some Tuya/Xiaomi variants) layouts by sniffing
// for known data type bytes.
func zclReportHeader(data []byte) (off int, attr uint16, dt byte, ok bool) {
	if len(data) < 6 {
		return 0, 0, 0, false
	}
	// Try with command byte at offset 2: header = FCF + TSN + CMD + AttrID(2) + DT
	// Standard layout for Report Attributes (cmd 0x0a) and Read Attribute Response.
	if len(data) >= 7 {
		cmd := data[2]
		if cmd == 0x0a || cmd == 0x01 { // ReportAttributes or ReadAttributesResponse
			attr = uint16(data[3]) | uint16(data[4])<<8
			// Read response has an extra status byte before data type — both
			// layouts are common in the wild.
			if cmd == 0x01 && len(data) >= 8 && data[5] == 0x00 {
				dt = data[6]
				return 7, attr, dt, true
			}
			dt = data[5]
			return 6, attr, dt, true
		}
	}
	// Fallback: no command byte (compact report). Layout: FCF + TSN + AttrID(2) + DT
	attr = uint16(data[2]) | uint16(data[3])<<8
	dt = data[4]
	return 5, attr, dt, true
}

func (z *DirectZigBeeTransport) handleDeviceAnnounce(f ZNPFrame) {
	if len(f.Data) < 11 {
		return
	}
	srcAddr := uint16(f.Data[0]) | uint16(f.Data[1])<<8
	ieeeAddr := fmt.Sprintf("%02x%02x%02x%02x%02x%02x%02x%02x",
		f.Data[9], f.Data[8], f.Data[7], f.Data[6],
		f.Data[5], f.Data[4], f.Data[3], f.Data[2])

	// Preserve any existing user-set alias / cached sensor values when the
	// device re-announces (e.g. after a battery change or a network rejoin).
	z.mu.Lock()
	existing, hadRow := z.devices[srcAddr]
	dev := &ZigBeeDevice{
		ShortAddr:  srcAddr,
		IEEEAddr:   ieeeAddr,
		LastSeen:   time.Now(),
		BatteryPct: -1,
		OnOff:      -1,
		ZoneStatus: -1,
	}
	if hadRow {
		dev.Alias = existing.Alias
		dev.Manufacturer = existing.Manufacturer
		dev.Model = existing.Model
		dev.Endpoint = existing.Endpoint
		dev.Temperature = existing.Temperature
		dev.Humidity = existing.Humidity
		if existing.BatteryPct >= 0 {
			dev.BatteryPct = existing.BatteryPct
		}
		if existing.OnOff >= 0 {
			dev.OnOff = existing.OnOff
		}
		if existing.ZoneStatus >= 0 {
			dev.ZoneStatus = existing.ZoneStatus
		}
	}
	z.devices[srcAddr] = dev
	devSnapshot := *dev
	z.mu.Unlock()

	log.Info().
		Uint16("short_addr", srcAddr).
		Str("ieee", ieeeAddr).
		Bool("rejoin", hadRow).
		Msg("zigbee: device joined")

	// Persist after dropping the mutex — the announce is a relatively rare
	// event, so the latency of a sqlite write is fine here.
	z.persistDevice(&devSnapshot)

	// Trigger an immediate Read Attributes for known sensor clusters so the
	// device-manager UI populates with current values without waiting for
	// the device's natural report cycle (Tuya stock firmware = 30 min).
	// Run on its own goroutine — Send takes serialMu and the caller is on
	// the read path which already holds it. [MESHSAT-509]
	go func(addr uint16) {
		// Small delay so the announce settles in the chip before we send.
		time.Sleep(800 * time.Millisecond)
		z.RefreshDeviceSensors(addr)
	}(srcAddr)

	z.emit(ZigBeeEvent{
		Type:      "join",
		Device:    devSnapshot,
		Timestamp: time.Now(),
	})
}

// SetDeviceAlias updates the user-given alias for a device by IEEE address
// and writes through to the DB. Returns false if no such device is known.
func (z *DirectZigBeeTransport) SetDeviceAlias(ieeeAddr, alias string) bool {
	z.mu.Lock()
	var matched *ZigBeeDevice
	for _, d := range z.devices {
		if d.IEEEAddr == ieeeAddr {
			d.Alias = alias
			matched = d
			break
		}
	}
	z.mu.Unlock()
	if matched == nil {
		return false
	}
	if z.db != nil {
		if err := z.db.SetZigBeeDeviceAlias(ieeeAddr, alias); err != nil {
			log.Warn().Err(err).Str("ieee", ieeeAddr).Msg("zigbee: persist alias failed")
		}
	}
	return true
}

// ProbeZNP checks if a serial port speaks Z-Stack ZNP protocol.
// Sends SYS_PING and checks for a valid response. Non-destructive.
func ProbeZNP(portName string) bool {
	mode := &serial.Mode{
		BaudRate: 115200,
		DataBits: 8,
		StopBits: serial.OneStopBit,
		Parity:   serial.NoParity,
	}

	p, err := serial.Open(portName, mode)
	if err != nil {
		return false
	}
	defer p.Close()

	// Clear DTR/RTS to prevent CC2652P auto-BSL reset circuit from triggering.
	// Many ZigBee dongles (SONOFF ZBDongle-P/E) have DTR+RTS wired to the BSL
	// circuit — asserting both enters bootloader mode and the coordinator won't
	// respond to ZNP commands until power-cycled. [MESHSAT-403]
	p.SetDTR(false)
	p.SetRTS(false)

	// Drain stale data and settle after potential DTR-triggered reset.
	// The CP210x kernel driver asserts DTR+RTS during open(), which triggers
	// the auto-BSL circuit on SONOFF ZBDongle-P/E and similar CC2652P boards.
	// After we clear DTR/RTS, the coordinator exits bootloader and starts the
	// Z-Stack firmware, which takes ~1-2s to initialize. [MESHSAT-403]
	p.SetReadTimeout(200 * time.Millisecond)
	drain := make([]byte, 256)
	for {
		n, _ := p.Read(drain)
		if n == 0 {
			break
		}
	}
	time.Sleep(1500 * time.Millisecond)

	// Try SYS_PING up to 3 times. The CC2652P may need additional time after
	// a DTR-triggered reset — the first SYS_PING may arrive while Z-Stack is
	// still initializing. Each retry drains and waits 500ms. [MESHSAT-403]
	frame := BuildSysPing()
	encoded, _ := EncodeZNP(frame)
	buf := make([]byte, 64)

	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			// Drain between retries
			p.SetReadTimeout(200 * time.Millisecond)
			for {
				n, _ := p.Read(buf)
				if n == 0 {
					break
				}
			}
			time.Sleep(500 * time.Millisecond)
		}

		if _, err := p.Write(encoded); err != nil {
			return false
		}

		p.SetReadTimeout(1 * time.Second)
		var accumulated []byte
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			n, _ := p.Read(buf)
			if n > 0 {
				accumulated = append(accumulated, buf[:n]...)
				resp, _, err := DecodeZNP(accumulated)
				if err == nil && resp.IsCmd(CmdSysPingRsp) {
					return true
				}
			}
		}
	}
	return false
}

// FindZigBeePort auto-detects a ZigBee coordinator dongle.
// Scans USB serial ports by VID:PID, then probes with ZNP SYS_PING.
func FindZigBeePort(excludePorts ...string) string {
	excludeSet := make(map[string]bool)
	for _, p := range excludePorts {
		excludeSet[p] = true
	}

	var candidates []string
	for _, pattern := range []string{"/dev/ttyUSB*", "/dev/ttyACM*"} {
		matches, _ := filepath.Glob(pattern)
		for _, port := range matches {
			if excludeSet[port] {
				continue
			}
			vidpid := findUSBVIDPID(port)
			if knownZigBeeVIDPIDs[strings.ToLower(vidpid)] {
				candidates = append(candidates, port)
			}
		}
	}

	// Protocol probe each candidate
	for _, port := range candidates {
		if ProbeZNP(port) {
			log.Info().Str("port", port).Msg("zigbee: coordinator detected via ZNP probe")
			return port
		}
	}

	return ""
}
