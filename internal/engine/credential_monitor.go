package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// CredentialMonitor periodically checks for expiring TLS certificates
// and emits SSE events to notify the dashboard.
type CredentialMonitor struct {
	db       *database.DB
	emitFn   func(transport.MeshEvent)
	warnDays int // days before expiry to warn (default 14)
}

// NewCredentialMonitor creates a new credential expiry monitor.
func NewCredentialMonitor(db *database.DB) *CredentialMonitor {
	return &CredentialMonitor{db: db, warnDays: 14}
}

// SetEmitter sets the SSE event emitter (processor.Emit).
func (cm *CredentialMonitor) SetEmitter(fn func(transport.MeshEvent)) {
	cm.emitFn = fn
}

// Start launches the periodic expiry checker.
// Runs immediately on startup, then every 6 hours.
func (cm *CredentialMonitor) Start(ctx context.Context) {
	go func() {
		cm.check()
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cm.check()
			}
		}
	}()
	log.Info().Int("warn_days", cm.warnDays).Msg("credential expiry monitor started")
}

func (cm *CredentialMonitor) check() {
	expiring, err := cm.db.ListExpiringCredentials(cm.warnDays)
	if err != nil {
		log.Debug().Err(err).Msg("credential monitor: query failed")
		return
	}

	now := time.Now()
	for _, cred := range expiring {
		if cred.CertNotAfter == "" {
			continue
		}

		notAfter, err := time.Parse("2006-01-02 15:04:05", cred.CertNotAfter)
		if err != nil {
			continue
		}

		daysLeft := int(notAfter.Sub(now).Hours() / 24)
		level := "warning"
		if daysLeft <= 0 {
			level = "error"
		}

		msg := fmt.Sprintf("Certificate %q (%s) expires in %d days", cred.Name, cred.Provider, daysLeft)
		if daysLeft <= 0 {
			msg = fmt.Sprintf("Certificate %q (%s) has EXPIRED", cred.Name, cred.Provider)
		}

		log.Warn().Str("provider", cred.Provider).Str("name", cred.Name).
			Int("days_left", daysLeft).Str("fingerprint", cred.CertFingerprint).
			Msg("credential expiry check")

		if cm.emitFn != nil {
			cm.emitFn(transport.MeshEvent{
				Type:    "cert_expiry_" + level,
				Message: msg,
			})
		}
	}

	if len(expiring) > 0 {
		log.Info().Int("expiring", len(expiring)).Int("within_days", cm.warnDays).
			Msg("credential monitor: found expiring certificates")
	}
}
