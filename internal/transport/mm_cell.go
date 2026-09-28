package transport

// The cellular transport over ModemManager, for a Linux phone or any host whose modem
// belongs to ModemManager and NetworkManager (the PinePhone Pro under Mobian,
// MESHSAT-1386): the same CellTransport the serial AT driver gives, taken from the
// system bus instead of a serial port that the system would not share. Selected with
// MESHSAT_CELLULAR_PORT=modemmanager. Data connections stay NetworkManager's.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/rs/zerolog/log"
)

const (
	mmBusName      = "org.freedesktop.ModemManager1"
	mmRootPath     = "/org/freedesktop/ModemManager1"
	mmIfaceModem   = "org.freedesktop.ModemManager1.Modem"
	mmIface3gpp    = "org.freedesktop.ModemManager1.Modem.Modem3gpp"
	mmIfaceMessage = "org.freedesktop.ModemManager1.Modem.Messaging"
	mmIfaceSignal  = "org.freedesktop.ModemManager1.Modem.Signal"
	mmIfaceSms     = "org.freedesktop.ModemManager1.Sms"
	mmIfaceSim     = "org.freedesktop.ModemManager1.Sim"
	mmIfaceBearer  = "org.freedesktop.ModemManager1.Bearer"
	dbusProps      = "org.freedesktop.DBus.Properties"
	dbusObjMgr     = "org.freedesktop.DBus.ObjectManager"

	// MMModemState
	mmStateFailed     = -1
	mmStateLocked     = 2
	mmStateEnabled    = 6
	mmStateRegistered = 8
	// MMModemStateFailedReason
	mmFailedSimMissing = 2
	mmFailedSimError   = 3
	// MMSmsState
	mmSmsReceived = 3
	// MMSmsPduType
	mmPduDeliver = 1
)

// IsModemManager says whether MESHSAT_CELLULAR_PORT names ModemManager rather than a serial port.
func IsModemManager(port string) bool {
	p := strings.ToLower(strings.TrimSpace(port))
	return p == "modemmanager" || p == "mm" || strings.HasPrefix(p, "mm://") || strings.HasPrefix(p, "modemmanager://")
}

// ErrNoModem is returned while ModemManager has no modem to offer.
var ErrNoModem = errors.New("modemmanager: no modem")

// MMCellTransport is a CellTransport over ModemManager's D-Bus API.
type MMCellTransport struct {
	mu        sync.Mutex
	conn      *dbus.Conn
	modemPath dbus.ObjectPath
	signals   chan *dbus.Signal
	stop      chan struct{}

	stateMu     sync.RWMutex
	smsSent     int64
	smsReceived int64
	sentHook    func(to, text string)
	fallbackPIN string
	seen        map[dbus.ObjectPath]bool // SMS objects already delivered as events

	eventMu   sync.RWMutex
	eventSubs map[int]chan CellEvent
	nextSubID int
}

// NewMMCellTransport makes the transport; the bus is opened on first use.
func NewMMCellTransport() *MMCellTransport {
	return &MMCellTransport{eventSubs: make(map[int]chan CellEvent), seen: make(map[dbus.ObjectPath]bool)}
}

// SetSentHook runs after the network accepted an SMS, with the destination and the text
// (the prepaid bundle counter hangs here, as on the serial transport).
func (t *MMCellTransport) SetSentHook(fn func(to, text string)) {
	t.stateMu.Lock()
	t.sentHook = fn
	t.stateMu.Unlock()
}

// SetFallbackPIN is the PIN tried when the SIM is locked (MESHSAT_SIM_PIN).
func (t *MMCellTransport) SetFallbackPIN(pin string) {
	t.stateMu.Lock()
	t.fallbackPIN = pin
	t.stateMu.Unlock()
}

