package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/config"
	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/oob"
	"meshsat/internal/spectrum"
	"meshsat/internal/transport"
)

// Device health watchdog wiring [MESHSAT-817]. main.go stays the single
// wiring point; this file only builds the per-device HealthTargets from the
// transports and the OOB reset actions that main.go already holds.

// deviceHealthTargets are the targets whose state is persisted per name.
var deviceHealthTargets = []string{"mesh", "cellular", "zigbee", "rtl_sdr", "gps", "imt", "iridium"}

// seedDeviceHealth reads the persisted per-target state (hard-reset
// timestamps, pause flag) so budgets survive a bridge restart.
func seedDeviceHealth(db *database.DB) map[string]gateway.PersistedTarget {
	seed := map[string]gateway.PersistedTarget{}
	for _, name := range deviceHealthTargets {
		raw, err := db.GetSystemConfig("device_health_" + name)
		if err != nil || raw == "" {
			continue
		}
		var p gateway.PersistedTarget
		if json.Unmarshal([]byte(raw), &p) == nil {
			seed[name] = p
		}
	}
	return seed
}

// deviceHealthActions wires the engine's outputs to the processor's SSE
// stream, the signed audit log and system_config.
func deviceHealthActions(db *database.DB, proc *engine.Processor, signing *engine.SigningService) gateway.DeviceHealthActions {
	return gateway.DeviceHealthActions{
		Emit: func(eventType, message string, data map[string]any) {
			raw, _ := json.Marshal(data)
			proc.Emit(transport.MeshEvent{Type: eventType, Message: message, Data: raw, Time: time.Now().UTC().Format(time.RFC3339)})
		},
		Audit: func(target, ifaceID, detail string) {
			if signing == nil {
				return
			}
			var iface *string
			if ifaceID != "" {
				iface = &ifaceID
			}
			signing.AuditEvent("device_health", iface, nil, nil, nil, target+": "+detail)
		},
		Persist: func(name string, p gateway.PersistedTarget) {
			raw, err := json.Marshal(p)
			if err != nil {
				return
			}
			if dbErr := db.SetSystemConfig("device_health_"+name, string(raw)); dbErr != nil {
				log.Warn().Err(dbErr).Str("target", name).Msg("device health: persist failed")
			}
		},
	}
}

func probeOK(detail string) gateway.ProbeResult   { return gateway.ProbeResult{OK: true, Detail: detail} }
func probeMiss(detail string) gateway.ProbeResult { return gateway.ProbeResult{Detail: detail} }

// registerDeviceHealthTargets builds every target the engine probes. The
// rungs reuse the oobActions closures so an operator's RESET and the
// ladder run the very same code.
func registerDeviceHealthTargets(dh *gateway.DeviceHealth, cfg *config.Config, oobActions map[string]map[byte]oob.Action,
	mesh transport.MeshTransport, cell transport.CellTransport, imt, sat transport.SatTransport,
	gwMgr *gateway.Manager, spectrumMon *spectrum.SpectrumMonitor, gpsReader *transport.GPSReader, rxWatchdog *gateway.RxWatchdog,
	supervisor *transport.DeviceSupervisor, powerCycle func(ctx context.Context, dev, tty string) bool) {

	if dm, ok := mesh.(*transport.DirectMeshTransport); ok && dm != nil {
		dh.Register(meshHealthTarget(cfg, dm, oobActions["mesh"]))
	}
	if dc, ok := cell.(*transport.DirectCellTransport); ok && dc != nil {
		dh.Register(cellularHealthTarget(cfg, dc, gwMgr, oobActions["cellular"]))
	}
	if gwMgr != nil {
		dh.Register(zigbeeHealthTarget(gwMgr, supervisor, oobActions["zigbee"], powerCycle))
	}
	// Registered even when the monitor starts without a dongle: the probe
	// reports unknown until one is attached at runtime. [MESHSAT-1002]
	if spectrumMon != nil {
		dh.Register(rtlSDRHealthTarget(spectrumMon, oobActions["rtl_sdr"]))
	}
	if gpsReader != nil {
		dh.Register(gpsHealthTarget(gpsReader, oobActions["gps"]))
	}
	if di, ok := imt.(*transport.DirectIMTTransport); ok && di != nil {
		dh.Register(imtHealthTarget(di))
	}
	if ds, ok := sat.(*transport.DirectSatTransport); ok && ds != nil {
		dh.Register(iridiumHealthTarget(ds))
	}

	if rxWatchdog != nil {
		dh.RegisterExternal("aprs", []string{"aprs_0", "ax25_0"}, func() (string, string) {
			switch s := rxWatchdog.State(); s {
			case gateway.ReceiveStateOK, gateway.ReceiveStateQuiet:
				return gateway.HealthStateOK, "receive " + s
			case gateway.ReceiveStateDeaf:
				return gateway.HealthStateHealing, "receive deaf, rx watchdog ladder running"
			default:
				return gateway.HealthStateUnknown, "receive " + s
			}
		})
	}
}

