package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"time"

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
)

// App holds all initialized MeshSat components. Extracted from main() to
// enable unit testing of the wiring and lifecycle logic.
type App struct {
	DB             *database.DB
	Config         *config.Config
	Registry       *channel.Registry
	InterfaceMgr   *engine.InterfaceManager
	AccessEval     *rules.AccessEvaluator
	Processor      *engine.Processor
	GatewayMgr     *gateway.Manager
	TLEMgr         *engine.TLEManager
	AstroTLEMgr    *engine.AstrocastTLEManager
	Dispatcher     *engine.Dispatcher
	Signing        *engine.SigningService
	Transforms     *engine.TransformPipeline
	Deduplicator   *dedup.Deduplicator
	SignalRecorder *engine.SignalRecorder
	CellRecorder   *engine.CellSignalRecorder
	GPSReader      *transport.GPSReader
	Server         *api.Server
	HTTPServer     *http.Server

	// Routing (v0.2.0 — Reticulum-inspired identity + announce + link layer)
	RoutingIdentity *routing.Identity
	DestTable       *routing.DestinationTable
	AnnounceRelay   *routing.AnnounceRelay
	LinkMgr         *routing.LinkManager
	BWLimiter       *routing.AnnounceBandwidthLimiter
	Keepalive       *routing.LinkKeepalive
	TransportNode   *routing.TransportNode
	IfaceRegistry   *routing.InterfaceRegistry
	PathFinder      *routing.PathFinder
	TCPIface        *routing.TCPInterface

	// Hub uplink (optional — connects bridge to MeshSat Hub)
	HubReporter  *hubreporter.HubReporter
	HubInventory *hubreporter.DeviceInventory
	HubEventTap  *hubreporter.EventTap

	// Transports (set before calling Setup)
	Mesh   transport.MeshTransport
	Sat    transport.SatTransport
	IMTSat transport.SatTransport // optional IMT (9704) transport for coexistence
	Cell   transport.CellTransport
	Astro  transport.AstrocastTransport

	// Optional: GPS exclude port funcs (direct mode only)
	GPSExcludePorts []func() string

	// Device supervisor (direct mode only — continuous USB discovery)
	DevSupervisor *transport.DeviceSupervisor
	BurstQueue    *engine.BurstQueue

	// Cleanup funcs for llamazip etc.
	cleanups []func()
}