// connect opens the system bus and finds the first modem. Safe to call again.
func (t *MMCellTransport) connect(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn == nil {
		conn, err := dbus.ConnectSystemBus()
		if err != nil {
			return fmt.Errorf("system bus: %w", err)
		}
		t.conn = conn
	}
	if t.modemPath != "" {
		return nil
	}
	path, err := t.findModem(ctx)
	if err != nil {
		return err
	}
	t.modemPath = path
	t.watch()
	// The signal interface gives real dBm once asked; without the right, the percentage does.
	_ = t.conn.Object(mmBusName, path).CallWithContext(ctx, mmIfaceSignal+".Setup", 0, uint32(30)).Err
	t.stateMu.Lock()
	pin := t.fallbackPIN
	t.stateMu.Unlock()
	if pin != "" {
		if state, _ := t.propInt(ctx, path, mmIfaceModem, "State"); state == mmStateLocked {
			if err := t.UnlockPIN(ctx, pin); err != nil {
				log.Warn().Err(err).Msg("modemmanager: fallback PIN refused")
			}
		}
	}
	log.Info().Str("modem", string(path)).Msg("modemmanager: modem found")
	t.emitEvent(CellEvent{Type: "connected", Message: "modem found through ModemManager", Time: time.Now().UTC().Format(time.RFC3339)})
	return nil
}

func (t *MMCellTransport) findModem(ctx context.Context) (dbus.ObjectPath, error) {
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := t.conn.Object(mmBusName, mmRootPath).CallWithContext(ctx, dbusObjMgr+".GetManagedObjects", 0).Store(&objects); err != nil {
		return "", fmt.Errorf("modemmanager: %w", err)
	}
	best := dbus.ObjectPath("")
	for path, ifaces := range objects {
		if _, ok := ifaces[mmIfaceModem]; ok && (best == "" || path < best) {
			best = path
		}
	}
	if best == "" {
		return "", ErrNoModem
	}
	return best, nil
}

// watch follows the modem's property changes and its incoming SMS. Called with t.mu held.
func (t *MMCellTransport) watch() {
	if t.signals != nil {
		return
	}
	path := t.modemPath
	_ = t.conn.AddMatchSignal(dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(dbusProps), dbus.WithMatchMember("PropertiesChanged"))
	_ = t.conn.AddMatchSignal(dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(mmIfaceMessage), dbus.WithMatchMember("Added"))
	_ = t.conn.AddMatchSignal(dbus.WithMatchObjectPath(mmRootPath), dbus.WithMatchInterface(dbusObjMgr), dbus.WithMatchMember("InterfacesRemoved"))
	t.signals = make(chan *dbus.Signal, 64)
	t.stop = make(chan struct{})
	t.conn.Signal(t.signals)
	go t.loop(t.signals, t.stop)
	// SMS already in the modem's storage (received while nothing listened) come out first.
	go t.drainStored(context.Background())
}

func (t *MMCellTransport) loop(signals chan *dbus.Signal, stop chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case sig, ok := <-signals:
			if !ok {
				return
			}
			t.handleSignal(sig)
		}
	}
}

func (t *MMCellTransport) handleSignal(sig *dbus.Signal) {
	switch sig.Name {
	case dbusProps + ".PropertiesChanged":
		if len(sig.Body) < 2 {
			return
		}
		iface, _ := sig.Body[0].(string)
		changed, _ := sig.Body[1].(map[string]dbus.Variant)
		if iface != mmIfaceModem {
			return
		}
		if v, ok := changed["SignalQuality"]; ok {
			if pct, _, err := unpackQuality(v); err == nil {
				t.emitEvent(CellEvent{Type: "signal", Signal: qualityToBars(pct), Message: fmt.Sprintf("%d%%", pct), Time: time.Now().UTC().Format(time.RFC3339)})
			}
		}
		if v, ok := changed["State"]; ok {
			state, _ := asInt(v.Value())
			t.emitEvent(CellEvent{Type: "network_change", Message: "modem state " + mmStateName(state), Time: time.Now().UTC().Format(time.RFC3339)})
		}
	case mmIfaceMessage + ".Added":
		if len(sig.Body) < 2 {
			return
		}
		path, _ := sig.Body[0].(dbus.ObjectPath)
		received, _ := sig.Body[1].(bool)
		if received {
			go t.deliver(context.Background(), path)
		}
	case dbusObjMgr + ".InterfacesRemoved":
		if len(sig.Body) < 1 {
			return
		}
		if path, _ := sig.Body[0].(dbus.ObjectPath); path == t.modemPath {
			t.mu.Lock()
			t.modemPath = ""
			t.mu.Unlock()
			t.emitEvent(CellEvent{Type: "disconnected", Message: "modem gone from ModemManager", Time: time.Now().UTC().Format(time.RFC3339)})
		}
	}
}

