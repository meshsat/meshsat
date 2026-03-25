package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
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
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/hubreporter"
	"meshsat/internal/routing"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
	"runtime"
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
	var astro transport.AstrocastTransport
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
		astroPort := "supervisor"
		if cfg.AstrocastPort != "" && cfg.AstrocastPort != "auto" {
			astroPort = cfg.AstrocastPort
		}

		directMesh := transport.NewDirectMeshTransport(meshPort)
		directMesh.SetWatchdogMinutes(cfg.MeshWatchdogMin)
		mesh = directMesh

		directIMT := transport.NewDirectIMTTransport(imtPort)
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
		cell = directCell

		directAstro := transport.NewDirectAstrocastTransport(astroPort)
		astro = directAstro

		// DeviceSupervisor: replaces one-shot auto-detect with continuous
		// two-tier polling (30s port scan + 15s reconciliation).
		supervisor = transport.NewDeviceSupervisor()

		// Register explicit port overrides from env vars
		supervisor.SetExplicitPort(transport.RoleMeshtastic, cfg.MeshtasticPort)
		supervisor.SetExplicitPort(transport.RoleIridium9704, cfg.IMTPort)
		supervisor.SetExplicitPort(transport.RoleIridium9603, cfg.IridiumPort)
		supervisor.SetExplicitPort(transport.RoleCellular, cfg.CellularPort)
		supervisor.SetExplicitPort(transport.RoleAstrocast, cfg.AstrocastPort)
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

		supervisor.SetCallbacks(transport.RoleAstrocast, &transport.DriverCallbacks{
			InstanceID: "astrocast_0",
			OnPortFound: func(port string) {
				directAstro.SetPort(port)
				log.Info().Str("port", port).Msg("supervisor: astronode port assigned")
			},
			OnPortLost: func(port string) {
				directAstro.Close()
				log.Warn().Str("port", port).Msg("supervisor: astronode port lost")
			},
			HasPort: func() bool { return directAstro.GetPort() != "" && directAstro.GetPort() != "supervisor" },
		})

		supervisor.Start()
		defer supervisor.Stop()

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
	if astro != nil {
		defer astro.Close()
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
	if astro != nil {
		gwMgr.SetAstrocastTransport(astro)
	}

	// TLE manager — daily Celestrak TLE refresh + SGP4 pass prediction
	// Created early so it's available to the gateway manager for pass scheduling
	tleMgr := engine.NewTLEManager(db)
	tleMgr.Start(ctx)

	// Astrocast TLE manager — daily Celestrak refresh for Astrocast LEO constellation
	astroTleMgr := engine.NewAstrocastTLEManager(db)
	astroTleMgr.Start(ctx)

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
	var routingID *routing.Identity
	var linkMgr *routing.LinkManager
	var destTable *routing.DestinationTable
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
		announceRelay := routing.NewAnnounceRelay(relayConfig, destTable, func(data []byte, announce *routing.Announce) {
			log.Debug().Str("dest_hash", routingID.DestHashHex()).Int("hops", int(announce.HopCount)).Msg("relaying announce")
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

		log.Info().Str("dest_hash", routingID.DestHashHex()).Msg("routing subsystem initialized")
	}

	// TCP/HDLC — RNS-compatible Reticulum interface over TCP
	var tcpIface *routing.TCPInterface
	if cfg.TCPListenAddr != "" || cfg.TCPConnectAddr != "" {
		tcpIface = routing.NewTCPInterface(routing.TCPInterfaceConfig{
			Name:              "tcp_0",
			ListenAddr:        cfg.TCPListenAddr,
			ConnectAddr:       cfg.TCPConnectAddr,
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
			// Register TCP as a packet sender so the processor can route
			// link proofs and data packets back to TCP peers.
			proc.RegisterPacketSender("tcp_0", tcpIface.Send)
			log.Info().Str("listen", cfg.TCPListenAddr).Str("connect", cfg.TCPConnectAddr).Msg("tcp reticulum interface started")
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

	// Signal recorder — persists Iridium signal bar readings to DB.
	// Uses gwManager.GetIridiumSignalFast which auto-selects the active transport
	// (9704 IMT if connected, otherwise 9603 SBD). This ensures the recorder works
	// regardless of which modem is physically present.
	sigRecorder := engine.NewSignalRecorder(db, gwMgr)
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

	// API server
	srv := api.NewServer(db, mesh, proc, gwMgr)
	srv.SetAccessEvaluator(accessEval)
	srv.SetRegistry(registry)
	srv.SetTLEManager(tleMgr)
	srv.SetAstrocastTLEManager(astroTleMgr)
	srv.SetPassScheduler(gwMgr.GetPassScheduler())
	srv.SetCellTransport(cell)
	log.Info().Bool("cell_set", cell != nil).Msg("API server: cellTransport configured")
	srv.SetGPSReader(gpsReader)
	srv.SetInterfaceManager(ifaceMgr)
	if signingService != nil {
		srv.SetSigningService(signingService)
	}
	srv.SetDispatcher(dispatcher)
	srv.SetPaidRateLimit(cfg.PaidRateLimit)
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

	// Periodic announce broadcasting
	if routingID != nil && cfg.AnnounceIntervalSec > 0 {
		go func() {
			broadcastAnnounce := func() {
				announce, aErr := routing.NewAnnounce(routingID, nil)
				if aErr != nil {
					return
				}
				data := announce.Marshal()
				log.Info().Str("dest_hash", routingID.DestHashHex()).Int("size", len(data)).Msg("broadcasting routing announce")
				if tcpIface != nil && tcpIface.IsOnline() {
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
	var hubReporter *hubreporter.HubReporter
	if cfg.HubURL != "" {
		reporterCfg := hubreporter.ReporterConfig{
			HubURL:         cfg.HubURL,
			BridgeID:       cfg.BridgeID,
			Username:       cfg.HubUsername,
			Password:       cfg.HubPassword,
			TLSCert:        cfg.HubTLSCert,
			TLSKey:         cfg.HubTLSKey,
			HealthInterval: time.Duration(cfg.HubHealthInterval) * time.Second,
		}
		birthFn := func() hubreporter.BridgeBirth {
			ifaces := []hubreporter.InterfaceInfo{}
			caps := []string{}
			if mesh != nil {
				ifaces = append(ifaces, hubreporter.InterfaceInfo{Name: "mesh_0", Type: "meshtastic", Status: "online"})
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
			birth := hubreporter.BridgeBirth{
				Protocol:     hubreporter.ProtocolVersion,
				BridgeID:     cfg.BridgeID,
				Version:      "0.18.0",
				Hostname:     cfg.BridgeID,
				Mode:         cfg.Mode,
				TenantID:     "default",
				Interfaces:   ifaces,
				Capabilities: caps,
				CoTType:      hubreporter.CoTBridge,
				CoTCallsign:  "MESHSAT-" + cfg.BridgeID,
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
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			health := hubreporter.BridgeHealth{
				Protocol:  hubreporter.ProtocolVersion,
				BridgeID:  cfg.BridgeID,
				UptimeSec: int64(time.Since(startTime).Seconds()),
				MemPct:    float64(m.Alloc) / float64(m.Sys) * 100,
				Timestamp: time.Now().UTC(),
			}
			return health
		}
		hubReporter = hubreporter.NewHubReporter(reporterCfg, birthFn, healthFn)

		// Outbox for offline queuing
		outbox := hubreporter.NewOutbox(db.DB.DB, 10000, 7*24*time.Hour)
		hubReporter.SetOutbox(outbox)

		if err := hubReporter.Start(ctx); err != nil {
			log.Error().Err(err).Msg("hub reporter start failed")
		} else {
			log.Info().Str("hub", cfg.HubURL).Str("bridge_id", cfg.BridgeID).Msg("hub reporter started")
		}
	}

	// Start HTTP server
	go func() {
		log.Info().Int("port", cfg.Port).Msg("HTTP server listening")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("HTTP server failed")
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Info().Str("signal", sig.String()).Msg("shutting down")

	cancel() // Stop processor + retention + gateways
	if hubReporter != nil {
		hubReporter.Stop()
	}
	sigRecorder.Stop()
	if cellSigRecorder != nil {
		cellSigRecorder.Stop()
	}
	ifaceMgr.Stop()
	tleMgr.Stop()
	astroTleMgr.Stop()
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
