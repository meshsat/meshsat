package routing

// RNode over Bluetooth LE: the RNode firmware exposes the Nordic UART
// Service; the host writes KISS bytes to the RX characteristic and
// receives them as notifications on the TX characteristic. This is the
// BlueZ (D-Bus) central for that service, presented to the RNode driver
// as a byte stream, the way RNodeInterface.py's BLEConnection does with
// bleak. Only bonded devices are used, as upstream. [MESHSAT-1349]

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/rs/zerolog/log"
)

// Nordic UART Service UUIDs (RNodeInterface.py BLEConnection).
const (
	nusServiceUUID = "6e400001-b5a3-f393-e0a9-e50e24dcca9e"
	nusRXCharUUID  = "6e400002-b5a3-f393-e0a9-e50e24dcca9e" // host writes here
	nusTXCharUUID  = "6e400003-b5a3-f393-e0a9-e50e24dcca9e" // device notifies here
)

// nusLink is an io.ReadWriteCloser over the Nordic UART Service.
type nusLink struct {
	conn     *dbus.Conn
	device   dbus.ObjectPath
	rxChar   dbus.BusObject
	txPath   dbus.ObjectPath
	txChar   dbus.BusObject
	signalCh chan *dbus.Signal
	pr       *io.PipeReader
	pw       *io.PipeWriter
	writeMax int
	once     sync.Once
	closed   chan struct{}
}

// ParseBLETarget splits a ble:// port into MAC or name (RNodeInterface.py
// port parsing): empty = the first bonded device named "RNode ...",
// 17 characters with five colons = a MAC, anything else = the device name.
func ParseBLETarget(port string) (mac, name string) {
	s := strings.TrimPrefix(strings.TrimPrefix(port, "ble://"), "BLE://")
	if s == "" {
		return "", ""
	}
	if len(s) == 17 && strings.Count(s, ":") == 5 {
		return strings.ToUpper(s), ""
	}
	return "", s
}