// drainStored delivers the received SMS the modem still holds.
func (t *MMCellTransport) drainStored(ctx context.Context) {
	t.mu.Lock()
	conn, path := t.conn, t.modemPath
	t.mu.Unlock()
	if conn == nil || path == "" {
		return
	}
	var list []dbus.ObjectPath
	if err := conn.Object(mmBusName, path).CallWithContext(ctx, mmIfaceMessage+".List", 0).Store(&list); err != nil {
		return
	}
	for _, sms := range list {
		t.deliver(ctx, sms)
	}
}

// deliver turns one received SMS object into an sms_received event, then deletes it from the modem.
func (t *MMCellTransport) deliver(ctx context.Context, sms dbus.ObjectPath) {
	t.stateMu.Lock()
	if t.seen[sms] {
		t.stateMu.Unlock()
		return
	}
	t.seen[sms] = true
	t.stateMu.Unlock()
	t.mu.Lock()
	conn, modem := t.conn, t.modemPath
	t.mu.Unlock()
	if conn == nil {
		return
	}
	obj := conn.Object(mmBusName, sms)
	state, _ := t.propUint(ctx, sms, mmIfaceSms, "State")
	pdu, _ := t.propUint(ctx, sms, mmIfaceSms, "PduType")
	if state != mmSmsReceived || (pdu != 0 && pdu != mmPduDeliver) {
		return
	}
	number, _ := t.propString(ctx, sms, mmIfaceSms, "Number")
	text, _ := t.propString(ctx, sms, mmIfaceSms, "Text")
	stamp, _ := t.propString(ctx, sms, mmIfaceSms, "Timestamp")
	if text == "" {
		if v, err := obj.GetProperty(mmIfaceSms + ".Data"); err == nil {
			if raw, ok := v.Value().([]byte); ok {
				text = string(raw)
			}
		}
	}
	msg := SMSMessage{Sender: number, Text: text, Time: mmTimestamp(stamp)}
	t.stateMu.Lock()
	t.smsReceived++
	t.stateMu.Unlock()
	data, _ := json.Marshal(msg)
	log.Info().Str("sender", number).Int("bytes", len(text)).Msg("modemmanager: SMS received")
	t.emitEvent(CellEvent{Type: "sms_received", Message: text, Data: data, Time: time.Now().UTC().Format(time.RFC3339)})
	if modem != "" {
		if err := conn.Object(mmBusName, modem).CallWithContext(ctx, mmIfaceMessage+".Delete", 0, sms).Err; err != nil {
			log.Warn().Err(err).Str("sms", string(sms)).Msg("modemmanager: could not delete the read SMS")
		}
	}
}

// Subscribe hands out the event stream: connected, disconnected, signal, sms_received, network_change.
func (t *MMCellTransport) Subscribe(ctx context.Context) (<-chan CellEvent, error) {
	if err := t.connect(ctx); err != nil && !errors.Is(err, ErrNoModem) {
		return nil, err
	}
	ch := make(chan CellEvent, 32)
	t.eventMu.Lock()
	id := t.nextSubID
	t.nextSubID++
	t.eventSubs[id] = ch
	t.eventMu.Unlock()
	go func() {
		<-ctx.Done()
		t.eventMu.Lock()
		delete(t.eventSubs, id)
		close(ch)
		t.eventMu.Unlock()
	}()
	return ch, nil
}

func (t *MMCellTransport) emitEvent(event CellEvent) {
	t.eventMu.RLock()
	defer t.eventMu.RUnlock()
	for _, ch := range t.eventSubs {
		select {
		case ch <- event:
		default:
		}
	}
}