// cellularQuietWindow is how long nothing may open the T-Call's port after
// a VBUS cut, so the device has re-enumerated and the ESP32's own boot
// sequence (modem reset, PWRKEY, passthrough) is not interrupted by an
// open that reboots it again. With the port opened lines-low the modem
// restart is deterministic, so a short window is enough. [MESHSAT-812]
const cellularQuietWindow = 20 * time.Second

// cellularQuietCut is the cellular hard reset shared by the OOB executor
// (RESET cellular level 3) and the device health ladder: hold the transport
// and the supervisor off the port, close the transport, cut the hub port.
// The gateway restart follows the window (the executor delays it 75 s, the
// ladder schedules its own). [MESHSAT-812, MESHSAT-817]
func cellularQuietCut(dc *transport.DirectCellTransport, sup func() *transport.DeviceSupervisor,
	powerCycle func(ctx context.Context, dev, tty string) bool) oob.Action {
	return func(ctx context.Context) error {
		s := sup()
		if s == nil {
			return errors.New("device supervisor not running")
		}
		port := s.Registry().PortByRole(transport.RoleCellular)
		if port == "" {
			return errors.New("no cellular port claimed")
		}
		dc.Hold(cellularQuietWindow)
		s.HoldRole(transport.RoleCellular, cellularQuietWindow)
		_ = dc.Close()
		if powerCycle(ctx, "cellular", port) {
			return nil
		}
		// No switchable port: a USBDEVFS_RESET re-enumerates the CH9102
		// but does not toggle the modem's power. Still the best we have.
		if !transport.USBResetSerialDevice("cellular", port) {
			return fmt.Errorf("usb reset of %s failed", port)
		}
		return nil
	}
}

// cellularHealthTarget: AT probe, rungs reconnect, AT+CFUN=1,1, and the
// quiet-window VBUS cut (level 3 only up to cfg.DeviceHealthCellularMaxLevel;
// a cut is a modem power toggle and stays confirm-only until the bench
// proves the window, MESHSAT-812).
func cellularHealthTarget(cfg *config.Config, dc *transport.DirectCellTransport, gwMgr *gateway.Manager, actions map[byte]oob.Action) gateway.HealthTarget {
	hard := actions[oob.LevelHard]
	maxLevel := byte(cfg.DeviceHealthCellularMaxLevel)
	if maxLevel < gateway.HealLevelSoft || maxLevel > gateway.HealLevelHard {
		maxLevel = gateway.HealLevelDevice
	}
	return gateway.HealthTarget{
		Name:         "cellular",
		IfaceIDs:     []string{"cellular_0", "sms_0"},
		ProbeTimeout: 15 * time.Second,
		HardBudget:   2,
		MaxLevel:     maxLevel,
		Probe: func(ctx context.Context) gateway.ProbeResult {
			port := dc.GetPort()
			if port == "" || port == "auto" || port == "supervisor" {
				return gateway.ProbeResult{Unknown: true, Detail: "no serial port assigned"}
			}
			err := dc.Probe(ctx)
			switch {
			case err == nil:
				return probeOK(fmt.Sprintf("modem answered, last rx %s ago", time.Since(dc.LastRxAt()).Truncate(time.Second)))
			case errors.Is(err, transport.ErrCellProbeBusy):
				return probeOK("modem busy with a long command, bytes flowing")
			case errors.Is(err, transport.ErrCellHeld):
				// Inside the quiet window nothing may touch the port; neither
				// a hit nor a miss (an OK here read as "recovered" one second
				// after the cut and reset the ladder, tesseract 6 Sep 09:59Z).
				return gateway.ProbeResult{Unknown: true, Detail: err.Error()}
			default:
				return probeMiss(err.Error())
			}
		},
		Steps: []gateway.HealStep{
			{Level: gateway.HealLevelSoft, Name: "serial reconnect", Grace: 100 * time.Second, Run: dc.Reconnect},
			{
				Level: gateway.HealLevelDevice, Name: "AT+CFUN=1,1", Grace: 75 * time.Second,
				Skip: func() bool { return !dc.IsConnected() },
				Run:  dc.DeviceReset,
			},
			{
				Level: gateway.HealLevelHard, Name: "hub port power cycle with quiet window", Grace: cellularQuietWindow + 60*time.Second,
				Skip: func() bool { return hard == nil },
				Run: func(ctx context.Context) error {
					if hard == nil {
						return errors.New("no hard reset action registered")
					}
					if err := hard(ctx); err != nil {
						return err
					}
					time.AfterFunc(cellularQuietWindow+10*time.Second, func() {
						rctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
						defer cancel()
						if err := gwMgr.RestartGatewayInstance(rctx, "cellular_0"); err != nil {
							log.Warn().Err(err).Msg("device health: cellular gateway restart after power cycle failed")
						}
					})
					return nil
				},
			},
		},
	}
}

