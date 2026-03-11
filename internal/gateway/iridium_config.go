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
type IridiumConfig struct {
	ForwardPortnums    []int        `json:"forward_portnums,omitempty"`    // portnums to forward (empty = use ForwardAll)
	ForwardAll         bool         `json:"forward_all"`                   // forward all message types
	Compression        string       `json:"compression"`                   // "none" or "compact"
	AutoReceive        bool         `json:"auto_receive"`                  // auto-receive on ring alerts
	PollInterval       int          `json:"poll_interval"`                 // seconds, 0 = no polling
	MaxTextLength      int          `json:"max_text_length"`               // max text bytes in SBD (default 320)
	IncludePosition    bool         `json:"include_position"`              // include GPS coords in compact encoding
	DLQMaxRetries      int          `json:"dlq_max_retries"`               // legacy global max retries, 0 = infinite (default 0); use ExpiryPolicy for per-priority control
	DLQRetryBase       int          `json:"dlq_retry_base_secs"`           // base retry interval in seconds (default 120, exponential backoff)
	DefaultDestination string       `json:"default_destination,omitempty"` // node ID or name to unicast inbound messages (empty = broadcast)
	MinSignalBars      int          `json:"min_signal_bars"`               // minimum signal bars to trigger opportunistic DLQ drain (default 1)
	DailyBudget        int          `json:"daily_budget"`                  // max credits per day, 0 = unlimited
	MonthlyBudget      int          `json:"monthly_budget"`                // max credits per month, 0 = unlimited
	CriticalReserve    int          `json:"critical_reserve"`              // % reserved for priority 0 (default 20)
	MailboxMode        string       `json:"mailbox_mode"`                  // "ring_alert_only" (default), "scheduled", "off"
	SchedulerEnabled   bool         `json:"scheduler_enabled"`             // enable pass-aware smart scheduling (default true)
	PreWakeMinutes     int          `json:"pre_wake_minutes"`              // minutes before AOS to enter pre-wake mode (default 5)
	PostPassGraceSec   int          `json:"post_pass_grace_sec"`           // seconds after LOS to stay in post-pass mode (default 120)
	IdlePollSec        int          `json:"idle_poll_sec"`                 // MT poll interval in idle mode (default 900)
	ActivePollSec      int          `json:"active_poll_sec"`               // MT poll interval in active mode (default 20)
	MinElevDeg         int          `json:"min_elev_deg"`                  // minimum pass elevation for scheduler (default 5, higher for obstructed environments)
	ExpiryPolicy       ExpiryPolicy `json:"expiry_policy"`                 // per-priority message expiration (0 = never expire)
}

// DefaultIridiumConfig returns sensible defaults.
func DefaultIridiumConfig() IridiumConfig {
	return IridiumConfig{
		ForwardAll:       false,
		ForwardPortnums:  []int{1}, // TEXT_MESSAGE only by default
		Compression:      "compact",
		AutoReceive:      true,
		PollInterval:     1800, // 30 minutes — safety net for missed ring alerts (SBDSX pre-check avoids credit waste)
		MaxTextLength:    320,
		IncludePosition:  false, // GPS position not populated — omit to save 10 bytes per message
		DLQMaxRetries:    10,    // legacy global max retries (default 10)
		DLQRetryBase:     120,
		MinSignalBars:    1,
		DailyBudget:      0, // unlimited
		MonthlyBudget:    0, // unlimited
		CriticalReserve:  20,
		MailboxMode:      "ring_alert_only",
		SchedulerEnabled: true,
		PreWakeMinutes:   5,
		PostPassGraceSec: 120,
		IdlePollSec:      900,
		ActivePollSec:    20,
		MinElevDeg:       5,
		ExpiryPolicy: ExpiryPolicy{
			CriticalMaxRetries: 20, // critical messages get more attempts
			NormalMaxRetries:   10, // standard limit
			LowMaxRetries:      5,  // low-priority expires faster
		},
	}
}

// ParseIridiumConfig parses JSON config into IridiumConfig.
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
	return &cfg, nil
}