// SendSMS hands the text to ModemManager and waits for the network to take it.
func (t *MMCellTransport) SendSMS(ctx context.Context, to string, text string) error {
	if err := t.connect(ctx); err != nil {
		return err
	}
	t.mu.Lock()
	conn, modem := t.conn, t.modemPath
	t.mu.Unlock()
	if modem == "" {
		return ErrNoModem
	}
	if state, err := t.propInt(ctx, modem, mmIfaceModem, "State"); err == nil {
		if state == mmStateFailed {
			reason, _ := t.propUint(ctx, modem, mmIfaceModem, "StateFailedReason")
			return fmt.Errorf("modemmanager: %s", mmFailedName(reason))
		}
		if state == mmStateLocked {
			return errors.New("modemmanager: the SIM needs its PIN")
		}
		if state < mmStateRegistered {
			return fmt.Errorf("modemmanager: not on the network (%s)", mmStateName(state))
		}
	}
	props := map[string]dbus.Variant{"number": dbus.MakeVariant(to), "text": dbus.MakeVariant(text)}
	var sms dbus.ObjectPath
	if err := conn.Object(mmBusName, modem).CallWithContext(ctx, mmIfaceMessage+".Create", 0, props).Store(&sms); err != nil {
		return fmt.Errorf("modemmanager: create SMS: %w", err)
	}
	sendCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	err := conn.Object(mmBusName, sms).CallWithContext(sendCtx, mmIfaceSms+".Send", 0).Err
	_ = conn.Object(mmBusName, modem).Call(mmIfaceMessage+".Delete", 0, sms).Err
	if err != nil {
		return fmt.Errorf("modemmanager: send SMS: %w", err)
	}
	t.stateMu.Lock()
	t.smsSent++
	hook := t.sentHook
	t.stateMu.Unlock()
	if hook != nil {
		hook(to, text)
	}
	log.Info().Str("to", to).Int("bytes", len(text)).Msg("modemmanager: SMS sent")
	return nil
}

// GetSignal reads the modem's signal: real dBm when the Signal interface answers, the
// percentage otherwise.
func (t *MMCellTransport) GetSignal(ctx context.Context) (*CellSignalInfo, error) {
	if err := t.connect(ctx); err != nil {
		return nil, err
	}
	t.mu.Lock()
	conn, modem := t.conn, t.modemPath
	t.mu.Unlock()
	if modem == "" {
		return nil, ErrNoModem
	}
	obj := conn.Object(mmBusName, modem)
	v, err := obj.GetProperty(mmIfaceModem + ".SignalQuality")
	if err != nil {
		return nil, fmt.Errorf("modemmanager: %w", err)
	}
	pct, _, _ := unpackQuality(v)
	tech, _ := t.propUint(ctx, modem, mmIfaceModem, "AccessTechnologies")
	info := &CellSignalInfo{Bars: qualityToBars(pct), Technology: mmTechName(tech), Timestamp: time.Now().UTC().Format(time.RFC3339)}
	info.Assessment = cellSignalAssessment(info.Bars)
	for _, key := range []string{"Lte", "Nr5g", "Umts", "Gsm"} {
		if sv, err := obj.GetProperty(mmIfaceSignal + "." + key); err == nil {
			if dict, ok := sv.Value().(map[string]dbus.Variant); ok {
				for _, field := range []string{"rsrp", "rssi", "rscp"} {
					if fv, ok := dict[field]; ok {
						if f, ok := fv.Value().(float64); ok && f != 0 {
							info.DBm = int(math.Round(f))
							return info, nil
						}
					}
				}
			}
		}
	}
	return info, nil
}

// GetSignalFast is GetSignal: ModemManager caches the reading itself.
func (t *MMCellTransport) GetSignalFast(ctx context.Context) (*CellSignalInfo, error) {
	return t.GetSignal(ctx)
}

