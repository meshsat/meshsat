package gateway

import "encoding/json"

// ExpiryPolicy defines per-priority message expiration settings.
// MaxRetries of 0 means never expire (infinite retries).
type ExpiryPolicy struct {
	CriticalMaxRetries int `json:"critical_max_retries"` // priority 0 (default 0 = never expire)
	NormalMaxRetries   int `json:"normal_max_retries"`   // priority 1 (default 0 = never expire)
	LowMaxRetries      int `json:"low_max_retries"`      // priority 2 (default 0 = never expire)
}

// MaxRetriesForPriority returns the configured max retries for a given priority level.
// Returns 0 (never expire) for unknown priorities.
func (e ExpiryPolicy) MaxRetriesForPriority(priority int) int {
	switch priority {
	case 0:
		return e.CriticalMaxRetries
	case 2:
		return e.LowMaxRetries
	default:
		return e.NormalMaxRetries
	}
}

// IridiumConfig holds the configuration for an Iridium satellite gateway.
// Shared fields apply to both SBD and IMT. SBD-only fields are documented
// and ignored when used with IMTGateway.
type IridiumConfig struct {
	// --- Shared (SBD + IMT) ---
	ForwardPortnums    []int        `json:"forward_portnums,omitempty"`    // portnums to forward (empty = use ForwardAll)
	ForwardAll         bool         `json:"forward_all"`                   // forward all message types
	DLQMaxRetries      int          `json:"dlq_max_retries"`               // legacy global max retries, 0 = infinite
	DLQRetryBase       int          `json:"dlq_retry_base_secs"`           // base retry interval in seconds (default 120)
	DefaultDestination string       `json:"default_destination,omitempty"` // node ID or name to unicast inbound messages
	MinSignalBars      int          `json:"min_signal_bars"`               // minimum signal bars for opportunistic DLQ drain
	SchedulerEnabled   bool         `json:"scheduler_enabled"`             // enable pass-aware smart scheduling
	PreWakeMinutes     int          `json:"pre_wake_minutes"`              // minutes before AOS to enter pre-wake mode
	PostPassGraceSec   int          `json:"post_pass_grace_sec"`           // seconds after LOS to stay in post-pass mode
	MinElevDeg         int          `json:"min_elev_deg"`                  // minimum pass elevation for scheduler
	ExpiryPolicy       ExpiryPolicy `json:"expiry_policy"`                 // per-priority message expiration

	// --- SBD-only (9603) — ignored by IMTGateway ---
	Compression     string `json:"compression"`      // "none" or "compact" (SBD only)
	AutoReceive     bool   `json:"auto_receive"`     // auto-receive on ring alerts (SBD only)
	PollInterval    int    `json:"poll_interval"`    // seconds, 0 = no polling (SBD only)
	MaxTextLength   int    `json:"max_text_length"`  // max text bytes in SBD (SBD only, default 320)
	IncludePosition bool   `json:"include_position"` // include GPS coords in compact encoding (SBD only)
	DailyBudget     int    `json:"daily_budget"`     // max credits per day (SBD only, 0 = unlimited)
	MonthlyBudget   int    `json:"monthly_budget"`   // max credits per month (SBD only, 0 = unlimited)
	CriticalReserve int    `json:"critical_reserve"` // % reserved for priority 0 (SBD only, default 20)
	MailboxMode     string `json:"mailbox_mode"`     // "ring_alert_only", "scheduled", "off" (SBD only)
	IdlePollSec     int    `json:"idle_poll_sec"`    // MT poll interval in idle mode (SBD only)
	ActivePollSec   int    `json:"active_poll_sec"`  // MT poll interval in active mode (SBD only)
	PowerProfile    string `json:"power_profile"`    // "default" or "low_power" (SBD only, sleep GPIO)
}

// DefaultIridiumConfig returns sensible defaults for SBD.
func DefaultIridiumConfig() IridiumConfig {
	return IridiumConfig{
		ForwardAll:       false,
		ForwardPortnums:  []int{1}, // TEXT_MESSAGE only by default
		Compression:      "compact",
		AutoReceive:      true,
		PollInterval:     1800,
		MaxTextLength:    320,
		IncludePosition:  false,
		DLQMaxRetries:    10,
		DLQRetryBase:     120,
		MinSignalBars:    1,
		DailyBudget:      0,
		MonthlyBudget:    0,
		CriticalReserve:  20,
		MailboxMode:      "ring_alert_only",
		SchedulerEnabled: true,
		PreWakeMinutes:   5,
		PostPassGraceSec: 120,
		IdlePollSec:      900,
		ActivePollSec:    20,
		MinElevDeg:       5,
		PowerProfile:     "default",
		ExpiryPolicy: ExpiryPolicy{
			CriticalMaxRetries: 20,
			NormalMaxRetries:   10,
			LowMaxRetries:      5,
		},
	}
}

// DefaultIMTConfig returns sensible defaults for IMT.
// SBD-only fields are zeroed/disabled since IMTGateway doesn't use them.
func DefaultIMTConfig() IridiumConfig {
	return IridiumConfig{
		ForwardAll:       false,
		ForwardPortnums:  []int{1},
		DLQMaxRetries:    10,
		DLQRetryBase:     120,
		MinSignalBars:    1,
		SchedulerEnabled: true,
		PreWakeMinutes:   5,
		PostPassGraceSec: 120,
		MinElevDeg:       5,
		ExpiryPolicy: ExpiryPolicy{
			CriticalMaxRetries: 20,
			NormalMaxRetries:   10,
			LowMaxRetries:      5,
		},
		// SBD-only fields — explicitly disabled for IMT
		AutoReceive:  false,
		PollInterval: 0,
		MailboxMode:  "off",
		PowerProfile: "default",
	}
}

// ParseIridiumConfig parses JSON config into IridiumConfig for SBD gateways.
func ParseIridiumConfig(data string) (*IridiumConfig, error) {
	cfg := DefaultIridiumConfig()
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, err
	}
	if cfg.MaxTextLength <= 0 || cfg.MaxTextLength > 320 {
		cfg.MaxTextLength = 320
	}
	switch cfg.MailboxMode {
	case "ring_alert_only", "scheduled", "off":
		// valid
	default:
		cfg.MailboxMode = "ring_alert_only"
	}
	switch cfg.PowerProfile {
	case "default", "low_power":
		// valid
	default:
		cfg.PowerProfile = "default"
	}
	return &cfg, nil
}

// ParseIMTConfig parses JSON config into IridiumConfig for IMT gateways.
// SBD-only fields are reset to safe defaults after parsing.
func ParseIMTConfig(data string) (*IridiumConfig, error) {
	cfg := DefaultIMTConfig()
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, err
	}
	// Force SBD-only fields to safe values regardless of input
	cfg.AutoReceive = false
	cfg.PollInterval = 0
	cfg.MailboxMode = "off"
	cfg.DailyBudget = 0
	cfg.MonthlyBudget = 0
	cfg.CriticalReserve = 0
	cfg.PowerProfile = "default"
	return &cfg, nil
}
