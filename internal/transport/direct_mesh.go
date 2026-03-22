package transport

// DirectMeshTransport implements MeshTransport with direct serial access.
// Ported from HAL's MeshtasticDriver — no HAL dependency, talks to USB radio directly.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"go.bug.st/serial"
)

const (
	meshBaud            = 115200
	meshConfigTimeout   = 15 * time.Second
	meshHeartbeatPeriod = 300 * time.Second
	meshMsgBufSize      = 200
)

// DirectMeshTransport implements MeshTransport via direct serial port access.
type DirectMeshTransport struct {
	port string // "/dev/ttyACM0" or "auto"

	mu        sync.RWMutex
	file      serial.Port
	reader    *meshFrameReader
	connected bool

	myNodeNum   uint32
	firmwareVer string
	configID    uint32
	configDone  bool

	nodes   map[uint32]*MeshNode
	nodesMu sync.RWMutex

	messages []MeshMessage
	msgIdx   int
	msgMu    sync.RWMutex

	configData map[string]interface{}
	configMu   sync.RWMutex

	neighbors   map[uint32]*NeighborInfo
	neighborsMu sync.RWMutex

	heartbeatNonce uint32

	readerDone chan struct{} // closed when readerLoop exits

	eventMu    sync.RWMutex
	eventSubs  map[uint64]chan MeshEvent
	nextSubID  uint64
	cancelFunc context.CancelFunc

	// Serial health watchdog: tracks last packet received from a remote node.
	// If no external packets arrive within watchdogMin minutes despite known
	// nodes, the reader loop forces a serial reconnect.
	lastExternalPkt atomic.Int64 // unix timestamp of last non-self packet
	watchdogMin     int          // 0 = disabled
}

// NewDirectMeshTransport creates a new direct serial Meshtastic transport.
// Pass "auto" or "" for port to use auto-detection.
func NewDirectMeshTransport(port string) *DirectMeshTransport {
	return &DirectMeshTransport{
		port:       port,
		nodes:      make(map[uint32]*MeshNode),
		messages:   make([]MeshMessage, 0, meshMsgBufSize),
		configData: make(map[string]interface{}),
		neighbors:  make(map[uint32]*NeighborInfo),
		eventSubs:  make(map[uint64]chan MeshEvent),
	}
}

// SetWatchdogMinutes configures the serial health watchdog timeout.
// If no external LoRa packets arrive within this many minutes despite
// known remote nodes, the serial connection is force-recycled.
// 0 disables the watchdog (default).
func (t *DirectMeshTransport) SetWatchdogMinutes(min int) {
	t.watchdogMin = min
}

// SetPort sets the serial port path. Called by DeviceSupervisor when a device
// is discovered. If already connected on a different port, the caller should
// Close() first.
func (t *DirectMeshTransport) SetPort(port string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.port = port
}

// IsConnected returns true if the transport has an active serial connection.
func (t *DirectMeshTransport) IsConnected() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.connected
}

// Reconnect closes any existing connection and reconnects on the current port.
func (t *DirectMeshTransport) Reconnect(ctx context.Context) error {
	t.Close()
	t.mu.Lock()
	err := t.connectLocked(ctx)
	t.mu.Unlock()
	return err
}

// GetPort returns the resolved serial port path (e.g., "/dev/ttyACM0").
// Returns "auto" or "" if not yet connected.
func (t *DirectMeshTransport) GetPort() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.port
}

// Subscribe opens the serial connection (if not already) and returns a channel
// that emits MeshEvents. The background reader goroutine is started on first subscribe.
func (t *DirectMeshTransport) Subscribe(ctx context.Context) (<-chan MeshEvent, error) {
	t.mu.Lock()
	if !t.connected {
		if err := t.connectLocked(ctx); err != nil {
			t.mu.Unlock()
			return nil, fmt.Errorf("connect: %w", err)
		}
	}
	t.mu.Unlock()

	ch := make(chan MeshEvent, 64)
	t.eventMu.Lock()
	id := t.nextSubID
	t.nextSubID++
	t.eventSubs[id] = ch
	t.eventMu.Unlock()

	// Wrap in unsubscribe-on-cancel
	go func() {
		<-ctx.Done()
		t.eventMu.Lock()
		delete(t.eventSubs, id)
		close(ch)
		t.eventMu.Unlock()
	}()

	return ch, nil
}

