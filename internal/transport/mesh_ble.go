package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/rs/zerolog/log"
)

// The Meshtastic node over Bluetooth LE. The firmware's GATT service carries
// the same protobufs the serial and TCP links carry, one ToRadio per write
// and one FromRadio per read, with FromNum as the doorbell; bleMeshStream
// turns that into the 0x94 0xC3 byte stream the frame reader was written
// against, so DirectMeshTransport does not change. [MESHSAT-1390]
const (
	meshBLEServiceUUID   = "6ba1b218-15a8-461f-9fa8-5dcae273eafd"
	meshBLEToRadioUUID   = "f75c76d2-129e-4dad-a1dd-7866124401e7"
	meshBLEFromRadioUUID = "2c55e69e-4993-11ed-b878-0242ac120002"
	// FromNum as the firmware defines it (src/BluetoothCommon.h); the phone
	// apps carry a mistyped copy and never receive the doorbell.
	meshBLEFromNumUUID = "ed9da18c-a800-4f66-a670-aa7547e34453"
	// meshBLELogRadioUUID notifies the node's log, one LogRecord per value,
	// while security.debug_log_api_enabled is set and a client subscribes;
	// older firmware has no such characteristic. [MESHSAT-1406]
	meshBLELogRadioUUID = "5a3d6e49-06e6-4423-9944-e9de8cdf9547"
	// bleLogLease is how long the node's log is followed after the last
	// request to follow it: the page asks every few seconds while it is open.
	bleLogLease = 30 * time.Second
	// meshsatPipeServiceUUID is the MeshSat firmware's Iridium modem pipe on
	// the same link (meshsat-esp32/docs/IRIDIUM-BLE.md). [MESHSAT-1391]
	meshsatPipeServiceUUID = "b3d305a2-7310-4877-ad12-8e245e71951a"

	// bleFromRadioReadTimeout bounds one FromRadio read: during the config
	// dump the firmware holds a read for up to 20 s until it has the next
	// packet.
	bleFromRadioReadTimeout = 30 * time.Second
	// bleFromRadioPoll drains FromRadio even when no FromNum doorbell came.
	bleFromRadioPoll = 3 * time.Second
	// bleWriteTimeout bounds a ToRadio write; the pace mutex is held meanwhile.
	bleWriteTimeout = 5 * time.Second
	// bleConnectTimeout bounds one attempt to open the LE link and read its
	// GATT table; bleConnectAttempts is how many are made in a row.
	bleConnectTimeout  = 25 * time.Second
	bleConnectAttempts = 3
	// bleNodeFile remembers the node chosen in the app across Bridge restarts.
	bleNodeFile = "ble-node.json"
)

// ErrMeshNotBLE is answered when the mesh port of this Bridge is not Bluetooth.
var ErrMeshNotBLE = errors.New("the mesh port of this Bridge is not Bluetooth (MESHSAT_MESHTASTIC_PORT=ble)")

var errBLELinkLost = errors.New("bluetooth link to the node lost")

// IsMeshBLE reports whether a mesh port setting means a node over Bluetooth:
// `ble` (the node chosen in the app, remembered by the Bridge) or
// `ble:AA:BB:CC:DD:EE:FF`.
func IsMeshBLE(port string) bool {
	p := strings.ToLower(strings.TrimSpace(port))
	return p == "ble" || strings.HasPrefix(p, "ble:")
}

// meshBLEAddress is the address a `ble:` port setting names, "" for bare `ble`.
func meshBLEAddress(port string) string {
	p := strings.TrimSpace(port)
	if len(p) <= 4 || !strings.EqualFold(p[:4], "ble:") {
		return ""
	}
	addr := strings.ToUpper(strings.TrimSpace(p[4:]))
	if _, err := net.ParseMAC(addr); err != nil {
		return ""
	}
	return addr
}

