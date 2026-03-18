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
	MeshtasticPort string
	IridiumPort    string
	CellularPort   string
	AstrocastPort  string
	ZigBeePort     string

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

	// Routing announce interval in seconds (0 = disabled)
	AnnounceIntervalSec int

	// APRS-IS IGate
	APRSISEnabled  bool
	APRSISServer   string
	APRSISCallsign string
	APRSISPasscode string

	// Hub store — database backend selection
	HubMode        string // "standalone" (SQLite) or "cluster" (PostgreSQL)
	HubDatabaseURL string // PostgreSQL connection URL (cluster mode only)
	HubDBPath      string // SQLite path (standalone mode only)
	HubRedisURL    string // Redis URL for rate limiting + dedup (cluster mode)

	// Message bus (NATS JetStream or Paho MQTT fallback)
	HubBusBackend  string // "nats" (default) or "mqtt" (Paho fallback)
	HubNATSURL     string // External NATS URL for cluster mode (nats://host:4222)
	HubNATSDataDir string // JetStream storage directory (embedded mode)
	HubMQTTPort    int    // MQTT adapter port for embedded NATS (default 1883)
	HubNATSPort    int    // NATS client port for embedded NATS (default 4222)
	HubMQTTURL     string // MQTT broker URL for Paho fallback (tcp://host:1883)

	// Cloudloop API (Iridium credit balance polling)
	HubCloudloopAPIKey       string
	HubCloudloopAPISecret    string
	HubCloudloopBaseURL      string
	HubCreditPollIntervalMin int

	// Backup
	BackupDir           string
	BackupIntervalHours int
	BackupMaxKeep       int
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
		AnnounceIntervalSec: envInt("MESHSAT_ANNOUNCE_INTERVAL", 300),

		APRSISEnabled:  envStr("HUB_APRSIS_ENABLED", "") == "true" || envStr("HUB_APRSIS_ENABLED", "") == "1",
		APRSISServer:   envStr("HUB_APRSIS_SERVER", "euro.aprs2.net:14580"),
		APRSISCallsign: envStr("HUB_APRSIS_CALLSIGN", ""),
		APRSISPasscode: envStr("HUB_APRSIS_PASSCODE", ""),

		HubMode:        envStr("HUB_MODE", "standalone"),
		HubDatabaseURL: envStr("HUB_DATABASE_URL", ""),
		HubDBPath:      envStr("HUB_DB_PATH", "/data/hub.db"),
		HubRedisURL:    envStr("HUB_REDIS_URL", ""),

		HubBusBackend:  envStr("HUB_BUS_BACKEND", "nats"),
		HubNATSURL:     envStr("HUB_NATS_URL", ""),
		HubNATSDataDir: envStr("HUB_NATS_DATA_DIR", "/cubeos/data/nats"),
		HubMQTTPort:    envInt("HUB_MQTT_PORT", 1883),
		HubNATSPort:    envInt("HUB_NATS_PORT", 4222),
		HubMQTTURL:     envStr("HUB_MQTT_URL", ""),

		HubCloudloopAPIKey:       envStr("HUB_CLOUDLOOP_API_KEY", ""),
		HubCloudloopAPISecret:    envStr("HUB_CLOUDLOOP_API_SECRET", ""),
		HubCloudloopBaseURL:      envStr("HUB_CLOUDLOOP_BASE_URL", "https://api.cloudloop.com"),
		HubCreditPollIntervalMin: envInt("HUB_CREDIT_POLL_INTERVAL_MIN", 60),

		BackupDir:           envStr("MESHSAT_BACKUP_DIR", "/cubeos/data/backups"),
		BackupIntervalHours: envInt("MESHSAT_BACKUP_INTERVAL_HOURS", 24),
		BackupMaxKeep:       envInt("MESHSAT_BACKUP_MAX_KEEP", 7),
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
