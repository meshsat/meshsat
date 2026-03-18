package ratelimit

import (
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// ThrottleReason describes why a send was rate-limited.
type ThrottleReason string

const (
	ThrottleBurst          ThrottleReason = "burst_limit"
	ThrottleDaily          ThrottleReason = "daily_limit"
	ThrottleMonthly        ThrottleReason = "monthly_limit"
	ThrottleDailyCredits   ThrottleReason = "daily_credit_limit"
	ThrottleMonthlyCredits ThrottleReason = "monthly_credit_limit"
)

// ThrottleEvent is emitted when a device is rate-limited.
type ThrottleEvent struct {
	DeviceID int64          `json:"device_id"`
	IMEI     string         `json:"imei"`
	Reason   ThrottleReason `json:"reason"`
	Message  string         `json:"message"`
}

// SatelliteRateLimiter enforces per-device rate limits on satellite sends.
// It combines a token bucket for burst control with daily/monthly usage caps.
type SatelliteRateLimiter struct {
	db      *database.DB
	mu      sync.RWMutex
	buckets map[int64]*TokenBucket // device_id → token bucket
	configs map[int64]*database.SatelliteRateLimit

	// alertFn is called when a device is throttled (for MQTT/SSE alerts)
	alertFn func(ThrottleEvent)
}

// NewSatelliteRateLimiter creates a new per-device satellite rate limiter.
func NewSatelliteRateLimiter(db *database.DB) *SatelliteRateLimiter {
	return &SatelliteRateLimiter{
		db:      db,
		buckets: make(map[int64]*TokenBucket),
		configs: make(map[int64]*database.SatelliteRateLimit),
	}
}

// SetAlertFunc sets the callback for throttle alert notifications.
func (s *SatelliteRateLimiter) SetAlertFunc(fn func(ThrottleEvent)) {
	s.alertFn = fn
}

// LoadFromDB loads all rate limit configurations from the database
// and initializes token buckets.
func (s *SatelliteRateLimiter) LoadFromDB() error {
	limits, err := s.db.GetAllSatelliteRateLimits()
	if err != nil {
		return fmt.Errorf("load satellite rate limits: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, rl := range limits {
		rlCopy := rl
		s.configs[rl.DeviceID] = &rlCopy
		s.buckets[rl.DeviceID] = NewTokenBucket(float64(rl.BurstSize), rl.RefillRate)
	}

	log.Info().Int("count", len(limits)).Msg("satellite rate limiter: loaded configs")
	return nil
}

// ReloadDevice reloads a single device's rate limit config from the database.
func (s *SatelliteRateLimiter) ReloadDevice(deviceID int64) error {
	rl, err := s.db.GetSatelliteRateLimit(deviceID)
	if err != nil {
		// Not found — remove from cache
		s.mu.Lock()
		delete(s.configs, deviceID)
		delete(s.buckets, deviceID)
		s.mu.Unlock()
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.configs[deviceID] = rl
	// Only recreate bucket if config changed
	if _, exists := s.buckets[deviceID]; !exists {
		s.buckets[deviceID] = NewTokenBucket(float64(rl.BurstSize), rl.RefillRate)
	}
	return nil
}

// Allow checks if a satellite send is permitted for the given device.
// creditCost is the number of SBD credits this send will consume (1 credit per 50 bytes, min 1).
// Returns (allowed, reason). SOS messages always bypass rate limiting.
func (s *SatelliteRateLimiter) Allow(deviceID int64, isSOS bool, creditCost int) (bool, ThrottleReason) {
	// SOS messages ALWAYS bypass rate limiting
	if isSOS {
		return true, ""
	}

	s.mu.RLock()
	cfg, hasCfg := s.configs[deviceID]
	bucket := s.buckets[deviceID]
	s.mu.RUnlock()

	// No rate limit configured for this device — allow
	if !hasCfg || cfg == nil {
		return true, ""
	}

	// Rate limiting disabled for this device
	if !cfg.Enabled {
		return true, ""
	}

	// Check temporary admin override
	if cfg.OverrideUntil != nil && *cfg.OverrideUntil != "" {
		if overrideTime, err := time.Parse("2006-01-02 15:04:05", *cfg.OverrideUntil); err == nil {
			if time.Now().UTC().Before(overrideTime) {
				return true, ""
			}
		}
	}

	// Check token bucket (burst control)
	if bucket != nil && !bucket.Allow() {
		s.emitAlert(deviceID, ThrottleBurst, "burst rate limit exceeded")
		return false, ThrottleBurst
	}

	// Fetch usage counters once
	dailySends, dailyCredits, _ := s.db.GetSatelliteUsageToday(deviceID)
	monthlySends, monthlyCredits, _ := s.db.GetSatelliteUsageMonth(deviceID)

	// Check daily send cap
	if cfg.DailyLimit > 0 && dailySends >= cfg.DailyLimit {
		s.emitAlert(deviceID, ThrottleDaily, fmt.Sprintf("daily send limit reached (%d/%d)", dailySends, cfg.DailyLimit))
		return false, ThrottleDaily
	}

	// Check daily credit budget
	if cfg.DailyCreditLimit > 0 && dailyCredits+creditCost > cfg.DailyCreditLimit {
		s.emitAlert(deviceID, ThrottleDailyCredits, fmt.Sprintf("daily credit budget exceeded (%d+%d > %d)", dailyCredits, creditCost, cfg.DailyCreditLimit))
		return false, ThrottleDailyCredits
	}

	// Check monthly send cap
	if cfg.MonthlyLimit > 0 && monthlySends >= cfg.MonthlyLimit {
		s.emitAlert(deviceID, ThrottleMonthly, fmt.Sprintf("monthly send limit reached (%d/%d)", monthlySends, cfg.MonthlyLimit))
		return false, ThrottleMonthly
	}

	// Check monthly credit budget
	if cfg.MonthlyCreditLimit > 0 && monthlyCredits+creditCost > cfg.MonthlyCreditLimit {
		s.emitAlert(deviceID, ThrottleMonthlyCredits, fmt.Sprintf("monthly credit budget exceeded (%d+%d > %d)", monthlyCredits, creditCost, cfg.MonthlyCreditLimit))
		return false, ThrottleMonthlyCredits
	}

	return true, ""
}

// RecordUsage increments the usage counters after a successful satellite send.
func (s *SatelliteRateLimiter) RecordUsage(deviceID int64, credits int) {
	if err := s.db.IncrementSatelliteUsage(deviceID, 1, credits); err != nil {
		log.Error().Err(err).Int64("device_id", deviceID).Msg("satellite rate limiter: failed to record usage")
	}
}

// GetDeviceUsage returns current usage stats for a device.
func (s *SatelliteRateLimiter) GetDeviceUsage(deviceID int64) map[string]interface{} {
	dailySends, dailyCredits, _ := s.db.GetSatelliteUsageToday(deviceID)
	monthlySends, monthlyCredits, _ := s.db.GetSatelliteUsageMonth(deviceID)

	s.mu.RLock()
	cfg := s.configs[deviceID]
	bucket := s.buckets[deviceID]
	s.mu.RUnlock()

	result := map[string]interface{}{
		"device_id":       deviceID,
		"daily_sends":     dailySends,
		"daily_credits":   dailyCredits,
		"monthly_sends":   monthlySends,
		"monthly_credits": monthlyCredits,
	}

	if cfg != nil {
		result["daily_limit"] = cfg.DailyLimit
		result["monthly_limit"] = cfg.MonthlyLimit
		result["daily_remaining"] = max(0, cfg.DailyLimit-dailySends)
		result["monthly_remaining"] = max(0, cfg.MonthlyLimit-monthlySends)
		result["daily_credit_limit"] = cfg.DailyCreditLimit
		result["monthly_credit_limit"] = cfg.MonthlyCreditLimit
		if cfg.DailyCreditLimit > 0 {
			result["daily_credits_remaining"] = max(0, cfg.DailyCreditLimit-dailyCredits)
		}
		if cfg.MonthlyCreditLimit > 0 {
			result["monthly_credits_remaining"] = max(0, cfg.MonthlyCreditLimit-monthlyCredits)
		}
		result["enabled"] = cfg.Enabled
	}

	if bucket != nil {
		result["burst_tokens"] = bucket.Tokens()
	}

	return result
}

func (s *SatelliteRateLimiter) emitAlert(deviceID int64, reason ThrottleReason, message string) {
	log.Warn().Int64("device_id", deviceID).Str("reason", string(reason)).Msg("satellite rate limiter: " + message)

	if s.alertFn == nil {
		return
	}

	// Look up IMEI for the alert
	imei := ""
	if dev, err := s.db.GetDeviceAnyTenant(deviceID); err == nil {
		imei = dev.IMEI
	}

	s.alertFn(ThrottleEvent{
		DeviceID: deviceID,
		IMEI:     imei,
		Reason:   reason,
		Message:  message,
	})
}