// zigbeeHealthTarget: SYS_PING probe through the gateway's current
// transport. Three failure shapes seen on the kits on 6 Sep 2026: the
// coordinator answers nothing (bootloader, wedge), the supervisor holds
// the port but no gateway runs (a restart race after a power cycle left
// parallax without a gateway for seven hours), and the dongle drops off
// the bus entirely (tesseract, twice for over an hour). Rungs: start the
// gateway or reopen+init on the same tty, Z-Stack soft reset, hub port
// power cycle (by last known port when the dongle is absent); the gateway
// restart after the cut is left to the device supervisor, with a late
// safety start if nothing came back.
func zigbeeHealthTarget(gwMgr *gateway.Manager, sup *transport.DeviceSupervisor, actions map[byte]oob.Action,
	powerCycle func(ctx context.Context, dev, tty string) bool) gateway.HealthTarget {
	hard := actions[oob.LevelHard]
	zt := func() *transport.DirectZigBeeTransport {
		if zgw := gwMgr.GetZigBeeGateway(); zgw != nil {
			return zgw.GetTransport()
		}
		return nil
	}
	port := func() string {
		if sup == nil {
			return ""
		}
		return sup.Registry().PortByRole(transport.RoleZigBee)
	}
	startGateway := func(ctx context.Context) error {
		err := gwMgr.StartGatewayInstance(ctx, "zigbee_0")
		if err != nil && (strings.Contains(err.Error(), "already running") || strings.Contains(err.Error(), "is starting")) {
			return nil
		}
		return err
	}
	var lastSeen time.Time // probes run one at a time
	return gateway.HealthTarget{
		Name:         "zigbee",
		IfaceIDs:     []string{"zigbee_0"},
		ProbeTimeout: 10 * time.Second,
		Probe: func(ctx context.Context) gateway.ProbeResult {
			p := port()
			if p == "" {
				if !lastSeen.IsZero() && time.Since(lastSeen) > 3*time.Minute {
					return probeMiss(fmt.Sprintf("coordinator off the bus for %s", time.Since(lastSeen).Truncate(time.Second)))
				}
				return gateway.ProbeResult{Unknown: true, Detail: "no zigbee coordinator on the bus"}
			}
			lastSeen = time.Now()
			t := zt()
			if t == nil {
				return probeMiss("port " + p + " claimed but no zigbee gateway running")
			}
			if !t.IsReady() {
				return probeMiss("coordinator not ready (" + transport.ZNPDevStateName(t.CoordState()) + ")")
			}
			if since := time.Since(t.LastFrameAt()); since < 120*time.Second {
				return probeOK(fmt.Sprintf("frame %s ago", since.Truncate(time.Second)))
			}
			if err := t.Ping(ctx); err != nil {
				return probeMiss("SYS_PING: " + err.Error())
			}
			return probeOK("SYS_PING answered")
		},
		Steps: []gateway.HealStep{
			{
				Level: gateway.HealLevelSoft, Name: "start gateway or reopen and re-init", Grace: 65 * time.Second,
				Skip: func() bool { return port() == "" },
				Run: func(ctx context.Context) error {
					if zt() == nil {
						return startGateway(ctx)
					}
					return zt().SoftReinit(ctx)
				},
			},
			{
				Level: gateway.HealLevelDevice, Name: "SYS_RESET_REQ", Grace: 45 * time.Second,
				Skip: func() bool { return zt() == nil },
				Run: func(ctx context.Context) error {
					t := zt()
					if t == nil {
						return errors.New("no zigbee transport")
					}
					return t.SysReset(ctx)
				},
			},
			{
				Level: gateway.HealLevelHard, Name: "hub port power cycle", Grace: 90 * time.Second,
				Run: func(ctx context.Context) error {
					if port() != "" && hard != nil {
						if err := hard(ctx); err != nil {
							return err
						}
					} else if !powerCycle(ctx, "zigbee", "") {
						return errors.New("coordinator off the bus and no remembered hub port to cycle")
					}
					// The supervisor re-identifies the dongle and the gateway
					// manager restarts the gateway; only if nothing came back
					// after a generous wait start it ourselves.
					time.AfterFunc(45*time.Second, func() {
						if zt() != nil {
							return
						}
						rctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
						defer cancel()
						if err := startGateway(rctx); err != nil {
							log.Warn().Err(err).Msg("device health: zigbee gateway start after power cycle failed")
						}
					})
					return nil
				},
			},
		},
	}
}

