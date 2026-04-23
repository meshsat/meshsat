package routing

// BLE GATT *client* (central) — the inverse of ble_interface.go.  When
// another MeshSat kit is paired via Settings > Routing > Bluetooth
// Peers and we detect its advertised Reticulum GATT service UUID, we
// connect as a BLE central, subscribe to the peers TX characteristic
// (notifications), and write segments to the peers RX characteristic.
//
// Phase 2 of MESHSAT-629 (Phase 1 = detection badge, 2b588a1).
// [MESHSAT-633]

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
)

// BLEClientConfig configures a single BLE client → remote MeshSat kit.
type BLEClientConfig struct {
	// Name is the interface ID in the Reticulum registry, e.g.
	// "ble_peer_0", "ble_peer_1". The caller picks a unique name.
	Name string
	// AdapterID is the local BlueZ adapter (default "hci0").
	AdapterID string
	// PeerAddress is the MAC address of the already-paired remote kit.
	PeerAddress string
}

// BLEClientInterface is a central-role GATT client that talks to a
// remote MeshSat peripherals Reticulum service. Reuses the SAR
// format from ble_interface.go so both ends speak the same wire.
type BLEClientInterface struct {
	config   BLEClientConfig
	callback func(packet []byte)

	mu      sync.Mutex
	online  bool
	stopped bool
	stopCh  chan struct{}

	conn         *dbus.Conn
	devicePath   dbus.ObjectPath
	rxCharPath   dbus.ObjectPath // peers RX — where we write
	txCharPath   dbus.ObjectPath // peers TX — where we subscribe
	rxChar       dbus.BusObject
	txChar       dbus.BusObject
	signalCh     chan *dbus.Signal
	txSeq        uint8
	sarBuf       sarBuffer
	notifyActive bool
}

// NewBLEClientInterface constructs the client state. Start() does the
// D-Bus work — keeping constructors side-effect free matches the rest
// of the routing package.
func NewBLEClientInterface(config BLEClientConfig, callback func(packet []byte)) *BLEClientInterface {
	if config.AdapterID == "" {
		config.AdapterID = "hci0"
	}
	return &BLEClientInterface{
		config:   config,
		callback: callback,
		stopCh:   make(chan struct{}),
		sarBuf:   newSARBuffer(),
	}
}

// deviceObjectPath maps "AA:BB:CC:DD:EE:FF" → BlueZ object path.
func deviceObjectPath(adapter, mac string) dbus.ObjectPath {
	return dbus.ObjectPath("/org/bluez/" + adapter + "/dev_" + strings.ReplaceAll(strings.ToUpper(mac), ":", "_"))
}

// Start connects to the peer (if not already connected), discovers the
// Reticulum service + chars by UUID, subscribes to TX notifications,
// and spins up a goroutine to reassemble incoming packets.
func (c *BLEClientInterface) Start(ctx context.Context) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("ble-client: D-Bus system bus: %w", err)
	}
	c.conn = conn

	c.devicePath = deviceObjectPath(c.config.AdapterID, c.config.PeerAddress)
	device := conn.Object("org.bluez", c.devicePath)

	// Ensure the remote device is connected. Use ConnectProfile(MeshSat
	// UUID) rather than Connect() — on Pi 5 brcmfmac BT, the generic
	// Connect() tries BR/EDR first and can time out with NoReply when
	// the peer is an LE-only GATT peripheral. ConnectProfile with the
	// specific UUID forces BlueZ straight to LE-GATT transport.
	// [MESHSAT-675]
	//
	// Watchdog: if ConnectProfile stalls or Connected stays false for
	// longer than connectTimeout, cycle hci0 via btmgmt (power off/on)
	// and retry. brcmfmac BT firmware accumulates bad state across many
	// pair/connect cycles; a power cycle of the adapter clears it
	// without requiring a full OS reboot. Same recovery shape as the
	// USBDEVFS_RESET we do for stuck serial ports.
	if err := c.ensureConnected(ctx, device); err != nil {
		conn.Close()
		return fmt.Errorf("ble-client: %w", err)
	}

	// Poll ServicesResolved — GATT discovery is async after Connect.
	if err := c.waitServicesResolved(ctx, device); err != nil {
		conn.Close()
		return fmt.Errorf("ble-client: services not resolved: %w", err)
	}

	// Enumerate characteristics under /org/bluez and find the MeshSat
	// TX/RX pair on this specific device.
	if err := c.findCharacteristics(); err != nil {
		conn.Close()
		return fmt.Errorf("ble-client: find characteristics: %w", err)
	}

	// Subscribe to notifications on the peers TX char. BlueZ pushes
	// updates as org.freedesktop.DBus.Properties.PropertiesChanged
	// signals on the char path.
	c.signalCh = make(chan *dbus.Signal, 16)
	conn.Signal(c.signalCh)
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
		dbus.WithMatchMember("PropertiesChanged"),
		dbus.WithMatchObjectPath(c.txCharPath),
	); err != nil {
		conn.Close()
		return fmt.Errorf("ble-client: AddMatch: %w", err)
	}

	if err := c.txChar.Call("org.bluez.GattCharacteristic1.StartNotify", 0).Err; err != nil {
		conn.Close()
		return fmt.Errorf("ble-client: StartNotify: %w", err)
	}
	c.notifyActive = true

	c.mu.Lock()
	c.online = true
	c.mu.Unlock()

	go c.notifyLoop()

	log.Info().Str("iface", c.config.Name).Str("peer", c.config.PeerAddress).
		Msg("ble-client: connected + subscribed to MeshSat peer")
	return nil
}

