package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"meshsat/internal/api"
	"meshsat/internal/channel"
	"meshsat/internal/compress"
	"meshsat/internal/config"
	"meshsat/internal/database"
	"meshsat/internal/dedup"
	"meshsat/internal/directory"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/hemb"
	"meshsat/internal/hubreporter"
	"meshsat/internal/keystore"
	"meshsat/internal/routing"
	"meshsat/internal/rules"
	"meshsat/internal/spectrum"
	"meshsat/internal/sysinfo"
	"meshsat/internal/timesync"
	"meshsat/internal/transport"
)

func main() {
	startTime := time.Now()

	// Console-friendly logging
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}).
		With().Timestamp().Caller().Logger()

	cfg := config.Load()

	log.Info().
		Int("port", cfg.Port).
		Str("db", cfg.DBPath).
		Str("hal", cfg.HALURL).
		Str("mode", cfg.Mode).
		Msg("starting MeshSat")

	// Database
	db, err := database.New(cfg.DBPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open database")
	}
	defer db.Close()
	log.Info().Str("path", cfg.DBPath).Msg("database ready")

	// Transport — mode selects communication backend
	var mesh transport.MeshTransport
	var sat transport.SatTransport
	var cell transport.CellTransport
	var imtTransport transport.SatTransport    // IMT (9704) transport for coexistence
	var gpsExcludePorts []func() string        // populated in direct mode for GPS reader
	var supervisor *transport.DeviceSupervisor // populated in direct mode for USB discovery
	switch cfg.Mode {
	case "cubeos", "standalone":
		mesh = transport.NewHALMeshTransport(cfg.HALURL, cfg.HALAPIKey)
		log.Info().Str("hal", cfg.HALURL).Str("mode", cfg.Mode).Msg("using HAL mesh transport")

		// Satellite transport (optional — only if Iridium is available)
		sat = transport.NewHALSatTransport(cfg.HALURL, cfg.HALAPIKey)
		log.Info().Msg("HAL satellite transport available")

	case "direct":
		// Direct serial — talk to USB devices without HAL.
		// DeviceSupervisor handles discovery, identification, and auto-reconnect.
		// Transports are created with port "supervisor" so they don't run their
		// own auto-detect — the supervisor assigns ports via SetPort() callbacks.
		// Only use explicit ports if the user set a non-"auto" value.
		meshPort := "supervisor"
		if cfg.MeshtasticPort != "" && cfg.MeshtasticPort != "auto" {
			meshPort = cfg.MeshtasticPort
		}
		imtPort := "supervisor"
		if cfg.IMTPort != "" && cfg.IMTPort != "auto" {
			imtPort = cfg.IMTPort
		}
		iridiumPort := "supervisor"
		if cfg.IridiumPort != "" && cfg.IridiumPort != "auto" {
			iridiumPort = cfg.IridiumPort
		}
		cellPort := "supervisor"
		if cfg.CellularPort != "" && cfg.CellularPort != "auto" {
			cellPort = cfg.CellularPort
		}

		directMesh := transport.NewDirectMeshTransport(meshPort)
		directMesh.SetWatchdogMinutes(cfg.MeshWatchdogMin)
		mesh = directMesh

		directIMT := transport.NewDirectIMTTransport(imtPort)

		// GPIO power control for 9704 on platform UART (Pi 5 UART2).
		// Cycles I_EN LOW→HIGH on each connect to reset the JSPR state machine.
		// Only needed for ttyAMA* — USB-connected 9704s reset via USB enumeration.
		if gpioIEN := os.Getenv("MESHSAT_IMT_GPIO_I_EN"); gpioIEN != "" {
			chip := os.Getenv("MESHSAT_IMT_GPIO_CHIP")
			if chip == "" {
				chip = "gpiochip4" // Pi 5 RP1 default
			}
			ien, _ := fmt.Sscanf(gpioIEN, "%d", new(int))
			ibtd := 23 // default BCM 23
			if v := os.Getenv("MESHSAT_IMT_GPIO_I_BTD"); v != "" {
				fmt.Sscanf(v, "%d", &ibtd)
			}
			if ien > 0 {
				ienVal := 0
				fmt.Sscanf(gpioIEN, "%d", &ienVal)
				directIMT.SetGPIO(chip, ienVal, ibtd)
				log.Info().Str("chip", chip).Int("i_en", ienVal).Int("i_btd", ibtd).
					Msg("9704 GPIO power control configured")
			}
		}

		directSat := transport.NewDirectSatTransport(iridiumPort)
		if cfg.IridiumSleepPin > 0 {
			directSat.SetSleepPin(cfg.IridiumSleepPin)
		}

		directCell := transport.NewDirectCellTransport(cellPort)
		directCell.SetSIMCardLookup(
			func(iccid string) (*transport.SIMCardInfo, error) {
				sim, err := db.GetSIMCardByICCID(iccid)
				if err != nil || sim == nil {
					return nil, err
				}
				return &transport.SIMCardInfo{
					ICCID: sim.ICCID, Phone: sim.Phone,
					PIN: sim.PIN, Label: sim.Label,
				}, nil
			},
			func(iccid string) { _ = db.TouchSIMCardLastSeen(iccid) },
		)
		// MESHSAT_SIM_PIN: fallback PIN when ICCID can't be read on a locked SIM
		// (e.g., Huawei E220 firmware doesn't expose ICCID until PIN is entered).
		// The DB lookup by ICCID takes priority when available. [MESHSAT-445]
		if envPIN := os.Getenv("MESHSAT_SIM_PIN"); envPIN != "" {
			directCell.SetFallbackPIN(envPIN)
		}
		cell = directCell

		// DeviceSupervisor: replaces one-shot auto-detect with continuous
		// two-tier polling (30s port scan + 15s reconciliation).
		supervisor = transport.NewDeviceSupervisor()

		// Register explicit port overrides from env vars
		supervisor.SetExplicitPort(transport.RoleMeshtastic, cfg.MeshtasticPort)
		supervisor.SetExplicitPort(transport.RoleIridium9704, cfg.IMTPort)
		supervisor.SetExplicitPort(transport.RoleIridium9603, cfg.IridiumPort)
		supervisor.SetExplicitPort(transport.RoleCellular, cfg.CellularPort)
		supervisor.SetExplicitPort(transport.RoleZigBee, cfg.ZigBeePort)

		// Wire driver callbacks: supervisor notifies transports when ports are
		// discovered or lost, replacing the old exclude-port daisy chain.
		supervisor.SetCallbacks(transport.RoleMeshtastic, &transport.DriverCallbacks{
			InstanceID: "mesh_0",
			OnPortFound: func(port string) {
				directMesh.SetPort(port)
				log.Info().Str("port", port).Msg("supervisor: meshtastic port assigned")
			},
			OnPortLost: func(port string) {
				directMesh.Close()
				log.Warn().Str("port", port).Msg("supervisor: meshtastic port lost")
			},
			HasPort: func() bool { return directMesh.GetPort() != "" && directMesh.GetPort() != "supervisor" },
		})

		supervisor.SetCallbacks(transport.RoleIridium9704, &transport.DriverCallbacks{
			InstanceID: "iridium_imt_0",
			OnPortFound: func(port string) {
				directIMT.SetPort(port)
				log.Info().Str("port", port).Msg("supervisor: 9704 IMT port assigned")
			},
			OnPortLost: func(port string) {
				directIMT.Close()
				log.Warn().Str("port", port).Msg("supervisor: 9704 IMT port lost")
			},
			HasPort: func() bool { return directIMT.GetPort() != "" && directIMT.GetPort() != "supervisor" },
		})

		supervisor.SetCallbacks(transport.RoleIridium9603, &transport.DriverCallbacks{
			InstanceID: "iridium_0",
			OnPortFound: func(port string) {
				directSat.SetPort(port)
				log.Info().Str("port", port).Msg("supervisor: 9603 SBD port assigned")
			},
			OnPortLost: func(port string) {
				directSat.Close()
				log.Warn().Str("port", port).Msg("supervisor: 9603 SBD port lost")
			},
			HasPort: func() bool { return directSat.GetPort() != "" && directSat.GetPort() != "supervisor" },
		})

		supervisor.SetCallbacks(transport.RoleCellular, &transport.DriverCallbacks{
			InstanceID: "cellular_0",
			OnPortFound: func(port string) {
				directCell.SetPort(port)
				log.Info().Str("port", port).Msg("supervisor: cellular port assigned")
			},
			OnPortLost: func(port string) {
				directCell.Close()
				log.Warn().Str("port", port).Msg("supervisor: cellular port lost")
			},
			HasPort: func() bool { return directCell.GetPort() != "" && directCell.GetPort() != "supervisor" },
		})

		supervisor.Start()
		defer supervisor.Stop()

		// Wait for the supervisor's initial scan+identify cycle to complete before
		// starting the gateway manager. Without this, gwMgr.Start() loads saved
		// gateway configs and calls Subscribe() on transports that still have
		// port="supervisor" (not yet assigned by the supervisor). [MESHSAT-403]
		supervisor.WaitForInitialScan()

		// Always register both satellite transports. The supervisor assigns ports
		// dynamically via callbacks — no need to choose one at startup.
		// "iridium" (SBD) gateway always uses directSat (9603).
		// "iridium_imt" (IMT) gateway always uses directIMT (9704).
		sat = directSat
		imtTransport = directIMT
		log.Info().
			Str("sbd_port", directSat.GetPort()).
			Str("imt_port", directIMT.GetPort()).
			Msg("satellite transports registered (ports assigned dynamically by supervisor)")

		log.Info().Msg("device supervisor active — continuous port discovery enabled")

		// GPS reader exclude ports — all radio devices so GPS auto-detect skips them
		gpsExcludePorts = []func() string{directMesh.GetPort, directSat.GetPort, directIMT.GetPort}

	default:
		log.Fatal().Str("mode", cfg.Mode).Msg("unsupported mode")
	}
	defer mesh.Close()
	if sat != nil {
		defer sat.Close()
	}
	if imtTransport != nil {
		defer imtTransport.Close()
	}
	if cell != nil {
		defer cell.Close()
	}

	// Graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Deduplicator (in-memory, composite key, 10min TTL, 10k max)
	deduplicator := dedup.New(10*time.Minute, 10000)
	deduplicator.StartPruner(ctx)
	log.Info().Msg("deduplicator ready")

	// Channel registry (v0.2.0)
	registry := channel.NewRegistry()
	channel.RegisterDefaults(registry)
	log.Info().Int("channels", len(registry.IDs())).Msg("channel registry ready")

	// Interface manager (v0.3.0 — interface-based routing foundation)
	ifaceMgr := engine.NewInterfaceManager(db)
	if err := ifaceMgr.Start(ctx); err != nil {
		log.Error().Err(err).Msg("interface manager start failed")
	}

	// v0.3.0 Access rule evaluator
	accessEval := rules.NewAccessEvaluator(db)
	if err := accessEval.ReloadFromDB(); err != nil {
		log.Warn().Err(err).Msg("failed to load access rules (table may not exist yet)")
	}

	// Processor
	proc := engine.NewProcessor(db, mesh)
	proc.SetDeduplicator(deduplicator)

	// Gateway manager
	gwMgr := gateway.NewManager(db, sat)
	if imtTransport != nil {
		gwMgr.SetIMTTransport(imtTransport)
	}
	if cell != nil {
		gwMgr.SetCellTransport(cell)
	}

	// TLE manager — daily Celestrak TLE refresh + SGP4 pass prediction
	// Created early so it's available to the gateway manager for pass scheduling
	tleMgr := engine.NewTLEManager(db)
	tleMgr.Start(ctx)

	// Wire TLE manager into gateway manager for pass-aware scheduling
	gwMgr.SetPassPredictor(&tleAdapter{tleMgr})

	// Register receiver callback so gateways started via API also get
	// their inbound channel drained by the processor (fixes silent drop bug).
	gwMgr.SetReceiverStartFunc(proc.StartGatewayReceiver)
	gwMgr.SetEventEmitFunc(func(eventType, message string) {
		proc.Emit(transport.MeshEvent{
			Type:    eventType,
			Message: message,
			Time:    time.Now().UTC().Format(time.RFC3339),
		})
	})

	// Wire node name resolver so SMS shows human-readable sender names
	gwMgr.SetNodeNameResolver(func(nodeID uint32) string {
		nodes, err := mesh.GetNodes(ctx)
		if err != nil {
			return ""
		}
		for _, n := range nodes {
			if n.Num == nodeID {
				if n.LongName != "" {
					return n.LongName
				}
				if n.ShortName != "" {
					return n.ShortName
				}
				return ""
			}
		}
		return ""
	})

	// Start gateway manager (loads enabled configs from DB).
	// The receiver callback fires for each gateway started here.
	if err := gwMgr.Start(ctx); err != nil {
		log.Error().Err(err).Msg("gateway manager start failed")
	}

	// Wire device supervisor → gateway manager: auto-start/stop gateways
	// when hardware is connected, disconnected, or hot-swapped.
	if supervisor != nil {
		gwMgr.WatchDeviceEvents(ctx, supervisor)
		// Reconcile DB configs with actual hardware: stop gateways for missing
		// devices, start gateways for detected devices with enabled configs.
		// Safe to call now — WaitForInitialScan() already completed above. [MESHSAT-403]
		gwMgr.ReconcileWithHardware(ctx)
	}

	// Register gateway manager as dynamic provider so processor always
	// forwards to live gateway instances (survives stop/start/reconfigure)
	proc.SetGatewayProvider(gwMgr)

	// Signing service (v0.3.0 — Ed25519 non-repudiation + hash-chain audit log)
	var signingService *engine.SigningService
	if ss, err := engine.NewSigningService(db); err != nil {
		log.Warn().Err(err).Msg("signing service init failed - non-repudiation disabled")
	} else {
		signingService = ss
	}

	// Routing identity — load or generate Ed25519 + X25519 keypair, persist via system_config
	var resourceXfer *routing.ResourceTransfer
	var routingID *routing.Identity
	var linkMgr *routing.LinkManager
	var destTable *routing.DestinationTable
	var announceRelay *routing.AnnounceRelay
	var ifaceReg *routing.InterfaceRegistry
	routingID, err = routing.NewIdentity(db)
	if err != nil {
		log.Error().Err(err).Msg("routing identity init failed - routing disabled")
	} else {
		// Destination table — in-memory + DB-persisted registry of known remote identities
		destTable = routing.NewDestinationTable(db)
		if err := destTable.LoadFromDB(); err != nil {
			log.Warn().Err(err).Msg("failed to load routing destinations from DB")
		}

		// Announce relay — dedup + hop-count enforcement + delayed retransmit
		relayConfig := routing.DefaultRelayConfig()
		announceRelay = routing.NewAnnounceRelay(relayConfig, destTable, func(data []byte, announce *routing.Announce, sourceIface string) {
			if ifaceReg == nil {
				return
			}
			floodable := ifaceReg.Floodable()
			for _, iface := range floodable {
				if iface.ID() == sourceIface {
					continue // don't send back to source
				}
				if err := iface.Send(ctx, data); err != nil {
					log.Warn().Err(err).Str("iface", iface.ID()).Msg("relay: failed to forward announce")
				}
			}
			log.Debug().Str("dest_hash", fmt.Sprintf("%x", announce.DestHash)).
				Int("hops", int(announce.HopCount)).
				Str("source", sourceIface).
				Msg("relayed announce to floodable interfaces")
		})
		announceRelay.RegisterLocal(routingID.DestHash())
		announceRelay.StartPruner(ctx)

		// Bandwidth limiter — token bucket per interface at 2% of channel BW
		bwLimiter := routing.NewAnnounceBandwidthLimiter()
		for _, chID := range registry.IDs() {
			bwLimiter.SetDefaultBandwidth(chID, chID)
		}
		_ = bwLimiter // used by announce relay path

		// Link manager — ECDH 3-packet handshake, AES-256-GCM encryption
		linkMgr = routing.NewLinkManager(routingID)

		// Keepalive — 18s heartbeat, 60s timeout for active links
		keepalive := routing.NewLinkKeepalive(linkMgr, func(linkID [routing.LinkIDLen]byte, data []byte) {
			log.Debug().Msg("keepalive sent")
		})
		keepalive.Start(ctx)

		// Wire routing subsystem into processor so incoming Reticulum packets
		// (from TCP or mesh) are dispatched to announce relay, link manager, etc.
		proc.SetRouting(announceRelay, linkMgr, keepalive, destTable)
		proc.SetRoutingIdentity(routingID)

		// Resource transfer — chunked reliable delivery over Reticulum links (RLNC enabled).
		rtConfig := routing.DefaultResourceTransferConfig()
		rtConfig.RLNCEnabled = true
		rtConfig.RLNCRedundancy = 1.2 // 20% coded redundancy for lossy links
		resourceXfer = routing.NewResourceTransfer(rtConfig, func(ifaceID string, packet []byte) error {
			return proc.SendReticulumPacketTo(ifaceID, packet)
		})
		resourceXfer.SetOnReceive(func(hash string, data []byte, iface string) {
			log.Info().Str("hash", hash[:16]).Int("size", len(data)).Str("iface", iface).
				Msg("resource received, persisting to DB")
			if _, err := db.InsertReceivedResource(hash, "", "application/octet-stream", iface, data); err != nil {
				log.Error().Err(err).Str("hash", hash[:16]).Msg("failed to persist received resource")
			}
		})
		resourceXfer.StartPruner(ctx)
		proc.SetResourceTransfer(resourceXfer)

		log.Info().Str("dest_hash", routingID.DestHashHex()).Msg("routing subsystem initialized")
	}

	// Interface registry — wraps all transports as named Reticulum interfaces
	// for the Transport Node to route packets between.
	if routingID != nil {
		ifaceReg = routing.NewInterfaceRegistry()

		// LoRa/Meshtastic — free, ~230 byte MTU (SF7), PRIVATE_APP portnum 256
		ifaceReg.Register(routing.NewReticulumInterface("mesh_0", "mesh", 230, func(fwdCtx context.Context, packet []byte) error {
			return mesh.SendRaw(fwdCtx, transport.RawRequest{
				PortNum: 256, // PRIVATE_APP
				Payload: base64.StdEncoding.EncodeToString(packet),
			})
		}))
		proc.RegisterPacketSender("mesh_0", func(sendCtx context.Context, data []byte) error {
			return mesh.SendRaw(sendCtx, transport.RawRequest{
				PortNum: 256,
				Payload: base64.StdEncoding.EncodeToString(data),
			})
		})
	}

	// TCP/HDLC — RNS-compatible Reticulum interface over TCP.
	// DB-persisted config (from Settings > Routing) overrides env vars after first save.
	tcpListenAddr := cfg.TCPListenAddr
	tcpConnectAddr := cfg.TCPConnectAddr
	if raw, dbErr := db.GetSystemConfig("reticulum_config"); dbErr == nil && raw != "" {
		var rc struct {
			ListenPort int `json:"listen_port"`
		}
		if json.Unmarshal([]byte(raw), &rc) == nil && rc.ListenPort > 0 {
			tcpListenAddr = fmt.Sprintf("0.0.0.0:%d", rc.ListenPort)
			log.Info().Int("port", rc.ListenPort).Msg("tcp listen address from DB config (overrides env)")
		}
	}

	var tcpIface *routing.TCPInterface
	if tcpListenAddr != "" || tcpConnectAddr != "" {
		tcpIface = routing.NewTCPInterface(routing.TCPInterfaceConfig{
			Name:              "tcp_0",
			ListenAddr:        tcpListenAddr,
			ConnectAddr:       tcpConnectAddr,
			Reconnect:         true,
			ReconnectInterval: 10 * time.Second,
		}, func(packet []byte) {
			log.Debug().Int("size", len(packet)).Msg("tcp: received reticulum packet")
			proc.InjectReticulumPacket(packet, "tcp_0")
		})
		if err := tcpIface.Start(ctx); err != nil {
			log.Error().Err(err).Msg("tcp interface start failed")
			tcpIface = nil
		} else {
			proc.RegisterPacketSender("tcp_0", tcpIface.Send)
			if ifaceReg != nil {
				ifaceReg.Register(routing.NewReticulumInterface("tcp_0", "tcp", 65535, tcpIface.Send))
			}
			log.Info().Str("listen", cfg.TCPListenAddr).Str("connect", cfg.TCPConnectAddr).Msg("tcp reticulum interface started")
		}
	}

	// Satellite Reticulum interfaces — wire SBD and IMT as Reticulum transports
	// so routing packets can flow over satellite links.
	if sat != nil {
		sbdIface := routing.NewSatInterface(routing.SatInterfaceConfig{
			Name: "iridium_0",
			Type: "iridium",
			MTU:  340,
		}, sat, func(packet []byte) {
			log.Debug().Int("size", len(packet)).Msg("iridium_0: received reticulum packet via SBD MT")
			proc.InjectReticulumPacket(packet, "iridium_0")
		})
		if err := sbdIface.Start(ctx); err != nil {
			log.Error().Err(err).Msg("iridium reticulum interface start failed")
		} else {
			proc.RegisterPacketSender("iridium_0", sbdIface.Send)
			if ifaceReg != nil {
				ifaceReg.Register(routing.NewReticulumInterface("iridium_0", "iridium", 340, sbdIface.Send))
			}
			log.Info().Msg("iridium SBD reticulum interface started")
		}
	}
	if imtTransport != nil {
		imtIface := routing.NewSatInterface(routing.SatInterfaceConfig{
			Name: "iridium_imt_0",
			Type: "iridium",
			MTU:  102400, // 100KB
		}, imtTransport, func(packet []byte) {
			log.Debug().Int("size", len(packet)).Msg("iridium_imt_0: received reticulum packet via IMT MT")
			proc.InjectReticulumPacket(packet, "iridium_imt_0")
		})
		if err := imtIface.Start(ctx); err != nil {
			log.Error().Err(err).Msg("IMT reticulum interface start failed")
		} else {
			proc.RegisterPacketSender("iridium_imt_0", imtIface.Send)
			if ifaceReg != nil {
				ifaceReg.Register(routing.NewReticulumInterface("iridium_imt_0", "iridium", 102400, imtIface.Send))
			}
			log.Info().Msg("iridium IMT reticulum interface started")
		}
	}

	// AX.25/APRS Reticulum interface — bidirectional via Direwolf KISS TNC
	if cfg.AX25KISSAddr != "" {
		ax25Iface := routing.NewAX25Interface(routing.AX25InterfaceConfig{
			Name:     "ax25_0",
			KISSAddr: cfg.AX25KISSAddr,
			Callsign: cfg.AX25Callsign,
		}, func(packet []byte) {
			log.Debug().Int("size", len(packet)).Msg("ax25_0: received reticulum packet via KISS")
			proc.InjectReticulumPacket(packet, "ax25_0")
		})
		// Wire shared KISS TX through a provider so we always resolve to
		// the current APRSGateway — Manager.ConfigureInstance recreates
		// the gateway on PUT /api/gateways/aprs, and a captured pointer
		// would otherwise outlive its target. [MESHSAT-403, MESHSAT-514]
		ax25Iface.SetKISSTXProvider(func() routing.KISSTXFunc {
			agw := gwMgr.GetAPRSGateway()
			if agw == nil {
				return nil
			}
			return func(payload []byte) error { return agw.KISSSendFrame(payload) }
		})
		log.Info().Msg("ax25_0: TX routed through APRS gateway KISS connection (provider)")

		if err := ax25Iface.Start(ctx); err != nil {
			log.Error().Err(err).Msg("ax25 reticulum interface start failed")
		} else {
			proc.RegisterPacketSender("ax25_0", ax25Iface.Send)
			if ifaceReg != nil {
				ifaceReg.Register(routing.NewReticulumInterface("ax25_0", "aprs", 256, ax25Iface.Send))
			}
			log.Info().Str("kiss", cfg.AX25KISSAddr).Str("call", cfg.AX25Callsign).Msg("ax25 reticulum interface started")
		}
	}

	// SMS Reticulum interface — cellular SMS transport for Reticulum packets.
	// Wraps existing CellTransport: outbound as base64 SMS with "RNS:" prefix, inbound decoded.
	// DB config overrides env var after first save via Settings > Routing.
	if cell != nil {
		smsPeer := cfg.SMSReticulumPeer
		if dbPeer, dbErr := db.GetSystemConfig("reticulum_sms_peer"); dbErr == nil && dbPeer != "" {
			smsPeer = dbPeer
		}
		smsIface, smsRI := routing.RegisterSMSInterface(routing.SMSInterfaceConfig{
			Name:       "sms_0",
			PeerNumber: smsPeer,
		}, cell, func(packet []byte) {
			log.Debug().Int("size", len(packet)).Msg("sms_0: received reticulum packet via SMS")
			proc.InjectReticulumPacket(packet, "sms_0")
		})
		if err := smsIface.Start(ctx); err != nil {
			log.Error().Err(err).Msg("sms reticulum interface start failed")
		} else {
			proc.RegisterPacketSender("sms_0", smsIface.Send)
			// Also register as cellular_0 so HeMB bond groups can use cellular as a bearer.
			// The sms_0 interface wraps CellTransport — cellular_0 is the same physical transport. [MESHSAT-448]
			proc.RegisterPacketSender("cellular_0", smsIface.Send)
			if ifaceReg != nil {
				ifaceReg.Register(smsRI)
			}
			log.Info().Str("peer", smsPeer).Msg("sms reticulum interface started")
		}
	}

	// (ZigBee sensor router is wired below, after gpsReader is created.)

	// ZigBee Reticulum interface — bidirectional via CC2652P coordinator.
	// Raw binary Reticulum packets over ZNP AF_DATA_REQUEST (100-byte MTU).
	if zgw := gwMgr.GetZigBeeGateway(); zgw != nil {
		if zt := zgw.GetTransport(); zt != nil {
			zigbeeIface := routing.NewZigBeeInterface(routing.ZigBeeInterfaceConfig{
				Name: "zigbee_0",
			}, zt, func(packet []byte) {
				log.Debug().Int("size", len(packet)).Msg("zigbee_0: received reticulum packet via ZigBee")
				proc.InjectReticulumPacket(packet, "zigbee_0")
			})
			if err := zigbeeIface.Start(ctx); err != nil {
				log.Error().Err(err).Msg("zigbee reticulum interface start failed")
			} else {
				proc.RegisterPacketSender("zigbee_0", zigbeeIface.Send)
				if ifaceReg != nil {
					ifaceReg.Register(routing.NewReticulumInterface("zigbee_0", "zigbee", 100, zigbeeIface.Send))
				}
				log.Info().Msg("zigbee reticulum interface started")
			}
		}
	}

	// BLE Reticulum interface — GATT peripheral via BlueZ D-Bus on Pi 5.
	// Reticulum packets segmented into BLE chunks (SAR), reassembled on receive.
	if cfg.BLEAdapter != "" {
		bleIface, bleRI := routing.RegisterBLEInterface(routing.BLEInterfaceConfig{
			Name:       "ble_0",
			AdapterID:  cfg.BLEAdapter,
			DeviceName: cfg.BLEDeviceName,
		}, func(packet []byte) {
			log.Debug().Int("size", len(packet)).Msg("ble_0: received reticulum packet via BLE")
			proc.InjectReticulumPacket(packet, "ble_0")
		})
		if err := bleIface.Start(ctx); err != nil {
			log.Error().Err(err).Msg("ble reticulum interface start failed")
		} else {
			proc.RegisterPacketSender("ble_0", bleIface.Send)
			if ifaceReg != nil {
				ifaceReg.Register(bleRI)
			}
			log.Info().Str("adapter", cfg.BLEAdapter).Msg("ble reticulum interface started")
		}
	}

	// MQTT Reticulum interface — raw binary pub/sub for multi-bridge mesh.
	// When connecting to Hub's NATS broker (wss://), reuse mTLS certs from hub_connection.
	if cfg.MQTTReticulumBroker != "" {
		mqttRnsCfg := routing.MQTTInterfaceConfig{
			Name:      "mqtt_rns_0",
			BrokerURL: cfg.MQTTReticulumBroker,
			ClientID:  "meshsat-rns-" + cfg.BridgeID,
			Topic:     cfg.MQTTReticulumTopic,
			QoS:       1,
		}
		// Load mTLS config from hub_connection DB config (same certs as HubReporter).
		if raw, dbErr := db.GetSystemConfig("hub_connection"); dbErr == nil && raw != "" {
			var hc struct {
				TLSCertPEM  string `json:"tls_cert_pem"`
				TLSKeyPEM   string `json:"tls_key_pem"`
				TLSCAPEM    string `json:"tls_ca_pem"`
				TLSInsecure bool   `json:"tls_insecure"`
			}
			if json.Unmarshal([]byte(raw), &hc) == nil {
				if hc.TLSCertPEM != "" {
					mqttRnsCfg.TLSCertPEM = []byte(hc.TLSCertPEM)
				}
				if hc.TLSKeyPEM != "" {
					mqttRnsCfg.TLSKeyPEM = []byte(hc.TLSKeyPEM)
				}
				if hc.TLSCAPEM != "" {
					mqttRnsCfg.TLSCAPEM = []byte(hc.TLSCAPEM)
				}
				mqttRnsCfg.TLSInsecure = hc.TLSInsecure
			}
		}
		mqttIface := routing.NewMQTTInterface(mqttRnsCfg, func(packet []byte) {
			log.Debug().Int("size", len(packet)).Msg("mqtt_rns_0: received reticulum packet")
			proc.InjectReticulumPacket(packet, "mqtt_rns_0")
		})
		if err := mqttIface.Start(ctx); err != nil {
			log.Error().Err(err).Msg("mqtt reticulum interface start failed")
		} else {
			proc.RegisterPacketSender("mqtt_rns_0", mqttIface.Send)
			if ifaceReg != nil {
				ifaceReg.Register(routing.NewReticulumInterface("mqtt_rns_0", "mqtt", 65535, mqttIface.Send))
			}
			log.Info().Str("broker", cfg.MQTTReticulumBroker).Msg("mqtt reticulum interface started")
		}
	}

	// Transport Node — cross-interface packet forwarding via routing table.
	// This is what makes the bridge a Reticulum relay: packets received on one
	// interface (e.g. TCP) are forwarded to the best route (e.g. satellite).
	var transportNode *routing.TransportNode
	var pathFinder *routing.PathFinder
	if routingID != nil && ifaceReg != nil {
		transportNode = routing.NewTransportNode(routingID, 30*time.Minute, ifaceReg.Send)
		transportNode.Enable()
		transportNode.StartExpiry(ctx)
		proc.SetTransportNode(transportNode)

		pathFinder = routing.NewPathFinder(
			routing.DefaultPathFinderConfig(),
			transportNode.Router(),
			ifaceReg,
			routingID,
			ifaceReg.Send,
		)
		pathFinder.StartPruner(ctx)
		proc.SetPathFinder(pathFinder)

		log.Info().Msg("transport node enabled — cross-interface forwarding active")

		// Time Sync service (MESHSAT-410) — centralized clock correction.
		timeService := timesync.NewTimeService(db)
		timeService.SetEmitter(proc.Emit)
		timeService.LoadPersistedState()
		timeService.Start(ctx)

		// Mesh time consensus — exchanges timestamps over Reticulum links.
		tsConsensus := timesync.NewMeshTimeConsensus(timeService, routingID, func(data []byte) {
			proc.BroadcastRoutingPacket(data)
		})
		tsConsensus.Start(ctx)

		// Wire time sync dispatch into processor.
		proc.SetTimeSyncHandler(func(data []byte, sourceIface string) {
			if len(data) > 0 && data[0] == 0x14 {
				tsConsensus.HandleTimeSyncRequest(data, sourceIface)
			} else if len(data) > 0 && data[0] == 0x15 {
				tsConsensus.HandleTimeSyncResponse(data)
			}
		})

		log.Info().Msg("time sync service started with mesh consensus")

		// Load persisted floodable overrides from system_config
		if raw, dbErr := db.GetSystemConfig("reticulum_floodable_overrides"); dbErr == nil && raw != "" {
			var overrides map[string]bool
			if json.Unmarshal([]byte(raw), &overrides) == nil {
				for id, f := range overrides {
					if iface := ifaceReg.Get(id); iface != nil {
						iface.SetFloodable(f)
						log.Info().Str("iface", id).Bool("floodable", f).Msg("applied persisted floodable override")
					}
				}
			}
		}

		// Load persisted routing peers from system_config and connect dynamically
		if tcpIface != nil {
			if raw, dbErr := db.GetSystemConfig("reticulum_peers"); dbErr == nil && raw != "" {
				var peers []string
				if json.Unmarshal([]byte(raw), &peers) == nil {
					for _, addr := range peers {
						if err := tcpIface.AddPeer(ctx, addr); err != nil {
							log.Warn().Err(err).Str("peer", addr).Msg("failed to add persisted peer")
						}
					}
				}
			}
		}
	}

	// Dispatcher — structured delivery fan-out (v0.3.0 access rules)
	dispatcher := engine.NewDispatcher(db, registry, gwMgr, mesh)
	dispatcher.SetEmitter(proc.Emit)
	dispatcher.SetAccessEvaluator(accessEval)
	failoverResolver := engine.NewFailoverResolver(db, ifaceMgr)
	dispatcher.SetFailoverResolver(failoverResolver)
	if signingService != nil {
		dispatcher.SetSigningService(signingService)
	}
	if routingID != nil {
		dispatcher.SetRoutingIdentity(routingID)
	}
	transforms := engine.NewTransformPipeline()
	if cfg.LlamaZipAddr != "" {
		lzClient := compress.NewLlamaZipClient(cfg.LlamaZipAddr, time.Duration(cfg.LlamaZipTimeoutSec)*time.Second)
		if err := lzClient.Connect(ctx); err != nil {
			log.Warn().Err(err).Str("addr", cfg.LlamaZipAddr).Msg("llamazip sidecar not available (compression fallback: smaz2)")
		} else {
			transforms.SetLlamaZipClient(lzClient)
			defer lzClient.Close()
			log.Info().Str("addr", cfg.LlamaZipAddr).Msg("llamazip sidecar connected")
		}
	}
	dispatcher.SetTransformPipeline(transforms)
	// KeyResolver wired after keystore init below [MESHSAT-447]
	// DTN reassembly buffer (MESHSAT-408)
	reassemblyBuf := engine.NewReassemblyBuffer(5*time.Minute, 100)
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n := reassemblyBuf.Reap(); n > 0 {
					log.Info().Int("expired", n).Msg("DTN: reaped incomplete bundles")
				}
			}
		}
	}()
	dispatcher.SetFragmentManager(reassemblyBuf)

	// Wire Reticulum packet senders into dispatcher for HeMB bearer resolution.
	dispatcher.SetPacketSenderProvider(proc)

	// DTN custody manager (MESHSAT-408)
	custodyMgr := engine.NewCustodyManager(60 * time.Second)
	dispatcher.SetCustodyManager(custodyMgr)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n := custodyMgr.Reap(); n > 0 {
					log.Info().Int("expired", n).Msg("DTN: reaped expired custody offers")
				}
			}
		}
	}()

	// Wire custody packet handlers into processor
	proc.SetCustodyHandlers(
		// Custody offer handler: another node offers us custody of a payload.
		func(data []byte, sourceIface string) {
			offer, err := engine.UnmarshalCustodyOffer(data)
			if err != nil {
				log.Warn().Err(err).Msg("DTN: invalid custody offer")
				return
			}
			log.Info().
				Str("custody_id", fmt.Sprintf("%x", offer.CustodyID[:8])).
				Str("source", fmt.Sprintf("%x", offer.SourceHash[:8])).
				Int("payload", len(offer.Payload)).
				Msg("DTN: accepting custody of payload")
			// Accept: create a local delivery for the payload. Custody-
			// transferred bundles carry no precedence on the wire today;
			// pass "" so the schema default (Routine) wins.
			if dispatcher != nil {
				_, _, qErr := dispatcher.QueueDirectSend("mesh", string(offer.Payload), "")
				if qErr != nil {
					log.Warn().Err(qErr).Msg("DTN: failed to queue custody payload")
				}
			}
			// Send ACK back on the same interface (point-to-point)
			if routingID != nil && signingService != nil {
				ack := &engine.CustodyACK{
					CustodyID:    offer.CustodyID,
					AcceptorHash: routingID.DestHash(),
				}
				sigData := append(ack.CustodyID[:], ack.AcceptorHash[:]...)
				sig := signingService.Sign(sigData)
				copy(ack.Signature[:], sig)
				ackData := engine.MarshalCustodyACK(ack)
				if err := proc.SendReticulumPacketTo(sourceIface, ackData); err != nil {
					log.Warn().Err(err).Msg("DTN: failed to send custody ACK")
				} else {
					log.Info().Str("custody_id", fmt.Sprintf("%x", offer.CustodyID[:8])).
						Msg("DTN: custody ACK sent")
				}
			}
		},
		// Custody ACK handler: a relay accepted our custody offer.
		func(data []byte) {
			ack, err := engine.UnmarshalCustodyACK(data)
			if err != nil {
				log.Warn().Err(err).Msg("DTN: invalid custody ACK")
				return
			}
			if custodyMgr.HandleACK(ack) {
				log.Info().
					Str("custody_id", fmt.Sprintf("%x", ack.CustodyID[:8])).
					Str("acceptor", fmt.Sprintf("%x", ack.AcceptorHash[:8])).
					Msg("DTN: custody transfer confirmed")
			}
		},
	)
	log.Info().Msg("DTN custody + fragment managers wired")

	dispatcher.Start(ctx)
	proc.SetDispatcher(dispatcher)
	log.Info().Msg("dispatcher + delivery workers started")

	// Wire interface state changes to dispatcher worker lifecycle
	ifaceMgr.SetStateChangeCallback(func(ifaceID, channelType string, newState engine.InterfaceState) {
		switch newState {
		case engine.StateOnline:
			dispatcher.StartWorker(ctx, ifaceID, channelType)
		case engine.StateOffline, engine.StateError:
			dispatcher.StopWorker(ifaceID)
		}
	})

	// Signal recorder — persists satellite signal bar readings to DB.
	// Each transport is recorded independently with its own source key ("sbd" / "imt").
	// If only one modem exists, only one poll loop runs.
	var primaryProvider engine.SignalProvider = gwMgr // fallback: auto-select
	if sat != nil {
		primaryProvider = sat // direct SBD transport → writes source="sbd"
	}
	sigRecorder := engine.NewSignalRecorder(db, primaryProvider)
	if imtTransport != nil {
		sigRecorder.AddProvider(imtTransport, "imt")
	}
	sigRecorder.Start(ctx)

	// Cellular signal recorder (optional)
	var cellSigRecorder *engine.CellSignalRecorder
	if cell != nil {
		cellSigRecorder = engine.NewCellSignalRecorder(db, cell)
		cellSigRecorder.SetProcessor(proc) // forward events to SSE stream
		cellSigRecorder.Start(ctx)
	}

	// GPS reader — reads NMEA from u-blox GPS receiver (direct mode only)
	var gpsReader *transport.GPSReader
	if gpsExcludePorts != nil {
		gpsReader = transport.NewGPSReader("auto", db)
		gpsReader.SetExcludePortFuncs(gpsExcludePorts)
		go gpsReader.Start(ctx)
		log.Info().Msg("GPS reader started (auto-detect)")
	}

	// Spectrum monitor — RTL-SDR jamming detection (no CGO, uses rtl_power subprocess)
	var spectrumMon *spectrum.SpectrumMonitor
	rtlScanner := spectrum.NewRTLPowerScanner()
	if rtlScanner != nil {
		spectrumMon = spectrum.NewSpectrumMonitor(rtlScanner, spectrum.DefaultBands)
		if signingService != nil {
			spectrumMon.SetSigningService(signingService)
		}
		spectrumMon.Start(ctx)
		log.Info().Msg("spectrum monitor started (RTL-SDR detected)")
	} else {
		spectrumMon = spectrum.NewSpectrumMonitor(nil, spectrum.DefaultBands)
		// Narrow the disabled-reason log — the scanner returns nil
		// for either (a) no binary on PATH or (b) no dongle detected.
		// Check which so the log actually helps. [MESHSAT-509]
		if _, err := exec.LookPath("rtl_power_fftw"); err != nil {
			if _, err := exec.LookPath("rtl_power"); err != nil {
				log.Info().Msg("spectrum monitor disabled (no rtl_power/rtl_power_fftw binary)")
			}
		} else if !spectrum.DetectRTLSDR() {
			log.Info().Msg("spectrum monitor disabled (no RTL-SDR dongle detected on USB)")
		} else {
			log.Info().Msg("spectrum monitor disabled (unknown reason)")
		}
	}

	// Burst queue — satellite message batching for pass-based sends
	burstQueue := engine.NewBurstQueue(db, 10, 30*time.Minute)

	// API server
	srv := api.NewServer(db, mesh, proc, gwMgr)
	srv.SetBurstQueue(burstQueue)
	srv.SetSpectrumMonitor(spectrumMon)

	// Health scorer — composite health scores for interfaces (with jamming awareness)
	healthScorer := engine.NewHealthScorer(db)
	if spectrumMon != nil {
		healthScorer.SetSpectrumChecker(spectrumMon)
	}
	srv.SetHealthScorer(healthScorer)
	srv.SetAccessEvaluator(accessEval)
	srv.SetRegistry(registry)
	srv.SetTLEManager(tleMgr)
	srv.SetPassScheduler(gwMgr.GetPassScheduler())
	srv.SetCellTransport(cell)
	log.Info().Bool("cell_set", cell != nil).Msg("API server: cellTransport configured")
	srv.SetGPSReader(gpsReader)

	// ZigBee sensor router — fan out temp/humidity/battery/onoff readings to
	// TAK + hub + log per the device's routing config. Wired here (not at
	// gateway start) because gpsReader and the TAK gateway are constructed
	// later than the gateway manager. Devices without a routing row default
	// to TAK on, hub on, log on, mesh off. [MESHSAT-509]
	if zgw := gwMgr.GetZigBeeGateway(); zgw != nil {
		var gpsFn func() (float64, float64, bool)
		if gpsReader != nil {
			gpsFn = func() (float64, float64, bool) {
				st := gpsReader.GetStatus()
				return st.Lat, st.Lon, st.Fix
			}
		}
		zgw.SetSensorRouter(gateway.SensorRoutingDeps{
			DB:             db,
			TAK:            gwMgr.GetTAKGateway(),
			GPS:            gpsFn,
			CallsignPrefix: "MESHSAT",
			CoTStaleSec:    600,
		})
	}
	srv.SetInterfaceManager(ifaceMgr)
	if signingService != nil {
		srv.SetSigningService(signingService)
	}
	srv.SetDispatcher(dispatcher)
	srv.SetPaidRateLimit(cfg.PaidRateLimit)
	srv.SetAPIRateLimit(cfg.APIRateLimit)
	srv.SetWebHandler(webHandler(cfg.WebDir))
	if linkMgr != nil {
		srv.SetLinkManager(linkMgr)
	}
	if destTable != nil {
		srv.SetDestinationTable(destTable)
	}
	if routingID != nil {
		srv.SetRoutingIdentity(routingID)
	}
	if supervisor != nil {
		srv.SetDeviceSupervisor(supervisor)
	}
	if resourceXfer != nil {
		srv.SetResourceTransfer(resourceXfer)
	}
	if ifaceReg != nil {
		srv.SetInterfaceRegistry(ifaceReg)
	}
	if tcpIface != nil {
		srv.SetTCPInterface(tcpIface)
	}

	// Key store — cross-platform key exchange with QR bundles and envelope encryption
	var ks *keystore.KeyStore
	if routingID != nil {
		var ksErr error
		ks, ksErr = keystore.NewKeyStore(db, routingID, os.Getenv("MESHSAT_KEY_PASSPHRASE"))
		if ksErr != nil {
			log.Error().Err(ksErr).Msg("key store init failed — key exchange disabled")
		} else {
			srv.SetKeyStore(ks)
			srv.SetTransformPipeline(transforms) // [MESHSAT-447] SMS egress transforms
			transforms.SetKeyResolver(ks)        // [MESHSAT-447] key_ref → keystore lookup
			// Wire credential loader so MQTT gateways can load certs from DB
			gateway.SetCredentialLoader(&credentialLoaderAdapter{db: db, ks: ks})
			log.Info().Msg("key store initialized")

			// Wrap the Ed25519 signing private key at rest (or unwrap it
			// on a restart where the migration already ran). [MESHSAT-536]
			if signingService != nil {
				if err := signingService.LoadWithKeystore(ks); err != nil {
					log.Error().Err(err).Msg("signing service: failed to load with keystore — signing degraded")
				}
			}

			// Per-contact AES traffic keys: unwrap-on-demand via the
			// directory store. Transform specs may now use
			// key_ref="contact:<uuid>" to encrypt to a contact without
			// having to pick the bearer's channel address. [MESHSAT-537]
			dirStore := directory.NewSQLStore(db.DB)
			if cks := engine.NewContactKeyService(ks, dirStore); cks != nil {
				transforms.SetContactKeyResolver(cks)
				log.Info().Msg("contact key service initialized")
			}
			// SendToRecipient — contact-aware dispatch via the
			// directory.Store's Resolve/GetPolicy methods.
			// [MESHSAT-544 / S2-01]
			dispatcher.SetRecipientResolver(dirStore)
			log.Info().Msg("dispatcher: recipient resolver wired")
		}
	}

	// Credential expiry monitor — periodic check + SSE events for dashboard
	credMonitor := engine.NewCredentialMonitor(db)
	credMonitor.SetEmitter(proc.Emit)
	credMonitor.Start(ctx)
	srv.SetOnMOCallback(func(imei string) {
		if err := db.TouchDeviceLastSeen(imei); err != nil {
			log.Warn().Err(err).Str("imei", imei).Msg("failed to update device last_seen")
		}
	})

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      srv.Router(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // SSE needs no write timeout
		IdleTimeout:  60 * time.Second,
	}

	// Start event processor
	go func() {
		if err := proc.Run(ctx); err != nil {
			log.Error().Err(err).Msg("processor stopped with error")
		}
	}()

	// Start retention worker
	go engine.StartRetentionWorker(ctx, db, cfg.RetentionDays)

	// Periodic announce broadcasting — send on all online Reticulum interfaces
	if routingID != nil && cfg.AnnounceIntervalSec > 0 {
		go func() {
			broadcastAnnounce := func() {
				announce, aErr := routing.NewAnnounce(routingID, nil)
				if aErr != nil {
					return
				}
				data := announce.Marshal()
				if ifaceReg != nil {
					floodable := ifaceReg.Floodable()
					ids := make([]string, len(floodable))
					for i, f := range floodable {
						ids[i] = f.ID()
					}
					log.Info().Str("dest_hash", routingID.DestHashHex()).Int("size", len(data)).Strs("interfaces", ids).Msg("broadcasting routing announce")
					for _, iface := range floodable {
						if err := iface.Send(ctx, data); err != nil {
							log.Warn().Err(err).Str("iface", iface.ID()).Msg("failed to broadcast announce")
						}
					}
				} else if tcpIface != nil && tcpIface.IsOnline() {
					log.Info().Str("dest_hash", routingID.DestHashHex()).Int("size", len(data)).Msg("broadcasting routing announce via tcp")
					if err := tcpIface.Send(ctx, data); err != nil {
						log.Warn().Err(err).Msg("failed to broadcast announce via tcp")
					}
				}
			}
			interval := time.Duration(cfg.AnnounceIntervalSec) * time.Second
			broadcastAnnounce() // immediate on startup
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					broadcastAnnounce()
				}
			}
		}()
	}

	// Hub Reporter — bridge-to-hub uplink (MESHSAT-280)
	// DB-persisted config (from Settings > Routing > Hub) overrides env vars.
	hubURL, hubBridgeID := cfg.HubURL, cfg.BridgeID
	hubUsername, hubPassword := cfg.HubUsername, cfg.HubPassword
	var hubTLSCertPEM, hubTLSKeyPEM, hubTLSCAPEM []byte
	hubTLSInsecure := false
	if raw, dbErr := db.GetSystemConfig("hub_connection"); dbErr == nil && raw != "" {
		var hc struct {
			URL         string `json:"url"`
			BridgeID    string `json:"bridge_id"`
			Username    string `json:"username"`
			Password    string `json:"password"`
			TLSCertPEM  string `json:"tls_cert_pem"`
			TLSKeyPEM   string `json:"tls_key_pem"`
			TLSCAPEM    string `json:"tls_ca_pem"`
			TLSInsecure bool   `json:"tls_insecure"`
		}
		if json.Unmarshal([]byte(raw), &hc) == nil {
			if hc.URL != "" {
				hubURL = hc.URL
			}
			if hc.BridgeID != "" {
				hubBridgeID = hc.BridgeID
			}
			if hc.Username != "" {
				hubUsername = hc.Username
			}
			if hc.Password != "" {
				hubPassword = hc.Password
			}
			if hc.TLSCertPEM != "" {
				hubTLSCertPEM = []byte(hc.TLSCertPEM)
			}
			if hc.TLSKeyPEM != "" {
				hubTLSKeyPEM = []byte(hc.TLSKeyPEM)
			}
			if hc.TLSCAPEM != "" {
				hubTLSCAPEM = []byte(hc.TLSCAPEM)
			}
			hubTLSInsecure = hc.TLSInsecure
		}
	}

	var hubReporter *hubreporter.HubReporter
	if hubURL != "" {
		reporterCfg := hubreporter.ReporterConfig{
			HubURL:         hubURL,
			BridgeID:       hubBridgeID,
			Username:       hubUsername,
			Password:       hubPassword,
			TLSCert:        cfg.HubTLSCert, // file path fallback (env var)
			TLSKey:         cfg.HubTLSKey,  // file path fallback (env var)
			TLSCertPEM:     hubTLSCertPEM,  // inline PEM from DB (priority)
			TLSKeyPEM:      hubTLSKeyPEM,   // inline PEM from DB (priority)
			TLSCA:          cfg.HubTLSCA,   // file path fallback (env var)
			TLSCAPEM:       hubTLSCAPEM,    // inline PEM from DB (priority)
			TLSInsecure:    hubTLSInsecure,
			HealthInterval: time.Duration(cfg.HubHealthInterval) * time.Second,
		}
		birthFn := func() hubreporter.BridgeBirth {
			ifaces := []hubreporter.InterfaceInfo{}
			caps := []string{}
			if mesh != nil {
				meshStatus := "offline"
				if ms, err := mesh.GetStatus(ctx); err == nil && ms.Connected {
					meshStatus = "online"
				}
				ifaces = append(ifaces, hubreporter.InterfaceInfo{Name: "mesh_0", Type: "meshtastic", Status: meshStatus})
				caps = append(caps, "meshtastic")
			}
			for _, gs := range gwMgr.GetStatus() {
				status := "offline"
				if gs.Connected {
					status = "online"
				}
				ifaces = append(ifaces, hubreporter.InterfaceInfo{Name: gs.Type, Type: gs.Type, Status: status})
				caps = append(caps, gs.Type)
			}
			if routingID != nil {
				caps = append(caps, "reticulum")
			}
			hostname, _ := os.Hostname()
			if hostname == "" {
				hostname = hubBridgeID
			}
			birth := hubreporter.BridgeBirth{
				Protocol:     hubreporter.ProtocolVersion,
				BridgeID:     hubBridgeID,
				Version:      "0.20.0",
				Hostname:     hostname,
				Mode:         cfg.Mode,
				TenantID:     "default",
				Interfaces:   ifaces,
				Capabilities: caps,
				CoTType:      hubreporter.CoTBridge,
				CoTCallsign:  "MESHSAT-" + hubBridgeID,
				Timestamp:    time.Now().UTC(),
			}
			if routingID != nil {
				birth.Reticulum = &hubreporter.ReticulumInfo{
					IdentityHash:     routingID.DestHashHex(),
					TransportEnabled: true,
				}
			}
			return birth
		}
		healthFn := func() hubreporter.BridgeHealth {
			sys := sysinfo.Collect(cfg.DBPath)
			health := hubreporter.BridgeHealth{
				Protocol:  hubreporter.ProtocolVersion,
				BridgeID:  hubBridgeID,
				UptimeSec: int64(time.Since(startTime).Seconds()),
				CPUPct:    sys.CPUPct,
				MemPct:    sys.MemPct,
				DiskPct:   sys.DiskPct,
				Timestamp: time.Now().UTC(),
			}

			// Meshtastic — base transport, not in gateway manager
			if mesh != nil {
				meshStatus := "offline"
				if ms, err := mesh.GetStatus(ctx); err == nil && ms.Connected {
					meshStatus = "online"
				}
				ih := hubreporter.InterfaceHealth{
					Name: "meshtastic", Status: meshStatus,
				}
				if spectrumMon != nil && spectrumMon.IsJammed("mesh_0") {
					ih.SpectrumState = "jamming"
				}
				health.Interfaces = append(health.Interfaces, ih)
			}

			// Interface health — status from gateway manager
			for _, gs := range gwMgr.GetStatus() {
				ih := hubreporter.InterfaceHealth{Name: gs.Type, Status: "offline"}
				if gs.Connected {
					ih.Status = "online"
				}
				if spectrumMon != nil && spectrumMon.IsJammed(gs.InstanceID) {
					ih.SpectrumState = "jamming"
				}
				health.Interfaces = append(health.Interfaces, ih)
			}

			// Spectrum monitoring (RTL-SDR)
			if spectrumMon != nil && spectrumMon.Enabled() {
				for _, bs := range spectrumMon.Status() {
					health.Spectrum = append(health.Spectrum, hubreporter.SpectrumBandHealth{
						Band:        bs.Band,
						InterfaceID: bs.InterfaceID,
						State:       string(bs.State),
						PowerDB:     bs.PowerDB,
						FreqLow:     bs.FreqLow,
						FreqHigh:    bs.FreqHigh,
					})
				}
			}

			// Burst queue
			if burstQueue != nil {
				health.BurstQueue = &hubreporter.BurstQueueInfo{
					Pending: burstQueue.Pending(),
				}
			}

			// Reticulum stats
			if routingID != nil {
				rs := &hubreporter.ReticulumStats{}
				if transportNode != nil {
					rs.Routes = transportNode.RouteCount()
				}
				if linkMgr != nil {
					rs.Links = len(linkMgr.ActiveLinks())
				}
				if announceRelay != nil {
					rs.AnnouncesRelayed = announceRelay.RelayedCount()
				}
				health.Reticulum = rs
			}

			return health
		}
		hubReporter = hubreporter.NewHubReporter(reporterCfg, birthFn, healthFn)

		// Outbox for offline queuing
		outbox := hubreporter.NewOutbox(db.DB.DB, 10000, 7*24*time.Hour)
		hubReporter.SetOutbox(outbox)

		// Command handler — processes commands from the Hub (ping, send_mt, etc.)
		cmdHandler := hubreporter.NewCommandHandler(hubReporter, hubBridgeID, healthFn)
		cmdHandler.SetDeps(hubreporter.CommandDeps{
			SendText: func(ifaceID, text string) (int64, string, error) {
				// Hub commands carry no precedence yet; Phase 2
				// (MESHSAT-546) extends the command envelope.
				return dispatcher.QueueDirectSend(ifaceID, text, "")
			},
			FlushBurst: func(ctx context.Context) ([]byte, int, error) {
				return burstQueue.Flush(ctx)
			},
			BurstPending: func() int {
				return burstQueue.Pending()
			},
		})
		cmdHandler.SetCredentialStore(&bridgeCredentialStore{db: db})
		if ks != nil {
			cmdHandler.SetKeyStore(ks) // [MESHSAT-447] Hub-driven key rotation
		}
		// Hub-driven directory sync: Hub pushes signed Snapshot JSON
		// via directory_push; we verify against the pinned anchor
		// (stored in system_config) and replace the local directory
		// tables via a thin applier. [MESHSAT-540]
		cmdHandler.SetTrustAnchorStore(&bridgeDirectoryTrustStore{db: db})
		cmdHandler.SetDirectoryApplier(&bridgeDirectoryApplier{dir: directory.NewSQLStore(db.DB)})
		hubReporter.SetCommandHandler(cmdHandler)

		// TAK CoT relay: when Hub broadcasts TAK positions from OTS,
		// parse them and store in the bridge's position DB so they appear on the map.
		hubReporter.SetTAKCoTHandler(func(cotXML []byte) {
			ev, err := gateway.ParseCotEvent(cotXML)
			if err != nil {
				return
			}
			if ev.Point.Lat == 0 && ev.Point.Lon == 0 {
				return
			}
			if ev.Type == "t-x-c-t" || ev.Type == "t-x-c-t-r" {
				return
			}

			callsign := ev.UID
			if ev.Detail != nil && ev.Detail.Contact != nil && ev.Detail.Contact.Callsign != "" {
				callsign = ev.Detail.Contact.Callsign
			}

			// Store position in bridge DB
			pos := &database.Position{
				NodeID:    ev.UID,
				Latitude:  ev.Point.Lat,
				Longitude: ev.Point.Lon,
				Altitude:  int(ev.Point.Hae),
			}
			db.InsertPosition(pos) //nolint:errcheck

			// Publish to TAK event bus for the TakView event stream
			gateway.GlobalTakEventBus.Publish(gateway.CotEventToRecord(ev, "inbound"))

			log.Debug().Str("uid", ev.UID).Str("callsign", callsign).
				Float64("lat", ev.Point.Lat).Float64("lon", ev.Point.Lon).
				Msg("tak: CoT position stored from Hub relay")
		})

		if err := hubReporter.Start(ctx); err != nil {
			log.Error().Err(err).Msg("hub reporter start failed")
		} else {
			log.Info().Str("hub", hubURL).Str("bridge_id", hubBridgeID).Msg("hub reporter started")
		}
	}

	// Spectrum jamming alert relay: subscribe to state-transition events
	// from the RTL-SDR monitor and fan them out to (a) the TAK gateway so
	// all connected ATAK/WinTAK parties and the TAK server see a CoT
	// jamming track at this bridge's location, and (b) the hub reporter
	// so the central hub can cross-correlate jamming across bridges
	// (coordinated EW lights up the same band on multiple kits). The
	// dashboard waterfall consumes the same events via the SSE stream on
	// /api/spectrum/stream — it does not need a goroutine here.
	//
	// Scan events are not relayed (high volume, no alert value) — only
	// transitions. [MESHSAT-509 spectrum alerts]
	if spectrumMon != nil && spectrumMon.Enabled() {
		spectrumEvents, unsubSpectrum := spectrumMon.Subscribe()
		go func() {
			defer unsubSpectrum()
			callsign := "MESHSAT-" + hubBridgeID
			for {
				select {
				case <-ctx.Done():
					return
				case evt, ok := <-spectrumEvents:
					if !ok {
						return
					}
					if evt.Kind != spectrum.EventTransition {
						continue
					}
					// Resolve position lazily per-event so a GPS fix that
					// arrives mid-run gets picked up without a restart.
					var lat, lon float64
					if gpsReader != nil {
						if st := gpsReader.GetStatus(); st.Fix {
							lat, lon = st.Lat, st.Lon
						}
					}
					relay := spectrumMon.RelayTracker()

					// TAK CoT: hits the TAK server (all remote parties) and
					// also feeds the in-process TAK event bus used by the
					// dashboard CoT viewer.
					if takGw := gwMgr.GetTAKGateway(); takGw != nil {
						cotEv := gateway.BuildSpectrumJammingEvent(
							hubBridgeID, callsign, lat, lon,
							evt.Band, evt.Label, evt.FreqLow, evt.FreqHigh,
							string(evt.State), string(evt.OldState),
							evt.AvgDB, evt.MaxDB, evt.BaselineMean,
							600, // 10 min stale — covers the cooldown window
						)
						if err := takGw.SendCotEvent(cotEv); err != nil {
							log.Warn().Err(err).Str("band", evt.Band).
								Msg("spectrum-alert: TAK CoT send failed")
							relay.RecordFailure("tak_cot", err)
						} else {
							relay.RecordSuccess("tak_cot")
						}
						gateway.GlobalTakEventBus.Publish(
							gateway.CotEventToRecord(&cotEv, "outbound"))
					} else {
						relay.RecordFailure("tak_cot", fmt.Errorf("tak gateway not running"))
					}
					// Hub: QoS 1 + outbox-queued so the alert survives a
					// link drop (which is exactly the state a jammed kit
					// may find itself in).
					if hubReporter != nil {
						alert := hubreporter.SpectrumAlert{
							Band:           evt.Band,
							Label:          evt.Label,
							InterfaceID:    evt.InterfaceID,
							FreqLow:        evt.FreqLow,
							FreqHigh:       evt.FreqHigh,
							State:          string(evt.State),
							OldState:       string(evt.OldState),
							PowerDB:        evt.AvgDB,
							MaxPowerDB:     evt.MaxDB,
							BaselineMeanDB: evt.BaselineMean,
							BaselineStdDB:  evt.BaselineStd,
							Lat:            lat,
							Lon:            lon,
							Timestamp:      evt.Timestamp,
						}
						if err := hubReporter.PublishSpectrumAlert(alert); err != nil {
							log.Warn().Err(err).Str("band", evt.Band).
								Msg("spectrum-alert: hub publish failed")
							relay.RecordFailure("hub", err)
						} else {
							relay.RecordSuccess("hub")
						}
					}
					log.Warn().
						Str("band", evt.Band).
						Str("old", string(evt.OldState)).
						Str("new", string(evt.State)).
						Float64("power_db", evt.AvgDB).
						Float64("baseline_db", evt.BaselineMean).
						Msg("spectrum-alert: relayed to TAK + hub")
				}
			}
		}()
		log.Info().Msg("spectrum-alert: CoT + hub relay active")
	}

	// HeMB receive-side reassembly — always active when bond groups exist.
	// Decoded payloads are re-injected into the processor as regular messages.
	{
		groups, gerr := db.GetAllBondGroups()
		if gerr == nil && len(groups) > 0 {
			receiveBonder := hemb.NewBonder(hemb.Options{
				Bearers: nil, // receive-only — no send bearers needed
				DeliverFn: func(payload []byte) {
					log.Info().Int("bytes", len(payload)).Msg("hemb: reassembled payload delivered")
					// Re-inject decoded HeMB payload into access rules [MESHSAT-447]
					if dispatcher != nil {
						if n := dispatcher.DispatchAccess("hemb_0",
							rules.RouteMessage{Text: string(payload), From: "hemb"},
							payload); n > 0 {
							log.Info().Int("deliveries", n).
								Msg("hemb: decoded payload dispatched via access rules")
						}
					}
				},
				EventCh: hemb.GlobalEventBus.Channel(),
			})
			proc.SetHeMBBonder(receiveBonder)
			log.Info().Int("groups", len(groups)).Msg("hemb: receive-side reassembly bonder registered")
		}
	}

	// HeMB TUN adapter — creates a virtual network interface for IP-over-HeMB bonding.
	// Enabled by MESHSAT_HEMB_TUN env var (e.g., MESHSAT_HEMB_TUN=hemb0).
	// IP addressing must be configured externally (ip addr add ... dev hemb0).
	if tunName := os.Getenv("MESHSAT_HEMB_TUN"); tunName != "" {
		// Build bearer profiles from bond group config.
		groups, gerr := db.GetAllBondGroups()
		if gerr != nil || len(groups) == 0 {
			log.Warn().Str("tun", tunName).Msg("hemb: TUN requested but no bond groups configured — skipping")
		} else {
			// Use first bond group for TUN bearer set.
			members, merr := db.GetBondMembers(groups[0].ID)
			if merr != nil || len(members) == 0 {
				log.Warn().Str("group", groups[0].ID).Msg("hemb: bond group has no members — TUN skipped")
			} else {
				var bearers []hemb.BearerProfile
				for i, m := range members {
					ifaceID := m.InterfaceID
					bearers = append(bearers, hemb.BearerProfile{
						Index:       uint8(i),
						InterfaceID: ifaceID,
						ChannelType: ifaceID,
						MTU:         237,
						HeaderMode:  hemb.HeaderModeCompact,
						SendFn: func(_ context.Context, data []byte) error {
							// Route HeMB frames through the dispatcher's delivery pipeline.
							return dispatcher.ForwardHeMBFrame(ifaceID, data)
						},
					})
				}

				tunMTU := hemb.ComputeTUNMTU(bearers)
				tunDev, terr := hemb.OpenTUN(tunName, tunMTU)
				if terr != nil {
					log.Error().Err(terr).Str("tun", tunName).Msg("hemb: TUN device creation failed")
				} else {
					tunAdapter, tunBonder := hemb.NewTUNAdapter(tunDev, bearers, hemb.TUNConfig{
						Name:    tunName,
						MTU:     tunMTU,
						EventCh: hemb.GlobalEventBus.Channel(),
					})
					proc.SetHeMBBonder(tunBonder)
					go func() {
						if err := tunAdapter.Start(ctx); err != nil {
							log.Error().Err(err).Msg("hemb: TUN adapter stopped")
						}
					}()
					defer tunAdapter.Close()
					log.Info().Str("iface", tunDev.Name()).Int("mtu", tunMTU).Int("bearers", len(bearers)).
						Msg("hemb: TUN adapter started")
				}
			}
		}
	}

	// Wire restart function — sends SIGTERM to self, Docker restart policy brings us back.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	srv.SetRestartFunc(func() {
		log.Info().Msg("restart requested via API")
		sigCh <- syscall.SIGTERM
	})

	// Start HTTP server
	go func() {
		log.Info().Int("port", cfg.Port).Msg("HTTP server listening")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("HTTP server failed")
		}
	}()

	// Wait for shutdown signal
	sig := <-sigCh
	log.Info().Str("signal", sig.String()).Msg("shutting down")

	cancel() // Stop processor + retention + gateways

	// Drain dispatcher goroutines (delivery workers + reapers). Workers
	// read the cancelled ctx, finish any in-flight DB write, then exit.
	// Without this wait, the deferred db.Close() on return races those
	// writes and leaves SQLite WAL/SHM files behind (observed on
	// integration-test teardown — same failure mode in prod would
	// corrupt the next boot). 10 s ceiling prevents a stuck worker
	// from blocking shutdown indefinitely; at that point forced close
	// is preferable to hanging.
	drained := make(chan struct{})
	go func() { dispatcher.Wait(); close(drained) }()
	select {
	case <-drained:
		log.Info().Msg("dispatcher workers drained")
	case <-time.After(10 * time.Second):
		log.Warn().Msg("dispatcher drain timed out after 10s — forcing shutdown")
	}

	if hubReporter != nil {
		hubReporter.Stop()
	}
	sigRecorder.Stop()
	if cellSigRecorder != nil {
		cellSigRecorder.Stop()
	}
	ifaceMgr.Stop()
	tleMgr.Stop()
	gwMgr.Stop()
	if tcpIface != nil {
		tcpIface.Stop()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("HTTP server shutdown error")
	}

	log.Info().Msg("MeshSat stopped")
}