// rtlSDRHealthTarget: a scan that returns samples is liveness; two failed
// scans in a row (each a 90 s hang) or five minutes without a good scan
// is a wedge. Rungs: cancel the running child (level 1), then the OOB
// action (level 3): a hub-port VBUS cut when the dongle sits on a
// switchable hub, else the root-port USBDEVFS_RESET.
func rtlSDRHealthTarget(mon *spectrum.SpectrumMonitor, actions map[byte]oob.Action) gateway.HealthTarget {
	hard := actions[oob.LevelHard]
	return gateway.HealthTarget{
		Name:       "rtl_sdr",
		IfaceIDs:   []string{},
		HardBudget: 2,
		Probe: func(ctx context.Context) gateway.ProbeResult {
			if !mon.Enabled() {
				return gateway.ProbeResult{Unknown: true, Detail: "spectrum monitor disabled"}
			}
			last, fails := mon.LastGoodScan()
			// A Blog V4 cold start takes about two minutes per band before
			// the first samples, so early failures are expected: only a
			// long run of failures, or a long gap after samples once
			// flowed, is a wedge.
			switch {
			case fails >= 4:
				return probeMiss(fmt.Sprintf("%d scans failed in a row: %s", fails, mon.Hardware().LastScanError))
			case last.IsZero() && mon.Uptime() > 8*time.Minute:
				return probeMiss(fmt.Sprintf("no scan has returned samples in %s: %s", mon.Uptime().Truncate(time.Second), mon.Hardware().LastScanError))
			case last.IsZero():
				return probeOK("warming up, no scan completed yet")
			case time.Since(last) > 5*time.Minute:
				return probeMiss(fmt.Sprintf("no good scan for %s: %s", time.Since(last).Truncate(time.Second), mon.Hardware().LastScanError))
			default:
				return probeOK(fmt.Sprintf("good scan %s ago", time.Since(last).Truncate(time.Second)))
			}
		},
		Steps: []gateway.HealStep{
			{Level: gateway.HealLevelSoft, Name: "cancel scan and restart", Grace: 180 * time.Second, Run: mon.RestartScan},
			{
				Level: gateway.HealLevelHard, Name: "USB reset", Grace: 180 * time.Second,
				Skip: func() bool { return hard == nil },
				Run: func(ctx context.Context) error {
					if hard == nil {
						return errors.New("no hard reset action registered")
					}
					if err := hard(ctx); err != nil {
						return err
					}
					time.AfterFunc(10*time.Second, func() { _ = mon.RestartScan(context.Background()) })
					return nil
				},
			},
		},
	}
}

// gpsHealthTarget: a u-blox streams NMEA about once a second whether or
// not it has a fix, so 90 s without a parsed sentence is a wedge. Soft rung
// only before TTC; the root-port USB reset needs confirm.
func gpsHealthTarget(g *transport.GPSReader, actions map[byte]oob.Action) gateway.HealthTarget {
	hard := actions[oob.LevelHard]
	return gateway.HealthTarget{
		Name:     "gps",
		IfaceIDs: []string{},
		MaxLevel: gateway.HealLevelSoft,
		Probe: func(ctx context.Context) gateway.ProbeResult {
			if g.CurrentPort() == "" {
				return gateway.ProbeResult{Unknown: true, Detail: "no GPS port open"}
			}
			last := g.LastSentenceAt()
			if last.IsZero() {
				return probeMiss("port open, no NMEA sentence yet")
			}
			if since := time.Since(last); since > 90*time.Second {
				return probeMiss(fmt.Sprintf("no NMEA sentence for %s", since.Truncate(time.Second)))
			}
			return probeOK(fmt.Sprintf("NMEA %s ago", time.Since(last).Truncate(time.Second)))
		},
		Steps: []gateway.HealStep{
			{Level: gateway.HealLevelSoft, Name: "reopen port", Grace: 30 * time.Second, Run: g.Restart},
			{
				Level: gateway.HealLevelHard, Name: "USB reset", Grace: 40 * time.Second,
				Skip: func() bool { return hard == nil },
				Run: func(ctx context.Context) error {
					if hard == nil {
						return errors.New("no hard reset action registered")
					}
					return hard(ctx)
				},
			},
		},
	}
}

