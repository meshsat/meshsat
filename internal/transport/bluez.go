package transport

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/rs/zerolog/log"
)

// BlueZ over the system bus, for the mesh node over Bluetooth LE: the phone
// (or any Linux host) adopts a Meshtastic node the way MeshSat Android and iOS
// do, and the MeshSat firmware's modem pipe rides the same link. Pure godbus,
// as the ModemManager transport: CGO stays off. [MESHSAT-1390]

const (
	bluezService    = "org.bluez"
	bluezAdapterIf  = "org.bluez.Adapter1"
	bluezDeviceIf   = "org.bluez.Device1"
	bluezGattSvcIf  = "org.bluez.GattService1"
	bluezGattChrIf  = "org.bluez.GattCharacteristic1"
	bluezAgentMgrIf = "org.bluez.AgentManager1"
	bluezAgentIf    = "org.bluez.Agent1"
	dbusPropsIf     = "org.freedesktop.DBus.Properties"
	dbusObjMgrIf    = "org.freedesktop.DBus.ObjectManager"

	// bleAgentPath is where this process answers BlueZ's pairing questions.
	bleAgentPath = dbus.ObjectPath("/net/meshsat/bluez/agent")
	// blePairWait is how long a pairing waits for the person to type the PIN the
	// node shows (a Meshtastic node with a screen shows a random one).
	blePairWait = 2 * time.Minute
)

// BLEDevice is a node BlueZ knows or has just seen.
type BLEDevice struct {
	Address   string `json:"address"`
	Name      string `json:"name"`
	RSSI      int    `json:"rssi,omitempty"`
	Paired    bool   `json:"paired"`
	Connected bool   `json:"connected"`
	// Chosen marks the node this Bridge was told to use.
	Chosen bool `json:"chosen,omitempty"`
	uuids  []string
}

func (d BLEDevice) hasUUID(uuid string) bool {
	for _, u := range d.uuids {
		if strings.EqualFold(u, uuid) {
			return true
		}
	}
	return false
}

// propsChange is a PropertiesChanged signal for one object, or its removal
// (Iface "removed").
type propsChange struct {
	Iface   string
	Changed map[string]dbus.Variant
}

// bluezBus is one connection to BlueZ. Signals are matched once and handed to
// whoever watches an object path.
type bluezBus struct {
	conn    *dbus.Conn
	adapter dbus.ObjectPath

	sigCh    chan *dbus.Signal
	watchMu  sync.Mutex
	watchers map[dbus.ObjectPath][]chan propsChange
}

