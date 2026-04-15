package routing

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
)

// BLE GATT service and characteristic UUIDs for Reticulum packet exchange.
const (
	bleServiceUUID = "7e57c0de-0001-4000-8000-000000000001"
	bleTXCharUUID  = "7e57c0de-0002-4000-8000-000000000001" // Bridge -> Peer (notify)
	bleRXCharUUID  = "7e57c0de-0003-4000-8000-000000000001" // Peer -> Bridge (write)

	bleAppPath     = "/com/meshsat/ble"
	bleServicePath = "/com/meshsat/ble/service0"
	bleTXCharPath  = "/com/meshsat/ble/service0/char_tx"
	bleRXCharPath  = "/com/meshsat/ble/service0/char_rx"
	bleAdvPath     = "/com/meshsat/ble/adv0"

	// SAR constants — segmentation and reassembly for BLE MTU < Reticulum MTU.
	// BLE 4.2+ negotiates up to ~244 byte ATT payload on Pi 5.
	bleSARMaxPayload = 243 // 244 byte ATT MTU minus 1 byte SAR header
	bleSARFlagSingle = 0x00
	bleSARFlagFirst  = 0x80
	bleSARFlagCont   = 0xA0
	bleSARFlagLast   = 0xC0
	bleSARFlagMask   = 0xE0
	bleSARSeqMask    = 0x1F
)

// BLEInterfaceConfig configures a BLE Reticulum interface.
type BLEInterfaceConfig struct {
	// Name is the interface identifier (e.g. "ble_0").
	Name string
	// AdapterID is the BlueZ adapter name (default "hci0").
	AdapterID string
	// DeviceName is the BLE advertised local name (default "MeshSat-RNS").
	DeviceName string
}

// BLEInterface is a bidirectional Reticulum interface over Bluetooth Low Energy.
// The bridge acts as a GATT peripheral (server) advertising a custom Reticulum
// service. Peers connect as BLE centrals, write to the RX characteristic, and
// subscribe to notifications on the TX characteristic.
//
// Packets larger than the BLE ATT MTU (~244 bytes) are transparently segmented
// via a simple SAR (Segmentation And Reassembly) layer. BLE provides reliable
// L2CAP delivery, so no retransmission logic is needed.
//
// Uses Pi 5 built-in BLE via BlueZ D-Bus API (pure Go, no CGO).
type BLEInterface struct {
	config   BLEInterfaceConfig
	callback func(packet []byte)

	mu        sync.Mutex
	online    bool
	stopCh    chan struct{}
	stopped   bool
	notifying bool

	conn   *dbus.Conn
	props  *prop.Properties
	txSeq  uint8
	sarBuf sarBuffer
}

// NewBLEInterface creates a new BLE Reticulum interface.
func NewBLEInterface(config BLEInterfaceConfig, callback func(packet []byte)) *BLEInterface {
	if config.AdapterID == "" {
		config.AdapterID = "hci0"
	}
	if config.DeviceName == "" {
		config.DeviceName = "MeshSat-RNS"
	}
	return &BLEInterface{
		config:   config,
		callback: callback,
		stopCh:   make(chan struct{}),
		sarBuf:   newSARBuffer(),
	}
}

// Start connects to BlueZ via D-Bus, registers the GATT service, and begins advertising.
func (b *BLEInterface) Start(ctx context.Context) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("ble: D-Bus system bus: %w", err)
	}
	b.conn = conn

	adapterPath := dbus.ObjectPath("/org/bluez/" + b.config.AdapterID)

	// Verify BlueZ adapter exists and power it on.
	adapter := conn.Object("org.bluez", adapterPath)
	var powered dbus.Variant
	if err := adapter.Call("org.freedesktop.DBus.Properties.Get", 0,
		"org.bluez.Adapter1", "Powered").Store(&powered); err != nil {
		conn.Close()
		return fmt.Errorf("ble: adapter %s not found: %w", b.config.AdapterID, err)
	}
	if p, ok := powered.Value().(bool); ok && !p {
		adapter.Call("org.freedesktop.DBus.Properties.Set", 0,
			"org.bluez.Adapter1", "Powered", dbus.MakeVariant(true))
	}

	// Export D-Bus objects for GATT application.
	if err := b.exportGATTObjects(); err != nil {
		conn.Close()
		return fmt.Errorf("ble: export GATT: %w", err)
	}

	// Register GATT application with BlueZ.
	if err := adapter.Call("org.bluez.GattManager1.RegisterApplication", 0,
		dbus.ObjectPath(bleAppPath), map[string]dbus.Variant{}).Err; err != nil {
		conn.Close()
		return fmt.Errorf("ble: register GATT application: %w", err)
	}

	// Export and register LE advertisement.
	if err := b.exportAdvertisement(); err != nil {
		conn.Close()
		return fmt.Errorf("ble: export advertisement: %w", err)
	}
	if err := adapter.Call("org.bluez.LEAdvertisingManager1.RegisterAdvertisement", 0,
		dbus.ObjectPath(bleAdvPath), map[string]dbus.Variant{}).Err; err != nil {
		// Non-fatal: advertising may fail but GATT server still works for paired devices.
		log.Warn().Err(err).Str("iface", b.config.Name).
			Msg("ble: advertising registration failed (GATT still active)")
	}

	b.mu.Lock()
	b.online = true
	b.mu.Unlock()

	log.Info().Str("iface", b.config.Name).Str("adapter", b.config.AdapterID).
		Str("name", b.config.DeviceName).Msg("ble reticulum interface started")
	return nil
}