// BLEStatus is what the app shows about the node over Bluetooth.
type BLEStatus struct {
	// Mode: off (the mesh port is not Bluetooth), idle (no node chosen),
	// scanning, pairing, connecting, ready, lost.
	Mode      string `json:"mode"`
	Address   string `json:"address,omitempty"`
	Name      string `json:"name,omitempty"`
	Paired    bool   `json:"paired"`
	Connected bool   `json:"connected"`
	// PairingPending: the node is showing its PIN; POST /api/mesh/ble/pair.
	PairingPending bool      `json:"pairing_pending"`
	PairingSince   time.Time `json:"pairing_since,omitempty"`
	// SatellitePipe: the node carries the MeshSat modem pipe (a RockBLOCK
	// reachable through it).
	SatellitePipe bool      `json:"satellite_pipe"`
	Since         time.Time `json:"since,omitempty"`
	Error         string    `json:"error,omitempty"`
	// AdapterPowered: whether the phone's Bluetooth adapter is on (nil when
	// BlueZ is not open). The apps say "Bluetooth is off on this phone" from
	// it. [MESHSAT-1397]
	AdapterPowered *bool `json:"adapter_powered,omitempty"`
}

// gattLink is the node's GATT service as the stream sees it: a fake in tests,
// BlueZ in the field.
type gattLink interface {
	ReadFromRadio(ctx context.Context) ([]byte, error)
	WriteToRadio(ctx context.Context, payload []byte) error
	// Notified fires when FromNum says there is something to read.
	Notified() <-chan struct{}
	// Lost is closed when the LE link is gone.
	Lost() <-chan struct{}
}

// bleMeshStream is the meshStream over a gattLink.
type bleMeshStream struct {
	link   gattLink
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	kick   chan struct{}
	avail  chan struct{}

	mu  sync.Mutex
	buf []byte
	err error

	writeMu   sync.Mutex
	closeOnce sync.Once
}

func newBLEMeshStream(link gattLink) *bleMeshStream {
	ctx, cancel := context.WithCancel(context.Background())
	s := &bleMeshStream{link: link, ctx: ctx, cancel: cancel, done: make(chan struct{}), kick: make(chan struct{}, 1), avail: make(chan struct{}, 1)}
	go s.reader()
	return s
}

// reader drains FromRadio after every doorbell, and on a timer in case one
// was missed. It waits for the first write (want_config) before its first
// read: a read before that holds in the radio for up to 20 s for nothing.
func (s *bleMeshStream) reader() {
	defer close(s.done)
	select {
	case <-s.kick:
	case <-s.link.Lost():
		s.fail(errBLELinkLost)
		return
	case <-s.ctx.Done():
		return
	}
	for {
		if !s.drain() {
			return
		}
		select {
		case <-s.link.Notified():
		case <-s.kick:
		case <-time.After(bleFromRadioPoll):
		case <-s.link.Lost():
			s.fail(errBLELinkLost)
			return
		case <-s.ctx.Done():
			return
		}
	}
}

// drain reads FromRadio until it comes back empty; false when the stream is
// over.
func (s *bleMeshStream) drain() bool {
	for {
		ctx, cancel := context.WithTimeout(s.ctx, bleFromRadioReadTimeout)
		data, err := s.link.ReadFromRadio(ctx)
		cancel()
		if err != nil {
			if s.ctx.Err() != nil {
				return false
			}
			s.fail(fmt.Errorf("read FromRadio: %w", err))
			return false
		}
		if len(data) == 0 {
			return true
		}
		s.push(data)
	}
}

func (s *bleMeshStream) push(payload []byte) {
	frame := make([]byte, 0, 4+len(payload))
	frame = append(frame, meshStart1, meshStart2, byte(len(payload)>>8), byte(len(payload)&0xFF))
	frame = append(frame, payload...)
	s.mu.Lock()
	s.buf = append(s.buf, frame...)
	s.mu.Unlock()
	select {
	case s.avail <- struct{}{}:
	default:
	}
}

func (s *bleMeshStream) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	select {
	case s.avail <- struct{}{}:
	default:
	}
}

// Read hands out framed bytes; (0, nil) when nothing came within the frame
// reader's pause, an error once the link is gone.
func (s *bleMeshStream) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		if len(s.buf) > 0 {
			n := copy(p, s.buf)
			s.buf = s.buf[n:]
			s.mu.Unlock()
			return n, nil
		}
		err := s.err
		s.mu.Unlock()
		if err != nil {
			return 0, err
		}
		select {
		case <-s.avail:
		case <-time.After(meshReadTimeout):
			return 0, nil
		}
	}
}

