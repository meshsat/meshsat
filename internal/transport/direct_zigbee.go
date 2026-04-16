package transport

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"go.bug.st/serial"
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
	ShortAddr   uint16    `json:"short_addr"`
	IEEEAddr    string    `json:"ieee_addr"` // hex-encoded 8-byte IEEE address
	Endpoint    byte      `json:"endpoint"`
	LQI         byte      `json:"lqi"`
	LastSeen    time.Time `json:"last_seen"`
	Temperature *float64  `json:"temperature,omitempty"` // Celsius (from cluster 0x0402) [MESHSAT-511]
	Humidity    *float64  `json:"humidity,omitempty"`    // percent (from cluster 0x0405) [MESHSAT-511]
}

// ZigBeeEvent is emitted when data arrives from a ZigBee device.
type ZigBeeEvent struct {
	Type        string       `json:"type"` // "data", "join", "leave", "temperature", "humidity"
	Device      ZigBeeDevice `json:"device"`
	ClusterID   uint16       `json:"cluster_id"`
	Data        []byte       `json:"data"`
	Timestamp   time.Time    `json:"timestamp"`
	Temperature *float64     `json:"temperature,omitempty"` // decoded Celsius [MESHSAT-511]
	Humidity    *float64     `json:"humidity,omitempty"`    // decoded percent [MESHSAT-511]
}

// ZCL cluster IDs for sensor data [MESHSAT-511]
const (
	ZCLClusterTemperature = 0x0402
	ZCLClusterHumidity    = 0x0405
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
}

// NewDirectZigBeeTransport creates a new ZigBee transport.
func NewDirectZigBeeTransport() *DirectZigBeeTransport {
	return &DirectZigBeeTransport{
		devices:       make(map[uint16]*ZigBeeDevice),
		reinitPending: make(chan struct{}, 1),
	}
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
	_, err = z.port.Write(encoded)
	return err
}

func (z *DirectZigBeeTransport) readFrameTimeout(timeout time.Duration) (ZNPFrame, error) {
	z.port.SetReadTimeout(timeout)
	buf := make([]byte, 256)
	var accumulated []byte

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		n, err := z.port.Read(buf)
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
		z.port.SetReadTimeout(500 * time.Millisecond)
		n, err := z.port.Read(buf)
		if n > 0 {
			accumulated = append(accumulated, buf[:n]...)
			z.processAccumulated(&accumulated)
		}
		z.serialMu.Unlock()
		if err != nil {
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

		z.serialMu.Lock()
		log.Info().Msg("zigbee: re-initialising coordinator after reset")
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
		dev = &ZigBeeDevice{ShortAddr: msg.SrcAddr, Endpoint: msg.SrcEP}
		z.devices[msg.SrcAddr] = dev
	}
	dev.LQI = msg.LQI
	dev.LastSeen = time.Now()

	// Decode ZCL Report Attributes for sensor clusters [MESHSAT-511]
	var temperature *float64
	var humidity *float64
	evtType := "data"

	switch msg.ClusterID {
	case ZCLClusterTemperature:
		if v, ok := decodeZCLInt16Attr(msg.Data); ok {
			t := float64(v) / 100.0 // ZCL temp is in 0.01 C
			temperature = &t
			dev.Temperature = &t
			evtType = "temperature"
			log.Info().Uint16("src", msg.SrcAddr).Float64("celsius", t).Msg("zigbee: temperature reading")
		}
	case ZCLClusterHumidity:
		if v, ok := decodeZCLUint16Attr(msg.Data); ok {
			h := float64(v) / 100.0 // ZCL humidity is in 0.01 %
			humidity = &h
			dev.Humidity = &h
			evtType = "humidity"
			log.Info().Uint16("src", msg.SrcAddr).Float64("percent", h).Msg("zigbee: humidity reading")
		}
	}
	z.mu.Unlock()

	log.Debug().
		Uint16("src", msg.SrcAddr).
		Uint16("cluster", msg.ClusterID).
		Uint8("lqi", msg.LQI).
		Int("len", len(msg.Data)).
		Msg("zigbee: incoming data")

	z.emit(ZigBeeEvent{
		Type:        evtType,
		Device:      *dev,
		ClusterID:   msg.ClusterID,
		Data:        msg.Data,
		Timestamp:   time.Now(),
		Temperature: temperature,
		Humidity:    humidity,
	})
}

// decodeZCLInt16Attr decodes a ZCL Report Attributes frame for a signed 16-bit value.
// Frame: frame_control(1) + seq(1) + attr_id(2) + data_type(1) + value(2)
func decodeZCLInt16Attr(data []byte) (int16, bool) {
	if len(data) < 7 {
		return 0, false
	}
	// Skip frame control (1), sequence (1), attr ID (2), data type (1) = 5 bytes
	val := int16(data[5]) | int16(data[6])<<8
	return val, true
}

// decodeZCLUint16Attr decodes a ZCL Report Attributes frame for an unsigned 16-bit value.
func decodeZCLUint16Attr(data []byte) (uint16, bool) {
	if len(data) < 7 {
		return 0, false
	}
	val := uint16(data[5]) | uint16(data[6])<<8
	return val, true
}

func (z *DirectZigBeeTransport) handleDeviceAnnounce(f ZNPFrame) {
	if len(f.Data) < 11 {
		return
	}
	srcAddr := uint16(f.Data[0]) | uint16(f.Data[1])<<8
	ieeeAddr := fmt.Sprintf("%02x%02x%02x%02x%02x%02x%02x%02x",
		f.Data[9], f.Data[8], f.Data[7], f.Data[6],
		f.Data[5], f.Data[4], f.Data[3], f.Data[2])

	z.mu.Lock()
	dev := &ZigBeeDevice{
		ShortAddr: srcAddr,
		IEEEAddr:  ieeeAddr,
		LastSeen:  time.Now(),
	}
	z.devices[srcAddr] = dev
	z.mu.Unlock()

	log.Info().
		Uint16("short_addr", srcAddr).
		Str("ieee", ieeeAddr).
		Msg("zigbee: device joined")

	z.emit(ZigBeeEvent{
		Type:      "join",
		Device:    *dev,
		Timestamp: time.Now(),
	})
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
