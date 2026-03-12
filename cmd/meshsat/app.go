package main

import (
	"context"
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

	// Dispatcher
	a.Dispatcher = engine.NewDispatcher(db, a.Registry, a.GatewayMgr, a.Mesh)
	a.Dispatcher.SetEmitter(a.Processor.Emit)
	a.Dispatcher.SetAccessEvaluator(a.AccessEval)
	failoverResolver := engine.NewFailoverResolver(db, a.InterfaceMgr)
	a.Dispatcher.SetFailoverResolver(failoverResolver)
	if a.Signing != nil {
		a.Dispatcher.SetSigningService(a.Signing)
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

// Start begins the event processor, retention worker, and HTTP server.
func (a *App) Start(ctx context.Context) {
	go func() {
		if err := a.Processor.Run(ctx); err != nil {
			log.Error().Err(err).Msg("processor stopped with error")
		}
	}()

	go engine.StartRetentionWorker(ctx, a.DB, a.Config.RetentionDays)

	go func() {
		log.Info().Int("port", a.Config.Port).Msg("HTTP server listening")
		if err := a.HTTPServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("HTTP server failed")
		}
	}()
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
