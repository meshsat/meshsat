package hubreporter

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// SatFallbackConfig configures the satellite fallback monitor.
type SatFallbackConfig struct {
	BridgeID         string
	ActivateAfter    time.Duration              // activate after this much MQTT downtime (default 5min)
	PositionInterval time.Duration              // send position every X (default 15min)
	HealthInterval   time.Duration              // send health every X (default 1hour)
	SendFn           func(payload []byte) error // function to send raw bytes via satellite
	HealthFn         func() BridgeHealth        // collect current health metrics
	PositionFn       func() *Location           // collect current bridge position
}

func (c *SatFallbackConfig) defaults() {
	if c.ActivateAfter <= 0 {
		c.ActivateAfter = 5 * time.Minute
	}
	if c.PositionInterval <= 0 {
		c.PositionInterval = 15 * time.Minute
	}
	if c.HealthInterval <= 0 {
		c.HealthInterval = 1 * time.Hour
	}
}

// SatFallback monitors MQTT connectivity and activates satellite fallback
// when internet is down, sending periodic position and health summaries
// via the satellite uplink binary format.
type SatFallback struct {
	cfg            SatFallbackConfig
	mu             sync.Mutex
	active         bool      // satellite fallback mode active
	disconnectTime time.Time // when MQTT disconnected
	stopCh         chan struct{}
	stopped        bool
}

// NewSatFallback creates a new satellite fallback monitor.
func NewSatFallback(cfg SatFallbackConfig) *SatFallback {
	cfg.defaults()
	return &SatFallback{
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
}

// OnMQTTDisconnect records the time MQTT was lost.
func (sf *SatFallback) OnMQTTDisconnect() {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if sf.disconnectTime.IsZero() {
		sf.disconnectTime = time.Now()
		log.Info().Msg("satfallback: MQTT disconnect recorded")
	}
}

// OnMQTTReconnect clears the disconnect state and deactivates satellite mode.
func (sf *SatFallback) OnMQTTReconnect() {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if sf.active {
		log.Info().Msg("satfallback: MQTT reconnected, deactivating satellite fallback")
	}
	sf.disconnectTime = time.Time{}
	sf.active = false
}

// IsActive returns whether satellite fallback mode is currently active.
func (sf *SatFallback) IsActive() bool {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.active
}

// PublishSOS encodes and sends an SOS message immediately, regardless of mode.
func (sf *SatFallback) PublishSOS(deviceID string, lat, lon float64, message string) error {
	if sf.cfg.SendFn == nil {
		log.Warn().Msg("satfallback: SOS requested but no send function configured")
		return nil
	}
	payload := EncodeSatSOS(sf.cfg.BridgeID, deviceID, lat, lon, message, time.Now().UTC())
	log.Info().
		Str("device_id", deviceID).
		Int("bytes", len(payload)).
		Msg("satfallback: sending SOS via satellite")
	return sf.cfg.SendFn(payload)
}

// Run starts the background satellite fallback monitor loop.
// It checks MQTT connectivity and sends periodic updates when in fallback mode.
func (sf *SatFallback) Run(ctx context.Context) {
	// Check every 30 seconds whether we should activate or send updates.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	var lastPosition, lastHealth time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case <-sf.stopCh:
			return
		case <-ticker.C:
			sf.mu.Lock()
			disconnectTime := sf.disconnectTime
			wasActive := sf.active
			sf.mu.Unlock()

			// Nothing to do if MQTT is connected.
			if disconnectTime.IsZero() {
				continue
			}

			// Check if we should activate fallback mode.
			elapsed := time.Since(disconnectTime)
			if !wasActive && elapsed >= sf.cfg.ActivateAfter {
				sf.mu.Lock()
				sf.active = true
				sf.mu.Unlock()
				log.Warn().
					Dur("downtime", elapsed).
					Msg("satfallback: activating satellite fallback mode")
				// Send an immediate position on activation.
				sf.sendPosition()
				lastPosition = time.Now()
				continue
			}

			if !wasActive {
				continue
			}

			// In active mode: send periodic position and health.
			now := time.Now()
			if now.Sub(lastPosition) >= sf.cfg.PositionInterval {
				sf.sendPosition()
				lastPosition = now
			}
			if now.Sub(lastHealth) >= sf.cfg.HealthInterval {
				sf.sendHealth()
				lastHealth = now
			}
		}
	}
}

// Stop stops the satellite fallback monitor.
func (sf *SatFallback) Stop() {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if !sf.stopped {
		sf.stopped = true
		close(sf.stopCh)
	}
}

func (sf *SatFallback) sendPosition() {
	if sf.cfg.SendFn == nil || sf.cfg.PositionFn == nil {
		return
	}
	loc := sf.cfg.PositionFn()
	if loc == nil {
		log.Debug().Msg("satfallback: no position available, skipping")
		return
	}
	var sourceByte byte
	switch loc.Source {
	case "gps":
		sourceByte = 1
	case "fixed":
		sourceByte = 2
	case "iridium_cep":
		sourceByte = 3
	case "cell_tower":
		sourceByte = 4
	default:
		sourceByte = 0
	}
	payload := EncodeSatPosition(sf.cfg.BridgeID, loc.Lat, loc.Lon, float32(loc.Alt), sourceByte, time.Now().UTC())
	log.Info().Int("bytes", len(payload)).Msg("satfallback: sending position via satellite")
	if err := sf.cfg.SendFn(payload); err != nil {
		log.Error().Err(err).Msg("satfallback: failed to send position")
	}
}

func (sf *SatFallback) sendHealth() {
	if sf.cfg.SendFn == nil || sf.cfg.HealthFn == nil {
		return
	}
	health := sf.cfg.HealthFn()

	var ifaces []SatIfaceStatus
	for _, ih := range health.Interfaces {
		online := ih.Status == "online"
		sig := byte(ih.SignalBars)
		ifaces = append(ifaces, SatIfaceStatus{
			Name:   ih.Name,
			Online: online,
			Signal: sig,
		})
	}

	payload := EncodeSatHealth(
		sf.cfg.BridgeID,
		uint32(health.UptimeSec),
		byte(health.CPUPct),
		byte(health.MemPct),
		byte(health.DiskPct),
		ifaces,
		time.Now().UTC(),
	)
	log.Info().Int("bytes", len(payload)).Msg("satfallback: sending health via satellite")
	if err := sf.cfg.SendFn(payload); err != nil {
		log.Error().Err(err).Msg("satfallback: failed to send health")
	}
}