// GetStatus reads the modem, the SIM and the registration as the serial driver reports them.
func (t *MMCellTransport) GetStatus(ctx context.Context) (*CellStatus, error) {
	t.stateMu.RLock()
	sent, received := t.smsSent, t.smsReceived
	t.stateMu.RUnlock()
	status := &CellStatus{Port: "modemmanager", SIMState: "UNKNOWN", Registration: "not_registered", SMSSent: sent, SMSReceived: received}
	if err := t.connect(ctx); err != nil {
		if errors.Is(err, ErrNoModem) {
			status.SIMState = "NO_MODEM"
			return status, nil
		}
		return nil, err
	}
	t.mu.Lock()
	modem := t.modemPath
	t.mu.Unlock()
	if modem == "" {
		status.SIMState = "NO_MODEM"
		return status, nil
	}
	status.Connected = true
	status.Port = "modemmanager:" + string(modem)
	manufacturer, _ := t.propString(ctx, modem, mmIfaceModem, "Manufacturer")
	model, _ := t.propString(ctx, modem, mmIfaceModem, "Model")
	status.Model = strings.TrimSpace(manufacturer + " " + model)
	status.IMEI, _ = t.propString(ctx, modem, mmIfaceModem, "EquipmentIdentifier")
	state, _ := t.propInt(ctx, modem, mmIfaceModem, "State")
	reason, _ := t.propUint(ctx, modem, mmIfaceModem, "StateFailedReason")
	simPath, _ := t.propPath(ctx, modem, mmIfaceModem, "Sim")
	status.SIMState = mmSIMState(state, reason, simPath)
	tech, _ := t.propUint(ctx, modem, mmIfaceModem, "AccessTechnologies")
	status.NetworkType = mmTechName(tech)
	reg, _ := t.propUint(ctx, modem, mmIface3gpp, "RegistrationState")
	status.Registration = mmRegistration(reg)
	status.Operator, _ = t.propString(ctx, modem, mmIface3gpp, "OperatorName")
	if numbers, err := t.propStrings(ctx, modem, mmIfaceModem, "OwnNumbers"); err == nil && len(numbers) > 0 {
		status.PhoneNumber = numbers[0]
	}
	if simPath != "" && simPath != "/" {
		status.ICCID, _ = t.propString(ctx, simPath, mmIfaceSim, "SimIdentifier")
		if status.Operator == "" {
			status.Operator, _ = t.propString(ctx, simPath, mmIfaceSim, "OperatorName")
		}
	}
	return status, nil
}

// GetDataStatus reports the bearer NetworkManager brought up, if any.
func (t *MMCellTransport) GetDataStatus(ctx context.Context) (*CellDataStatus, error) {
	if err := t.connect(ctx); err != nil {
		return nil, err
	}
	t.mu.Lock()
	conn, modem := t.conn, t.modemPath
	t.mu.Unlock()
	if modem == "" {
		return nil, ErrNoModem
	}
	out := &CellDataStatus{}
	var bearers []dbus.ObjectPath
	v, err := conn.Object(mmBusName, modem).GetProperty(mmIfaceModem + ".Bearers")
	if err != nil {
		return out, nil
	}
	if list, ok := v.Value().([]dbus.ObjectPath); ok {
		bearers = list
	}
	for _, b := range bearers {
		obj := conn.Object(mmBusName, b)
		connected := false
		if cv, err := obj.GetProperty(mmIfaceBearer + ".Connected"); err == nil {
			connected, _ = cv.Value().(bool)
		}
		if !connected {
			continue
		}
		out.Active = true
		if iv, err := obj.GetProperty(mmIfaceBearer + ".Interface"); err == nil {
			out.Interface, _ = iv.Value().(string)
		}
		if pv, err := obj.GetProperty(mmIfaceBearer + ".Properties"); err == nil {
			if dict, ok := pv.Value().(map[string]dbus.Variant); ok {
				if apn, ok := dict["apn"]; ok {
					out.APN, _ = apn.Value().(string)
				}
			}
		}
		if iv, err := obj.GetProperty(mmIfaceBearer + ".Ip4Config"); err == nil {
			if dict, ok := iv.Value().(map[string]dbus.Variant); ok {
				if addr, ok := dict["address"]; ok {
					out.IPAddress, _ = addr.Value().(string)
				}
			}
		}
		break
	}
	return out, nil
}