// connectTimeout caps one ConnectProfile attempt. If D-Bus reply
// doesn't arrive in this window, we assume the brcmfmac BT firmware
// is stuck, cycle hci0, and retry. Under a clean BT stack LE
// connections complete in under 2 s; 8 s gives slower paths headroom
// without being long enough for the operator to notice the stall.
const connectTimeout = 8 * time.Second

// connectMaxAttempts caps the total number of ConnectProfile tries
// across hci0 power cycles. Three is the proven-empirical recovery
// threshold: 1st try on a stuck adapter times out, power cycle, 2nd
// try usually succeeds, 3rd is a final fallback.
const connectMaxAttempts = 3

// ensureConnected drives the device to the Connected=true state,
// using ConnectProfile(MeshSat UUID) with a bounded D-Bus wait and
// an hci-power-cycle retry on timeout. Idempotent: if Connected is
// already true, returns immediately.
func (c *BLEClientInterface) ensureConnected(ctx context.Context, device dbus.BusObject) error {
	// Fast path: already connected.
	if c.readConnected(device) {
		return nil
	}

	var lastErr error
	for attempt := 1; attempt <= connectMaxAttempts; attempt++ {
		// Kick off ConnectProfile in a goroutine so we can bound the
		// wait. ConnectProfile blocks until the LE handshake finishes
		// or BlueZ gives up. godbus's Call is synchronous and will
		// hang for the default 25 s if BlueZ never replies; the
		// goroutine + select wraps that with our own 8 s cap.
		done := make(chan error, 1)
		go func() {
			done <- device.Call("org.bluez.Device1.ConnectProfile", 0, bleServiceUUID).Err
		}()

		select {
		case err := <-done:
			if err == nil {
				return nil
			}
			// NotAvailable ("br-connection-profile-unavailable") is
			// the expected no-op when BlueZ previously indexed a
			// BR/EDR class bit for this device; the LE side still
			// completes in parallel. Check Connected before giving up.
			if c.readConnected(device) {
				log.Info().Str("peer", c.config.PeerAddress).Int("attempt", attempt).
					Msg("ble-client: ConnectProfile returned non-fatal; Connected=true")
				return nil
			}
			lastErr = err
		case <-time.After(connectTimeout):
			lastErr = fmt.Errorf("ConnectProfile timed out after %s", connectTimeout)
		case <-ctx.Done():
			return ctx.Err()
		}

		// If we got here the attempt failed. Double-check Connected —
		// BlueZ sometimes races the reply after the LE link is up.
		if c.readConnected(device) {
			return nil
		}

		if attempt < connectMaxAttempts {
			log.Warn().Str("peer", c.config.PeerAddress).Int("attempt", attempt).
				Err(lastErr).Msg("ble-client: cycling hci0 to unstick brcmfmac BT firmware")
			c.cycleAdapter(ctx)
		}
	}
	return fmt.Errorf("Connect failed after %d attempts: %w", connectMaxAttempts, lastErr)
}

// readConnected fetches the current Device1.Connected property. Any
// error falls through as "not connected" so the caller retries.
func (c *BLEClientInterface) readConnected(device dbus.BusObject) bool {
	var v dbus.Variant
	if err := device.Call("org.freedesktop.DBus.Properties.Get", 0,
		"org.bluez.Device1", "Connected").Store(&v); err != nil {
		return false
	}
	b, ok := v.Value().(bool)
	return ok && b
}

