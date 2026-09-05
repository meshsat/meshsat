package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/config"
	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/oob"
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
	mesh transport.MeshTransport, cell transport.CellTransport, gwMgr *gateway.Manager, rxWatchdog *gateway.RxWatchdog) {

	if dm, ok := mesh.(*transport.DirectMeshTransport); ok && dm != nil {
		dh.Register(meshHealthTarget(cfg, dm, oobActions["mesh"]))
	}
	if dc, ok := cell.(*transport.DirectCellTransport); ok && dc != nil {
		dh.Register(cellularHealthTarget(cfg, dc, gwMgr, oobActions["cellular"]))
	}
	if gwMgr != nil {
		dh.Register(zigbeeHealthTarget(gwMgr, oobActions["zigbee"]))
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
// a VBUS cut: the ESP32 boots and pulses PWRKEY once (modem ON); any DTR
// reset from an open inside the window pulses it again (modem OFF). The
// recipe that worked on 5 Sep 2026 was container stopped, cut, 60 s quiet,
// start. [MESHSAT-812]
const cellularQuietWindow = 60 * time.Second

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
				return probeOK(err.Error()) // inside the quiet window by design
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
					time.AfterFunc(cellularQuietWindow+15*time.Second, func() {
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
// transport, rungs reopen+init on the same tty, Z-Stack soft reset, hub
// port power cycle followed by a gateway restart.
func zigbeeHealthTarget(gwMgr *gateway.Manager, actions map[byte]oob.Action) gateway.HealthTarget {
	hard := actions[oob.LevelHard]
	zt := func() *transport.DirectZigBeeTransport {
		if zgw := gwMgr.GetZigBeeGateway(); zgw != nil {
			return zgw.GetTransport()
		}
		return nil
	}
	return gateway.HealthTarget{
		Name:         "zigbee",
		IfaceIDs:     []string{"zigbee_0"},
		ProbeTimeout: 10 * time.Second,
		Probe: func(ctx context.Context) gateway.ProbeResult {
			t := zt()
			if t == nil {
				return gateway.ProbeResult{Unknown: true, Detail: "no zigbee gateway running"}
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
				Level: gateway.HealLevelSoft, Name: "reopen and re-init", Grace: 65 * time.Second,
				Run: func(ctx context.Context) error {
					t := zt()
					if t == nil {
						return errors.New("no zigbee transport")
					}
					return t.SoftReinit(ctx)
				},
			},
			{
				Level: gateway.HealLevelDevice, Name: "SYS_RESET_REQ", Grace: 45 * time.Second,
				Run: func(ctx context.Context) error {
					t := zt()
					if t == nil {
						return errors.New("no zigbee transport")
					}
					return t.SysReset(ctx)
				},
			},
			{
				Level: gateway.HealLevelHard, Name: "hub port power cycle", Grace: 60 * time.Second,
				Skip: func() bool { return hard == nil },
				Run: func(ctx context.Context) error {
					if hard == nil {
						return errors.New("no hard reset action registered")
					}
					if err := hard(ctx); err != nil {
						return err
					}
					time.AfterFunc(10*time.Second, func() {
						rctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
						defer cancel()
						if err := gwMgr.RestartGatewayInstance(rctx, "zigbee_0"); err != nil {
							log.Warn().Err(err).Msg("device health: zigbee gateway restart after power cycle failed")
						}
					})
					return nil
				},
			},
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