// Send transmits a Reticulum packet via BLE GATT notification.
// Packets larger than the BLE MTU are transparently segmented via SAR.
func (b *BLEInterface) Send(ctx context.Context, packet []byte) error {
	b.mu.Lock()
	online := b.online
	notifying := b.notifying
	b.mu.Unlock()

	if !online {
		return fmt.Errorf("ble interface %s is offline", b.config.Name)
	}
	if !notifying {
		return fmt.Errorf("ble interface %s: no peer subscribed to notifications", b.config.Name)
	}

	segments := sarSegment(packet, &b.txSeq)
	for _, seg := range segments {
		if err := b.sendNotification(seg); err != nil {
			return fmt.Errorf("ble notify: %w", err)
		}
	}

	log.Debug().Str("iface", b.config.Name).Int("size", len(packet)).
		Int("segments", len(segments)).Msg("ble iface: packet sent")
	return nil
}

// Stop shuts down the BLE interface.
func (b *BLEInterface) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.stopped {
		return
	}
	b.stopped = true
	b.online = false
	close(b.stopCh)

	if b.conn != nil {
		adapterPath := dbus.ObjectPath("/org/bluez/" + b.config.AdapterID)
		adapter := b.conn.Object("org.bluez", adapterPath)
		adapter.Call("org.bluez.GattManager1.UnregisterApplication", 0, dbus.ObjectPath(bleAppPath))
		adapter.Call("org.bluez.LEAdvertisingManager1.UnregisterAdvertisement", 0, dbus.ObjectPath(bleAdvPath))
		b.conn.Close()
	}
	log.Info().Str("iface", b.config.Name).Msg("ble reticulum interface stopped")
}

// IsOnline returns whether the BLE interface is active.
func (b *BLEInterface) IsOnline() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.online
}

// RegisterBLEInterface creates the BLEInterface and its ReticulumInterface wrapper.
func RegisterBLEInterface(config BLEInterfaceConfig, callback func(packet []byte)) (*BLEInterface, *ReticulumInterface) {
	bleIface := NewBLEInterface(config, callback)
	ri := NewReticulumInterface(
		config.Name,
		reticulum.IfaceBLE,
		500, // Reticulum MTU — BLE SAR handles segmentation transparently
		bleIface.Send,
	)
	return bleIface, ri
}

// --- D-Bus GATT export ---

// bleGATTApp implements org.freedesktop.DBus.ObjectManager for the GATT application.
type bleGATTApp struct {
	b *BLEInterface
}

func (a *bleGATTApp) GetManagedObjects() (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, *dbus.Error) {
	objects := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
		bleServicePath: {
			"org.bluez.GattService1": {
				"UUID":    dbus.MakeVariant(bleServiceUUID),
				"Primary": dbus.MakeVariant(true),
			},
		},
		bleTXCharPath: {
			"org.bluez.GattCharacteristic1": {
				"UUID":    dbus.MakeVariant(bleTXCharUUID),
				"Service": dbus.MakeVariant(dbus.ObjectPath(bleServicePath)),
				"Flags":   dbus.MakeVariant([]string{"notify", "read"}),
			},
		},
		bleRXCharPath: {
			"org.bluez.GattCharacteristic1": {
				"UUID":    dbus.MakeVariant(bleRXCharUUID),
				"Service": dbus.MakeVariant(dbus.ObjectPath(bleServicePath)),
				"Flags":   dbus.MakeVariant([]string{"write-without-response", "write"}),
			},
		},
	}
	return objects, nil
}