// Write takes one framed packet (as sendFrame writes it) and puts its
// protobuf into ToRadio. The serial wake burst (0xC3s) has no meaning on a
// GATT link and is swallowed.
func (s *bleMeshStream) Write(p []byte) (int, error) {
	if len(p) > 0 && isWakeBurst(p) {
		return len(p), nil
	}
	if len(p) < 4 || p[0] != meshStart1 || p[1] != meshStart2 {
		return 0, errors.New("ble: not a framed packet")
	}
	n := int(p[2])<<8 | int(p[3])
	if n != len(p)-4 {
		return 0, fmt.Errorf("ble: frame length %d does not match %d bytes", n, len(p)-4)
	}
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, bleWriteTimeout)
	defer cancel()
	if err := s.link.WriteToRadio(ctx, p[4:]); err != nil {
		return 0, fmt.Errorf("write ToRadio: %w", err)
	}
	select {
	case s.kick <- struct{}{}:
	default:
	}
	return len(p), nil
}

func isWakeBurst(p []byte) bool {
	for _, b := range p {
		if b != 0xC3 {
			return false
		}
	}
	return true
}

func (s *bleMeshStream) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.fail(io.EOF)
		select {
		case <-s.done:
		case <-time.After(2 * time.Second):
		}
	})
	return nil
}

// bleGattSession is the open LE link to the node as BlueZ exposes it.
type bleGattSession struct {
	bus       *bluezBus
	device    dbus.ObjectPath
	toRadio   dbus.ObjectPath
	fromRadio dbus.ObjectPath
	fromNum   dbus.ObjectPath
	// logRadio is the node's log characteristic, empty when the firmware
	// has none; onLog receives each value it notifies, and logNotifying
	// (guarded by the link's mu) says whether it is subscribed. [MESHSAT-1406]
	logRadio     dbus.ObjectPath
	onLog        func([]byte)
	logNotifying bool
	notif        chan struct{}
	lost         chan struct{}
	lostOnce     sync.Once
	unwatch      func()
}

func (b *bluezBus) openMeshSession(ctx context.Context, device dbus.ObjectPath, onLog func([]byte)) (*bleGattSession, error) {
	chars, found, err := b.characteristics(device, meshBLEServiceUUID, meshBLEToRadioUUID, meshBLEFromRadioUUID, meshBLEFromNumUUID, meshBLELogRadioUUID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("this device has no Meshtastic service")
	}
	s := &bleGattSession{bus: b, device: device, toRadio: chars[meshBLEToRadioUUID], fromRadio: chars[meshBLEFromRadioUUID], fromNum: chars[meshBLEFromNumUUID],
		logRadio: chars[meshBLELogRadioUUID], onLog: onLog, notif: make(chan struct{}, 1), lost: make(chan struct{})}
	if s.toRadio == "" || s.fromRadio == "" {
		return nil, errors.New("the Meshtastic service is missing its characteristics")
	}
	devCh, stopDev := b.watch(device)
	var numCh, logCh <-chan propsChange
	stopNum, stopLog := func() {}, func() {}
	if s.fromNum != "" {
		numCh, stopNum = b.watch(s.fromNum)
	}
	if s.logRadio != "" {
		logCh, stopLog = b.watch(s.logRadio)
	}
	s.unwatch = func() { stopDev(); stopNum(); stopLog() }
	go s.pump(devCh, numCh, logCh)
	if s.fromNum != "" {
		if err := b.startNotify(ctx, s.fromNum); err != nil {
			log.Warn().Err(err).Msg("bluez: no FromNum notifications; polling FromRadio instead")
		}
	} else {
		log.Warn().Msg("bluez: the node has no FromNum characteristic; polling FromRadio instead")
	}
	return s, nil
}

func (s *bleGattSession) pump(dev, num, logc <-chan propsChange) {
	for {
		select {
		case c := <-logc:
			if v, ok := c.Changed["Value"]; ok && s.onLog != nil {
				if value, ok := v.Value().([]byte); ok && len(value) > 0 {
					s.onLog(value)
				}
			}
		case c := <-dev:
			if c.Iface == "removed" {
				s.markLost()
				return
			}
			if v, ok := c.Changed["Connected"]; ok {
				if on, _ := v.Value().(bool); !on {
					s.markLost()
					return
				}
			}
		case c := <-num:
			if _, ok := c.Changed["Value"]; ok {
				select {
				case s.notif <- struct{}{}:
				default:
				}
			}
		case <-s.lost:
			return
		}
	}
}

