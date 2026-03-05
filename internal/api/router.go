package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

// Server holds the API dependencies.
type Server struct {
	db            *database.DB
	mesh          transport.MeshTransport
	processor     *engine.Processor
	gwManager     *gateway.Manager
	ruleEngine    *rules.Engine
	tleMgr        *engine.TLEManager
	scheduler     *gateway.PassScheduler
	cellTransport transport.CellTransport
	paidRateLimit int
	sos           *SOSState
	webHandler    http.Handler
}

// NewServer creates a new API server.
func NewServer(db *database.DB, mesh transport.MeshTransport, proc *engine.Processor, gwMgr *gateway.Manager) *Server {
	return &Server{
		db:        db,
		mesh:      mesh,
		processor: proc,
		gwManager: gwMgr,
	}
}

// SetRuleEngine sets the forwarding rules engine for rule CRUD reload.
func (s *Server) SetRuleEngine(e *rules.Engine) {
	s.ruleEngine = e
}

// SetTLEManager sets the TLE manager for pass prediction.
func (s *Server) SetTLEManager(m *engine.TLEManager) {
	s.tleMgr = m
}

// SetCellTransport sets the cellular transport for cellular API endpoints.
func (s *Server) SetCellTransport(cell transport.CellTransport) {
	s.cellTransport = cell
}

// SetPaidRateLimit sets the global paid transport rate limit for cost analysis.
func (s *Server) SetPaidRateLimit(limit int) {
	s.paidRateLimit = limit
}

// SetWebHandler sets the handler for serving the web UI.
func (s *Server) SetWebHandler(h http.Handler) {
	s.webHandler = h
}

// SetPassScheduler sets the pass scheduler for scheduler status endpoint.
func (s *Server) SetPassScheduler(ps *gateway.PassScheduler) {
	s.scheduler = ps
}