func openBlueZ(adapter string) (*bluezBus, error) {
	if adapter == "" {
		adapter = "hci0"
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	b := &bluezBus{conn: conn, adapter: dbus.ObjectPath("/org/bluez/" + adapter), watchers: map[dbus.ObjectPath][]chan propsChange{}}
	objs, err := b.managedObjects()
	if err != nil {
		conn.Close()
		return nil, err
	}
	if _, ok := objs[b.adapter][bluezAdapterIf]; !ok {
		conn.Close()
		return nil, fmt.Errorf("bluetooth adapter %s not found", adapter)
	}
	if err := conn.AddMatchSignal(dbus.WithMatchSender(bluezService), dbus.WithMatchInterface(dbusPropsIf), dbus.WithMatchMember("PropertiesChanged")); err != nil {
		conn.Close()
		return nil, fmt.Errorf("bluez signals: %w", err)
	}
	if err := conn.AddMatchSignal(dbus.WithMatchSender(bluezService), dbus.WithMatchInterface(dbusObjMgrIf), dbus.WithMatchMember("InterfacesRemoved")); err != nil {
		conn.Close()
		return nil, fmt.Errorf("bluez signals: %w", err)
	}
	b.sigCh = make(chan *dbus.Signal, 256)
	conn.Signal(b.sigCh)
	go b.dispatch()
	return b, nil
}

func (b *bluezBus) close() {
	b.conn.RemoveSignal(b.sigCh)
	b.conn.Close()
}

func (b *bluezBus) dispatch() {
	for sig := range b.sigCh {
		switch sig.Name {
		case dbusPropsIf + ".PropertiesChanged":
			if len(sig.Body) < 2 {
				continue
			}
			iface, _ := sig.Body[0].(string)
			changed, _ := sig.Body[1].(map[string]dbus.Variant)
			b.deliver(sig.Path, propsChange{Iface: iface, Changed: changed})
		case dbusObjMgrIf + ".InterfacesRemoved":
			if len(sig.Body) < 1 {
				continue
			}
			path, _ := sig.Body[0].(dbus.ObjectPath)
			b.deliver(path, propsChange{Iface: "removed"})
		}
	}
}

func (b *bluezBus) deliver(path dbus.ObjectPath, change propsChange) {
	b.watchMu.Lock()
	watchers := append([]chan propsChange(nil), b.watchers[path]...)
	b.watchMu.Unlock()
	for _, w := range watchers {
		select {
		case w <- change:
		default:
		}
	}
}

// watch delivers an object's property changes (and its removal) until the
// returned function is called.
func (b *bluezBus) watch(path dbus.ObjectPath) (<-chan propsChange, func()) {
	ch := make(chan propsChange, 64)
	b.watchMu.Lock()
	b.watchers[path] = append(b.watchers[path], ch)
	b.watchMu.Unlock()
	return ch, func() {
		b.watchMu.Lock()
		defer b.watchMu.Unlock()
		ws := b.watchers[path]
		for i, w := range ws {
			if w == ch {
				b.watchers[path] = append(ws[:i], ws[i+1:]...)
				return
			}
		}
	}
}

func (b *bluezBus) managedObjects() (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := b.conn.Object(bluezService, "/").CallWithContext(ctx, dbusObjMgrIf+".GetManagedObjects", 0).Store(&objs); err != nil {
		return nil, fmt.Errorf("bluez objects: %w", err)
	}
	return objs, nil
}

func (b *bluezBus) call(ctx context.Context, path dbus.ObjectPath, method string, args ...interface{}) error {
	return b.conn.Object(bluezService, path).CallWithContext(ctx, method, 0, args...).Err
}

func (b *bluezBus) getProp(ctx context.Context, path dbus.ObjectPath, iface, name string) (dbus.Variant, error) {
	var v dbus.Variant
	err := b.conn.Object(bluezService, path).CallWithContext(ctx, dbusPropsIf+".Get", 0, iface, name).Store(&v)
	return v, err
}

func (b *bluezBus) getBool(ctx context.Context, path dbus.ObjectPath, iface, name string) (bool, error) {
	v, err := b.getProp(ctx, path, iface, name)
	if err != nil {
		return false, err
	}
	on, _ := v.Value().(bool)
	return on, nil
}

func (b *bluezBus) setProp(ctx context.Context, path dbus.ObjectPath, iface, name string, value interface{}) error {
	return b.conn.Object(bluezService, path).CallWithContext(ctx, dbusPropsIf+".Set", 0, iface, name, dbus.MakeVariant(value)).Err
}

func variantString(v dbus.Variant) string {
	s, _ := v.Value().(string)
	return s
}

// bleDevicePath is BlueZ's object for a device address under the adapter.
func (b *bluezBus) devicePath(address string) dbus.ObjectPath {
	return dbus.ObjectPath(string(b.adapter) + "/dev_" + strings.ReplaceAll(strings.ToUpper(address), ":", "_"))
}

func bleDeviceFromProps(props map[string]dbus.Variant) BLEDevice {
	dev := BLEDevice{Address: variantString(props["Address"]), Name: variantString(props["Name"])}
	if dev.Name == "" {
		dev.Name = variantString(props["Alias"])
	}
	if v, ok := props["RSSI"]; ok {
		if n, ok := v.Value().(int16); ok {
			dev.RSSI = int(n)
		}
	}
	dev.Paired, _ = props["Paired"].Value().(bool)
	dev.Connected, _ = props["Connected"].Value().(bool)
	dev.uuids, _ = props["UUIDs"].Value().([]string)
	return dev
}

// device is BlueZ's current view of one address, or nil when it has never
// seen it.
func (b *bluezBus) device(address string) (*BLEDevice, error) {
	objs, err := b.managedObjects()
	if err != nil {
		return nil, err
	}
	props, ok := objs[b.devicePath(address)][bluezDeviceIf]
	if !ok {
		return nil, nil
	}
	dev := bleDeviceFromProps(props)
	return &dev, nil
}

// discover scans for d and returns the devices advertising serviceUUID (and
// the paired ones BlueZ still knows), strongest first.
func (b *bluezBus) discover(ctx context.Context, d time.Duration, serviceUUID string) ([]BLEDevice, error) {
	adapter := b.conn.Object(bluezService, b.adapter)
	if on, err := b.getBool(ctx, b.adapter, bluezAdapterIf, "Powered"); err == nil && !on {
		if err := b.setProp(ctx, b.adapter, bluezAdapterIf, "Powered", true); err != nil {
			return nil, fmt.Errorf("bluetooth is off and could not be switched on: %w", err)
		}
	}
	filter := map[string]dbus.Variant{
		"UUIDs":         dbus.MakeVariant([]string{serviceUUID}),
		"Transport":     dbus.MakeVariant("le"),
		"DuplicateData": dbus.MakeVariant(false),
	}
	if err := adapter.CallWithContext(ctx, bluezAdapterIf+".SetDiscoveryFilter", 0, filter).Err; err != nil {
		log.Debug().Err(err).Msg("bluez: discovery filter refused, scanning unfiltered")
	}
	if err := adapter.CallWithContext(ctx, bluezAdapterIf+".StartDiscovery", 0).Err; err != nil && !strings.Contains(err.Error(), "InProgress") {
		return nil, fmt.Errorf("start discovery: %w", err)
	}
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
	_ = adapter.Call(bluezAdapterIf+".StopDiscovery", 0).Err
	_ = adapter.Call(bluezAdapterIf+".SetDiscoveryFilter", 0, map[string]dbus.Variant{}).Err

	objs, err := b.managedObjects()
	if err != nil {
		return nil, err
	}
	var out []BLEDevice
	for path, ifaces := range objs {
		props, ok := ifaces[bluezDeviceIf]
		if !ok || !strings.HasPrefix(string(path), string(b.adapter)+"/") {
			continue
		}
		dev := bleDeviceFromProps(props)
		if !dev.hasUUID(serviceUUID) {
			continue
		}
		// A device BlueZ remembers from an earlier day has no RSSI: keep it
		// only when it is ours (paired), so the list is what is in range now.
		if dev.RSSI == 0 && !dev.Paired && !dev.Connected {
			continue
		}
		out = append(out, dev)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Connected != out[j].Connected {
			return out[i].Connected
		}
		return out[i].RSSI > out[j].RSSI
	})
	return out, nil
}