func (s *bleGattSession) markLost() { s.lostOnce.Do(func() { close(s.lost) }) }

func (s *bleGattSession) isLost() bool {
	select {
	case <-s.lost:
		return true
	default:
		return false
	}
}

func (s *bleGattSession) ReadFromRadio(ctx context.Context) ([]byte, error) {
	return s.bus.readValue(ctx, s.fromRadio)
}

func (s *bleGattSession) WriteToRadio(ctx context.Context, payload []byte) error {
	return s.bus.writeValue(ctx, s.toRadio, payload)
}

func (s *bleGattSession) Notified() <-chan struct{} { return s.notif }

func (s *bleGattSession) Lost() <-chan struct{} { return s.lost }

func (s *bleGattSession) close() {
	s.markLost()
	if s.unwatch != nil {
		s.unwatch()
	}
	if s.fromNum != "" {
		s.bus.stopNotify(s.fromNum)
	}
	if s.logRadio != "" && s.logNotifying {
		s.bus.stopNotify(s.logRadio)
	}
}

// bleLink manages the node over Bluetooth for a DirectMeshTransport: the
// chosen address, pairing, the LE link, and the session the stream rides. The
// slow parts (pairing waits for a PIN, a connect can take tens of seconds)
// run on their own goroutine, never under the transport's lock. [MESHSAT-1390]
type bleLink struct {
	t        *DirectMeshTransport
	adapter  string
	stateDir string

	mu         sync.Mutex
	bus        *bluezBus
	agent      *bleAgent
	address    string
	name       string
	mode       string
	since      time.Time
	lastErr    string
	session    *bleGattSession
	pipe       bool
	connecting bool
	loaded     bool
	// logUntil is when following the node's log ends unless it is asked
	// for again. [MESHSAT-1406]
	logUntil time.Time
}

func newBLELink(t *DirectMeshTransport, port string) *bleLink {
	l := &bleLink{t: t, adapter: os.Getenv("MESHSAT_BLE_ADAPTER"), stateDir: "/var/lib/meshsat", mode: "idle"}
	if addr := meshBLEAddress(port); addr != "" {
		l.address = addr
		l.loaded = true
	}
	return l
}

type bleNodeRecord struct {
	Address  string    `json:"address"`
	Name     string    `json:"name,omitempty"`
	ChosenAt time.Time `json:"chosen_at"`
}

func (l *bleLink) nodeFile() string { return filepath.Join(l.stateDir, bleNodeFile) }

// load reads the remembered node once (unless the port named one).
func (l *bleLink) load() {
	if l.loaded {
		return
	}
	l.loaded = true
	data, err := os.ReadFile(l.nodeFile())
	if err != nil {
		return
	}
	var rec bleNodeRecord
	if json.Unmarshal(data, &rec) == nil && rec.Address != "" {
		l.address, l.name = strings.ToUpper(rec.Address), rec.Name
	}
}

func (l *bleLink) save() {
	rec := bleNodeRecord{Address: l.address, Name: l.name, ChosenAt: time.Now().UTC()}
	data, _ := json.Marshal(rec)
	if err := os.MkdirAll(l.stateDir, 0o755); err == nil {
		if err := os.WriteFile(l.nodeFile(), data, 0o644); err != nil {
			log.Warn().Err(err).Msg("bluez: cannot remember the node")
		}
	}
}

func (l *bleLink) forgetFile() { _ = os.Remove(l.nodeFile()) }

// ensureBus opens BlueZ and registers the pairing agent once. Caller holds l.mu.
func (l *bleLink) ensureBus() error {
	if l.bus != nil {
		return nil
	}
	bus, err := openBlueZ(l.adapter)
	if err != nil {
		return err
	}
	agent := &bleAgent{}
	if err := bus.registerAgent(agent); err != nil {
		bus.close()
		return err
	}
	l.bus, l.agent = bus, agent
	return nil
}

func (l *bleLink) setMode(mode, errText string) {
	l.mu.Lock()
	l.mode = mode
	l.since = time.Now()
	l.lastErr = errText
	l.mu.Unlock()
}

