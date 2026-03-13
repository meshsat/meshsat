package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
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

	// Transports (set before calling Setup)
	Mesh  transport.MeshTransport
	Sat   transport.SatTransport
	Cell  transport.CellTransport
	Astro transport.AstrocastTransport

	// Optional: GPS exclude port funcs (direct mode only)
	GPSExcludePorts []func() string

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
		a.AnnounceRelay = routing.NewAnnounceRelay(relayConfig, a.DestTable, func(data []byte, announce *routing.Announce) {
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

		log.Info().Str("dest_hash", routingID.DestHashHex()).Msg("routing subsystem initialized")
	}

	// Wire routing into the processor event loop so incoming PRIVATE_APP
	// packets (announces, link handshakes, keepalives) are handled.
	if a.AnnounceRelay != nil {
		a.Processor.SetRouting(a.AnnounceRelay, a.LinkMgr, a.Keepalive, a.DestTable)
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

	// Signal recorder
	a.SignalRecorder = engine.NewSignalRecorder(db, a.Sat)
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
	if a.Signing != nil {
		srv.SetSigningService(a.Signing)
	}
	srv.SetDispatcher(a.Dispatcher)
	srv.SetPaidRateLimit(cfg.PaidRateLimit)
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
		log.Warn().Err(err).Msg("failed to broadcast announce")
	}
}

// Shutdown gracefully stops all components.
func (a *App) Shutdown() {
	a.SignalRecorder.Stop()
	if a.CellRecorder != nil {
		a.CellRecorder.Stop()
	}
	a.InterfaceMgr.Stop()
	a.TLEMgr.Stop()
	a.AstroTLEMgr.Stop()
	a.GatewayMgr.Stop()

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
