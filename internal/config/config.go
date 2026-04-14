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
	MeshtasticPort  string
	IridiumPort     string
	IridiumSleepPin int    // GPIO pin for 9603N sleep/wake (0 = disabled)
	IMTPort         string // RockBLOCK 9704 (JSPR/IMT) — "auto", "", or /dev/ttyUSBx
	CellularPort    string
	ZigBeePort      string

	// Cost safety: global rate limit for paid transports (messages/hour)
	PaidRateLimit int

	// HTTP API rate limit: max requests per minute per source IP (0 = disabled)
	APIRateLimit int

	// Serial health watchdog: minutes of silence before forcing serial reconnect (0 = disabled)
	MeshWatchdogMin int

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

	// AX.25/APRS Reticulum interface — bidirectional via Direwolf KISS TNC
	// Direwolf TCP KISS address (e.g. "localhost:8001"). Empty = disabled.
	AX25KISSAddr string
	// AX.25 source callsign (e.g. "MESHSAT-1"). Required if KISS addr is set.
	AX25Callsign string

	// SMS Reticulum interface — cellular SMS transport for routing
	// Peer phone number for point-to-point SMS Reticulum link. Empty = disabled.
	SMSReticulumPeer string

	// MQTT Reticulum interface — raw binary pub/sub for multi-bridge mesh
	// MQTT broker URL (e.g. "tcp://broker:1883"). Empty = disabled.
	MQTTReticulumBroker string
	// MQTT topic for Reticulum packets (default "meshsat/reticulum/packet", matches Hub)
	MQTTReticulumTopic string

	// Routing announce interval in seconds (0 = disabled)
	AnnounceIntervalSec int

	// Hub uplink — connects bridge to MeshSat Hub MQTT broker
	HubURL            string // MQTT broker URL (empty = hub disabled)
	BridgeID          string // unique bridge identifier (default: hostname)
	HubUsername       string // MQTT username
	HubPassword       string // MQTT password
	HubTLSCert        string // path to client TLS certificate
	HubTLSKey         string // path to client TLS key
	HubTLSCA          string // path to CA certificate for hub server verification
	HubHealthInterval int    // health publish interval in seconds (default 30)
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		Port:                envInt("MESHSAT_PORT", 6050),
		DBPath:              envStr("MESHSAT_DB_PATH", "/cubeos/data/meshsat.db"),
		HALURL:              envStr("HAL_URL", "http://cubeos-hal:6005"),
		HALAPIKey:           envStr("HAL_CORE_KEY", envStr("HAL_API_KEY", "")),
		Mode:                envStr("MESHSAT_MODE", "cubeos"),
		RetentionDays:       envInt("MESHSAT_RETENTION_DAYS", 30),
		WebDir:              envStr("MESHSAT_WEB_DIR", ""),
		MeshtasticPort:      envStr("MESHSAT_MESHTASTIC_PORT", "auto"),
		IridiumPort:         envStr("MESHSAT_IRIDIUM_PORT", "auto"),
		IridiumSleepPin:     envInt("MESHSAT_IRIDIUM_SLEEP_PIN", 0),
		IMTPort:             envStr("MESHSAT_IMT_PORT", "auto"),
		CellularPort:        envStr("MESHSAT_CELLULAR_PORT", "auto"),
		ZigBeePort:          envStr("MESHSAT_ZIGBEE_PORT", "auto"),
		PaidRateLimit:       envInt("MESHSAT_PAID_RATE_LIMIT", 60),
		APIRateLimit:        envInt("MESHSAT_API_RATE_LIMIT", 600),
		MeshWatchdogMin:     envInt("MESHSAT_MESH_WATCHDOG_MIN", 10),
		LlamaZipAddr:        envStr("MESHSAT_LLAMAZIP_ADDR", ""),
		LlamaZipTimeoutSec:  envInt("MESHSAT_LLAMAZIP_TIMEOUT", 30),
		MSVQSCAddr:          envStr("MESHSAT_MSVQSC_ADDR", ""),
		MSVQSCTimeoutSec:    envInt("MESHSAT_MSVQSC_TIMEOUT", 30),
		MSVQSCCodebook:      envStr("MESHSAT_MSVQSC_CODEBOOK", ""),
		TCPListenAddr:       envStr("MESHSAT_TCP_LISTEN", ""),
		TCPConnectAddr:      envStr("MESHSAT_TCP_CONNECT", ""),
		AX25KISSAddr:        envStr("MESHSAT_AX25_KISS_ADDR", ""),
		AX25Callsign:        envStr("MESHSAT_AX25_CALLSIGN", ""),
		SMSReticulumPeer:    envStr("MESHSAT_SMS_RETICULUM_PEER", ""),
		MQTTReticulumBroker: envStr("MESHSAT_MQTT_RETICULUM_BROKER", ""),
		MQTTReticulumTopic:  envStr("MESHSAT_MQTT_RETICULUM_TOPIC", "meshsat/reticulum/packet"),
		AnnounceIntervalSec: envInt("MESHSAT_ANNOUNCE_INTERVAL", 300),

		HubURL:            envStr("MESHSAT_HUB_URL", ""),
		BridgeID:          envStr("MESHSAT_BRIDGE_ID", defaultHostname()),
		HubUsername:       envStr("MESHSAT_HUB_USERNAME", "meshsat"), // default to shared NATS user
		HubPassword:       envStrAlt("MESHSAT_HUB_PASSWORD", "MESHSAT_MQTT_PASSWORD", ""),
		HubTLSCert:        envStr("MESHSAT_HUB_TLS_CERT", ""),
		HubTLSKey:         envStr("MESHSAT_HUB_TLS_KEY", ""),
		HubTLSCA:          envStr("MESHSAT_HUB_TLS_CA", ""),
		HubHealthInterval: envInt("MESHSAT_HUB_HEALTH_INTERVAL", 30),
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