// Router builds the chi router with all API routes.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(corsMiddleware)

	// Health check
	r.Get("/health", s.handleHealth)

	// API routes
	r.Route("/api", func(r chi.Router) {
		r.Get("/messages", s.handleGetMessages)
		r.Get("/messages/stats", s.handleGetMessageStats)
		r.Post("/messages/send", s.handleSendMessage)
		r.Delete("/messages", s.handlePurgeMessages)

		r.Get("/telemetry", s.handleGetTelemetry)
		r.Get("/positions", s.handleGetPositions)

		r.Get("/nodes", s.handleGetNodes)
		r.Delete("/nodes/{num}", s.handleRemoveNode)
		r.Get("/status", s.handleGetStatus)

		r.Get("/events", s.handleSSE)

		// Gateway management (Phase 4)
		r.Get("/gateways", s.handleGetGateways)
		r.Get("/gateways/{type}", s.handleGetGateway)
		r.Put("/gateways/{type}", s.handlePutGateway)
		r.Delete("/gateways/{type}", s.handleDeleteGateway)
		r.Post("/gateways/{type}/start", s.handleStartGateway)
		r.Post("/gateways/{type}/stop", s.handleStopGateway)
		r.Post("/gateways/{type}/test", s.handleTestGateway)

		// Iridium signal
		r.Get("/iridium/signal", s.handleGetIridiumSignal)
		r.Get("/iridium/signal/fast", s.handleGetIridiumSignalFast)
		r.Get("/iridium/signal/history", s.handleGetSignalHistory)

		// Iridium credits
		r.Get("/iridium/credits", s.handleGetCredits)
		r.Post("/iridium/credits/budget", s.handleSetCreditBudget)

		// Iridium passes (TLE-based prediction)
		r.Get("/iridium/passes", s.handleGetPasses)
		r.Post("/iridium/passes/refresh", s.handleRefreshTLEs)

		// Iridium locations (ground stations)
		r.Get("/iridium/locations", s.handleGetLocations)
		r.Post("/iridium/locations", s.handleCreateLocation)
		r.Delete("/iridium/locations/{id}", s.handleDeleteLocation)

		// Iridium scheduler (pass-aware smart timing)
		r.Get("/iridium/scheduler", s.handleGetSchedulerStatus)

		// Iridium mailbox — manual one-shot check
		r.Post("/iridium/mailbox/check", s.handleManualMailboxCheck)

		// Iridium geolocation + AUTO location resolution
		r.Get("/iridium/geolocation", s.handleGetIridiumGeolocation)
		r.Get("/locations/resolved", s.handleGetGeolocationSources)

		// Cellular modem
		r.Get("/cellular/signal", s.handleGetCellularSignal)
		r.Get("/cellular/signal/history", s.handleGetCellularSignalHistory)
		r.Get("/cellular/status", s.handleGetCellularStatus)
		r.Post("/cellular/data/connect", s.handleCellularDataConnect)
		r.Post("/cellular/data/disconnect", s.handleCellularDataDisconnect)
		r.Get("/cellular/data/status", s.handleCellularDataStatus)
		r.Get("/cellular/dyndns/status", s.handleGetDynDNSStatus)
		r.Post("/cellular/dyndns/update", s.handleDynDNSForceUpdate)

		// Webhook receiver (inbound) + log
		r.Post("/webhooks/cellular/inbound", s.handleWebhookCellularInbound)
		r.Get("/webhooks/log", s.handleGetWebhookLog)

		// SMS contacts (address book)
		r.Get("/cellular/contacts", s.handleGetSMSContacts)
		r.Post("/cellular/contacts", s.handleCreateSMSContact)
		r.Put("/cellular/contacts/{id}", s.handleUpdateSMSContact)
		r.Delete("/cellular/contacts/{id}", s.handleDeleteSMSContact)
		r.Post("/cellular/sms/send", s.handleSendSMS)

		// Iridium queue — offline compose and priority management
		r.Get("/iridium/queue", s.handleGetIridiumQueue)
		r.Post("/iridium/queue", s.handleEnqueueIridiumMessage)
		r.Post("/iridium/queue/{id}/cancel", s.handleCancelQueueItem)
		r.Delete("/iridium/queue/{id}", s.handleDeleteQueueItem)
		r.Post("/iridium/queue/{id}/priority", s.handleSetQueuePriority)

		// Admin commands (Phase 2)
		r.Post("/admin/reboot", s.handleAdminReboot)
		r.Post("/admin/factory_reset", s.handleAdminFactoryReset)
		r.Post("/admin/traceroute", s.handleTraceroute)

		// Radio/module config (Phase 2)
		r.Get("/config", s.handleGetConfig)
		r.Get("/config/{section}", s.handleGetConfigSection)
		r.Post("/config/radio", s.handleSetRadioConfig)
		r.Post("/config/module", s.handleSetModuleConfig)
		r.Get("/config/module/{section}", s.handleGetModuleConfigSection)

		// Channel management
		r.Post("/channels", s.handleSetChannel)

		// Waypoints (Phase 2)
		r.Post("/waypoints", s.handleSendWaypoint)

		// Position sharing
		r.Post("/position/send", s.handleSendPosition)
		r.Post("/position/fixed", s.handleSetFixedPosition)
		r.Delete("/position/fixed", s.handleRemoveFixedPosition)

		// Neighbor info
		r.Get("/neighbors", s.handleGetNeighborInfo)

		// Store & Forward
		r.Post("/store-forward/request", s.handleRequestStoreForward)

		// Range test
		r.Post("/range-test/send", s.handleSendRangeTest)
		r.Get("/range-test", s.handleGetRangeTests)

		// Canned messages
		r.Get("/canned-messages", s.handleGetCannedMessages)
		r.Post("/canned-messages", s.handleSetCannedMessages)

		// Forwarding rules
		r.Get("/rules", s.handleGetRules)
		r.Post("/rules", s.handleCreateRule)
		r.Get("/rules/{id}", s.handleGetRule)
		r.Put("/rules/{id}", s.handleUpdateRule)
		r.Delete("/rules/{id}", s.handleDeleteRule)
		r.Post("/rules/{id}/enable", s.handleEnableRule)
		r.Post("/rules/{id}/disable", s.handleDisableRule)
		r.Post("/rules/reorder", s.handleReorderRules)
		r.Get("/rules/{id}/stats", s.handleGetRuleStats)

		// Preset messages
		r.Get("/presets", s.handleGetPresets)
		r.Post("/presets", s.handleCreatePreset)
		r.Put("/presets/{id}", s.handleUpdatePreset)
		r.Delete("/presets/{id}", s.handleDeletePreset)
		r.Post("/presets/{id}/send", s.handleSendPreset)

		// SOS
		r.Post("/sos/activate", s.handleSOSActivate)
		r.Post("/sos/cancel", s.handleSOSCancel)
		r.Get("/sos/status", s.handleSOSStatus)
	})

	// Web UI (SPA) — catch-all after API routes
	if s.webHandler != nil {
		r.Handle("/*", s.webHandler)
	}

	return r
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-HAL-Key")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