// stream gives the transport a fresh stream over the open session, or asks
// for the link to be brought up and says why there is none yet.
func (l *bleLink) stream() (meshStream, error) {
	l.mu.Lock()
	l.load()
	if l.session != nil && !l.session.isLost() {
		s := newBLEMeshStream(l.session)
		l.mu.Unlock()
		return s, nil
	}
	if l.address == "" {
		l.mu.Unlock()
		return nil, errors.New("no node chosen over Bluetooth yet: connect one in the app (Setup > Node)")
	}
	mode, why := l.mode, l.lastErr
	l.mu.Unlock()
	l.connectAsync()
	if why != "" {
		return nil, fmt.Errorf("waiting for the node over Bluetooth (%s: %s)", mode, why)
	}
	return nil, fmt.Errorf("waiting for the node over Bluetooth (%s)", mode)
}

// Address is the chosen node's address.
func (l *bleLink) Address() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	return l.address
}

// connectAsync brings the link up in the background, once at a time.
func (l *bleLink) connectAsync() {
	l.mu.Lock()
	if l.connecting {
		l.mu.Unlock()
		return
	}
	l.connecting = true
	l.mu.Unlock()
	go func() {
		err := l.connectOnce()
		l.mu.Lock()
		l.connecting = false
		l.mu.Unlock()
		if err != nil {
			log.Warn().Err(err).Msg("meshtastic over bluetooth: link not up")
			return
		}
		// Wake the processor's retry loop: the stream can be opened now.
		l.t.SetPort(l.t.GetPort())
	}()
}

func (l *bleLink) connectOnce() error {
	l.mu.Lock()
	if err := l.ensureBus(); err != nil {
		l.mu.Unlock()
		l.setMode("idle", err.Error())
		return err
	}
	bus, address := l.bus, l.address
	if old := l.session; old != nil {
		old.close()
		l.session = nil
	}
	l.mu.Unlock()

	path := bus.devicePath(address)
	dev, err := bus.device(address)
	if err != nil {
		l.setMode("lost", err.Error())
		return err
	}
	if dev == nil {
		// BlueZ has not seen it since boot: a short scan makes the device object.
		l.setMode("scanning", "")
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		_, _ = bus.discover(ctx, 8*time.Second, meshBLEServiceUUID)
		cancel()
		if dev, err = bus.device(address); err != nil || dev == nil {
			l.setMode("lost", "the node is not in Bluetooth range")
			return errors.New("the node is not in Bluetooth range")
		}
	}
	if !dev.Paired {
		l.setMode("pairing", "")
		ctx, cancel := context.WithTimeout(context.Background(), blePairWait+10*time.Second)
		err := bus.pair(ctx, path)
		cancel()
		if err != nil {
			l.setMode("idle", "pairing failed: "+bleErrorText(err))
			return fmt.Errorf("pair: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = bus.setProp(ctx, path, bluezDeviceIf, "Trusted", true)
	cancel()

	l.setMode("connecting", "")
	var lastErr error
	for attempt := 1; attempt <= bleConnectAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), bleConnectTimeout)
		lastErr = bus.connect(ctx, path)
		if lastErr == nil {
			lastErr = bus.waitServicesResolved(ctx, path)
		}
		var session *bleGattSession
		if lastErr == nil {
			session, lastErr = bus.openMeshSession(ctx, path, l.t.recordLogRadio)
		}
		cancel()
		if lastErr == nil {
			_, pipe, _ := bus.characteristics(path, meshsatPipeServiceUUID)
			name := ""
			if d, err := bus.device(address); err == nil && d != nil {
				name = d.Name
			}
			l.mu.Lock()
			l.session, l.pipe, l.name = session, pipe, name
			l.mode, l.since, l.lastErr = "ready", time.Now(), ""
			l.mu.Unlock()
			l.save()
			log.Info().Str("address", address).Str("name", name).Bool("satellite_pipe", pipe).Msg("meshtastic over bluetooth: link up")
			return nil
		}
		log.Warn().Err(lastErr).Int("attempt", attempt).Msg("meshtastic over bluetooth: connect failed")
		bus.disconnect(path)
		select {
		case <-time.After(2 * time.Second):
		}
	}
	l.setMode("lost", bleErrorText(lastErr))
	return lastErr
}