// credentialLoaderAdapter implements gateway.CredentialLoader by reading from
// the credential cache DB and decrypting with the keystore master key.
type credentialLoaderAdapter struct {
	db *database.DB
	ks *keystore.KeyStore
}

func (a *credentialLoaderAdapter) LoadCredentialPEM(credentialID string) (caCertPEM, clientCertPEM, clientKeyPEM []byte, err error) {
	row, err := a.db.GetCredentialCache(credentialID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("credential %s not found: %w", credentialID, err)
	}

	decrypted, err := a.ks.UnwrapData(row.EncryptedData)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("decrypt credential %s: %w", credentialID, err)
	}

	// Parse JSON: {"ca_cert_pem":"...","client_cert_pem":"...","client_key_pem":"..."}
	var bundle struct {
		CACertPEM     string `json:"ca_cert_pem"`
		ClientCertPEM string `json:"client_cert_pem"`
		ClientKeyPEM  string `json:"client_key_pem"`
	}
	if err := json.Unmarshal(decrypted, &bundle); err != nil {
		return nil, nil, nil, fmt.Errorf("parse credential JSON: %w", err)
	}

	return []byte(bundle.CACertPEM), []byte(bundle.ClientCertPEM), []byte(bundle.ClientKeyPEM), nil
}

// bridgeCredentialStore implements hubreporter.CredentialStore for Hub-pushed credentials.
type bridgeCredentialStore struct {
	db *database.DB
}