func (t *DirectMeshTransport) connectLocked(ctx context.Context) error {
	portPath := t.port
	if portPath == "supervisor" {
		return fmt.Errorf("waiting for device supervisor to assign port")
	}
	if portPath == "" || portPath == "auto" {
		portPath = autoDetectMeshtastic()
		if portPath == "" {
			return fmt.Errorf("no Meshtastic device found")
		}
	}

	sp, err := openSerial(portPath, meshBaud)
	if err != nil {
		return err
	}

	// Set read timeout for frame reader loop
	sp.SetReadTimeout(meshReadTimeout)

	t.file = sp
	t.reader = &meshFrameReader{port: sp}
	t.port = portPath
	log.Info().Str("port", portPath).Msg("meshtastic serial opened")

	// Wake device
	if err := wakeDevice(sp); err != nil {
		sp.Close()
		return err
	}

	// Config handshake — send want_config_id
	t.configID = uint32(time.Now().UnixNano() & 0xFFFFFFFF)
	configReq := buildWantConfigID(t.configID)
	if err := sendFrame(sp, configReq); err != nil {
		sp.Close()
		return fmt.Errorf("send want_config_id: %w", err)
	}
	log.Info().Uint32("config_id", t.configID).Msg("meshtastic config handshake started")

	t.connected = true
	t.configDone = false

	// Start background reader
	readerCtx, cancel := context.WithCancel(context.Background())
	t.cancelFunc = cancel
	t.readerDone = make(chan struct{})
	go t.readerLoop(readerCtx)

	// Wait for config completion (or timeout)
	deadline := time.After(meshConfigTimeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	t.mu.Unlock() // release lock while waiting
	defer t.mu.Lock()

	for {
		select {
		case <-deadline:
			log.Warn().Msg("meshtastic config handshake timed out, continuing with partial NodeDB")
			t.mu.Lock()
			t.configDone = true
			t.mu.Unlock()
			return nil
		case <-ticker.C:
			t.mu.RLock()
			done := t.configDone
			t.mu.RUnlock()
			if done {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// readerLoop continuously reads serial frames, parses them, and updates state.
func (t *DirectMeshTransport) readerLoop(ctx context.Context) {
	defer close(t.readerDone)
	log.Info().Msg("meshtastic reader loop started")
	defer log.Info().Msg("meshtastic reader loop stopped")

	// Seed watchdog timestamp so it doesn't trigger immediately after connect.
	t.lastExternalPkt.Store(time.Now().Unix())

	heartbeat := time.NewTicker(meshHeartbeatPeriod)
	defer heartbeat.Stop()

	// Serial health watchdog — detects stale CDC sessions where the firmware
	// stops forwarding LoRa packets despite the serial fd remaining open.
	var watchdog *time.Ticker
	if t.watchdogMin > 0 {
		watchdog = time.NewTicker(time.Duration(t.watchdogMin) * time.Minute)
		defer watchdog.Stop()
		log.Info().Int("minutes", t.watchdogMin).Msg("meshtastic serial watchdog enabled")
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			t.sendHeartbeat()
			continue
		default:
		}

		// Check watchdog between reads (non-blocking)
		if watchdog != nil {
			select {
			case <-watchdog.C:
				if t.watchdogTriggered() {
					return
				}
			default:
			}
		}

		payload, err := t.reader.readFrame(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error().Err(err).Msg("meshtastic serial read error")
			t.mu.Lock()
			t.connected = false
			if t.file != nil {
				t.file.Close()
				t.file = nil
			}
			t.mu.Unlock()
			t.emitEvent(MeshEvent{
				Type:    "disconnected",
				Message: "Serial connection lost",
				Time:    time.Now().UTC().Format(time.RFC3339),
			})
			return
		}

		t.handleFromRadio(payload)
	}
}

// watchdogTriggered checks if the serial session is stale and forces a reconnect if needed.
// Returns true if a reconnect was triggered (caller should exit readerLoop).
func (t *DirectMeshTransport) watchdogTriggered() bool {
	// Only trigger if we have remote nodes (otherwise there's nothing to receive)
	t.nodesMu.RLock()
	myNum := t.myNodeNum
	remoteCount := 0
	for num := range t.nodes {
		if num != myNum {
			remoteCount++
		}
	}
	t.nodesMu.RUnlock()

	if remoteCount == 0 {
		return false
	}

	lastPkt := t.lastExternalPkt.Load()
	silenceSec := time.Now().Unix() - lastPkt
	thresholdSec := int64(t.watchdogMin) * 60

	if silenceSec < thresholdSec {
		return false
	}

	log.Warn().
		Int64("silence_sec", silenceSec).
		Int("remote_nodes", remoteCount).
		Int("threshold_min", t.watchdogMin).
		Msg("meshtastic serial watchdog: no external packets received, forcing reconnect")

	t.mu.Lock()
	t.connected = false
	if t.file != nil {
		t.file.Close()
		t.file = nil
	}
	t.mu.Unlock()

	t.emitEvent(MeshEvent{
		Type:    "disconnected",
		Message: fmt.Sprintf("Serial watchdog: no external packets for %d min, reconnecting", t.watchdogMin),
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
	return true
}

func (t *DirectMeshTransport) handleFromRadio(data []byte) {
	fr, err := parseFromRadio(data)
	if err != nil {
		log.Warn().Err(err).Msg("meshtastic parse error")
		return
	}

	// MyNodeInfo
	if fr.MyInfo != nil {
		t.mu.Lock()
		t.myNodeNum = fr.MyInfo.MyNodeNum
		t.mu.Unlock()
		log.Info().Uint32("node_num", fr.MyInfo.MyNodeNum).Msg("meshtastic my_node_num")
	}

	// NodeInfo (from config download)
	if fr.NodeInfo != nil {
		node := protoNodeInfoToMeshNode(fr.NodeInfo)
		t.nodesMu.Lock()
		t.nodes[node.Num] = &node
		t.nodesMu.Unlock()
	}

	// Config sections
	if fr.ConfigRaw != nil {
		decoded := decodeProtoToMap(fr.ConfigRaw)
		t.configMu.Lock()
		for k, v := range decoded {
			t.configData["config_"+k] = v
		}
		t.configMu.Unlock()
	}
	if fr.ModuleConfigRaw != nil {
		decoded := decodeProtoToMap(fr.ModuleConfigRaw)
		t.configMu.Lock()
		for k, v := range decoded {
			t.configData["module_"+k] = v
		}
		t.configMu.Unlock()
	}
	if fr.ChannelRaw != nil {
		decoded := decodeProtoToMap(fr.ChannelRaw)
		t.configMu.Lock()
		idx := "0"
		if v, ok := decoded["1"]; ok {
			idx = fmt.Sprintf("%v", v)
		}
		t.configData["channel_"+idx] = decoded
		t.configMu.Unlock()
	}

	// Config complete
	if fr.ConfigCompleteID != 0 {
		t.mu.Lock()
		if fr.ConfigCompleteID == t.configID {
			t.configDone = true
			t.nodesMu.RLock()
			n := len(t.nodes)
			t.nodesMu.RUnlock()
			log.Info().Int("nodes", n).Msg("meshtastic config complete")
		} else {
			log.Warn().Uint32("got", fr.ConfigCompleteID).Uint32("expected", t.configID).Msg("config_complete_id mismatch")
		}
		t.mu.Unlock()

		t.nodesMu.RLock()
		n := len(t.nodes)
		t.nodesMu.RUnlock()
		t.emitEvent(MeshEvent{
			Type:    "config_complete",
			Message: fmt.Sprintf("config download complete (%d nodes)", n),
			Time:    time.Now().UTC().Format(time.RFC3339),
		})

		// Push UTC time to the radio so it stamps packets with correct time.
		// T-Echo/T-Beam without GPS fix have no time source — this fixes "17h ago" display issues.
		t.sendTimeSync()
	}

	// MeshPacket
	if fr.Packet != nil {
		t.handlePacket(fr.Packet)
	}
}

func (t *DirectMeshTransport) handlePacket(pkt *ProtoMeshPacket) {
	// Encrypted passthrough: relay encrypted packets with envelope metadata
	// instead of dropping them. The encrypted payload is preserved as-is
	// for re-injection into the mesh on the receiving side (AES-256-CTR passthrough).
	if pkt.Decoded == nil && len(pkt.Encrypted) > 0 {
		log.Debug().Uint32("from", pkt.From).Int("enc_len", len(pkt.Encrypted)).
			Msg("meshtastic: encrypted packet — passthrough relay")
	}

	msg := protoPacketToMeshMessage(pkt)

	// Track last external packet for serial health watchdog
	t.mu.RLock()
	myNum := t.myNodeNum
	t.mu.RUnlock()
	if pkt.From != myNum {
		t.lastExternalPkt.Store(time.Now().Unix())
	}

	// Update node DB from any packet — create node if unknown
	t.nodesMu.Lock()
	node, ok := t.nodes[pkt.From]
	isNewNode := !ok
	if !ok {
		node = &MeshNode{Num: pkt.From}
		t.nodes[pkt.From] = node
	}
	needsNodeInfo := node.LongName == "" && pkt.From != myNum
	node.LastHeard = msg.RxTime
	node.LastHeardStr = msg.Timestamp
	if pkt.RxSNR != 0 {
		node.SNR = pkt.RxSNR
	}
	if pkt.RxRSSI != 0 {
		node.RSSI = pkt.RxRSSI
	}
	// Update signal quality on every packet
	if node.SNR != 0 || node.RSSI != 0 {
		node.SignalQuality, node.DiagnosticNotes = computeSignalQuality(float64(node.RSSI), float64(node.SNR))
	}
	// Track last text message exchange time
	if pkt.Decoded != nil && pkt.Decoded.PortNum == PortNumTextMessage {
		node.LastMessageTime = msg.RxTime
		node.LastMessageStr = msg.Timestamp
	}
	t.nodesMu.Unlock()

	// Auto-request NodeInfo from unknown nodes (no name yet).
	// Only on first discovery or if still nameless, and not for NodeInfo packets
	// (which will be handled below and fill in the name).
	if needsNodeInfo && (isNewNode || pkt.Decoded == nil || pkt.Decoded.PortNum != PortNumNodeInfo) {
		t.mu.RLock()
		connected := t.connected && t.file != nil
		t.mu.RUnlock()
		if connected {
			log.Debug().Uint32("node", pkt.From).Msg("auto-requesting NodeInfo from unnamed node")
			toRadio := buildRequestNodeInfo(myNum, pkt.From)
			_ = sendFrame(t.file, toRadio)
		}
	}

	// Handle specific portnums
	if pkt.Decoded != nil {
		switch pkt.Decoded.PortNum {
		case PortNumPosition:
			t.handlePositionPacket(pkt)
		case PortNumNodeInfo:
			t.handleNodeInfoPacket(pkt)
		case PortNumTelemetry:
			t.handleTelemetryPacket(pkt)
		case PortNumNeighborInfo:
			t.handleNeighborInfoPacket(pkt)
		case PortNumStoreForward:
			t.handleStoreForwardPacket(pkt)
		case PortNumRangeTest:
			t.handleRangeTestPacket(pkt)
		}
	}

	// Store message in ring buffer
	t.msgMu.Lock()
	if len(t.messages) < meshMsgBufSize {
		t.messages = append(t.messages, msg)
	} else {
		t.messages[t.msgIdx] = msg
		t.msgIdx = (t.msgIdx + 1) % meshMsgBufSize
	}
	t.msgMu.Unlock()

	// Emit event
	dataJSON, _ := json.Marshal(msg)
	t.emitEvent(MeshEvent{
		Type:    "message",
		Message: fmt.Sprintf("from !%08x portnum=%s", pkt.From, msg.PortNumName),
		Data:    dataJSON,
		Time:    msg.Timestamp,
	})
}

func (t *DirectMeshTransport) handlePositionPacket(pkt *ProtoMeshPacket) {
	if pkt.Decoded == nil || pkt.Decoded.Payload == nil {
		return
	}
	pos, err := parsePosition(pkt.Decoded.Payload)
	if err != nil || pos == nil {
		return
	}

	t.nodesMu.Lock()
	node, ok := t.nodes[pkt.From]
	if !ok {
		node = &MeshNode{Num: pkt.From}
		t.nodes[pkt.From] = node
	}
	node.Latitude = float64(pos.LatitudeI) / 1e7
	node.Longitude = float64(pos.LongitudeI) / 1e7
	node.Altitude = pos.Altitude
	node.Sats = int(pos.SatsInView)
	t.nodesMu.Unlock()

	dataJSON, _ := json.Marshal(node)
	t.emitEvent(MeshEvent{
		Type:    "position",
		Message: fmt.Sprintf("position update from !%08x", pkt.From),
		Data:    dataJSON,
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

func (t *DirectMeshTransport) handleNodeInfoPacket(pkt *ProtoMeshPacket) {
	if pkt.Decoded == nil || pkt.Decoded.Payload == nil {
		return
	}
	user, err := parseUser(pkt.Decoded.Payload)
	if err != nil || user == nil {
		return
	}

	t.nodesMu.Lock()
	node, ok := t.nodes[pkt.From]
	if !ok {
		node = &MeshNode{Num: pkt.From}
		t.nodes[pkt.From] = node
	}
	node.UserID = user.ID
	node.LongName = user.LongName
	node.ShortName = user.ShortName
	node.HWModel = int(user.HWModel)
	node.HWModelName = hwModelName(int(user.HWModel))
	t.nodesMu.Unlock()

	dataJSON, _ := json.Marshal(node)
	t.emitEvent(MeshEvent{
		Type:    "node_update",
		Message: fmt.Sprintf("node info from %s (!%08x)", user.LongName, pkt.From),
		Data:    dataJSON,
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

func (t *DirectMeshTransport) handleTelemetryPacket(pkt *ProtoMeshPacket) {
	if pkt.Decoded == nil || pkt.Decoded.Payload == nil {
		return
	}

	// Try device metrics first (Telemetry field 1)
	dm, err := parseDeviceMetrics(pkt.Decoded.Payload)
	if err != nil {
		return
	}

	// Try environment metrics (Telemetry field 2)
	env := parseEnvironmentMetrics(pkt.Decoded.Payload)

	// Nothing useful parsed
	if dm == nil && env == nil {
		return
	}

	t.nodesMu.Lock()
	node, ok := t.nodes[pkt.From]
	if !ok {
		node = &MeshNode{Num: pkt.From}
		t.nodes[pkt.From] = node
	}
	if dm != nil {
		if dm.BatteryLevel > 0 {
			node.BatteryLevel = int(dm.BatteryLevel)
		}
		if dm.Voltage > 0 {
			node.Voltage = dm.Voltage
		}
		if dm.ChannelUtil > 0 {
			node.ChannelUtil = dm.ChannelUtil
		}
		if dm.AirUtilTx > 0 {
			node.AirUtilTx = dm.AirUtilTx
		}
		if dm.UptimeSeconds > 0 {
			node.UptimeSeconds = int(dm.UptimeSeconds)
		}
	}
	if env != nil {
		if env.Temperature != 0 {
			node.Temperature = &env.Temperature
		}
		if env.Humidity != 0 {
			node.Humidity = &env.Humidity
		}
		if env.Pressure != 0 {
			node.Pressure = &env.Pressure
		}
	}
	t.nodesMu.Unlock()

	dataJSON, _ := json.Marshal(node)
	t.emitEvent(MeshEvent{
		Type:    "node_update",
		Message: fmt.Sprintf("telemetry from !%08x", pkt.From),
		Data:    dataJSON,
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

// sendTimeSync pushes the current UTC time to ALL mesh nodes via
// AdminMessage.set_time_unixsec (field 99). This is critical for devices without
// GPS (like indoor T-Echo/T-Deck) — without it, timestamps display as hours/days off.
// Syncs local node first, then all known remote nodes.
func (t *DirectMeshTransport) sendTimeSync() {
	t.mu.Lock()
	if !t.connected || t.file == nil {
		t.mu.Unlock()
		return
	}
	myNode := t.myNodeNum
	t.mu.Unlock()

	now := uint32(time.Now().Unix())

	// Sync local node first
	frame := buildAdminSetTime(myNode, myNode, now)
	t.mu.Lock()
	err := sendFrame(t.file, frame)
	t.mu.Unlock()
	if err != nil {
		log.Warn().Err(err).Msg("meshtastic time sync (local) failed")
		return
	}
	log.Info().Uint32("unix_sec", now).Msg("meshtastic time synced to local radio")

	// Sync all known remote nodes (via LoRa relay)
	t.nodesMu.RLock()
	var remoteNodes []uint32
	for num := range t.nodes {
		if num != myNode {
			remoteNodes = append(remoteNodes, num)
		}
	}
	t.nodesMu.RUnlock()

	for _, nodeNum := range remoteNodes {
		frame = buildAdminSetTime(myNode, nodeNum, now)
		t.mu.Lock()
		err = sendFrame(t.file, frame)
		t.mu.Unlock()
		if err != nil {
			log.Warn().Err(err).Uint32("node", nodeNum).Msg("meshtastic time sync (remote) failed")
			continue
		}
		log.Info().Uint32("node", nodeNum).Uint32("unix_sec", now).Msg("meshtastic time synced to remote node")
		// Small delay between admin messages to avoid flooding
		time.Sleep(500 * time.Millisecond)
	}
}

func (t *DirectMeshTransport) sendHeartbeat() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.connected || t.file == nil {
		return
	}
	t.heartbeatNonce++
	hb := buildHeartbeat(t.heartbeatNonce)
	if err := sendFrame(t.file, hb); err != nil {
		log.Warn().Err(err).Msg("meshtastic heartbeat send failed")
	}
}

// buildHeartbeat builds a ToRadio heartbeat message (field 7, Heartbeat submessage).
// The nonce field inside Heartbeat (field 1) lets the firmware detect stale connections.
func buildHeartbeat(nonce uint32) []byte {
	// Heartbeat submessage: field 1 (nonce) = varint
	inner := make([]byte, 0, 8)
	inner = append(inner, 0x08) // field 1, varint
	inner = appendVarint(inner, uint64(nonce))
	// ToRadio field 7 (heartbeat), length-delimited
	buf := make([]byte, 0, len(inner)+4)
	buf = append(buf, 0x3A) // field 7, wire type 2 (length-delimited)
	buf = appendVarint(buf, uint64(len(inner)))
	buf = append(buf, inner...)
	return buf
}

func (t *DirectMeshTransport) emitEvent(event MeshEvent) {
	t.eventMu.RLock()
	defer t.eventMu.RUnlock()
	for _, ch := range t.eventSubs {
		select {
		case ch <- event:
		default:
		}
	}
}

// ============================================================================
// MeshTransport interface implementation
// ============================================================================

func (t *DirectMeshTransport) SendMessage(ctx context.Context, req SendRequest) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}

	var to uint32
	if req.To != "" {
		n, err := strconv.ParseUint(strings.TrimPrefix(req.To, "!"), 16, 32)
		if err != nil {
			return fmt.Errorf("invalid destination: %s", req.To)
		}
		to = uint32(n)
	}

	packet := buildTextMessage(req.Text, to, uint32(req.Channel))
	toRadio := buildToRadioPacket(packet)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) SendRaw(ctx context.Context, req RawRequest) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}

	var to uint32
	if req.To != "" {
		n, err := strconv.ParseUint(strings.TrimPrefix(req.To, "!"), 16, 32)
		if err != nil {
			return fmt.Errorf("invalid destination: %s", req.To)
		}
		to = uint32(n)
	}

	payload, err := base64.StdEncoding.DecodeString(req.Payload)
	if err != nil {
		return fmt.Errorf("invalid base64 payload: %w", err)
	}

	packet := buildRawPacket(payload, req.PortNum, to, uint32(req.Channel), req.WantAck)
	toRadio := buildToRadioPacket(packet)
	return sendFrame(t.file, toRadio)
}

// SendEncryptedRelay re-injects an encrypted Meshtastic payload into the mesh
// without decryption (AES-256-CTR passthrough). The encrypted bytes are placed
// in MeshPacket field 5 (encrypted) instead of field 4 (decoded), so the
// receiving radio decrypts using its channel PSK.
func (t *DirectMeshTransport) SendEncryptedRelay(_ context.Context, encryptedPayload []byte, to uint32, channel uint32, hopLimit uint32) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}

	packet := buildEncryptedPacket(encryptedPayload, to, channel, hopLimit)
	toRadio := buildToRadioPacket(packet)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) GetNodes(_ context.Context) ([]MeshNode, error) {
	t.nodesMu.RLock()
	defer t.nodesMu.RUnlock()
	nodes := make([]MeshNode, 0, len(t.nodes))
	for _, n := range t.nodes {
		nodes = append(nodes, *n)
	}
	return nodes, nil
}

func (t *DirectMeshTransport) GetStatus(_ context.Context) (*MeshStatus, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	t.nodesMu.RLock()
	numNodes := len(t.nodes)
	t.nodesMu.RUnlock()

	status := &MeshStatus{
		Connected: t.connected,
		Transport: "serial",
		Address:   t.port,
		NumNodes:  numNodes,
	}

	if t.myNodeNum != 0 {
		status.NodeID = fmt.Sprintf("!%08x", t.myNodeNum)
		t.nodesMu.RLock()
		if node, ok := t.nodes[t.myNodeNum]; ok {
			status.NodeName = node.LongName
			status.HWModel = node.HWModel
			status.HWModelName = node.HWModelName
		}
		t.nodesMu.RUnlock()
	}
	return status, nil
}

func (t *DirectMeshTransport) GetMessages(_ context.Context, limit int) ([]MeshMessage, error) {
	t.msgMu.RLock()
	defer t.msgMu.RUnlock()

	total := len(t.messages)
	n := total
	if limit > 0 && limit < n {
		n = limit
	}
	result := make([]MeshMessage, n)
	if total < meshMsgBufSize {
		// Buffer hasn't wrapped — messages are in order
		copy(result, t.messages[total-n:])
	} else {
		// Ring buffer wrapped — reconstruct chronological order
		// msgIdx points to the oldest entry; most recent is at msgIdx-1
		ordered := make([]MeshMessage, total)
		copy(ordered, t.messages[t.msgIdx:])
		copy(ordered[total-t.msgIdx:], t.messages[:t.msgIdx])
		copy(result, ordered[total-n:])
	}
	return result, nil
}

func (t *DirectMeshTransport) GetConfig(_ context.Context) (map[string]interface{}, error) {
	t.configMu.RLock()
	defer t.configMu.RUnlock()
	result := make(map[string]interface{}, len(t.configData))
	for k, v := range t.configData {
		result[k] = v
	}
	return result, nil
}

func (t *DirectMeshTransport) AdminReboot(_ context.Context, nodeNum uint32, delay int) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	toRadio := buildAdminReboot(t.myNodeNum, nodeNum, delay)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) AdminFactoryReset(_ context.Context, nodeNum uint32) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	toRadio := buildAdminFactoryReset(t.myNodeNum, nodeNum)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) Traceroute(_ context.Context, nodeNum uint32) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	packet := buildTraceroutePacket(nodeNum)
	toRadio := buildToRadioPacket(packet)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) SetRadioConfig(_ context.Context, _ string, data json.RawMessage) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	toRadio := buildAdminSetConfig(t.myNodeNum, data)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) SetModuleConfig(_ context.Context, _ string, data json.RawMessage) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	toRadio := buildAdminSetModuleConfig(t.myNodeNum, data)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) SetChannel(_ context.Context, req ChannelRequest) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}

	var psk []byte
	if req.PSK != "" {
		var err error
		psk, err = base64.StdEncoding.DecodeString(req.PSK)
		if err != nil {
			return fmt.Errorf("invalid PSK base64: %w", err)
		}
	}

	role := 0
	switch req.Role {
	case "PRIMARY":
		role = 1
	case "SECONDARY":
		role = 2
	case "DISABLED":
		role = 0
	}

	toRadio := buildSetChannel(t.myNodeNum, req.Index, req.Name, psk, role, req.UplinkEnabled, req.DownlinkEnabled)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) SendWaypoint(_ context.Context, wp Waypoint) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	packet := buildWaypointPacket(wp, 0, 0) // broadcast
	toRadio := buildToRadioPacket(packet)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) handleNeighborInfoPacket(pkt *ProtoMeshPacket) {
	if pkt.Decoded == nil || pkt.Decoded.Payload == nil {
		return
	}
	ni, err := parseNeighborInfo(pkt.Decoded.Payload)
	if err != nil || ni == nil {
		return
	}

	info := &NeighborInfo{
		NodeID:                   ni.NodeID,
		LastSentByID:             ni.LastSentByID,
		NodeBroadcastIntervalSec: ni.NodeBroadcastIntervalSec,
		LastUpdated:              time.Now().UTC().Format(time.RFC3339),
	}
	for _, n := range ni.Neighbors {
		info.Neighbors = append(info.Neighbors, Neighbor{
			NodeID:                   n.NodeID,
			SNR:                      n.SNR,
			LastRxTime:               n.LastRxTime,
			NodeBroadcastIntervalSec: n.NodeBroadcastIntervalSec,
		})
	}

	t.neighborsMu.Lock()
	t.neighbors[ni.NodeID] = info
	t.neighborsMu.Unlock()

	dataJSON, _ := json.Marshal(info)
	t.emitEvent(MeshEvent{
		Type:    "neighbor_info",
		Message: fmt.Sprintf("neighbor info from !%08x (%d neighbors)", ni.NodeID, len(ni.Neighbors)),
		Data:    dataJSON,
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

func (t *DirectMeshTransport) handleStoreForwardPacket(pkt *ProtoMeshPacket) {
	if pkt.Decoded == nil || pkt.Decoded.Payload == nil {
		return
	}
	sf := parseStoreForward(pkt.Decoded.Payload)
	if sf == nil {
		return
	}

	info := &StoreForwardInfo{RequestResponse: sf.RequestResponse}
	if sf.Text != nil {
		info.Text = string(sf.Text)
	}

	dataJSON, _ := json.Marshal(info)
	t.emitEvent(MeshEvent{
		Type:    "store_forward",
		Message: fmt.Sprintf("store_forward rr=%d from !%08x", sf.RequestResponse, pkt.From),
		Data:    dataJSON,
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

func (t *DirectMeshTransport) handleRangeTestPacket(pkt *ProtoMeshPacket) {
	if pkt.Decoded == nil || pkt.Decoded.Payload == nil {
		return
	}
	text := string(pkt.Decoded.Payload)
	dataJSON, _ := json.Marshal(map[string]interface{}{
		"from":    pkt.From,
		"text":    text,
		"rx_snr":  pkt.RxSNR,
		"rx_rssi": pkt.RxRSSI,
	})
	t.emitEvent(MeshEvent{
		Type:    "range_test",
		Message: fmt.Sprintf("range test from !%08x: %s", pkt.From, text),
		Data:    dataJSON,
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

func (t *DirectMeshTransport) GetConfigSection(_ context.Context, section string) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	enumVal, ok := configSectionToEnum(section)
	if !ok {
		return fmt.Errorf("unknown config section: %s", section)
	}
	toRadio := buildAdminGetConfig(t.myNodeNum, enumVal)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) GetModuleConfigSection(_ context.Context, section string) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	enumVal, ok := moduleConfigSectionToEnum(section)
	if !ok {
		return fmt.Errorf("unknown module config section: %s", section)
	}
	toRadio := buildAdminGetModuleConfig(t.myNodeNum, enumVal)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) SendPosition(_ context.Context, lat, lon float64, alt int32) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	packet := buildPositionPacket(lat, lon, alt, uint32(time.Now().Unix()))
	toRadio := buildToRadioPacket(packet)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) SetFixedPosition(_ context.Context, lat, lon float64, alt int32) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	toRadio := buildAdminSetFixedPosition(t.myNodeNum, lat, lon, alt)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) RemoveFixedPosition(_ context.Context) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	toRadio := buildAdminRemoveFixedPosition(t.myNodeNum)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) SetOwner(_ context.Context, longName, shortName string) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	toRadio := buildAdminSetOwner(t.myNodeNum, longName, shortName)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) RequestNodeInfo(_ context.Context, nodeNum uint32) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	toRadio := buildRequestNodeInfo(t.myNodeNum, nodeNum)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) RequestStoreForward(_ context.Context, nodeNum uint32, window uint32) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	packet := buildStoreForwardRequest(nodeNum, window)
	toRadio := buildToRadioPacket(packet)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) SendRangeTest(_ context.Context, text string, to uint32) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	packet := buildRangeTestPacket(text, to)
	toRadio := buildToRadioPacket(packet)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) SetCannedMessages(_ context.Context, messages string) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	toRadio := buildAdminSetCannedMessages(t.myNodeNum, messages)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) GetCannedMessages(_ context.Context) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	toRadio := buildAdminGetCannedMessages(t.myNodeNum)
	return sendFrame(t.file, toRadio)
}