// bleErrorText strips D-Bus error names down to what a person can act on.
func bleErrorText(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "AuthenticationCanceled"), strings.Contains(text, "Canceled"):
		return "no PIN was entered in time"
	case strings.Contains(text, "AuthenticationFailed"), strings.Contains(text, "AuthenticationRejected"):
		return "the PIN was wrong"
	case strings.Contains(text, "AuthenticationTimeout"):
		return "the node stopped waiting for the PIN"
	case strings.Contains(text, "le-connection-abort"), strings.Contains(text, "connection abort"):
		return "the node did not answer"
	}
	return text
}

// scan lists the Meshtastic nodes in range.
func (l *bleLink) scan(ctx context.Context, seconds int) ([]BLEDevice, error) {
	if seconds <= 0 {
		seconds = 8
	}
	if seconds > 30 {
		seconds = 30
	}
	l.mu.Lock()
	if err := l.ensureBus(); err != nil {
		l.mu.Unlock()
		return nil, err
	}
	l.load()
	bus, chosen, before := l.bus, l.address, l.mode
	if before == "idle" || before == "lost" {
		l.mode = "scanning"
	}
	l.mu.Unlock()
	devices, err := bus.discover(ctx, time.Duration(seconds)*time.Second, meshBLEServiceUUID)
	l.mu.Lock()
	if l.mode == "scanning" {
		l.mode = before
	}
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	for i := range devices {
		devices[i].Chosen = devices[i].Address == chosen
	}
	return devices, nil
}

// choose makes address the node, replacing a previous one, and starts the link.
func (l *bleLink) choose(address string) error {
	addr := strings.ToUpper(strings.TrimSpace(address))
	if _, err := net.ParseMAC(addr); err != nil || len(addr) != 17 {
		return fmt.Errorf("not a Bluetooth address: %q", address)
	}
	l.mu.Lock()
	l.load()
	previous := l.address
	if previous != "" && previous != addr {
		if l.session != nil {
			l.session.close()
			l.session = nil
		}
		if l.bus != nil {
			l.bus.disconnect(l.bus.devicePath(previous))
		}
	}
	l.address, l.name, l.pipe = addr, "", false
	l.mode, l.since, l.lastErr = "idle", time.Now(), ""
	l.mu.Unlock()
	l.save()
	if previous != "" && previous != addr {
		l.t.Close()
	}
	l.connectAsync()
	return nil
}

// pair hands the PIN the person typed to the waiting pairing.
func (l *bleLink) pair(pin string) error {
	l.mu.Lock()
	agent := l.agent
	l.mu.Unlock()
	if agent == nil {
		return errors.New("no pairing is waiting for a PIN")
	}
	return agent.supply(pin)
}

// forget drops the node: link and memory, and the bond too when asked (Android's
// "Disconnect" keeps the bond; a stale one is cleared by forgetting with it).
func (l *bleLink) forget(removeBond bool) error {
	l.t.Close()
	l.mu.Lock()
	l.load()
	address, bus := l.address, l.bus
	if l.session != nil {
		l.session.close()
		l.session = nil
	}
	l.address, l.name, l.pipe = "", "", false
	l.mode, l.since, l.lastErr = "idle", time.Now(), ""
	l.mu.Unlock()
	l.forgetFile()
	if address != "" && bus != nil {
		path := bus.devicePath(address)
		bus.disconnect(path)
		if removeBond {
			if err := bus.removeDevice(path); err != nil {
				return fmt.Errorf("remove the bond: %w", err)
			}
		}
	}
	return nil
}

func (l *bleLink) status() BLEStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	st := BLEStatus{Mode: l.mode, Address: l.address, Name: l.name, SatellitePipe: l.pipe, Since: l.since, Error: l.lastErr}
	if l.session != nil && !l.session.isLost() {
		st.Connected = true
	} else if l.mode == "ready" {
		st.Mode = "lost"
	}
	if l.agent != nil {
		st.PairingPending, st.PairingSince = l.agent.pending()
	}
	if l.bus != nil && l.address != "" {
		if dev, err := l.bus.device(l.address); err == nil && dev != nil {
			st.Paired = dev.Paired
			if dev.Name != "" {
				st.Name = dev.Name
			}
		}
	}
	if l.bus != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if on, err := l.bus.getBool(ctx, l.bus.adapter, bluezAdapterIf, "Powered"); err == nil {
			st.AdapterPowered = &on
		}
		cancel()
	}
	return st
}

