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
	"meshsat/internal/keystore"
	"meshsat/internal/routing"
	"meshsat/internal/rules"
	"meshsat/internal/spectrum"
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
	cellTransport transport.CellTransport
	gpsReader     *transport.GPSReader
	ifaceMgr      *engine.InterfaceManager
	signing       *engine.SigningService
	dispatcher    *engine.Dispatcher
	linkMgr       *routing.LinkManager
	destTable     *routing.DestinationTable
	routingID     *routing.Identity
	paidRateLimit int
	apiRateLimit  int
	sos           *SOSState
	webHandler    http.Handler
	healthScorer  *engine.HealthScorer
	geofenceMon   *engine.GeofenceMonitor
	deadman       *engine.DeadManSwitch
	burstQueue    *engine.BurstQueue
	onMOCallback  func(imei string)
	devSupervisor *transport.DeviceSupervisor
	resourceXfer  *routing.ResourceTransfer
	keyStore      *keystore.KeyStore
	transforms    *engine.TransformPipeline
	ifaceRegistry *routing.InterfaceRegistry
	tcpIface      *routing.TCPInterface
	spectrumMon   *spectrum.SpectrumMonitor
	restartFn     func()
	// blePeerMgr manages BLE-client Reticulum links to remote MeshSat
	// kits paired via Settings > Routing > Bluetooth Peers. Nil until
	// SetBLEPeerManager() has been called from wiring in main.go;
	// handlers are safe to call without it (they just skip auto-peer).
	// [MESHSAT-633]
	blePeerMgr *BLEPeerManager
}

// SetBLEPeerManager wires the BLE peer manager for auto-RNS-peer on
// paired MeshSat kits. [MESHSAT-633]
func (s *Server) SetBLEPeerManager(m *BLEPeerManager) {
	s.blePeerMgr = m
}