// imtHealthTarget: the 9704's 30 s constellationState poll is the active
// probe; the transport's own serial watchdog reconnects at 2 min stale, so
// the ladder waits four misses before it steps in. The I_EN power cycle
// (level 2) needs confirm before TTC; there is no USB rung on a UART.
func imtHealthTarget(t *transport.DirectIMTTransport) gateway.HealthTarget {
	return gateway.HealthTarget{
		Name:     "imt",
		IfaceIDs: []string{"iridium_imt_0"},
		Misses:   4,
		MaxLevel: gateway.HealLevelSoft,
		Probe: func(ctx context.Context) gateway.ProbeResult {
			port := t.GetPort()
			if port == "" || port == "auto" || port == "supervisor" {
				return gateway.ProbeResult{Unknown: true, Detail: "no serial port assigned"}
			}
			if !t.IsConnected() {
				return probeMiss("not connected")
			}
			last := t.LastActivity()
			if last.IsZero() {
				return probeOK("connected, no activity stamp yet")
			}
			if since := time.Since(last); since > 120*time.Second {
				return probeMiss(fmt.Sprintf("no JSPR traffic for %s", since.Truncate(time.Second)))
			}
			return probeOK(fmt.Sprintf("JSPR traffic %s ago", time.Since(last).Truncate(time.Second)))
		},
		Steps: []gateway.HealStep{
			{Level: gateway.HealLevelSoft, Name: "serial reconnect", Grace: 60 * time.Second, Run: t.ForceReconnect},
			{Level: gateway.HealLevelDevice, Name: "I_EN power cycle", Grace: 90 * time.Second, Run: t.DeviceReset},
		},
	}
}

// iridiumHealthTarget: the 9603's AT polls (signal every 30 s, SBDSX on
// mailbox checks) are the liveness input; SBDIX holds the modem for up to
// a minute, so the window is generous. Soft rung only: the OnOff line is
// not wired on tesseract.
func iridiumHealthTarget(t *transport.DirectSatTransport) gateway.HealthTarget {
	return gateway.HealthTarget{
		Name:     "iridium",
		IfaceIDs: []string{"iridium_0"},
		Misses:   4,
		MaxLevel: gateway.HealLevelSoft,
		Probe: func(ctx context.Context) gateway.ProbeResult {
			port := t.GetPort()
			if port == "" || port == "auto" || port == "supervisor" {
				return gateway.ProbeResult{Unknown: true, Detail: "no serial port assigned"}
			}
			if !t.IsConnected() {
				return probeMiss("not connected")
			}
			last := t.LastReplyAt()
			if last.IsZero() {
				return probeOK("connected, no reply stamp yet")
			}
			if since := time.Since(last); since > 10*time.Minute {
				return probeMiss(fmt.Sprintf("no AT reply for %s", since.Truncate(time.Second)))
			}
			return probeOK(fmt.Sprintf("AT reply %s ago", time.Since(last).Truncate(time.Second)))
		},
		Steps: []gateway.HealStep{
			{Level: gateway.HealLevelSoft, Name: "serial reconnect", Grace: 120 * time.Second, Run: t.Reconnect},
		},
	}
}

