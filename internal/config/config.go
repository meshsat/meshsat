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
	AstrocastPort   string
	ZigBeePort      string

	// Cost safety: global rate limit for paid transports (messages/hour)
	PaidRateLimit int

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

	// Routing announce interval in seconds (0 = disabled)
	AnnounceIntervalSec int
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
		AstrocastPort:       envStr("MESHSAT_ASTROCAST_PORT", "auto"),
		ZigBeePort:          envStr("MESHSAT_ZIGBEE_PORT", "auto"),
		PaidRateLimit:       envInt("MESHSAT_PAID_RATE_LIMIT", 60),
		MeshWatchdogMin:     envInt("MESHSAT_MESH_WATCHDOG_MIN", 10),
		LlamaZipAddr:        envStr("MESHSAT_LLAMAZIP_ADDR", ""),
		LlamaZipTimeoutSec:  envInt("MESHSAT_LLAMAZIP_TIMEOUT", 30),
		MSVQSCAddr:          envStr("MESHSAT_MSVQSC_ADDR", ""),
		MSVQSCTimeoutSec:    envInt("MESHSAT_MSVQSC_TIMEOUT", 30),
		MSVQSCCodebook:      envStr("MESHSAT_MSVQSC_CODEBOOK", ""),
		TCPListenAddr:       envStr("MESHSAT_TCP_LISTEN", ""),
		TCPConnectAddr:      envStr("MESHSAT_TCP_CONNECT", ""),
		AnnounceIntervalSec: envInt("MESHSAT_ANNOUNCE_INTERVAL", 300),
	}
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
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