// OpenNUSLink connects to a bonded RNode over BLE and returns a stream.
func OpenNUSLink(ctx context.Context, adapter, port string) (io.ReadWriteCloser, error) {
	if adapter == "" {
		adapter = "hci0"
	}
	mac, name := ParseBLETarget(port)
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("ble: D-Bus system bus: %w", err)
	}
	devicePath, err := findNUSDevice(conn, adapter, mac, name)
	if err != nil {
		conn.Close()
		return nil, err
	}
	device := conn.Object("org.bluez", devicePath)
	// Connect over LE to the NUS profile; a connected device is left alone.
	var connected bool
	if v, err := device.GetProperty("org.bluez.Device1.Connected"); err == nil {
		connected, _ = v.Value().(bool)
	}
	if !connected {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := device.CallWithContext(cctx, "org.bluez.Device1.ConnectProfile", 0, nusServiceUUID).Err
		cancel()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("ble: ConnectProfile: %w", err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		v, err := device.GetProperty("org.bluez.Device1.ServicesResolved")
		if err == nil {
			if b, _ := v.Value().(bool); b {
				break
			}
		}
		if time.Now().After(deadline) {
			conn.Close()
			return nil, fmt.Errorf("ble: services never resolved on %s", devicePath)
		}
		select {
		case <-ctx.Done():
			conn.Close()
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	l := &nusLink{conn: conn, device: devicePath, writeMax: 20, closed: make(chan struct{})}
	if err := l.findChars(); err != nil {
		conn.Close()
		return nil, err
	}
	l.pr, l.pw = io.Pipe()
	l.signalCh = make(chan *dbus.Signal, 64)
	conn.Signal(l.signalCh)
	if err := conn.AddMatchSignal(dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
		dbus.WithMatchMember("PropertiesChanged"), dbus.WithMatchObjectPath(l.txPath)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ble: AddMatch: %w", err)
	}
	if err := l.txChar.Call("org.bluez.GattCharacteristic1.StartNotify", 0).Err; err != nil {
		conn.Close()
		return nil, fmt.Errorf("ble: StartNotify: %w", err)
	}
	go l.notifyLoop()
	log.Info().Str("device", string(devicePath)).Int("write_max", l.writeMax).Msg("ble: RNode UART link open")
	return l, nil
}

// findNUSDevice picks the bonded device with the UART service that
// matches the MAC or name; with neither, the first named "RNode ...".
func findNUSDevice(conn *dbus.Conn, adapter, mac, name string) (dbus.ObjectPath, error) {
	if mac != "" {
		return deviceObjectPath(adapter, mac), nil
	}
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := conn.Object("org.bluez", "/").Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objects); err != nil {
		return "", fmt.Errorf("ble: GetManagedObjects: %w", err)
	}
	prefix := "/org/bluez/" + adapter + "/dev_"
	for path, ifaces := range objects {
		if !strings.HasPrefix(string(path), prefix) {
			continue
		}
		dev, ok := ifaces["org.bluez.Device1"]
		if !ok {
			continue
		}
		bonded := false
		if v, ok := dev["Bonded"]; ok {
			bonded, _ = v.Value().(bool)
		} else if v, ok := dev["Paired"]; ok {
			bonded, _ = v.Value().(bool)
		}
		if !bonded {
			continue
		}
		hasNUS := false
		if v, ok := dev["UUIDs"]; ok {
			if uuids, ok := v.Value().([]string); ok {
				for _, u := range uuids {
					if strings.EqualFold(u, nusServiceUUID) {
						hasNUS = true
					}
				}
			}
		}
		if !hasNUS {
			continue
		}
		devName := ""
		if v, ok := dev["Name"]; ok {
			devName, _ = v.Value().(string)
		}
		if name != "" {
			if devName == name {
				return path, nil
			}
			if v, ok := dev["Alias"]; ok {
				if alias, _ := v.Value().(string); alias == name {
					return path, nil
				}
			}
			continue
		}
		if strings.HasPrefix(devName, "RNode ") {
			return path, nil
		}
	}
	if name != "" {
		return "", fmt.Errorf("ble: no bonded device named %q with the UART service", name)
	}
	return "", fmt.Errorf("ble: no bonded RNode with the UART service; pair and bond it first")
}

func (l *nusLink) findChars() error {
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := l.conn.Object("org.bluez", "/").Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objects); err != nil {
		return fmt.Errorf("ble: GetManagedObjects: %w", err)
	}
	prefix := string(l.device) + "/"
	for path, ifaces := range objects {
		if !strings.HasPrefix(string(path), prefix) {
			continue
		}
		ch, ok := ifaces["org.bluez.GattCharacteristic1"]
		if !ok {
			continue
		}
		uuid, _ := ch["UUID"].Value().(string)
		switch strings.ToLower(uuid) {
		case nusRXCharUUID:
			l.rxChar = l.conn.Object("org.bluez", path)
			if v, ok := ch["MTU"]; ok {
				if mtu, ok := v.Value().(uint16); ok && int(mtu) > 3 {
					l.writeMax = int(mtu) - 3
				}
			}
		case nusTXCharUUID:
			l.txPath = path
			l.txChar = l.conn.Object("org.bluez", path)
		}
	}
	if l.rxChar == nil || l.txChar == nil {
		return fmt.Errorf("ble: UART characteristics not found on %s", l.device)
	}
	return nil
}

func (l *nusLink) notifyLoop() {
	defer l.pw.Close()
	for {
		select {
		case <-l.closed:
			return
		case sig, ok := <-l.signalCh:
			if !ok {
				return
			}
			if sig.Path != l.txPath || len(sig.Body) < 2 {
				continue
			}
			changed, ok := sig.Body[1].(map[string]dbus.Variant)
			if !ok {
				continue
			}
			if v, ok := changed["Value"]; ok {
				if chunk, ok := v.Value().([]byte); ok && len(chunk) > 0 {
					if _, err := l.pw.Write(chunk); err != nil {
						return
					}
				}
			}
		}
	}
}

func (l *nusLink) Read(b []byte) (int, error) { return l.pr.Read(b) }

func (l *nusLink) Write(b []byte) (int, error) {
	opts := map[string]dbus.Variant{"type": dbus.MakeVariant("command")}
	for off := 0; off < len(b); off += l.writeMax {
		end := off + l.writeMax
		if end > len(b) {
			end = len(b)
		}
		if err := l.rxChar.Call("org.bluez.GattCharacteristic1.WriteValue", 0, b[off:end], opts).Err; err != nil {
			return off, fmt.Errorf("ble: WriteValue: %w", err)
		}
	}
	return len(b), nil
}

func (l *nusLink) Close() error {
	l.once.Do(func() {
		close(l.closed)
		_ = l.txChar.Call("org.bluez.GattCharacteristic1.StopNotify", 0).Err
		l.pr.Close()
		l.conn.Close()
	})
	return nil
}
