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
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		Port:           envInt("MESHSAT_PORT", 6050),
		DBPath:         envStr("MESHSAT_DB_PATH", "/cubeos/data/meshsat.db"),
		HALURL:         envStr("HAL_URL", "http://cubeos-hal:6005"),
		HALAPIKey:      envStr("HAL_CORE_KEY", envStr("HAL_API_KEY", "")),
		Mode:           envStr("MESHSAT_MODE", "cubeos"),
		RetentionDays:  envInt("MESHSAT_RETENTION_DAYS", 30),
		WebDir:         envStr("MESHSAT_WEB_DIR", ""),
		MeshtasticPort: envStr("MESHSAT_MESHTASTIC_PORT", "auto"),
		IridiumPort:    envStr("MESHSAT_IRIDIUM_PORT", "auto"),
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