func (s *bridgeCredentialStore) StoreCredential(id, provider, name, credType string, encryptedData []byte, certNotAfter, certFingerprint string, version int) error {
	row := &database.CredentialCacheRow{
		ID:              id,
		Provider:        provider,
		Name:            name,
		CredType:        credType,
		EncryptedData:   encryptedData,
		CertNotAfter:    certNotAfter,
		CertFingerprint: certFingerprint,
		Version:         version,
		Source:          "hub",
	}
	return s.db.InsertCredentialCache(row)
}

func (s *bridgeCredentialStore) RemoveCredential(id string) error {
	return s.db.DeleteCredentialCache(id)
}

// bridgeDirectoryTrustStore persists the Hub's directory-signing
// public key (PKIX DER) in system_config under a well-known key.
// Hex-encoded so existing system_config tooling (plain TEXT column)
// round-trips cleanly. [MESHSAT-540]
type bridgeDirectoryTrustStore struct {
	db *database.DB
}

const hubDirectoryTrustAnchorKey = "hub_directory_signing_pubkey"

func (s *bridgeDirectoryTrustStore) SetDirectoryTrustAnchor(pubkey []byte) error {
	return s.db.SetSystemConfig(hubDirectoryTrustAnchorKey, hex.EncodeToString(pubkey))
}