// SetRestartFunc sets the function called by POST /api/system/restart
// to trigger a graceful process restart (typically sends SIGTERM to self).
func (s *Server) SetRestartFunc(fn func()) {
	s.restartFn = fn
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

// SetAPIRateLimit sets the per-IP HTTP API rate limit (requests per minute).
// 0 disables rate limiting.
func (s *Server) SetAPIRateLimit(rpm int) {
	s.apiRateLimit = rpm
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

// SetKeyStore sets the key store for cross-platform key exchange.
func (s *Server) SetKeyStore(ks *keystore.KeyStore) {
	s.keyStore = ks
}

// SetTransformPipeline sets the transform pipeline for applying egress transforms on SMS send. [MESHSAT-447]
func (s *Server) SetTransformPipeline(tp *engine.TransformPipeline) {
	s.transforms = tp
}

// SetInterfaceRegistry sets the Reticulum interface registry for flood control API.
func (s *Server) SetInterfaceRegistry(reg *routing.InterfaceRegistry) {
	s.ifaceRegistry = reg
}

// SetTCPInterface sets the TCP Reticulum interface for peer management API.
func (s *Server) SetTCPInterface(iface *routing.TCPInterface) {
	s.tcpIface = iface
}

// SetSpectrumMonitor sets the RTL-SDR spectrum monitor for jamming detection.
func (s *Server) SetSpectrumMonitor(sm *spectrum.SpectrumMonitor) {
	s.spectrumMon = sm
}

// Router builds the chi router with all API routes.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(corsMiddleware)
	if s.apiRateLimit > 0 {
		r.Use(apiRateLimiter(s.apiRateLimit))
	}

	// Health check
	r.Get("/health", s.handleHealth)

	// Prometheus metrics
	r.Handle("/metrics", newMetricsHandler(s.gwManager, s.dispatcher, s.transforms, s.db, "/"))

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

		// TAK integration
		r.Post("/tak/enroll", s.handleTAKEnroll)
		r.Get("/tak/enroll/status", s.handleTAKEnrollStatus)
		r.Get("/tak/certificates", s.handleTAKCertificates)
		r.Get("/tak/missions", s.handleTAKMissions)
		r.Post("/tak/upload", s.handleTAKUpload)
		r.Get("/tak/download", s.handleTAKDownload)
		r.Get("/tak/sa", s.handleTAKSASnapshot)
		r.Post("/tak/missions/{name}/subscribe", s.handleTAKSubscribeMission)
		r.Get("/tak/events", s.handleTAKEventsSSE)
		r.Get("/tak/events/recent", s.handleTAKRecentEvents)
		r.Post("/tak/chat", s.handleTAKSendChat)
		r.Post("/tak/nineline", s.handleTAKSendNineLine)

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

		// Cellular modem
		r.Get("/cellular/signal", s.handleGetCellularSignal)
		r.Get("/cellular/signal/fast", s.handleGetCellularSignalFast)
		r.Get("/cellular/signal/history", s.handleGetCellularSignalHistory)
		r.Get("/cellular/status", s.handleGetCellularStatus)
		r.Post("/cellular/at", s.handleCellularAT)
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

		// SMS contacts — DEPRECATED [MESHSAT-542]. Retained during the
		// v50 grace window; new clients: /api/contacts?kind=sms (GET)
		// and /api/contacts (POST/PUT/DELETE). Responses carry
		// Deprecation + Sunset + Link rel="successor-version" headers
		// so honouring clients can migrate automatically.
		r.Get("/cellular/contacts", s.handleGetSMSContacts)
		r.Post("/cellular/contacts", s.handleCreateSMSContact)
		r.Put("/cellular/contacts/{id}", s.handleUpdateSMSContact)
		r.Delete("/cellular/contacts/{id}", s.handleDeleteSMSContact)
		r.Post("/cellular/sms/send", s.handleSendSMS)

		// Directory import / export — vCard 4.0 + CSV [MESHSAT-541].
		// Writes land in the v44+ directory_* tables; legacy /api/contacts
		// continues to serve the v23 path during the grace window.
		r.Post("/directory/import/vcard", s.handleImportVCard)
		r.Post("/directory/import/csv", s.handleImportCSV)
		r.Get("/directory/export/vcard", s.handleExportVCard)

		// Contact-aware send [MESHSAT-545 / S2-02]. Wraps
		// Dispatcher.SendToRecipient — resolves the contact to its
		// addresses, picks the dispatch strategy from policy, queues
		// a delivery per selected bearer.
		r.Post("/messages/send-to-contact", s.handleSendToContact)

		// Unified contacts (multi-transport address book)
		r.Get("/contacts", s.handleGetContacts)
		r.Post("/contacts", s.handleCreateContact)
		r.Get("/contacts/lookup", s.handleLookupContact)
		r.Get("/contacts/{id}", s.handleGetContact)
		r.Put("/contacts/{id}", s.handleUpdateContact)
		r.Delete("/contacts/{id}", s.handleDeleteContact)
		r.Post("/contacts/{id}/verify", s.handleVerifyContact)
		r.Post("/contacts/{id}/addresses", s.handleAddContactAddress)
		r.Put("/contacts/{id}/addresses/{aid}", s.handleUpdateContactAddress)
		r.Delete("/contacts/{id}/addresses/{aid}", s.handleDeleteContactAddress)
		r.Get("/contacts/{id}/conversation", s.handleGetConversation)

		// QR contact-card export — meshsat://contact/<base64url>
		// [MESHSAT-561]. Signed by the bridge's Ed25519 identity so
		// scanners can verify the card hasn't been forged.
		r.Get("/directory/contacts/{id}/qr", s.handleDirectoryContactQR)
		r.Get("/contacts/{id}/qr", s.handleDirectoryContactQR)
		r.Post("/directory/contacts/import-qr", s.handleDirectoryContactImportQR)

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
		r.Get("/zigbee/devices2", s.handleGetZigBeeDevicesEnriched) // device-manager UI [MESHSAT-509]
		r.Get("/zigbee/devices/{addr}", s.handleGetZigBeeDevice)
		r.Patch("/zigbee/devices/{addr}", s.handlePatchZigBeeDevice)
		r.Delete("/zigbee/devices/{addr}", s.handleDeleteZigBeeDevice)
		r.Get("/zigbee/devices/{addr}/history", s.handleGetZigBeeDeviceHistory)
		r.Get("/zigbee/devices/{addr}/routing", s.handleGetZigBeeDeviceRouting)
		r.Put("/zigbee/devices/{addr}/routing", s.handlePutZigBeeDeviceRouting)
		r.Post("/zigbee/devices/{addr}/command", s.handlePostZigBeeDeviceCommand)
		r.Post("/zigbee/devices/{addr}/refresh", s.handlePostZigBeeDeviceRefresh)
		r.Get("/zigbee/status", s.handleGetZigBeeStatus)
		r.Post("/zigbee/permit-join", s.handlePostZigBeePermitJoin)
		r.Get("/zigbee/permit-join", s.handleGetZigBeePermitJoin)

		// APRS dashboard (v0.4.0)
		r.Get("/aprs/status", s.handleGetAPRSStatus)
		r.Get("/aprs/heard", s.handleGetAPRSHeard)
		r.Get("/aprs/activity", s.handleGetAPRSActivity)

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
		r.Get("/routing/floodable", s.handleGetFloodable)
		r.Put("/routing/floodable/{ifaceID}", s.handleSetFloodable)
		r.Get("/routing/config", s.handleGetRoutingConfig)
		r.Put("/routing/config", s.handleSetRoutingConfig)
		r.Get("/routing/peers", s.handleGetPeers)
		r.Post("/routing/peers", s.handleAddPeer)
		r.Delete("/routing/peers/{addr}", s.handleRemovePeer)
		r.Get("/routing/hub", s.handleGetHubConfig)
		r.Put("/routing/hub", s.handleSetHubConfig)

		// Geofence zones
		r.Get("/geofences", s.handleGetGeofences)
		r.Post("/geofences", s.handleCreateGeofence)
		r.Delete("/geofences/{id}", s.handleDeleteGeofence)

		// HeMB bond groups + observability
		r.Get("/bond-groups", s.handleGetBondGroups)
		r.Post("/bond-groups", s.handleCreateBondGroup)
		r.Delete("/bond-groups/{id}", s.handleDeleteBondGroup)
		r.Post("/hemb/send", s.handleHeMBSend)
		r.Get("/hemb/stats", s.handleGetHeMBStats)
		r.Get("/hemb/events", s.handleHeMBSSE)
		r.Get("/hemb/topology", s.handleGetHeMBTopology)
		r.Get("/hemb/events/history", s.handleGetHeMBEventHistory)
		r.Get("/hemb/streams", s.handleGetHeMBStreams)
		r.Get("/hemb/streams/{id}", s.handleGetHeMBStreamDetail)
		r.Get("/hemb/generations/{stream_id}/{gen_id}", s.handleGetHeMBGenerationInspect)
		r.Post("/hemb/fault-inject", s.handleHeMBFaultInject)
		r.Get("/hemb/fault-inject", s.handleHeMBFaultList)
		r.Delete("/hemb/fault-inject/{id}", s.handleHeMBFaultClear)

		// Dead man's switch
		r.Get("/deadman", s.handleGetDeadmanConfig)
		r.Post("/deadman", s.handleSetDeadmanConfig)

		// System
		r.Post("/system/restart", s.handleSystemRestart)
		r.Post("/system/backlight", s.handleBacklight)
		r.Get("/system/battery", s.handleGetBattery)

		// Bluetooth device management (ported from cubeos/hal). [MESHSAT-623]
		r.Get("/system/bluetooth/status", s.handleBluetoothStatus)
		r.Get("/system/bluetooth/devices", s.handleBluetoothDevices)
		r.Post("/system/bluetooth/scan", s.handleBluetoothScan)
		r.Post("/system/bluetooth/power/on", s.handleBluetoothPowerOn)
		r.Post("/system/bluetooth/power/off", s.handleBluetoothPowerOff)
		r.Post("/system/bluetooth/pair", s.handleBluetoothPair)
		r.Post("/system/bluetooth/connect/{address}", s.handleBluetoothConnect)
		r.Post("/system/bluetooth/disconnect/{address}", s.handleBluetoothDisconnect)
		r.Delete("/system/bluetooth/remove/{address}", s.handleBluetoothRemove)
		r.Post("/system/bluetooth/rfkill", s.handleBluetoothRFKill)

		// WiFi network management (ported from cubeos/hal). [MESHSAT-624]
		r.Get("/system/wifi/scan/{iface}", s.handleWiFiScan)
		r.Get("/system/wifi/scan", s.handleWiFiScan)
		r.Post("/system/wifi/connect", s.handleWiFiConnect)
		r.Post("/system/wifi/disconnect/{iface}", s.handleWiFiDisconnect)
		r.Get("/system/wifi/status/{iface}", s.handleWiFiStatus)
		r.Get("/system/wifi/status", s.handleWiFiStatus)
		r.Get("/system/wifi/saved/{iface}", s.handleWiFiSaved)
		r.Get("/system/wifi/saved", s.handleWiFiSaved)

		// WiFi peer-link (kit-to-kit without AP). [MESHSAT-630]
		r.Get("/system/wifi/capabilities/{iface}", s.handleWiFiCapabilities)
		r.Get("/system/wifi/capabilities", s.handleWiFiCapabilities)
		r.Post("/system/wifi/ibss/join", s.handleWiFiIBSSJoin)
		r.Post("/system/wifi/ibss/leave/{iface}", s.handleWiFiIBSSLeave)
		r.Post("/system/wifi/ibss/leave", s.handleWiFiIBSSLeave)

		// Pair mode — touch-display arm + remote-device claim
		// [MESHSAT-596]. Mounted under /api/v2/pair/ so the old
		// /api/* JWT-less rail remains stable during the Phase-8
		// rollout; the mTLS middleware (MESHSAT-598) attaches
		// here when it lands.
		r.Post("/v2/pair/arm", s.handlePairArm)
		r.Post("/v2/pair/claim", s.handlePairClaim)
		r.Post("/v2/pair/refresh", s.handlePairRefresh)
		r.Get("/v2/pair/list", s.handlePairList)
		r.Post("/v2/pair/revoke/{id}", s.handlePairRevoke)

		// Burst queue
		r.Get("/burst/status", s.handleGetBurstStatus)
		r.Post("/burst/flush", s.handleFlushBurst)

		// Spectrum monitoring (RTL-SDR jamming detection)
		r.Get("/spectrum/status", s.handleGetSpectrumStatus)
		r.Get("/spectrum/hardware", s.handleGetSpectrumHardware)
		r.Get("/spectrum/relay-status", s.handleGetSpectrumRelay)
		r.Get("/spectrum/stream", s.handleSpectrumStream)

		// Device registry (IMEI-keyed)
		r.Get("/device-registry", s.handleGetRegisteredDevices)
		r.Post("/device-registry", s.handleCreateRegisteredDevice)
		r.Get("/device-registry/{id}", s.handleGetRegisteredDevice)
		r.Put("/device-registry/{id}", s.handleUpdateRegisteredDevice)
		r.Delete("/device-registry/{id}", s.handleDeleteRegisteredDevice)

		// Credential management (cert/credential upload, storage, expiry)
		r.Post("/credentials/upload", s.handleUploadCredential)
		r.Get("/credentials", s.handleListCredentials)
		r.Get("/credentials/expiry", s.handleListExpiringCredentials)
		r.Get("/credentials/{id}", s.handleGetCredential)
		r.Delete("/credentials/{id}", s.handleDeleteCredential)
		r.Post("/credentials/{id}/apply", s.handleApplyCredential)

		// Key exchange (cross-platform QR key sharing)
		r.Post("/keys/bundle", s.handleGenerateKeyBundle)
		r.Get("/keys/bundle/qr", s.handleGetKeyBundleQR)
		r.Post("/keys/rotate", s.handleRotateKey)
		r.Get("/keys", s.handleListKeys)
		r.Get("/keys/stats", s.handleGetKeyStats)
		r.Get("/keys/signing", s.handleGetSigningKey)
		r.Delete("/keys/{type}/{address}", s.handleRevokeKey)

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
