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

	// Ensure the remote device is connected. bluetoothctl connect would
	// also do this but we're safe calling it again — BlueZ no-ops when
	// already connected.
	var connected dbus.Variant
	if err := device.Call("org.freedesktop.DBus.Properties.Get", 0,
		"org.bluez.Device1", "Connected").Store(&connected); err != nil {
		conn.Close()
		return fmt.Errorf("ble-client: device %s not found (pair first): %w", c.config.PeerAddress, err)
	}
	if p, ok := connected.Value().(bool); !ok || !p {
		if err := device.Call("org.bluez.Device1.Connect", 0).Err; err != nil {
			conn.Close()
			return fmt.Errorf("ble-client: Connect failed: %w", err)
		}
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