// Setup initializes all components and wires them together.
// Transports (Mesh, Sat, Cell, Astro) must be set on the App before calling.
// Returns an error if critical initialization fails.
func (a *App) Setup(ctx context.Context) error {
	cfg := a.Config
	db := a.DB

	// Deduplicator (in-memory, composite key, 10min TTL, 10k max)
	a.Deduplicator = dedup.New(10*time.Minute, 10000)
	a.Deduplicator.StartPruner(ctx)

	// Channel registry
	a.Registry = channel.NewRegistry()
	channel.RegisterDefaults(a.Registry)

	// Interface manager
	a.InterfaceMgr = engine.NewInterfaceManager(db)
	if err := a.InterfaceMgr.Start(ctx); err != nil {
		log.Error().Err(err).Msg("interface manager start failed")
	}

	// Access rule evaluator
	a.AccessEval = rules.NewAccessEvaluator(db)
	if err := a.AccessEval.ReloadFromDB(); err != nil {
		log.Warn().Err(err).Msg("failed to load access rules (table may not exist yet)")
	}

	// Processor
	a.Processor = engine.NewProcessor(db, a.Mesh)
	a.Processor.SetDeduplicator(a.Deduplicator)

	// Gateway manager
	a.GatewayMgr = gateway.NewManager(db, a.Sat)
	if a.IMTSat != nil {
		a.GatewayMgr.SetIMTTransport(a.IMTSat)
	}
	if a.Cell != nil {
		a.GatewayMgr.SetCellTransport(a.Cell)
	}
	if a.Astro != nil {
		a.GatewayMgr.SetAstrocastTransport(a.Astro)
	}

	// TLE managers
	a.TLEMgr = engine.NewTLEManager(db)
	a.TLEMgr.Start(ctx)

	a.AstroTLEMgr = engine.NewAstrocastTLEManager(db)
	a.AstroTLEMgr.Start(ctx)

	// Wire TLE into gateway manager
	a.GatewayMgr.SetPassPredictor(&tleAdapter{a.TLEMgr})

	// Gateway receiver + event callbacks
	a.GatewayMgr.SetReceiverStartFunc(a.Processor.StartGatewayReceiver)
	a.GatewayMgr.SetEventEmitFunc(func(eventType, message string) {
		a.Processor.Emit(transport.MeshEvent{
			Type:    eventType,
			Message: message,
			Time:    time.Now().UTC().Format(time.RFC3339),
		})
	})

	// Node name resolver
	mesh := a.Mesh
	a.GatewayMgr.SetNodeNameResolver(func(nodeID uint32) string {
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

	// Start gateway manager
	if err := a.GatewayMgr.Start(ctx); err != nil {
		log.Error().Err(err).Msg("gateway manager start failed")
	}

	// Wire device supervisor events into gateway manager for hot-swap support
	if a.DevSupervisor != nil {
		a.GatewayMgr.WatchDeviceEvents(ctx, a.DevSupervisor)
	}

	a.Processor.SetGatewayProvider(a.GatewayMgr)

	// Signing service
	if ss, err := engine.NewSigningService(db); err != nil {
		log.Warn().Err(err).Msg("signing service init failed - non-repudiation disabled")
	} else {
		a.Signing = ss
	}

	// Routing identity — load or generate Ed25519 + X25519 keypair, persist via system_config
	routingID, err := routing.NewIdentity(db)
	if err != nil {
		log.Error().Err(err).Msg("routing identity init failed - routing disabled")
	} else {
		a.RoutingIdentity = routingID

		// Destination table — in-memory + DB-persisted registry of known remote identities
		a.DestTable = routing.NewDestinationTable(db)
		if err := a.DestTable.LoadFromDB(); err != nil {
			log.Warn().Err(err).Msg("failed to load routing destinations from DB")
		}

		// Announce relay — dedup + hop-count enforcement + delayed retransmit
		relayConfig := routing.DefaultRelayConfig()
		a.AnnounceRelay = routing.NewAnnounceRelay(relayConfig, a.DestTable, func(data []byte, announce *routing.Announce, sourceIface string) {
			log.Info().Str("dest_hash", routingID.DestHashHex()).Int("hops", int(announce.HopCount)).Int("size", len(data)).Msg("relaying announce via mesh")
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := mesh.SendRaw(ctx, transport.RawRequest{
				PortNum: 256, // PRIVATE_APP
				Payload: base64Encode(data),
			}); err != nil {
				log.Warn().Err(err).Msg("failed to relay announce")
			}
		})
		a.AnnounceRelay.RegisterLocal(routingID.DestHash())
		a.AnnounceRelay.StartPruner(ctx)

		// Bandwidth limiter — token bucket per interface at 2% of channel BW
		a.BWLimiter = routing.NewAnnounceBandwidthLimiter()
		for _, chID := range a.Registry.IDs() {
			a.BWLimiter.SetDefaultBandwidth(chID, chID)
		}

		// Link manager — ECDH 3-packet handshake, AES-256-GCM encryption
		a.LinkMgr = routing.NewLinkManager(routingID)

		// Keepalive — 18s heartbeat, 60s timeout for active links
		a.Keepalive = routing.NewLinkKeepalive(a.LinkMgr, func(linkID [routing.LinkIDLen]byte, data []byte) {
			kaCtx, kaCancel := context.WithTimeout(ctx, 10*time.Second)
			defer kaCancel()
			if err := mesh.SendRaw(kaCtx, transport.RawRequest{
				PortNum: 256, // PRIVATE_APP
				Payload: base64Encode(data),
			}); err != nil {
				log.Warn().Err(err).Msg("failed to send keepalive")
			}
		})
		a.Keepalive.Start(ctx)

		// Interface registry — wraps transports as Reticulum interfaces
		a.IfaceRegistry = routing.NewInterfaceRegistry()
		ifaceReg := a.IfaceRegistry

		// LoRa/Meshtastic — free, ~230 byte MTU (SF7), PRIVATE_APP portnum 256
		ifaceReg.Register(routing.NewReticulumInterface("mesh_0", "mesh", 230, func(fwdCtx context.Context, packet []byte) error {
			return mesh.SendRaw(fwdCtx, transport.RawRequest{
				PortNum: 256, // PRIVATE_APP
				Payload: base64Encode(packet),
			})
		}))

		// Iridium SBD — $0.05/msg, 340 byte MO MTU, raw binary via SBD
		if a.Sat != nil {
			iridiumMTU := 340
			if a.IMTSat != nil {
				// If IMT is the primary sat transport, register with 100KB MTU
				iridiumMTU = 100000
			}
			ifaceReg.Register(routing.NewReticulumInterface("iridium_0", "iridium", iridiumMTU, func(fwdCtx context.Context, packet []byte) error {
				_, err := a.Sat.Send(fwdCtx, packet)
				return err
			}))
		}

		// Iridium IMT — separate interface for 9704 when coexisting with 9603
		if a.IMTSat != nil && a.IMTSat != a.Sat {
			ifaceReg.Register(routing.NewReticulumInterface("iridium_imt_0", "iridium_imt", 100000, func(fwdCtx context.Context, packet []byte) error {
				_, err := a.IMTSat.Send(fwdCtx, packet)
				return err
			}))
		}

		// Astrocast — $0.01/msg, ~200 byte uplink MTU, raw binary
		if a.Astro != nil {
			ifaceReg.Register(routing.NewReticulumInterface("astrocast_0", "astrocast", 200, func(fwdCtx context.Context, packet []byte) error {
				_, err := a.Astro.Send(fwdCtx, packet)
				return err
			}))
		}

		// TCP/HDLC — RNS-compatible Reticulum interface over TCP (free, ~64KB MTU)
		if cfg.TCPListenAddr != "" || cfg.TCPConnectAddr != "" {
			tcpIface := routing.NewTCPInterface(routing.TCPInterfaceConfig{
				Name:              "tcp_0",
				ListenAddr:        cfg.TCPListenAddr,
				ConnectAddr:       cfg.TCPConnectAddr,
				Reconnect:         true,
				ReconnectInterval: 10 * time.Second,
			}, func(packet []byte) {
				// Feed received packets into the routing subsystem
				a.Processor.InjectReticulumPacket(packet, "tcp_0")
			})
			if err := tcpIface.Start(ctx); err != nil {
				log.Error().Err(err).Msg("tcp interface start failed")
			} else {
				a.TCPIface = tcpIface
				ifaceReg.Register(routing.NewReticulumInterface("tcp_0", "tcp", 65535, tcpIface.Send))
				log.Info().Str("listen", cfg.TCPListenAddr).Str("connect", cfg.TCPConnectAddr).Msg("tcp reticulum interface started")
			}
		}

		// Cellular SMS — text-only, needs base64 wrapper (not registered as raw binary interface)
		// APRS/AX.25 — text-only APRS format (needs bidirectional binary API, deferred)
		// ZigBee — raw binary capable but managed by GatewayManager (registered dynamically)
		// MQTT — PublishRaw() exists, registered dynamically when gateway starts

		// Transport Node — cross-interface packet forwarding via routing table
		a.TransportNode = routing.NewTransportNode(routingID, 30*time.Minute, ifaceReg.Send)
		a.TransportNode.Enable()
		a.TransportNode.StartExpiry(ctx)

		// Path discovery — flooding-based route resolution for unknown destinations
		a.PathFinder = routing.NewPathFinder(
			routing.DefaultPathFinderConfig(),
			a.TransportNode.Router(),
			a.IfaceRegistry,
			routingID,
			ifaceReg.Send,
		)
		a.PathFinder.StartPruner(ctx)

		log.Info().Str("dest_hash", routingID.DestHashHex()).Msg("routing subsystem initialized (transport node enabled)")
	}

	// Wire routing into the processor event loop so incoming PRIVATE_APP
	// packets (announces, link handshakes, keepalives) are handled.
	if a.AnnounceRelay != nil {
		a.Processor.SetRouting(a.AnnounceRelay, a.LinkMgr, a.Keepalive, a.DestTable)
	}
	if a.TransportNode != nil {
		a.Processor.SetTransportNode(a.TransportNode)
	}
	if a.PathFinder != nil {
		a.Processor.SetPathFinder(a.PathFinder)
	}

	// Dispatcher
	a.Dispatcher = engine.NewDispatcher(db, a.Registry, a.GatewayMgr, a.Mesh)
	a.Dispatcher.SetEmitter(a.Processor.Emit)
	a.Dispatcher.SetAccessEvaluator(a.AccessEval)
	failoverResolver := engine.NewFailoverResolver(db, a.InterfaceMgr)
	a.Dispatcher.SetFailoverResolver(failoverResolver)
	if a.Signing != nil {
		a.Dispatcher.SetSigningService(a.Signing)
	}
	if a.RoutingIdentity != nil {
		a.Dispatcher.SetRoutingIdentity(a.RoutingIdentity)
	}
	a.Transforms = engine.NewTransformPipeline()
	if cfg.LlamaZipAddr != "" {
		lzClient := compress.NewLlamaZipClient(cfg.LlamaZipAddr, time.Duration(cfg.LlamaZipTimeoutSec)*time.Second)
		if err := lzClient.Connect(ctx); err != nil {
			log.Warn().Err(err).Str("addr", cfg.LlamaZipAddr).Msg("llamazip sidecar not available (compression fallback: smaz2)")
		} else {
			a.Transforms.SetLlamaZipClient(lzClient)
			a.cleanups = append(a.cleanups, func() { lzClient.Close() })
		}
	}
	if cfg.MSVQSCAddr != "" {
		msvqscClient := compress.NewMSVQSCClient(cfg.MSVQSCAddr, time.Duration(cfg.MSVQSCTimeoutSec)*time.Second)
		if err := msvqscClient.Connect(ctx); err != nil {
			log.Warn().Err(err).Str("addr", cfg.MSVQSCAddr).Msg("msvqsc sidecar not available (lossy compression disabled)")
		} else {
			a.Transforms.SetMSVQSCClient(msvqscClient)
			a.cleanups = append(a.cleanups, func() { msvqscClient.Close() })
		}
	}
	if cfg.MSVQSCCodebook != "" {
		cbData, err := os.ReadFile(cfg.MSVQSCCodebook)
		if err != nil {
			log.Warn().Err(err).Str("path", cfg.MSVQSCCodebook).Msg("msvqsc codebook not found (pure-Go decode disabled)")
		} else {
			cb, err := compress.LoadCodebook(cbData)
			if err != nil {
				log.Warn().Err(err).Msg("msvqsc codebook parse failed")
			} else {
				// Try to load corpus index from same directory
				corpusPath := cfg.MSVQSCCodebook[:len(cfg.MSVQSCCodebook)-len("codebook_v1.bin")] + "corpus_index.bin"
				if ciData, err := os.ReadFile(corpusPath); err == nil {
					if err := cb.LoadCorpusIndex(ciData); err != nil {
						log.Warn().Err(err).Msg("msvqsc corpus index parse failed")
					}
				}
				a.Transforms.SetCodebook(cb)
				log.Info().Int("stages", cb.Stages).Int("K", cb.K).Int("corpus", len(cb.Corpus)).Msg("msvqsc codebook loaded (pure-Go decode enabled)")
			}
		}
	}
	a.Dispatcher.SetTransformPipeline(a.Transforms)
	a.Dispatcher.Start(ctx)
	a.Processor.SetDispatcher(a.Dispatcher)

	// Wire interface state changes to dispatcher worker lifecycle
	a.InterfaceMgr.SetStateChangeCallback(func(ifaceID, channelType string, newState engine.InterfaceState) {
		switch newState {
		case engine.StateOnline:
			a.Dispatcher.StartWorker(ctx, ifaceID, channelType)
		case engine.StateOffline, engine.StateError:
			a.Dispatcher.StopWorker(ifaceID)
		}
	})

	// Signal recorder — use whichever satellite transport is available.
	// Prefer IMT (9704) over SBD (9603) since it's the primary on pifour01.
	signalSat := a.Sat
	if a.IMTSat != nil {
		signalSat = a.IMTSat
	}
	a.SignalRecorder = engine.NewSignalRecorder(db, signalSat)
	a.SignalRecorder.Start(ctx)

	// Cellular signal recorder (optional)
	if a.Cell != nil {
		a.CellRecorder = engine.NewCellSignalRecorder(db, a.Cell)
		a.CellRecorder.SetProcessor(a.Processor)
		a.CellRecorder.Start(ctx)
	}

	// GPS reader (direct mode only)
	if a.GPSExcludePorts != nil {
		a.GPSReader = transport.NewGPSReader("auto", db)
		a.GPSReader.SetExcludePortFuncs(a.GPSExcludePorts)
		go a.GPSReader.Start(ctx)
	}

	// API server
	srv := api.NewServer(db, a.Mesh, a.Processor, a.GatewayMgr)
	srv.SetAccessEvaluator(a.AccessEval)
	srv.SetRegistry(a.Registry)
	srv.SetTLEManager(a.TLEMgr)
	srv.SetAstrocastTLEManager(a.AstroTLEMgr)
	srv.SetPassScheduler(a.GatewayMgr.GetPassScheduler())
	srv.SetCellTransport(a.Cell)
	srv.SetGPSReader(a.GPSReader)
	srv.SetInterfaceManager(a.InterfaceMgr)
	if a.DevSupervisor != nil {
		srv.SetDeviceSupervisor(a.DevSupervisor)
	}
	if a.Signing != nil {
		srv.SetSigningService(a.Signing)
	}
	srv.SetDispatcher(a.Dispatcher)
	srv.SetPaidRateLimit(cfg.PaidRateLimit)

	// Field intelligence features
	healthScorer := engine.NewHealthScorer(db)
	srv.SetHealthScorer(healthScorer)

	deadman := engine.NewDeadManSwitch(db, 4*time.Hour)
	deadman.Start(ctx)
	srv.SetDeadManSwitch(deadman)
	a.cleanups = append(a.cleanups, func() { deadman.Stop() })

	burstQueue := engine.NewBurstQueue(db, 10, 30*time.Minute)
	srv.SetBurstQueue(burstQueue)
	a.BurstQueue = burstQueue

	geofenceMon := engine.NewGeofenceMonitor()
	srv.SetGeofenceMonitor(geofenceMon)
	srv.SetWebHandler(webHandler(cfg.WebDir))
	if a.LinkMgr != nil {
		srv.SetLinkManager(a.LinkMgr)
	}
	if a.DestTable != nil {
		srv.SetDestinationTable(a.DestTable)
	}
	if a.RoutingIdentity != nil {
		srv.SetRoutingIdentity(a.RoutingIdentity)
	}
	a.Server = srv

	a.HTTPServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      srv.Router(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // SSE needs no write timeout
		IdleTimeout:  60 * time.Second,
	}

	// Hub uplink — connects bridge to MeshSat Hub MQTT broker (optional)
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

		startTime := time.Now()

		birthFn := func() hubreporter.BridgeBirth {
			birth := hubreporter.BridgeBirth{
				Version:     "0.18.0",
				Hostname:    cfg.BridgeID,
				Mode:        cfg.Mode,
				TenantID:    "default",
				CoTType:     hubreporter.CoTBridge,
				CoTCallsign: fmt.Sprintf("MESHSAT-%s", cfg.BridgeID),
				UptimeSec:   int64(time.Since(startTime).Seconds()),
			}
			if h, err := os.Hostname(); err == nil {
				birth.Hostname = h
			}
			// Collect interface info
			if a.InterfaceMgr != nil {
				for _, iface := range a.InterfaceMgr.GetAllStatus() {
					birth.Interfaces = append(birth.Interfaces, hubreporter.InterfaceInfo{
						Name:   iface.ID,
						Type:   iface.ChannelType,
						Status: string(iface.State),
						Port:   iface.DevicePort,
					})
				}
			}
			// Capabilities from active transports
			if a.Mesh != nil {
				birth.Capabilities = append(birth.Capabilities, "meshtastic")
			}
			if a.Sat != nil {
				birth.Capabilities = append(birth.Capabilities, "iridium_sbd")
			}
			if a.IMTSat != nil {
				birth.Capabilities = append(birth.Capabilities, "iridium_imt")
			}
			if a.Cell != nil {
				birth.Capabilities = append(birth.Capabilities, "cellular")
			}
			if a.Astro != nil {
				birth.Capabilities = append(birth.Capabilities, "astrocast")
			}
			if a.RoutingIdentity != nil {
				birth.Capabilities = append(birth.Capabilities, "reticulum")
				birth.Reticulum = &hubreporter.ReticulumInfo{
					IdentityHash:     a.RoutingIdentity.DestHashHex(),
					TransportEnabled: true,
				}
			}
			return birth
		}

		// Outbox for offline message queueing
		outbox := hubreporter.NewOutbox(a.DB.DB.DB, 10000, 7*24*time.Hour)

		healthFn := func() hubreporter.BridgeHealth {
			health := hubreporter.BridgeHealth{
				UptimeSec: int64(time.Since(startTime).Seconds()),
			}
			// Collect interface health
			if a.InterfaceMgr != nil {
				for _, iface := range a.InterfaceMgr.GetAllStatus() {
					health.Interfaces = append(health.Interfaces, hubreporter.InterfaceHealth{
						Name:   iface.ID,
						Status: string(iface.State),
					})
				}
			}
			// Outbox stats
			if stats, err := outbox.Stats(); err == nil {
				health.Outbox = &stats
			}
			return health
		}

		reporter := hubreporter.NewHubReporter(reporterCfg, birthFn, healthFn)
		reporter.SetOutbox(outbox)
		a.HubReporter = reporter

		// Command handler — processes commands from the Hub (ping, flush_burst, etc.)
		cmdHandler := hubreporter.NewCommandHandler(reporter, cfg.BridgeID, healthFn)
		cmdHandler.SetDeps(hubreporter.CommandDeps{
			SendText: func(ifaceID, text string) (int64, string, error) {
				return a.Dispatcher.QueueDirectSend(ifaceID, text)
			},
			FlushBurst: func(ctx context.Context) ([]byte, int, error) {
				if a.BurstQueue == nil {
					return nil, 0, fmt.Errorf("burst queue not initialized")
				}
				return a.BurstQueue.Flush(ctx)
			},
			BurstPending: func() int {
				if a.BurstQueue == nil {
					return 0
				}
				return a.BurstQueue.Pending()
			},
		})
		reporter.SetCommandHandler(cmdHandler)

		// Device inventory + event tap — tracks mesh devices and streams
		// position/telemetry to the Hub. The EventTap subscribes to the
		// processor's event broadcast in Start().
		a.HubInventory = hubreporter.NewDeviceInventory(reporter, cfg.BridgeID)
		a.HubEventTap = hubreporter.NewEventTap(reporter, a.HubInventory, cfg.BridgeID)

		log.Info().Str("hub", cfg.HubURL).Str("bridge_id", cfg.BridgeID).Msg("hub reporter configured (will start with app)")
	}

	return nil
}

// Start begins the event processor, retention worker, HTTP server, and
// periodic announce broadcasting.
func (a *App) Start(ctx context.Context) {
	go func() {
		if err := a.Processor.Run(ctx); err != nil {
			log.Error().Err(err).Msg("processor stopped with error")
		}
	}()

	go engine.StartRetentionWorker(ctx, a.DB, a.Config.RetentionDays)

	// Periodic announce broadcasting
	if a.RoutingIdentity != nil && a.Config.AnnounceIntervalSec > 0 {
		go a.announceLoop(ctx)
	}

	// Hub reporter — start MQTT uplink after all components are ready
	if a.HubReporter != nil {
		go func() {
			if err := a.HubReporter.Start(ctx); err != nil {
				log.Error().Err(err).Msg("hub reporter start failed (bridge will operate without hub)")
			}
		}()

		// Event tap — subscribe to processor's event stream and forward
		// positions/telemetry/node info to the Hub via MQTT.
		if a.HubEventTap != nil {
			eventCh, unsub := a.Processor.Subscribe()
			a.cleanups = append(a.cleanups, unsub)
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case event, ok := <-eventCh:
						if !ok {
							return
						}
						a.HubEventTap.HandleMeshEvent(event)
					}
				}
			}()
			log.Info().Msg("hub event tap started (streaming positions/telemetry to hub)")
		}
	}

	go func() {
		log.Info().Int("port", a.Config.Port).Msg("HTTP server listening")
		if err := a.HTTPServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("HTTP server failed")
		}
	}()
}