// bleTXChar implements the TX characteristic (Bridge -> Peer via notify).
type bleTXChar struct {
	b *BLEInterface
}

func (c *bleTXChar) ReadValue(options map[string]dbus.Variant) ([]byte, *dbus.Error) {
	return []byte{}, nil
}

func (c *bleTXChar) StartNotify() *dbus.Error {
	c.b.mu.Lock()
	c.b.notifying = true
	c.b.mu.Unlock()
	log.Debug().Str("iface", c.b.config.Name).Msg("ble: peer subscribed to notifications")
	return nil
}

func (c *bleTXChar) StopNotify() *dbus.Error {
	c.b.mu.Lock()
	c.b.notifying = false
	c.b.mu.Unlock()
	log.Debug().Str("iface", c.b.config.Name).Msg("ble: peer unsubscribed from notifications")
	return nil
}

// bleRXChar implements the RX characteristic (Peer -> Bridge via write).
type bleRXChar struct {
	b *BLEInterface
}

func (c *bleRXChar) WriteValue(value []byte, options map[string]dbus.Variant) *dbus.Error {
	if len(value) < 1 {
		return nil
	}

	packet, complete := sarReassemble(&c.b.sarBuf, value)
	if complete && len(packet) >= 2 {
		log.Debug().Str("iface", c.b.config.Name).Int("size", len(packet)).
			Msg("ble iface: received reticulum packet")
		c.b.callback(packet)
	}
	return nil
}

func (b *BLEInterface) exportGATTObjects() error {
	// Export the ObjectManager (GATT application root).
	app := &bleGATTApp{b: b}
	if err := b.conn.Export(app, bleAppPath, "org.freedesktop.DBus.ObjectManager"); err != nil {
		return fmt.Errorf("export ObjectManager: %w", err)
	}

	// Export introspectable for the app path.
	b.conn.Export(introspect.NewIntrospectable(&introspect.Node{
		Name: bleAppPath,
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			{
				Name: "org.freedesktop.DBus.ObjectManager",
				Methods: []introspect.Method{
					{Name: "GetManagedObjects"},
				},
			},
		},
	}), bleAppPath, "org.freedesktop.DBus.Introspectable")

	// Export TX characteristic.
	txChar := &bleTXChar{b: b}
	if err := b.conn.Export(txChar, bleTXCharPath, "org.bluez.GattCharacteristic1"); err != nil {
		return fmt.Errorf("export TX char: %w", err)
	}

	// Export TX properties for notification support.
	propsSpec := map[string]map[string]*prop.Prop{
		"org.bluez.GattCharacteristic1": {
			"UUID": {
				Value:    bleTXCharUUID,
				Writable: false,
				Emit:     prop.EmitTrue,
			},
			"Service": {
				Value:    dbus.ObjectPath(bleServicePath),
				Writable: false,
				Emit:     prop.EmitFalse,
			},
			"Flags": {
				Value:    []string{"notify", "read"},
				Writable: false,
				Emit:     prop.EmitFalse,
			},
			"Value": {
				Value:    []byte{},
				Writable: false,
				Emit:     prop.EmitTrue,
			},
		},
	}
	var err error
	b.props, err = prop.Export(b.conn, bleTXCharPath, propsSpec)
	if err != nil {
		return fmt.Errorf("export TX props: %w", err)
	}

	// Export RX characteristic.
	rxChar := &bleRXChar{b: b}
	if err := b.conn.Export(rxChar, bleRXCharPath, "org.bluez.GattCharacteristic1"); err != nil {
		return fmt.Errorf("export RX char: %w", err)
	}

	return nil
}

func (b *BLEInterface) exportAdvertisement() error {
	adv := map[string]dbus.Variant{
		"Type":         dbus.MakeVariant("peripheral"),
		"ServiceUUIDs": dbus.MakeVariant([]string{bleServiceUUID}),
		"LocalName":    dbus.MakeVariant(b.config.DeviceName),
	}

	advObj := &bleAdv{props: adv}
	if err := b.conn.Export(advObj, bleAdvPath, "org.bluez.LEAdvertisement1"); err != nil {
		return fmt.Errorf("export advertisement: %w", err)
	}
	if err := b.conn.Export(advObj, bleAdvPath, "org.freedesktop.DBus.Properties"); err != nil {
		return fmt.Errorf("export advertisement properties: %w", err)
	}
	return nil
}