// ConnectData is NetworkManager's job on a system that runs ModemManager.
func (t *MMCellTransport) ConnectData(_ context.Context, _ string) error {
	return errors.New("modemmanager: mobile data is NetworkManager's on this system (nmcli connection up)")
}

// DisconnectData: see ConnectData.
func (t *MMCellTransport) DisconnectData(_ context.Context) error {
	return errors.New("modemmanager: mobile data is NetworkManager's on this system (nmcli connection down)")
}

// UnlockPIN sends the PIN to the SIM.
func (t *MMCellTransport) UnlockPIN(ctx context.Context, pin string) error {
	if err := t.connect(ctx); err != nil {
		return err
	}
	t.mu.Lock()
	conn, modem := t.conn, t.modemPath
	t.mu.Unlock()
	if modem == "" {
		return ErrNoModem
	}
	simPath, err := t.propPath(ctx, modem, mmIfaceModem, "Sim")
	if err != nil || simPath == "" || simPath == "/" {
		return errors.New("modemmanager: no SIM")
	}
	if err := conn.Object(mmBusName, simPath).CallWithContext(ctx, mmIfaceSim+".SendPin", 0, pin).Err; err != nil {
		return fmt.Errorf("modemmanager: %w", err)
	}
	return nil
}

// GetCellInfo asks ModemManager for the serving cell (MM 1.20 and later, modem permitting).
func (t *MMCellTransport) GetCellInfo(ctx context.Context) (*CellInfo, error) {
	if err := t.connect(ctx); err != nil {
		return nil, err
	}
	t.mu.Lock()
	conn, modem := t.conn, t.modemPath
	t.mu.Unlock()
	if modem == "" {
		return nil, ErrNoModem
	}
	var cells []map[string]dbus.Variant
	if err := conn.Object(mmBusName, modem).CallWithContext(ctx, mmIfaceModem+".GetCellInfo", 0).Store(&cells); err != nil {
		return nil, fmt.Errorf("modemmanager: %w", err)
	}
	var chosen map[string]dbus.Variant
	for _, c := range cells {
		if serving, ok := c["serving"]; ok {
			if s, _ := serving.Value().(bool); s {
				chosen = c
				break
			}
		}
	}
	if chosen == nil && len(cells) > 0 {
		chosen = cells[0]
	}
	if chosen == nil {
		return nil, errors.New("modemmanager: no cell")
	}
	return mmCellInfo(chosen), nil
}

// ExecAT goes through ModemManager's Command, which only a debug-mode daemon allows.
func (t *MMCellTransport) ExecAT(ctx context.Context, command string, timeout time.Duration) (string, error) {
	if err := t.connect(ctx); err != nil {
		return "", err
	}
	t.mu.Lock()
	conn, modem := t.conn, t.modemPath
	t.mu.Unlock()
	if modem == "" {
		return "", ErrNoModem
	}
	var out string
	if err := conn.Object(mmBusName, modem).CallWithContext(ctx, mmIfaceModem+".Command", 0, command, uint32(timeout/time.Second)).Store(&out); err != nil {
		return "", fmt.Errorf("modemmanager: AT passthrough needs ModemManager started with --debug: %w", err)
	}
	return out, nil
}

// Reconnect drops the modem path and finds the modem again (OOB RESET cellular, level 1).
func (t *MMCellTransport) Reconnect(ctx context.Context) error {
	t.mu.Lock()
	t.modemPath = ""
	t.mu.Unlock()
	return t.connect(ctx)
}

// DeviceReset asks ModemManager to reset the modem (level 2).
func (t *MMCellTransport) DeviceReset(ctx context.Context) error {
	if err := t.connect(ctx); err != nil {
		return err
	}
	t.mu.Lock()
	conn, modem := t.conn, t.modemPath
	t.mu.Unlock()
	if modem == "" {
		return ErrNoModem
	}
	if err := conn.Object(mmBusName, modem).CallWithContext(ctx, mmIfaceModem+".Reset", 0).Err; err != nil {
		return fmt.Errorf("modemmanager: %w", err)
	}
	return nil
}