// followLog subscribes to the node's log for bleLogLease from now, and
// reports whether the node has a log to follow and whether it is followed.
// The subscription ends by itself when nobody asks again; after a reconnect
// the next request takes it up on the new session. [MESHSAT-1406]
func (l *bleLink) followLog() (available, following bool) {
	l.mu.Lock()
	l.logUntil = time.Now().Add(bleLogLease)
	s, bus := l.session, l.bus
	if s == nil || s.isLost() || s.logRadio == "" || bus == nil {
		l.mu.Unlock()
		return s != nil && s.logRadio != "", false
	}
	if s.logNotifying {
		l.mu.Unlock()
		return true, true
	}
	// Claimed before the call, so two requests at once start one
	// subscription and one lease.
	s.logNotifying = true
	l.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := bus.startNotify(ctx, s.logRadio)
	cancel()
	if err != nil {
		l.mu.Lock()
		s.logNotifying = false
		l.mu.Unlock()
		log.Warn().Err(err).Msg("meshtastic over bluetooth: cannot follow the node's log")
		return true, false
	}
	go l.endLogLease(s)
	return true, true
}

// endLogLease stops following the node's log on session s once nobody has
// asked for bleLogLease, or when the session goes.
func (l *bleLink) endLogLease(s *bleGattSession) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.lost:
			return
		case <-tick.C:
		}
		l.mu.Lock()
		expired := time.Now().After(l.logUntil)
		if expired {
			s.logNotifying = false
		}
		l.mu.Unlock()
		if expired {
			s.bus.stopNotify(s.logRadio)
			return
		}
	}
}

// logStatus reports whether the node has a log to follow and whether it is
// followed now.
func (l *bleLink) logStatus() (available, following bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.session
	if s == nil || s.isLost() {
		return false, false
	}
	return s.logRadio != "", s.logNotifying
}

// The transport's side: the mesh port `ble` makes a bleLink; these are what
// the API exposes. [MESHSAT-1390]

// SetBLEStateDir is where the chosen node is remembered (the Bridge's state
// directory). Call before the first connect.
func (t *DirectMeshTransport) SetBLEStateDir(dir string) {
	if t.ble != nil && dir != "" {
		t.ble.mu.Lock()
		t.ble.stateDir = dir
		t.ble.mu.Unlock()
	}
}

func (t *DirectMeshTransport) bleStream() (meshStream, error) {
	if t.ble == nil {
		return nil, ErrMeshNotBLE
	}
	return t.ble.stream()
}

// BLEScan lists the Meshtastic nodes in Bluetooth range.
func (t *DirectMeshTransport) BLEScan(ctx context.Context, seconds int) ([]BLEDevice, error) {
	if t.ble == nil {
		return nil, ErrMeshNotBLE
	}
	return t.ble.scan(ctx, seconds)
}

// BLEConnect chooses the node and brings the link up (pairing first if needed).
func (t *DirectMeshTransport) BLEConnect(_ context.Context, address string) error {
	if t.ble == nil {
		return ErrMeshNotBLE
	}
	return t.ble.choose(address)
}

// BLEPair answers the node's PIN prompt.
func (t *DirectMeshTransport) BLEPair(pin string) error {
	if t.ble == nil {
		return ErrMeshNotBLE
	}
	return t.ble.pair(pin)
}

// BLEStatus is the state of the node over Bluetooth.
func (t *DirectMeshTransport) BLEStatus() BLEStatus {
	if t.ble == nil {
		return BLEStatus{Mode: "off"}
	}
	return t.ble.status()
}

// BLEForget drops the node and the memory of it, and its bond when removeBond.
func (t *DirectMeshTransport) BLEForget(_ context.Context, removeBond bool) error {
	if t.ble == nil {
		return ErrMeshNotBLE
	}
	return t.ble.forget(removeBond)
}

// FollowNodeLog follows the log of the node over Bluetooth for the next
// 30 s; with no node over Bluetooth it reports nothing to follow.
// [MESHSAT-1406]
func (t *DirectMeshTransport) FollowNodeLog() (available, following bool) {
	if t.ble == nil {
		return false, false
	}
	return t.ble.followLog()
}

// NodeLogStatus reports whether the node over Bluetooth has a log to follow
// and whether it is followed now.
func (t *DirectMeshTransport) NodeLogStatus() (available, following bool) {
	if t.ble == nil {
		return false, false
	}
	return t.ble.logStatus()
}