// cycleAdapter resets the local HCI adapter via btmgmt. BlueZ keeps
// its state (pair records, trust list) but the controller firmware
// restarts. Non-fatal — if btmgmt isn't available or the cycle fails,
// we fall through to the next attempt without the reset and log it.
func (c *BLEClientInterface) cycleAdapter(ctx context.Context) {
	adapter := c.config.AdapterID
	if adapter == "" {
		adapter = "hci0"
	}
	for _, cmd := range [][]string{{"power", "off"}, {"power", "on"}} {
		args := append([]string{"-i", adapter}, cmd...)
		if out, err := exec.CommandContext(ctx, "btmgmt", args...).CombinedOutput(); err != nil {
			log.Warn().Str("adapter", adapter).Str("cmd", strings.Join(cmd, " ")).
				Err(err).Str("output", strings.TrimSpace(string(out))).
				Msg("ble-client: btmgmt cycle failed (continuing)")
			return
		}
		// BlueZ needs a moment between off and on for the firmware to
		// quiesce; 1 s is the typical settle time observed on Pi 5.
		select {
		case <-time.After(1 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

// waitServicesResolved polls the Device1.ServicesResolved property
// for up to 10 s. BlueZ flips this to true once GATT discovery is
// complete; until then ObjectManager has an incomplete view.
func (c *BLEClientInterface) waitServicesResolved(ctx context.Context, device dbus.BusObject) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var v dbus.Variant
		if err := device.Call("org.freedesktop.DBus.Properties.Get", 0,
			"org.bluez.Device1", "ServicesResolved").Store(&v); err == nil {
			if b, ok := v.Value().(bool); ok && b {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("ServicesResolved never became true")
}

// findCharacteristics walks ObjectManager to locate the peers Reticulum
// service + characteristics on this device. Sets c.txChar / c.rxChar
// and their paths.
func (c *BLEClientInterface) findCharacteristics() error {
	bluez := c.conn.Object("org.bluez", "/")
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := bluez.Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objects); err != nil {
		return fmt.Errorf("GetManagedObjects: %w", err)
	}
	devicePrefix := string(c.devicePath) + "/"
	for path, ifaces := range objects {
		// Limit scan to characteristics under our specific device path.
		if !strings.HasPrefix(string(path), devicePrefix) {
			continue
		}
		chProps, ok := ifaces["org.bluez.GattCharacteristic1"]
		if !ok {
			continue
		}
		uuidVar, ok := chProps["UUID"]
		if !ok {
			continue
		}
		uuid, ok := uuidVar.Value().(string)
		if !ok {
			continue
		}
		lower := strings.ToLower(uuid)
		switch lower {
		case bleTXCharUUID:
			c.txCharPath = path
			c.txChar = c.conn.Object("org.bluez", path)
		case bleRXCharUUID:
			c.rxCharPath = path
			c.rxChar = c.conn.Object("org.bluez", path)
		}
	}
	if c.txChar == nil || c.rxChar == nil {
		return fmt.Errorf("Reticulum TX/RX chars not found on peer (is_meshsat check should have gated this)")
	}
	return nil
}

// notifyLoop pulls PropertiesChanged signals and reassembles packets.
func (c *BLEClientInterface) notifyLoop() {
	for {
		select {
		case <-c.stopCh:
			return
		case sig, ok := <-c.signalCh:
			if !ok {
				return
			}
			if sig.Path != c.txCharPath {
				continue
			}
			// PropertiesChanged signature: (string interfaceName,
			// dict changed_props, array<string> invalidated).
			if len(sig.Body) < 2 {
				continue
			}
			changed, ok := sig.Body[1].(map[string]dbus.Variant)
			if !ok {
				continue
			}
			valVar, ok := changed["Value"]
			if !ok {
				continue
			}
			chunk, ok := valVar.Value().([]byte)
			if !ok || len(chunk) == 0 {
				continue
			}
			if packet, complete := sarReassemble(&c.sarBuf, chunk); complete && c.callback != nil {
				c.callback(packet)
			}
		}
	}
}

// Send segments the packet and writes each segment to the peers RX
// characteristic. Uses write-without-response for throughput — BLE
// L2CAP already provides reliability.
func (c *BLEClientInterface) Send(ctx context.Context, packet []byte) error {
	c.mu.Lock()
	online := c.online
	c.mu.Unlock()
	if !online {
		return fmt.Errorf("ble-client %s offline", c.config.Name)
	}
	segments := sarSegment(packet, &c.txSeq)
	opts := map[string]dbus.Variant{
		"type": dbus.MakeVariant("command"), // write-without-response
	}
	for _, seg := range segments {
		if err := c.rxChar.Call("org.bluez.GattCharacteristic1.WriteValue", 0, seg, opts).Err; err != nil {
			return fmt.Errorf("ble-client WriteValue: %w", err)
		}
	}
	log.Debug().Str("iface", c.config.Name).Int("size", len(packet)).
		Int("segments", len(segments)).Msg("ble-client: packet sent")
	return nil
}

// Stop tears down the subscription + D-Bus connection. Safe to call
// multiple times.
func (c *BLEClientInterface) Stop() {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	c.stopped = true
	c.online = false
	close(c.stopCh)
	conn := c.conn
	c.mu.Unlock()

	if c.notifyActive && c.txChar != nil {
		c.txChar.Call("org.bluez.GattCharacteristic1.StopNotify", 0)
	}
	if conn != nil {
		conn.Close()
	}
	log.Info().Str("iface", c.config.Name).Str("peer", c.config.PeerAddress).Msg("ble-client: stopped")
}

// IsOnline reports the current state.
func (c *BLEClientInterface) IsOnline() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.online
}

// RegisterBLEClientInterface builds the client + the ReticulumInterface
// wrapper, mirroring RegisterBLEInterface on the peripheral side.
func RegisterBLEClientInterface(config BLEClientConfig, callback func(packet []byte)) (*BLEClientInterface, *ReticulumInterface) {
	cli := NewBLEClientInterface(config, callback)
	ri := NewReticulumInterface(config.Name, reticulum.IfaceBLE, 500, cli.Send)
	return cli, ri
}