// Close leaves the bus.
func (t *MMCellTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stop != nil {
		close(t.stop)
		t.stop = nil
	}
	if t.conn != nil {
		if t.signals != nil {
			t.conn.RemoveSignal(t.signals)
			t.signals = nil
		}
		err := t.conn.Close()
		t.conn = nil
		t.modemPath = ""
		return err
	}
	return nil
}

// Property readers

func (t *MMCellTransport) prop(ctx context.Context, path dbus.ObjectPath, iface, name string) (dbus.Variant, error) {
	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if conn == nil {
		return dbus.Variant{}, ErrNoModem
	}
	var v dbus.Variant
	err := conn.Object(mmBusName, path).CallWithContext(ctx, dbusProps+".Get", 0, iface, name).Store(&v)
	return v, err
}

func (t *MMCellTransport) propString(ctx context.Context, path dbus.ObjectPath, iface, name string) (string, error) {
	v, err := t.prop(ctx, path, iface, name)
	if err != nil {
		return "", err
	}
	s, _ := v.Value().(string)
	return s, nil
}

func (t *MMCellTransport) propStrings(ctx context.Context, path dbus.ObjectPath, iface, name string) ([]string, error) {
	v, err := t.prop(ctx, path, iface, name)
	if err != nil {
		return nil, err
	}
	s, _ := v.Value().([]string)
	return s, nil
}

func (t *MMCellTransport) propPath(ctx context.Context, path dbus.ObjectPath, iface, name string) (dbus.ObjectPath, error) {
	v, err := t.prop(ctx, path, iface, name)
	if err != nil {
		return "", err
	}
	p, _ := v.Value().(dbus.ObjectPath)
	return p, nil
}

func (t *MMCellTransport) propInt(ctx context.Context, path dbus.ObjectPath, iface, name string) (int, error) {
	v, err := t.prop(ctx, path, iface, name)
	if err != nil {
		return 0, err
	}
	return asInt(v.Value())
}

func (t *MMCellTransport) propUint(ctx context.Context, path dbus.ObjectPath, iface, name string) (uint32, error) {
	v, err := t.prop(ctx, path, iface, name)
	if err != nil {
		return 0, err
	}
	n, err := asInt(v.Value())
	if err != nil || n < 0 {
		return 0, err
	}
	return uint32(n), nil
}

// The pure mappings, tested on their own

func asInt(v interface{}) (int, error) {
	switch n := v.(type) {
	case int32:
		return int(n), nil
	case uint32:
		return int(n), nil
	case int64:
		return int(n), nil
	case uint64:
		return int(n), nil
	case int:
		return n, nil
	case uint8:
		return int(n), nil
	case int16:
		return int(n), nil
	case uint16:
		return int(n), nil
	}
	return 0, fmt.Errorf("not a number: %T", v)
}

// unpackQuality reads ModemManager's (ub) SignalQuality: the percentage and whether it is recent.
func unpackQuality(v dbus.Variant) (int, bool, error) {
	switch q := v.Value().(type) {
	case []interface{}:
		if len(q) >= 1 {
			pct, err := asInt(q[0])
			recent := true
			if len(q) >= 2 {
				recent, _ = q[1].(bool)
			}
			return pct, recent, err
		}
	case struct {
		V0 uint32
		V1 bool
	}:
		return int(q.V0), q.V1, nil
	}
	return 0, false, fmt.Errorf("unexpected SignalQuality %T", v.Value())
}

// qualityToBars maps ModemManager's 0-100 % to the 0-5 bars every cellular view uses.
func qualityToBars(pct int) int {
	if pct <= 0 {
		return 0
	}
	if pct >= 100 {
		return 5
	}
	return int(math.Ceil(float64(pct) / 20.0))
}