// bleAdv implements LEAdvertisement1 properties.
type bleAdv struct {
	props map[string]dbus.Variant
}

func (a *bleAdv) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	return a.props, nil
}

func (a *bleAdv) Get(iface, property string) (dbus.Variant, *dbus.Error) {
	if v, ok := a.props[property]; ok {
		return v, nil
	}
	return dbus.Variant{}, nil
}

func (a *bleAdv) Set(string, string, dbus.Variant) *dbus.Error {
	return nil
}

func (a *bleAdv) Release() *dbus.Error {
	return nil
}

func (b *BLEInterface) sendNotification(data []byte) error {
	if b.props == nil {
		return fmt.Errorf("GATT properties not initialized")
	}
	b.props.SetMust("org.bluez.GattCharacteristic1", "Value", data)
	return nil
}

// --- SAR (Segmentation And Reassembly) ---
//
// Header format (1 byte):
//   bits 7-5: flags (000=single, 100=first, 101=continuation, 110=last)
//   bits 4-0: sequence number (0-31, wrapping)
//
// BLE provides reliable L2CAP delivery, so no retransmission logic is needed.
// We only need fragmentation to handle BLE MTU < Reticulum MTU (500 bytes).

// sarSegment splits a Reticulum packet into BLE-sized chunks with SAR headers.
func sarSegment(packet []byte, seq *uint8) [][]byte {
	if len(packet) <= bleSARMaxPayload {
		chunk := make([]byte, 1+len(packet))
		chunk[0] = bleSARFlagSingle | (*seq & bleSARSeqMask)
		copy(chunk[1:], packet)
		*seq = (*seq + 1) & bleSARSeqMask
		return [][]byte{chunk}
	}

	var segments [][]byte
	remaining := packet
	first := true

	for len(remaining) > 0 {
		size := bleSARMaxPayload
		if size > len(remaining) {
			size = len(remaining)
		}

		chunk := make([]byte, 1+size)
		switch {
		case first:
			chunk[0] = bleSARFlagFirst | (*seq & bleSARSeqMask)
			first = false
		case size == len(remaining):
			chunk[0] = bleSARFlagLast | (*seq & bleSARSeqMask)
		default:
			chunk[0] = bleSARFlagCont | (*seq & bleSARSeqMask)
		}
		copy(chunk[1:], remaining[:size])
		remaining = remaining[size:]
		*seq = (*seq + 1) & bleSARSeqMask
		segments = append(segments, chunk)
	}
	return segments
}

// sarBuffer accumulates SAR fragments for reassembly.
type sarBuffer struct {
	mu        sync.Mutex
	fragments []byte
	active    bool
	lastSeen  time.Time
}

func newSARBuffer() sarBuffer {
	return sarBuffer{}
}

// sarReassemble processes an incoming BLE chunk and returns the complete packet
// when the last fragment is received. Returns (nil, false) for partial data.
func sarReassemble(buf *sarBuffer, chunk []byte) ([]byte, bool) {
	if len(chunk) < 1 {
		return nil, false
	}

	header := chunk[0]
	flags := header & bleSARFlagMask
	payload := chunk[1:]

	buf.mu.Lock()
	defer buf.mu.Unlock()

	switch flags {
	case bleSARFlagSingle:
		buf.fragments = nil
		buf.active = false
		result := make([]byte, len(payload))
		copy(result, payload)
		return result, true

	case bleSARFlagFirst:
		buf.fragments = make([]byte, 0, 512)
		buf.fragments = append(buf.fragments, payload...)
		buf.active = true
		buf.lastSeen = time.Now()
		return nil, false

	case bleSARFlagCont:
		if !buf.active {
			return nil, false
		}
		buf.fragments = append(buf.fragments, payload...)
		buf.lastSeen = time.Now()
		return nil, false

	case bleSARFlagLast:
		if !buf.active {
			return nil, false
		}
		buf.fragments = append(buf.fragments, payload...)
		result := make([]byte, len(buf.fragments))
		copy(result, buf.fragments)
		buf.fragments = nil
		buf.active = false
		return result, true

	default:
		buf.fragments = nil
		buf.active = false
		return nil, false
	}
}