// meshHealthTarget: the Meshtastic radio answers a self-addressed admin
// get_device_metadata over the serial link with no LoRa transmission, so a
// radio with zero neighbours still probes healthy. Rungs: reconnect, a
// DTR/RTS reboot of the ESP32-S3, an admin reboot, then the hub-port VBUS
// cut through the same action the OOB executor uses.
func meshHealthTarget(cfg *config.Config, dm *transport.DirectMeshTransport, actions map[byte]oob.Action) gateway.HealthTarget {
	configTimeout := time.Duration(cfg.MeshConfigTimeoutSec) * time.Second
	if configTimeout <= 0 {
		configTimeout = 60 * time.Second
	}
	handshakeGrace := configTimeout + 15*time.Second

	hard := actions[oob.LevelHard]

	return gateway.HealthTarget{
		Name:         "mesh",
		IfaceIDs:     []string{"mesh_0"},
		ProbeTimeout: 10 * time.Second,
		Probe: func(ctx context.Context) gateway.ProbeResult {
			port := dm.GetPort()
			if port == "" || port == "auto" || port == "supervisor" {
				return gateway.ProbeResult{Unknown: true, Detail: "no serial port assigned"}
			}
			if !dm.IsConnected() {
				if n, last := dm.ConnectFails(); n > 0 {
					return probeMiss(fmt.Sprintf("serial open failed %d times: %s", n, last))
				}
				if n := dm.HandshakeFails(); n > 0 {
					return probeMiss(fmt.Sprintf("radio silent on %d consecutive handshakes", n))
				}
				return probeMiss("not connected")
			}
			if dm.MyNodeNum() == 0 {
				return probeMiss("connected but the radio never sent its node number")
			}
			sinceFrame := time.Since(dm.LastFrameAt())
			if !dm.ConfigReal() && time.Since(dm.ConnectedAt()) > handshakeGrace && sinceFrame > 60*time.Second {
				return probeMiss("config handshake never completed and no frames")
			}
			if sinceFrame < 60*time.Second {
				return probeOK(fmt.Sprintf("frame %s ago", sinceFrame.Truncate(time.Second)))
			}
			if err := dm.ProbeLocal(ctx, 5*time.Second); err != nil {
				return probeMiss("local probe: " + err.Error())
			}
			return probeOK("local probe answered")
		},
		Steps: []gateway.HealStep{
			{Level: gateway.HealLevelSoft, Name: "serial reconnect", Grace: handshakeGrace, Run: dm.Reconnect},
			{Level: gateway.HealLevelSoft, Name: "DTR/RTS reboot", Grace: handshakeGrace, Run: dm.RebootViaLines},
			{
				Level: gateway.HealLevelDevice, Name: "admin reboot", Grace: 60 * time.Second,
				Skip: func() bool { return !dm.IsConnected() || dm.MyNodeNum() == 0 },
				Run: func(ctx context.Context) error {
					return dm.AdminReboot(ctx, dm.MyNodeNum(), 5)
				},
			},
			{
				Level: gateway.HealLevelHard, Name: "hub port power cycle", Grace: 150 * time.Second,
				Skip: func() bool { return hard == nil },
				Run: func(ctx context.Context) error {
					if hard == nil {
						return errors.New("no hard reset action registered")
					}
					if err := hard(ctx); err != nil {
						return err
					}
					// The device comes back under the same tty name, which
					// the supervisor does not see as a change; reconnect
					// ourselves once it has enumerated. [MESHSAT-786]
					time.AfterFunc(15*time.Second, func() {
						if dm.IsConnected() {
							return
						}
						rctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
						defer cancel()
						if err := dm.Reconnect(rctx); err != nil {
							log.Warn().Err(err).Msg("device health: mesh reconnect after power cycle failed")
						}
					})
					return nil
				},
			},
		},
	}
}

// aprsKISSDevice is the serial TNC path the APRS gateway will open: the
// stored aprs_0 gateway config wins, the environment is the first-boot
// default. Empty when the kit runs Direwolf. [MESHSAT-821]
func aprsKISSDevice(cfg *config.Config, db *database.DB) string {
	if db != nil {
		if gc, err := db.GetGatewayConfigByInstance("aprs_0"); err == nil && gc != nil {
			if ac, perr := gateway.ParseAPRSConfig(gc.Config); perr == nil && ac.KISSDevice != "" {
				return ac.KISSDevice
			}
		}
	}
	return cfg.APRSKISSDevice
}

// aprsTNCReopen returns the receive watchdog's step 2 and the OOB level 3
// for a hardware-TNC kit, or nil when the running gateway drives Direwolf
// (then the AIOC hub-port cut applies). Resolved per call so a gateway
// recreated by a config change is always the one acted on. [MESHSAT-821]
func aprsTNCReopen(gwMgr *gateway.Manager) func(ctx context.Context) error {
	if gwMgr == nil {
		return nil
	}
	ag := gwMgr.APRSGateway()
	if ag == nil || !ag.SerialTNC() {
		return nil
	}
	return func(ctx context.Context) error {
		cur := gwMgr.APRSGateway()
		if cur == nil {
			return errors.New("aprs gateway not running")
		}
		return cur.ReopenTNC(ctx)
	}
}
