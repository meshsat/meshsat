package config

import (
	"os"
	"strconv"
)

// Config holds all application configuration, driven by environment variables.
type Config struct {
	Port          int
	DBPath        string
	HALURL        string
	HALAPIKey     string
	Mode          string // "cubeos" (HAL transport), "standalone" (HAL sidecar), or "direct" (serial)
	RetentionDays int
	WebDir        string // "" = embedded, path = serve from disk

	// Direct mode device ports ("auto" or "" = auto-detect)
	MeshtasticPort         string
	IridiumPort            string
	IridiumSleepPin        int    // GPIO BCM pin for 9603N sleep/wake (0 = disabled)
	IridiumNetAvPin        int    // GPIO BCM pin for 9603 NetAv input (0 = disabled, MESHSAT-666)
	IridiumRIPin           int    // GPIO BCM pin for 9603 RI input (0 = disabled, MESHSAT-667)
	IridiumOnOffPin        int    // GPIO BCM pin for 9603 OnOff output (0 = disabled, MESHSAT-668)
	IridiumOnOffActiveHigh bool   // OnOff polarity: true = direct wire (HIGH=on); false = MOSFET buffer (LOW=on, default)
	IMTPort                string // RockBLOCK 9704 (JSPR/IMT) — "auto", "", or /dev/ttyUSBx
	CellularPort           string
	ZigBeePort             string

	// Cost safety: global rate limit for paid transports (messages/hour)
	PaidRateLimit int

	// HTTP API rate limit: max requests per minute per source IP (0 = disabled)
	APIRateLimit int

	// Serial health watchdog: minutes of silence before forcing serial reconnect (0 = disabled)
	MeshWatchdogMin int

	// APRS receive watchdog [MESHSAT-814]: minutes without a decoded frame,
	// after the channel was heard within two hours, before the recovery
	// ladder runs (gateway restart, AIOC power cycle, bridge restart). 0 = off.
	APRSRxWatchdogMin int
	// APRS receive watchdog expectation window (minutes) and the Direwolf
	// stats-stale threshold (seconds) that gates its hardware rungs. [MESHSAT-857]
	APRSRxHeardWithinMin int
	APRSRxStatsStaleSec  int

	// OOB management frames [MESHSAT-756]. First-boot defaults only; the
	// persisted system_config values are UI-managed afterwards.
	OOBEnabled     bool
	OOBReplyBudget int
	OOBHostSocket  string
	// OOBRequestTTLMin and OOBExpirySkewSec bound how late a management
	// command may be acted on. [MESHSAT-1293]
	OOBRequestTTLMin int
	OOBExpirySkewSec int

	// Device health watchdog [MESHSAT-817]: protocol-level liveness probes
	// for every USB device and the heal ladder (soft, device, hub-port VBUS
	// cut). DeviceHealth=false disables it entirely. Misses is consecutive
	// probe misses before the ladder starts; HardBudget caps level-3 resets
	// per target per hour; HardGapSec spaces any two level-3 resets;
	// CellularMaxLevel caps the cellular ladder (a VBUS cut on the T-Call is
	// a modem power toggle, MESHSAT-812) until the quiet window is proven.
	DeviceHealth                 bool
	DeviceHealthTickSec          int
	DeviceHealthMisses           int
	DeviceHealthHardBudget       int
	DeviceHealthHardGapSec       int
	DeviceHealthCellularMaxLevel int

	// MeshTimeSyncRemote re-enables the admin set-time to every remote
	// NodeDB entry after a handshake (off: 42 LoRa transmissions on
	// parallax per reconnect, the suspected XIAO wedge trigger). [MESHSAT-783]
	MeshTimeSyncRemote bool

	// Meshtastic want_config_id handshake timeout in seconds.
	// 60s default comfortably covers kits with ~50 NodeDB entries on
	// SF7-LongFast (drain ~30-45s). 15s caused partial handshakes with
	// unconfigured my_node_num / region on parallax. [MESHSAT-619]
	MeshConfigTimeoutSec int

	// llama-zip gRPC sidecar address (empty = disabled)
	LlamaZipAddr string
	// llama-zip RPC timeout in seconds
	LlamaZipTimeoutSec int

	// MSVQ-SC gRPC sidecar address (empty = disabled)
	MSVQSCAddr string
	// MSVQ-SC RPC timeout in seconds
	MSVQSCTimeoutSec int
	// MSVQ-SC codebook file path (empty = no pure-Go decode)
	MSVQSCCodebook string

	// TCP Reticulum interface — RNS-compatible HDLC over TCP
	// Listen address for inbound RNS nodes (e.g. "0.0.0.0:4242"). Empty = disabled.
	TCPListenAddr string
	// Remote RNS node to connect to (e.g. "rns-node.example.com:4242"). Empty = disabled.
	TCPConnectAddr string

	// APRS hardware KISS TNC over serial (PicoAPRS V4 on USB-C). First-boot
	// defaults for the aprs gateway config; the device is also kept out of
	// the device supervisor's scans. Empty = Direwolf. [MESHSAT-821]
	APRSKISSDevice string
	APRSKISSBaud   int

	// AX.25/APRS Reticulum interface — bidirectional via a KISS TNC.
	// Direwolf TCP KISS address (e.g. "localhost:8001"), or "gateway" to
	// receive frames through the APRS gateway's own TNC link (required for
	// a serial TNC, which has one file handle). Empty = disabled.
	AX25KISSAddr string
	// AX.25 source callsign (e.g. "MESHSAT-1"). Required if KISS addr is set.
	AX25Callsign string

	// SMS Reticulum interface — cellular SMS transport for routing
	// Peer phone number for point-to-point SMS Reticulum link. Empty = disabled.
	SMSReticulumPeer string

	// BLE Reticulum interface — GATT peripheral via BlueZ D-Bus
	// BlueZ adapter name (e.g. "hci0"). Empty = disabled.
	BLEAdapter string
	// BLE advertised device name (default "MeshSat-RNS").
	BLEDeviceName string

	// MQTT Reticulum interface — raw binary pub/sub for multi-bridge mesh
	// MQTT broker URL (e.g. "tcp://broker:1883"). Empty = disabled.
	MQTTReticulumBroker string
	// MQTT topic for Reticulum packets (default "meshsat/reticulum/packet", matches Hub)
	MQTTReticulumTopic string

	// Routing announce interval in seconds (0 = disabled)
	AnnounceIntervalSec int

	// Reticulum node (internal/rns): upstream-compatible transport, links and
	// path requests on every registered interface. [MESHSAT-1348]
	RNSEnabled      bool   // MESHSAT_RNS_ENABLED (default true)
	RNSAcceptLinks  bool   // MESHSAT_RNS_ACCEPT_LINKS: peers may open links to this bridge
	RNSIFACNetname  string // MESHSAT_RNS_IFAC_NETNAME: interface access code network name for tcp_0
	RNSIFACNetkey   string // MESHSAT_RNS_IFAC_NETKEY: interface access code passphrase for tcp_0
	RNSPathTTLHours int    // MESHSAT_RNS_PATH_TTL_HOURS (default 168, one week like upstream)

	// RNode LoRa radio as a Reticulum interface (rnode_0). [MESHSAT-1349]
	RNodePort         string  // MESHSAT_RNODE_PORT: "" = off, auto, usb_serial:<sn>, /dev/..., tcp://host[:7633], ble://...
	RNodePreset       string  // MESHSAT_RNODE_PRESET: us-915, eu-868, au-915, ism-433 (CrossTalk's starters)
	RNodeFrequency    int     // MESHSAT_RNODE_FREQUENCY Hz (overrides the preset)
	RNodeBandwidth    int     // MESHSAT_RNODE_BANDWIDTH Hz
	RNodeSF           int     // MESHSAT_RNODE_SF
	RNodeCR           int     // MESHSAT_RNODE_CR
	RNodeTXPower      int     // MESHSAT_RNODE_TXPOWER dBm
	RNodeAirtimeShort float64 // MESHSAT_RNODE_AIRTIME_SHORT percent, 0 = unset
	RNodeAirtimeLong  float64 // MESHSAT_RNODE_AIRTIME_LONG percent, 0 = unset
	RNodeFlowControl  bool    // MESHSAT_RNODE_FLOW_CONTROL
	RNodeIDCallsign   string  // MESHSAT_RNODE_ID_CALLSIGN: station id beacon, empty = none
	RNodeIDIntervalS  int     // MESHSAT_RNODE_ID_INTERVAL seconds

	// IP-mesh and TNC Reticulum interfaces, first-boot seeds for the
	// routing_ifaces table (Settings > Routing owns them after that). [MESHSAT-1350]
	UDPListen        string // MESHSAT_UDP_LISTEN host:port, "" = off unless MESHSAT_UDP_FORWARD or _DEVICE set
	UDPForward       string // MESHSAT_UDP_FORWARD host:port (Haven: 10.41.255.255:4242)
	UDPDevice        string // MESHSAT_UDP_DEVICE network device, derives listen/forward from its broadcast address
	AutoIfaceDevices string // MESHSAT_AUTO_IFACE_DEVICES comma-separated, "" = off; never wlan0
	AutoIfaceGroup   string // MESHSAT_AUTO_IFACE_GROUP, default "reticulum"
	KISSPort         string // MESHSAT_KISS_PORT /dev/... or tcp://host:8100 (Mercury), "" = off
	KISSBaud         int    // MESHSAT_KISS_BAUD, default 115200
	KISSFlowControl  bool   // MESHSAT_KISS_FLOW_CONTROL

	// CrossTalk IMT framing ("RNSI\x01" header) on iridium_imt_0; the DB key
	// imt_rns_framing in reticulum_config overrides it. [MESHSAT-1351]
	IMTRNSFraming bool // MESHSAT_IMT_RNS_FRAMING

	// LXMF endpoint on the Reticulum node. [MESHSAT-1348]
	LXMFEnabled              bool   // MESHSAT_LXMF_ENABLED (default true)
	LXMFDisplayName          string // MESHSAT_LXMF_DISPLAY_NAME (default "MeshSat <hostname>")
	LXMFStampCost            int    // MESHSAT_LXMF_STAMP_COST: inbound stamp cost announced, 0 = none
	LXMFEnforceStamps        bool   // MESHSAT_LXMF_ENFORCE_STAMPS: drop inbound messages without a valid stamp
	LXMFMaxOutboundStampCost int    // MESHSAT_LXMF_MAX_OUTBOUND_STAMP_COST (default 16)
	LXMFAnnounceIntervalSec  int    // MESHSAT_LXMF_ANNOUNCE_INTERVAL (default 1800)

	// Hub uplink — connects bridge to MeshSat Hub MQTT broker
	HubURL            string // MQTT broker URL (empty = hub disabled)
	BridgeID          string // unique bridge identifier (default: hostname)
	HubUsername       string // MQTT username
	HubPassword       string // MQTT password
	HubTLSCert        string // path to client TLS certificate
	HubTLSKey         string // path to client TLS key
	HubTLSCA          string // path to CA certificate for hub server verification
	HubHealthInterval int    // health publish interval in seconds (default 30)

	// Satellite fallback uplink to the Hub when MQTT is down [MESHSAT-963]
	HubSatFallback         bool   // enable the fallback monitor (default true; needs a Hub URL)
	HubSMSNumber           string // the Hub's SMS number (Twilio); empty = no SMS leg
	HubFallbackAfterMin    int    // minutes of MQTT loss before activating (default 5)
	HubFallbackPositionMin int    // position frame interval in minutes (default 15)
	HubFallbackHealthMin   int    // health frame interval in minutes (default 60)
	HubFallbackBearer      string // auto | satellite | sms (default auto)
	// IMTMTPollMin is how long the 9704 may stay quiet before the bridge
	// sends a short MO to fetch waiting MTs; 0 turns it off. [MESHSAT-1282]
	IMTMTPollMin int

	// Hub WebSocket relay: serve this bridge's API to the tenant's other
	// bridges (phones) through the Hub when no direct path exists [MESHSAT-613]
	HubRelayEnabled bool   // default true; needs a Hub connection and a re-issued certificate
	HubAPIURL       string // Hub HTTPS API base; empty = derived from the MQTT URL (mqtt-hub.X -> https://hub.X)
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		Port:                         envInt("MESHSAT_PORT", 6050),
		DBPath:                       envStr("MESHSAT_DB_PATH", "/cubeos/data/meshsat.db"),
		HALURL:                       envStr("HAL_URL", "http://cubeos-hal:6005"),
		HALAPIKey:                    envStr("HAL_CORE_KEY", envStr("HAL_API_KEY", "")),
		Mode:                         envStr("MESHSAT_MODE", "cubeos"),
		RetentionDays:                envInt("MESHSAT_RETENTION_DAYS", 30),
		WebDir:                       envStr("MESHSAT_WEB_DIR", ""),
		MeshtasticPort:               envStr("MESHSAT_MESHTASTIC_PORT", "auto"),
		IridiumPort:                  envStr("MESHSAT_IRIDIUM_PORT", "auto"),
		IridiumSleepPin:              envInt("MESHSAT_IRIDIUM_SLEEP_PIN", 0),
		IridiumNetAvPin:              envInt("MESHSAT_IRIDIUM_NETAV_PIN", 0),
		IridiumRIPin:                 envInt("MESHSAT_IRIDIUM_RI_PIN", 0),
		IridiumOnOffPin:              envInt("MESHSAT_IRIDIUM_ONOFF_PIN", 0),
		IridiumOnOffActiveHigh:       envBool("MESHSAT_IRIDIUM_ONOFF_ACTIVE_HIGH", false),
		IMTPort:                      envStr("MESHSAT_IMT_PORT", "auto"),
		CellularPort:                 envStr("MESHSAT_CELLULAR_PORT", "auto"),
		ZigBeePort:                   envStr("MESHSAT_ZIGBEE_PORT", "auto"),
		PaidRateLimit:                envInt("MESHSAT_PAID_RATE_LIMIT", 60),
		APIRateLimit:                 envInt("MESHSAT_API_RATE_LIMIT", 600),
		MeshWatchdogMin:              envInt("MESHSAT_MESH_WATCHDOG_MIN", 0),
		APRSRxWatchdogMin:            envInt("MESHSAT_APRS_RX_WATCHDOG_MIN", 5),
		APRSRxHeardWithinMin:         envInt("MESHSAT_APRS_RX_HEARD_WITHIN_MIN", 120),
		APRSRxStatsStaleSec:          envInt("MESHSAT_APRS_RX_STATS_STALE_SEC", 90),
		OOBEnabled:                   envBool("MESHSAT_OOB_ENABLED", false),
		OOBReplyBudget:               envInt("MESHSAT_OOB_REPLY_BUDGET", 12),
		OOBHostSocket:                envStr("MESHSAT_OOB_HOST_SOCKET", "/run/meshsat-oob/agent.sock"),
		OOBRequestTTLMin:             envInt("MESHSAT_OOB_REQUEST_TTL_MIN", 15),
		OOBExpirySkewSec:             envInt("MESHSAT_OOB_EXPIRY_SKEW_S", 120),
		DeviceHealth:                 envBool("MESHSAT_DEVICE_HEALTH", true),
		DeviceHealthTickSec:          envInt("MESHSAT_DEVICE_HEALTH_TICK_SEC", 30),
		DeviceHealthMisses:           envInt("MESHSAT_DEVICE_HEALTH_MISSES", 3),
		DeviceHealthHardBudget:       envInt("MESHSAT_DEVICE_HEALTH_HARD_BUDGET", 3),
		DeviceHealthHardGapSec:       envInt("MESHSAT_DEVICE_HEALTH_HARD_GAP_SEC", 45),
		DeviceHealthCellularMaxLevel: envInt("MESHSAT_DEVICE_HEALTH_CELLULAR_MAX_LEVEL", 2),
		MeshTimeSyncRemote:           envBool("MESHSAT_MESH_TIMESYNC_REMOTE", false),
		MeshConfigTimeoutSec:         envInt("MESHSAT_MESH_CONFIG_TIMEOUT_SEC", 60),
		LlamaZipAddr:                 envStr("MESHSAT_LLAMAZIP_ADDR", ""),
		LlamaZipTimeoutSec:           envInt("MESHSAT_LLAMAZIP_TIMEOUT", 30),
		MSVQSCAddr:                   envStr("MESHSAT_MSVQSC_ADDR", ""),
		MSVQSCTimeoutSec:             envInt("MESHSAT_MSVQSC_TIMEOUT", 30),
		MSVQSCCodebook:               envStr("MESHSAT_MSVQSC_CODEBOOK", ""),
		TCPListenAddr:                envStr("MESHSAT_TCP_LISTEN", ""),
		TCPConnectAddr:               envStr("MESHSAT_TCP_CONNECT", ""),
		APRSKISSDevice:               envStr("MESHSAT_APRS_KISS_DEVICE", ""),
		APRSKISSBaud:                 envInt("MESHSAT_APRS_KISS_BAUD", 115200),
		AX25KISSAddr:                 envStr("MESHSAT_AX25_KISS_ADDR", ""),
		AX25Callsign:                 envStr("MESHSAT_AX25_CALLSIGN", ""),
		SMSReticulumPeer:             envStr("MESHSAT_SMS_RETICULUM_PEER", ""),
		BLEAdapter:                   envStr("MESHSAT_BLE_ADAPTER", ""),
		BLEDeviceName:                envStr("MESHSAT_BLE_DEVICE_NAME", "MeshSat-RNS"),
		MQTTReticulumBroker:          envStr("MESHSAT_MQTT_RETICULUM_BROKER", ""),
		MQTTReticulumTopic:           envStr("MESHSAT_MQTT_RETICULUM_TOPIC", "meshsat/reticulum/packet"),
		AnnounceIntervalSec:          envInt("MESHSAT_ANNOUNCE_INTERVAL", 300),
		RNSEnabled:                   envBool("MESHSAT_RNS_ENABLED", true),
		RNSAcceptLinks:               envBool("MESHSAT_RNS_ACCEPT_LINKS", true),
		RNSIFACNetname:               envStr("MESHSAT_RNS_IFAC_NETNAME", ""),
		RNSIFACNetkey:                envStr("MESHSAT_RNS_IFAC_NETKEY", ""),
		RNSPathTTLHours:              envInt("MESHSAT_RNS_PATH_TTL_HOURS", 168),
		RNodePort:                    envStr("MESHSAT_RNODE_PORT", ""),
		RNodePreset:                  envStr("MESHSAT_RNODE_PRESET", "eu-868"),
		RNodeFrequency:               envInt("MESHSAT_RNODE_FREQUENCY", 0),
		RNodeBandwidth:               envInt("MESHSAT_RNODE_BANDWIDTH", 0),
		RNodeSF:                      envInt("MESHSAT_RNODE_SF", 0),
		RNodeCR:                      envInt("MESHSAT_RNODE_CR", 0),
		RNodeTXPower:                 envInt("MESHSAT_RNODE_TXPOWER", -1),
		RNodeAirtimeShort:            envFloat("MESHSAT_RNODE_AIRTIME_SHORT", 0),
		RNodeAirtimeLong:             envFloat("MESHSAT_RNODE_AIRTIME_LONG", 0),
		RNodeFlowControl:             envBool("MESHSAT_RNODE_FLOW_CONTROL", false),
		RNodeIDCallsign:              envStr("MESHSAT_RNODE_ID_CALLSIGN", ""),
		RNodeIDIntervalS:             envInt("MESHSAT_RNODE_ID_INTERVAL", 600),
		UDPListen:                    envStr("MESHSAT_UDP_LISTEN", ""),
		UDPForward:                   envStr("MESHSAT_UDP_FORWARD", ""),
		UDPDevice:                    envStr("MESHSAT_UDP_DEVICE", ""),
		AutoIfaceDevices:             envStr("MESHSAT_AUTO_IFACE_DEVICES", ""),
		AutoIfaceGroup:               envStr("MESHSAT_AUTO_IFACE_GROUP", "reticulum"),
		KISSPort:                     envStr("MESHSAT_KISS_PORT", ""),
		KISSBaud:                     envInt("MESHSAT_KISS_BAUD", 115200),
		KISSFlowControl:              envBool("MESHSAT_KISS_FLOW_CONTROL", false),
		IMTRNSFraming:                envBool("MESHSAT_IMT_RNS_FRAMING", false),
		LXMFEnabled:                  envBool("MESHSAT_LXMF_ENABLED", true),
		LXMFDisplayName:              envStr("MESHSAT_LXMF_DISPLAY_NAME", "MeshSat "+defaultHostname()),
		LXMFStampCost:                envInt("MESHSAT_LXMF_STAMP_COST", 0),
		LXMFEnforceStamps:            envBool("MESHSAT_LXMF_ENFORCE_STAMPS", false),
		LXMFMaxOutboundStampCost:     envInt("MESHSAT_LXMF_MAX_OUTBOUND_STAMP_COST", 16),
		LXMFAnnounceIntervalSec:      envInt("MESHSAT_LXMF_ANNOUNCE_INTERVAL", 1800),

		HubURL:            envStr("MESHSAT_HUB_URL", ""),
		BridgeID:          envStr("MESHSAT_BRIDGE_ID", defaultHostname()),
		HubUsername:       envStr("MESHSAT_HUB_USERNAME", "meshsat"), // default to shared NATS user
		HubPassword:       envStrAlt("MESHSAT_HUB_PASSWORD", "MESHSAT_MQTT_PASSWORD", ""),
		HubTLSCert:        envStr("MESHSAT_HUB_TLS_CERT", ""),
		HubTLSKey:         envStr("MESHSAT_HUB_TLS_KEY", ""),
		HubTLSCA:          envStr("MESHSAT_HUB_TLS_CA", ""),
		HubHealthInterval: envInt("MESHSAT_HUB_HEALTH_INTERVAL", 30),

		HubSatFallback:         envBool("MESHSAT_HUB_SAT_FALLBACK", true),
		HubSMSNumber:           envStr("MESHSAT_HUB_SMS_NUMBER", ""),
		HubFallbackAfterMin:    envInt("MESHSAT_HUB_FALLBACK_AFTER_MIN", 5),
		HubFallbackPositionMin: envInt("MESHSAT_HUB_FALLBACK_POSITION_MIN", 15),
		HubFallbackHealthMin:   envInt("MESHSAT_HUB_FALLBACK_HEALTH_MIN", 60),
		HubFallbackBearer:      envStr("MESHSAT_HUB_FALLBACK_BEARER", "auto"),
		IMTMTPollMin:           envInt("MESHSAT_IMT_MT_POLL_MIN", 10),

		HubRelayEnabled: envBool("MESHSAT_HUB_RELAY_ENABLED", true),
		HubAPIURL:       envStr("MESHSAT_HUB_API_URL", ""),
	}
}

func defaultHostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "meshsat"
	}
	return h
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envStrAlt tries the primary key, then an alternate key, then falls back.
func envStrAlt(primary, alt, fallback string) string {
	if v := os.Getenv(primary); v != "" {
		return v
	}
	if v := os.Getenv(alt); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// envBool reads a boolean env var. Accepts "1", "true", "yes", "on"
// (case-insensitive) as true; anything else as false. Unset → fallback.
func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	switch v {
	case "1", "true", "TRUE", "True", "yes", "YES", "Yes", "on", "ON", "On":
		return true
	}
	return false
}