// mmSIMState is the serial driver's vocabulary for the SIM: READY, NOT_INSERTED, PIN_REQUIRED, SIM_ERROR.
func mmSIMState(state int, failedReason uint32, sim dbus.ObjectPath) string {
	switch {
	case state == mmStateLocked:
		return "PIN_REQUIRED"
	case state == mmStateFailed && failedReason == mmFailedSimMissing:
		return "NOT_INSERTED"
	case state == mmStateFailed && failedReason == mmFailedSimError:
		return "SIM_ERROR"
	case sim == "" || sim == "/":
		return "NOT_INSERTED"
	default:
		return "READY"
	}
}

// mmRegistration maps MMModem3gppRegistrationState.
func mmRegistration(reg uint32) string {
	switch reg {
	case 1, 6, 8, 10:
		return "registered_home"
	case 2:
		return "searching"
	case 3:
		return "denied"
	case 5, 7, 9:
		return "registered_roaming"
	case 4:
		return "unknown"
	default:
		return "not_registered"
	}
}

// mmTechName folds the MMModemAccessTechnology bitmask into the generation the views show.
func mmTechName(mask uint32) string {
	switch {
	case mask&(1<<15) != 0:
		return "5G"
	case mask&(1<<14) != 0:
		return "LTE"
	case mask&(1<<5|1<<6|1<<7|1<<8|1<<9) != 0:
		return "3G"
	case mask&(1<<1|1<<2|1<<3|1<<4) != 0:
		return "2G"
	}
	return ""
}

func mmStateName(state int) string {
	names := map[int]string{-1: "failed", 0: "unknown", 1: "initializing", 2: "locked", 3: "disabled", 4: "disabling", 5: "enabling", 6: "enabled", 7: "searching", 8: "registered", 9: "disconnecting", 10: "connecting", 11: "connected"}
	if n, ok := names[state]; ok {
		return n
	}
	return strconv.Itoa(state)
}

func mmFailedName(reason uint32) string {
	switch reason {
	case mmFailedSimMissing:
		return "no SIM in this modem"
	case mmFailedSimError:
		return "the SIM does not work"
	case 0:
		return "the modem failed"
	}
	return "the modem failed (reason " + strconv.Itoa(int(reason)) + ")"
}

// mmTimestamp turns ModemManager's ISO 8601 into the RFC 3339 UTC the SMS records carry.
func mmTimestamp(stamp string) string {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-07", "2006-01-02T15:04:05"} {
		if ts, err := time.Parse(layout, stamp); err == nil {
			return ts.UTC().Format(time.RFC3339)
		}
	}
	return time.Now().UTC().Format(time.RFC3339)
}

// mmCellInfo reads one GetCellInfo dictionary.
func mmCellInfo(c map[string]dbus.Variant) *CellInfo {
	info := &CellInfo{}
	if v, ok := c["cell-type"]; ok {
		if n, err := asInt(v.Value()); err == nil {
			info.NetworkType = map[int]string{1: "CDMA", 2: "GSM", 3: "WCDMA", 4: "TDSCDMA", 5: "LTE", 6: "NR5G"}[n]
		}
	}
	if v, ok := c["operator-id"]; ok {
		if s, ok := v.Value().(string); ok && len(s) >= 5 {
			info.MCC, info.MNC = s[:3], s[3:]
		}
	}
	for _, key := range []string{"tac", "lac"} {
		if v, ok := c[key]; ok {
			if s, ok := v.Value().(string); ok && s != "" {
				info.LAC = s
				break
			}
		}
	}
	if v, ok := c["ci"]; ok {
		info.CellID, _ = v.Value().(string)
	}
	for key, target := range map[string]**int{"rsrp": &info.RSRP, "rsrq": &info.RSRQ} {
		if v, ok := c[key]; ok {
			if f, ok := v.Value().(float64); ok {
				n := int(math.Round(f))
				*target = &n
			}
		}
	}
	return info
}

// Present says whether ModemManager has a modem for us right now (the device supervisor
// never sees it, so the gateway manager asks here).
func (t *MMCellTransport) Present(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := t.connect(ctx); err != nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.modemPath != ""
}