// pair bonds with a device; BlueZ asks the agent for the PIN meanwhile.
func (b *bluezBus) pair(ctx context.Context, path dbus.ObjectPath) error {
	err := b.call(ctx, path, bluezDeviceIf+".Pair")
	if err != nil && strings.Contains(err.Error(), "AlreadyExists") {
		return nil
	}
	return err
}

// connect opens the LE link (a device that is already connected is fine).
func (b *bluezBus) connect(ctx context.Context, path dbus.ObjectPath) error {
	err := b.call(ctx, path, bluezDeviceIf+".Connect")
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "already") {
		return nil
	}
	return err
}

func (b *bluezBus) disconnect(path dbus.ObjectPath) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = b.call(ctx, path, bluezDeviceIf+".Disconnect")
}

// removeDevice drops the bond and everything BlueZ keeps about the device.
func (b *bluezBus) removeDevice(path dbus.ObjectPath) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := b.call(ctx, b.adapter, bluezAdapterIf+".RemoveDevice", path)
	if err != nil && strings.Contains(err.Error(), "DoesNotExist") {
		return nil
	}
	return err
}

// waitServicesResolved waits for BlueZ to have read the device's GATT table.
func (b *bluezBus) waitServicesResolved(ctx context.Context, path dbus.ObjectPath) error {
	for {
		resolved, err := b.getBool(ctx, path, bluezDeviceIf, "ServicesResolved")
		if err != nil {
			return err
		}
		if resolved {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the node's services did not resolve: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// characteristics finds, under a device, the characteristics of one service by
// UUID (lower case keys), and reports whether the service exists at all.
func (b *bluezBus) characteristics(device dbus.ObjectPath, serviceUUID string, charUUIDs ...string) (map[string]dbus.ObjectPath, bool, error) {
	objs, err := b.managedObjects()
	if err != nil {
		return nil, false, err
	}
	services := map[dbus.ObjectPath]bool{}
	for path, ifaces := range objs {
		if props, ok := ifaces[bluezGattSvcIf]; ok && strings.HasPrefix(string(path), string(device)+"/") && strings.EqualFold(variantString(props["UUID"]), serviceUUID) {
			services[path] = true
		}
	}
	out := map[string]dbus.ObjectPath{}
	for path, ifaces := range objs {
		props, ok := ifaces[bluezGattChrIf]
		if !ok {
			continue
		}
		svc, _ := props["Service"].Value().(dbus.ObjectPath)
		if !services[svc] {
			continue
		}
		uuid := strings.ToLower(variantString(props["UUID"]))
		for _, want := range charUUIDs {
			if uuid == strings.ToLower(want) {
				out[uuid] = path
			}
		}
	}
	return out, len(services) > 0, nil
}

func (b *bluezBus) readValue(ctx context.Context, char dbus.ObjectPath) ([]byte, error) {
	var out []byte
	err := b.conn.Object(bluezService, char).CallWithContext(ctx, bluezGattChrIf+".ReadValue", 0, map[string]dbus.Variant{}).Store(&out)
	return out, err
}

// writeValue writes with response ("request"): a frame can be longer than the
// MTU, and a write without response would be silently cut.
func (b *bluezBus) writeValue(ctx context.Context, char dbus.ObjectPath, data []byte) error {
	return b.call(ctx, char, bluezGattChrIf+".WriteValue", data, map[string]dbus.Variant{"type": dbus.MakeVariant("request")})
}

func (b *bluezBus) startNotify(ctx context.Context, char dbus.ObjectPath) error {
	err := b.call(ctx, char, bluezGattChrIf+".StartNotify")
	if err != nil && strings.Contains(err.Error(), "InProgress") {
		return nil
	}
	return err
}

func (b *bluezBus) stopNotify(char dbus.ObjectPath) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = b.call(ctx, char, bluezGattChrIf+".StopNotify")
}

// bleAgent answers BlueZ's pairing questions for the process: a Meshtastic
// node shows a six-digit passkey on its screen (or logs it, or uses a fixed
// one), and the person types it in the app, which hands it here. Exported on
// the bus as org.bluez.Agent1 with the KeyboardOnly capability.
type bleAgent struct {
	mu      sync.Mutex
	waiting bool
	device  dbus.ObjectPath
	since   time.Time
	pin     chan string
}

func (a *bleAgent) begin(device dbus.ObjectPath) chan string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.waiting, a.device, a.since = true, device, time.Now()
	a.pin = make(chan string, 1)
	return a.pin
}

func (a *bleAgent) end() {
	a.mu.Lock()
	a.waiting = false
	a.mu.Unlock()
}

// pending reports whether a pairing is waiting for a PIN, and since when.
func (a *bleAgent) pending() (bool, time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.waiting, a.since
}

// supply hands the PIN the person typed to the waiting pairing.
func (a *bleAgent) supply(pin string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.waiting {
		return errors.New("no pairing is waiting for a PIN")
	}
	select {
	case a.pin <- pin:
	default:
	}
	return nil
}

func (a *bleAgent) askPasskey(device dbus.ObjectPath) (uint32, *dbus.Error) {
	ch := a.begin(device)
	log.Info().Str("device", string(device)).Msg("bluez: the node asks for its passkey")
	defer a.end()
	select {
	case pin := <-ch:
		n, err := strconv.ParseUint(strings.TrimSpace(pin), 10, 32)
		if err != nil || n > 999999 {
			return 0, dbus.NewError("org.bluez.Error.Rejected", nil)
		}
		return uint32(n), nil
	case <-time.After(blePairWait):
		return 0, dbus.NewError("org.bluez.Error.Canceled", nil)
	}
}

// RequestPasskey is what BlueZ calls for LE passkey entry.
func (a *bleAgent) RequestPasskey(device dbus.ObjectPath) (uint32, *dbus.Error) {
	return a.askPasskey(device)
}

// RequestPinCode is the legacy form of the same question.
func (a *bleAgent) RequestPinCode(device dbus.ObjectPath) (string, *dbus.Error) {
	n, err := a.askPasskey(device)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n), nil
}