// announceLoop broadcasts the local routing identity at the configured interval.
func (a *App) announceLoop(ctx context.Context) {
	interval := time.Duration(a.Config.AnnounceIntervalSec) * time.Second

	// Announce immediately on startup
	a.broadcastAnnounce()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.broadcastAnnounce()
		}
	}
}

// broadcastAnnounce creates a local announce packet and transmits it via mesh.
func (a *App) broadcastAnnounce() {
	announce, err := routing.NewAnnounce(a.RoutingIdentity, nil)
	if err != nil {
		log.Error().Err(err).Msg("failed to create announce packet")
		return
	}
	data := announce.Marshal()
	log.Info().Str("dest_hash", a.RoutingIdentity.DestHashHex()).
		Int("size", len(data)).
		Msg("broadcasting routing announce")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.Mesh.SendRaw(ctx, transport.RawRequest{
		PortNum: 256, // PRIVATE_APP
		Payload: base64Encode(data),
	}); err != nil {
		log.Warn().Err(err).Msg("failed to broadcast announce via mesh")
	}

	// Also announce via TCP interface (if active)
	if a.TCPIface != nil && a.TCPIface.IsOnline() {
		if err := a.TCPIface.Send(ctx, data); err != nil {
			log.Warn().Err(err).Msg("failed to broadcast announce via tcp")
		}
	}
}

// Shutdown gracefully stops all components.
func (a *App) Shutdown() {
	// Unregister all devices from hub inventory before stopping reporter
	if a.HubInventory != nil {
		a.HubInventory.UnregisterAll("bridge_shutdown")
	}
	// Stop hub reporter — publishes bridge death before disconnecting
	if a.HubReporter != nil {
		a.HubReporter.Stop()
	}

	if a.DevSupervisor != nil {
		a.DevSupervisor.Stop()
	}
	a.SignalRecorder.Stop()
	if a.CellRecorder != nil {
		a.CellRecorder.Stop()
	}
	a.InterfaceMgr.Stop()
	a.TLEMgr.Stop()
	a.AstroTLEMgr.Stop()
	a.GatewayMgr.Stop()
	if a.TCPIface != nil {
		a.TCPIface.Stop()
	}

	for _, fn := range a.cleanups {
		fn()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := a.HTTPServer.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("HTTP server shutdown error")
	}
}

func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