func (s *bridgeDirectoryTrustStore) GetDirectoryTrustAnchor() ([]byte, error) {
	v, err := s.db.GetSystemConfig(hubDirectoryTrustAnchorKey)
	if err != nil || v == "" {
		return nil, err
	}
	return hex.DecodeString(v)
}

// bridgeDirectoryApplier turns a verified Hub snapshot into writes
// against the bridge's internal/directory SQLStore. Contacts, groups,
// and policies are upserted by ID; any rows not in the snapshot are
// left in place (delta semantics — Hub must emit a follow-up revoke
// command to clear a removed contact). [MESHSAT-540]
type bridgeDirectoryApplier struct {
	dir *directory.SQLStore
}

func (a *bridgeDirectoryApplier) ApplySnapshot(ctx context.Context, snap *hubreporter.DirectorySnapshot) error {
	if snap == nil {
		return fmt.Errorf("nil snapshot")
	}
	for i := range snap.Contacts {
		src := &snap.Contacts[i]
		c := &directory.Contact{
			ID:              src.ID,
			TenantID:        src.TenantID,
			DisplayName:     src.DisplayName,
			GivenName:       src.GivenName,
			FamilyName:      src.FamilyName,
			Org:             src.Org,
			Role:            src.Role,
			Team:            src.Team,
			SIDC:            src.SIDC,
			Notes:           src.Notes,
			TrustLevel:      directory.TrustLevel(src.TrustLevel),
			TrustVerifiedBy: "",
			HubVersion:      src.HubVersion,
			HubEtag:         src.HubEtag,
			Origin:          directory.Origin(src.Origin),
			CreatedAt:       src.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
			UpdatedAt:       src.UpdatedAt.UTC().Format("2006-01-02 15:04:05"),
		}
		if src.TrustVerifiedAt != nil {
			t := src.TrustVerifiedAt.UTC().Format("2006-01-02 15:04:05")
			c.TrustVerifiedAt = &t
		}
		if err := a.dir.CreateContact(ctx, c); err != nil {
			// On ErrConflict the contact already exists — update instead.
			if errors.Is(err, directory.ErrConflict) {
				if uerr := a.dir.UpdateContact(ctx, c); uerr != nil {
					return fmt.Errorf("update contact %s: %w", c.ID, uerr)
				}
			} else {
				return fmt.Errorf("create contact %s: %w", c.ID, err)
			}
		}
		// Replace addresses atomically by deleting existing and
		// inserting the snapshot set.
		existing, _ := a.dir.ListAddresses(ctx, c.ID)
		for _, old := range existing {
			_ = a.dir.DeleteAddress(ctx, old.ID)
		}
		for j := range src.Addresses {
			sa := &src.Addresses[j]
			addr := &directory.Address{
				ID:           sa.ID,
				ContactID:    c.ID,
				Kind:         directory.Kind(sa.Kind),
				Value:        sa.Value,
				Subvalue:     sa.Subvalue,
				Label:        sa.Label,
				PrimaryRank:  sa.PrimaryRank,
				Verified:     sa.Verified,
				BearerHint:   sa.BearerHint,
				MaxCostCents: sa.MaxCostCents,
			}
			if err := a.dir.AddAddress(ctx, addr); err != nil && !errors.Is(err, directory.ErrConflict) {
				return fmt.Errorf("add address %s: %w", addr.Value, err)
			}
		}
	}
	return nil
}