func (a *bleAgent) DisplayPasskey(device dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	return nil
}

func (a *bleAgent) DisplayPinCode(device dbus.ObjectPath, pincode string) *dbus.Error { return nil }

// RequestConfirmation: the node shows the same number; nothing to compare here.
func (a *bleAgent) RequestConfirmation(device dbus.ObjectPath, passkey uint32) *dbus.Error {
	return nil
}

func (a *bleAgent) RequestAuthorization(device dbus.ObjectPath) *dbus.Error { return nil }

func (a *bleAgent) AuthorizeService(device dbus.ObjectPath, uuid string) *dbus.Error { return nil }

func (a *bleAgent) Cancel() *dbus.Error {
	a.end()
	return nil
}

func (a *bleAgent) Release() *dbus.Error { return nil }

// registerAgent makes this process BlueZ's default pairing agent.
func (b *bluezBus) registerAgent(agent *bleAgent) error {
	if err := b.conn.Export(agent, bleAgentPath, bluezAgentIf); err != nil {
		return fmt.Errorf("export agent: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr := b.conn.Object(bluezService, "/org/bluez")
	if err := mgr.CallWithContext(ctx, bluezAgentMgrIf+".RegisterAgent", 0, bleAgentPath, "KeyboardOnly").Err; err != nil && !strings.Contains(err.Error(), "AlreadyExists") {
		return fmt.Errorf("register agent: %w", err)
	}
	if err := mgr.CallWithContext(ctx, bluezAgentMgrIf+".RequestDefaultAgent", 0, bleAgentPath).Err; err != nil {
		log.Warn().Err(err).Msg("bluez: not the default pairing agent; another agent may answer the node's PIN prompt")
	}
	return nil
}
