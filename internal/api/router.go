package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/routing"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

// Server holds the API dependencies.
type Server struct {
	db            *database.DB
	mesh          transport.MeshTransport
	processor     *engine.Processor
	gwManager     *gateway.Manager
	accessEval    *rules.AccessEvaluator
	tleMgr        *engine.TLEManager
	scheduler     *gateway.PassScheduler
	registry      *channel.Registry
	astroTleMgr   *engine.AstrocastTLEManager
	cellTransport transport.CellTransport
	gpsReader     *transport.GPSReader
	ifaceMgr      *engine.InterfaceManager
	signing       *engine.SigningService
	dispatcher    *engine.Dispatcher
	linkMgr       *routing.LinkManager
	destTable     *routing.DestinationTable
	routingID     *routing.Identity
	paidRateLimit int
	sos           *SOSState
	webHandler    http.Handler
	healthScorer  *engine.HealthScorer
	geofenceMon   *engine.GeofenceMonitor
	deadman       *engine.DeadManSwitch
	burstQueue    *engine.BurstQueue
	onMOCallback  func(imei string)
	devSupervisor *transport.DeviceSupervisor
	resourceXfer  *routing.ResourceTransfer
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

// SetTLEManager sets the TLE manager for pass prediction.
func (s *Server) SetTLEManager(m *engine.TLEManager) {
	s.tleMgr = m
}

// SetCellTransport sets the cellular transport for cellular API endpoints.
func (s *Server) SetCellTransport(cell transport.CellTransport) {
	s.cellTransport = cell
}

// SetGPSReader sets the GPS reader for satellite count and status.
func (s *Server) SetGPSReader(r *transport.GPSReader) {
	s.gpsReader = r
}

// SetPaidRateLimit sets the global paid transport rate limit for cost analysis.
func (s *Server) SetPaidRateLimit(limit int) {
	s.paidRateLimit = limit
}

// SetWebHandler sets the handler for serving the web UI.
func (s *Server) SetWebHandler(h http.Handler) {
	s.webHandler = h
}

// SetRegistry sets the channel registry for the channels API.
func (s *Server) SetRegistry(r *channel.Registry) {
	s.registry = r
}

// SetPassScheduler sets the pass scheduler for scheduler status endpoint.
func (s *Server) SetPassScheduler(ps *gateway.PassScheduler) {
	s.scheduler = ps
}

// SetAccessEvaluator sets the v0.3.0 access rule evaluator for rule CRUD reload.
func (s *Server) SetAccessEvaluator(ae *rules.AccessEvaluator) {
	s.accessEval = ae
}

// SetInterfaceManager sets the interface manager for v0.3.0 interface CRUD.
func (s *Server) SetInterfaceManager(m *engine.InterfaceManager) {
	s.ifaceMgr = m
}

// SetDispatcher sets the dispatcher for loop metrics exposure.
func (s *Server) SetDispatcher(d *engine.Dispatcher) {
	s.dispatcher = d
}

// SetLinkManager sets the routing link manager for link API endpoints.
func (s *Server) SetLinkManager(lm *routing.LinkManager) {
	s.linkMgr = lm
}

// SetDestinationTable sets the routing destination table for routing API.
func (s *Server) SetDestinationTable(dt *routing.DestinationTable) {
	s.destTable = dt
}

// SetRoutingIdentity sets the local routing identity for routing API.
func (s *Server) SetRoutingIdentity(id *routing.Identity) {
	s.routingID = id
}

// SetHealthScorer sets the health scorer for interface health endpoints.
func (s *Server) SetHealthScorer(hs *engine.HealthScorer) {
	s.healthScorer = hs
}

// SetGeofenceMonitor sets the geofence monitor for geofence API endpoints.
func (s *Server) SetGeofenceMonitor(gm *engine.GeofenceMonitor) {
	s.geofenceMon = gm
}

// SetDeadManSwitch sets the dead man's switch for deadman API endpoints.
func (s *Server) SetDeadManSwitch(dm *engine.DeadManSwitch) {
	s.deadman = dm
}

// SetBurstQueue sets the burst queue for burst API endpoints.
func (s *Server) SetBurstQueue(bq *engine.BurstQueue) {
	s.burstQueue = bq
}

// SetDeviceSupervisor sets the device supervisor for USB device inventory.
func (s *Server) SetDeviceSupervisor(ds *transport.DeviceSupervisor) {
	s.devSupervisor = ds
}

// SetResourceTransfer sets the resource transfer manager for file delivery API.
func (s *Server) SetResourceTransfer(rt *routing.ResourceTransfer) {
	s.resourceXfer = rt
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
		r.Post("/nodes/request-info", s.handleRequestNodeInfo)
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

		// Iridium modem info
		r.Get("/iridium/modem", s.handleGetSatModemInfo)
		r.Get("/iridium/provisioning", s.handleCheckProvisioning)

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

		// Iridium system time (AT-MSSTM)
		r.Get("/iridium/time", s.handleGetIridiumTime)

		// Iridium geolocation (AT-MSGEO satellite sub-point)
		r.Get("/iridium/geolocation", s.handleGetIridiumGeolocation)
		r.Get("/iridium/geolocation/history", s.handleGetIridiumGeoHistory)

		// Location resolution (GPS > Custom)
		r.Get("/locations/resolved", s.handleGetGeolocationSources)

		// Astrocast LEO satellite passes
		r.Get("/astrocast/passes", s.handleGetAstrocastPasses)
		r.Post("/astrocast/passes/refresh", s.handleRefreshAstrocastTLEs)

		// Cellular modem
		r.Get("/cellular/signal", s.handleGetCellularSignal)
		r.Get("/cellular/signal/fast", s.handleGetCellularSignalFast)
		r.Get("/cellular/signal/history", s.handleGetCellularSignalHistory)
		r.Get("/cellular/status", s.handleGetCellularStatus)
		r.Post("/cellular/pin", s.handleSubmitCellularPIN)
		r.Get("/cellular/info", s.handleGetCellInfo)
		r.Get("/cellular/sms", s.handleGetSMSMessages)
		r.Get("/cellular/broadcasts", s.handleGetCellBroadcasts)
		r.Post("/cellular/broadcasts/{id}/ack", s.handleAckCellBroadcast)
		r.Post("/cellular/data/connect", s.handleCellularDataConnect)
		r.Post("/cellular/data/disconnect", s.handleCellularDataDisconnect)
		r.Get("/cellular/data/status", s.handleCellularDataStatus)
		r.Get("/cellular/dyndns/status", s.handleGetDynDNSStatus)
		r.Post("/cellular/dyndns/update", s.handleDynDNSForceUpdate)

		// Webhook receiver (inbound) + log
		r.Post("/webhooks/inbound", s.handleWebhookInbound)
		r.Post("/webhooks/cellular/inbound", s.handleWebhookCellularInbound) // backwards compat
		r.Get("/webhooks/log", s.handleGetWebhookLog)

		// RockBLOCK webhook (Iridium SBD MO via HTTP)
		r.Post("/webhook/rockblock", s.handleRockBLOCKWebhook)

		// SMS contacts (legacy — backward compatible, reads from sms_contacts)
		r.Get("/cellular/contacts", s.handleGetSMSContacts)
		r.Post("/cellular/contacts", s.handleCreateSMSContact)
		r.Put("/cellular/contacts/{id}", s.handleUpdateSMSContact)
		r.Delete("/cellular/contacts/{id}", s.handleDeleteSMSContact)
		r.Post("/cellular/sms/send", s.handleSendSMS)

		// Unified contacts (multi-transport address book)
		r.Get("/contacts", s.handleGetContacts)
		r.Post("/contacts", s.handleCreateContact)
		r.Get("/contacts/lookup", s.handleLookupContact)
		r.Get("/contacts/{id}", s.handleGetContact)
		r.Put("/contacts/{id}", s.handleUpdateContact)
		r.Delete("/contacts/{id}", s.handleDeleteContact)
		r.Post("/contacts/{id}/addresses", s.handleAddContactAddress)
		r.Put("/contacts/{id}/addresses/{aid}", s.handleUpdateContactAddress)
		r.Delete("/contacts/{id}/addresses/{aid}", s.handleDeleteContactAddress)
		r.Get("/contacts/{id}/conversation", s.handleGetConversation)

		// SIM card management
		r.Get("/cellular/sim-cards", s.handleGetSIMCards)
		r.Post("/cellular/sim-cards", s.handleCreateSIMCard)
		r.Put("/cellular/sim-cards/{id}", s.handleUpdateSIMCard)
		r.Delete("/cellular/sim-cards/{id}", s.handleDeleteSIMCard)
		r.Get("/cellular/sim-cards/current", s.handleGetCurrentSIMICCID)

		// Iridium queue — offline compose and priority management
		r.Get("/iridium/queue", s.handleGetIridiumQueue)
		r.Post("/iridium/queue", s.handleEnqueueIridiumMessage)
		r.Post("/iridium/queue/{id}/cancel", s.handleCancelQueueItem)
		r.Delete("/iridium/queue/{id}", s.handleDeleteQueueItem)
		r.Post("/iridium/queue/{id}/priority", s.handleSetQueuePriority)

		// Radio setup detection (MESHSAT-235)
		r.Get("/radio/setup", s.handleGetRadioSetup)

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
		r.Post("/config/owner", s.handleSetOwner)

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

		// Transport channel registry (v0.2.0)
		r.Get("/transport/channels", s.handleGetChannels)

		// Delivery ledger (v0.2.0)
		r.Get("/deliveries", s.handleGetDeliveries)
		r.Get("/deliveries/stats", s.handleGetDeliveryStats)
		r.Get("/deliveries/{id}", s.handleGetDelivery)
		r.Post("/deliveries/{id}/cancel", s.handleCancelDelivery)
		r.Post("/deliveries/{id}/retry", s.handleRetryDelivery)
		r.Get("/deliveries/message/{ref}", s.handleGetMessageDeliveries)

		// Legacy /rules routes removed — use /access-rules instead

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

		// v0.3.0 Interface-based routing
		r.Get("/interfaces", s.handleGetInterfaces)
		r.Get("/interfaces/{id}", s.handleGetInterface)
		r.Post("/interfaces", s.handleCreateInterface)
		r.Put("/interfaces/{id}", s.handleUpdateInterface)
		r.Delete("/interfaces/{id}", s.handleDeleteInterface)
		r.Post("/interfaces/{id}/bind", s.handleBindDevice)
		r.Post("/interfaces/{id}/unbind", s.handleUnbindDevice)
		r.Post("/interfaces/{id}/enable", s.handleEnableInterface)
		r.Post("/interfaces/{id}/disable", s.handleDisableInterface)
		r.Get("/interfaces/health", s.handleGetHealthScores)
		r.Get("/devices", s.handleGetDevices)
		r.Get("/devices/usb", s.handleGetUSBDevices)
		r.Get("/devices/usb/events", s.handleUSBDeviceEvents)
		r.Post("/devices/usb/scan", s.handleTriggerUSBScan)
		r.Get("/access-rules", s.handleGetAccessRules)
		r.Post("/access-rules", s.handleCreateAccessRule)
		r.Put("/access-rules/{id}", s.handleUpdateAccessRule)
		r.Delete("/access-rules/{id}", s.handleDeleteAccessRule)
		r.Post("/access-rules/{id}/enable", s.handleEnableAccessRule)
		r.Post("/access-rules/{id}/disable", s.handleDisableAccessRule)
		r.Post("/access-rules/reorder", s.handleReorderAccessRules)
		r.Get("/access-rules/{id}/stats", s.handleGetAccessRuleStats)
		r.Get("/object-groups", s.handleGetObjectGroups)
		r.Post("/object-groups", s.handleCreateObjectGroup)
		r.Put("/object-groups/{id}", s.handleUpdateObjectGroup)
		r.Delete("/object-groups/{id}", s.handleDeleteObjectGroup)
		r.Get("/failover-groups", s.handleGetFailoverGroups)
		r.Post("/failover-groups", s.handleCreateFailoverGroup)
		r.Put("/failover-groups/{id}", s.handleUpdateFailoverGroup)
		r.Delete("/failover-groups/{id}", s.handleDeleteFailoverGroup)

		// Config export/import (v0.3.0 — Cisco-style running-config)
		r.Get("/config/export", s.handleConfigExport)
		r.Post("/config/import", s.handleConfigImport)
		r.Post("/config/diff", s.handleConfigDiff)

		// Audit log and non-repudiation (v0.3.0)
		r.Get("/audit", s.handleGetAuditLog)
		r.Get("/audit/verify", s.handleVerifyAuditChain)
		r.Get("/audit/signer", s.handleGetSignerID)

		// ZigBee coordinator (v0.3.0)
		r.Get("/zigbee/devices", s.handleGetZigBeeDevices)
		r.Get("/zigbee/status", s.handleGetZigBeeStatus)

		// Crypto utilities (v0.3.0)
		r.Post("/crypto/generate-key", s.handleGenerateEncryptionKey)
		r.Post("/crypto/validate-transforms", s.handleValidateTransforms)

		// Loop prevention metrics (v0.3.0)
		r.Get("/loop-metrics", s.handleGetLoopMetrics)

		// Mesh topology (graph visualization)
		r.Get("/topology", s.handleGetTopology)

		// Routing — links and destinations (v0.2.0)
		r.Post("/links", s.handleCreateLink)
		r.Get("/links", s.handleGetLinks)
		r.Delete("/links/{id}", s.handleDeleteLink)
		r.Get("/routing/destinations", s.handleGetRoutingDestinations)
		r.Get("/routing/identity", s.handleGetRoutingIdentity)

		// Geofence zones
		r.Get("/geofences", s.handleGetGeofences)
		r.Post("/geofences", s.handleCreateGeofence)
		r.Delete("/geofences/{id}", s.handleDeleteGeofence)

		// Dead man's switch
		r.Get("/deadman", s.handleGetDeadmanConfig)
		r.Post("/deadman", s.handleSetDeadmanConfig)

		// Burst queue
		r.Get("/burst/status", s.handleGetBurstStatus)
		r.Post("/burst/flush", s.handleFlushBurst)

		// Device registry (IMEI-keyed)
		r.Get("/device-registry", s.handleGetRegisteredDevices)
		r.Post("/device-registry", s.handleCreateRegisteredDevice)
		r.Get("/device-registry/{id}", s.handleGetRegisteredDevice)
		r.Put("/device-registry/{id}", s.handleUpdateRegisteredDevice)
		r.Delete("/device-registry/{id}", s.handleDeleteRegisteredDevice)

		// Resource transfer (Reticulum chunked file delivery)
		r.Get("/resources", s.handleGetResources)
		r.Get("/resources/stats", s.handleGetResourceStats)
		r.Post("/resources/offer", s.handleOfferResource)
		r.Get("/resources/{hash}/data", s.handleGetResourceData)
		r.Delete("/resources/{hash}", s.handleDeleteResource)

		// Device configuration versioning (MESHSAT-99)
		r.Get("/device-registry/{id}/config", s.handleGetDeviceConfig)
		r.Put("/device-registry/{id}/config", s.handlePutDeviceConfig)
		r.Get("/device-registry/{id}/config/versions", s.handleGetDeviceConfigVersions)
		r.Get("/device-registry/{id}/config/versions/{version}", s.handleGetDeviceConfigVersion)
		r.Post("/device-registry/{id}/config/rollback/{version}", s.handleRollbackDeviceConfig)
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