func (t *DirectMeshTransport) GetNeighborInfo(_ context.Context) ([]NeighborInfo, error) {
	t.neighborsMu.RLock()
	defer t.neighborsMu.RUnlock()
	result := make([]NeighborInfo, 0, len(t.neighbors))
	for _, ni := range t.neighbors {
		result = append(result, *ni)
	}
	return result, nil
}

func (t *DirectMeshTransport) RemoveNode(_ context.Context, nodeNum uint32) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}
	toRadio := buildAdminRemoveNode(t.myNodeNum, nodeNum)
	if err := sendFrame(t.file, toRadio); err != nil {
		return err
	}
	// Remove from in-memory node map so GetNodes reflects the deletion immediately
	t.nodesMu.Lock()
	delete(t.nodes, nodeNum)
	t.nodesMu.Unlock()
	return nil
}

func (t *DirectMeshTransport) Close() error {
	t.mu.Lock()

	if t.cancelFunc != nil {
		t.cancelFunc()
		// Wait for readerLoop to exit (2s timeout, matching HAL pattern)
		done := t.readerDone
		if done != nil {
			t.mu.Unlock()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				log.Warn().Msg("meshtastic: reader loop did not exit in time")
			}
			t.mu.Lock()
		}
	}
	t.connected = false
	if t.file != nil {
		t.file.Close()
		t.file = nil
	}
	t.mu.Unlock()
	return nil
}
