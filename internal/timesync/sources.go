package timesync

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/transport"
)

// GPSTimeProvider is the interface needed from GPSReader — only GetStatus().
type GPSTimeProvider interface {
	GetStatus() transport.GPSStatus
}

// SBDTimeProvider is the interface needed from SBDTransport — only GetSystemTime().
type SBDTimeProvider interface {
	GetSystemTime(ctx context.Context) (*transport.IridiumTime, error)
}

// ---------- GPS Source (Stratum 1) ----------

// GPSSource derives time corrections from GPS NMEA sentences.
type GPSSource struct {
	gps GPSTimeProvider
}

// NewGPSSource creates a GPS time source adapter.
func NewGPSSource(gps GPSTimeProvider) *GPSSource {
	return &GPSSource{gps: gps}
}

func (s *GPSSource) Name() string { return "gps" }
func (s *GPSSource) Stratum() int { return 1 }

func (s *GPSSource) Start(ctx context.Context, cb CorrectionCallback) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			status := s.gps.GetStatus()
			if !status.Fix || status.Time.IsZero() {
				continue
			}

			// GPS time vs local clock.
			localNow := time.Now()
			offsetNs := status.Time.UnixNano() - localNow.UnixNano()

			// NMEA parsing introduces ~100ms uncertainty.
			cb("gps", 1, offsetNs, 100_000_000)

			log.Debug().
				Float64("offset_ms", float64(offsetNs)/1e6).
				Int("sats", status.Sats).
				Msg("timesync: gps reading")
		}
	}
}

// ---------- MSSTM Source (Stratum 1) ----------

// MSSTMSource derives time corrections from Iridium AT-MSSTM system time.
// Only available on SBD (9603) modems — the 9704 IMT uses JSPR.
type MSSTMSource struct {
	sat SBDTimeProvider
}

// NewMSSTMSource creates an Iridium MSSTM time source adapter.
func NewMSSTMSource(sat SBDTimeProvider) *MSSTMSource {
	return &MSSTMSource{sat: sat}
}

func (s *MSSTMSource) Name() string { return "msstm" }
func (s *MSSTMSource) Stratum() int { return 1 }

func (s *MSSTMSource) Start(ctx context.Context, cb CorrectionCallback) {
	ticker := time.NewTicker(5 * time.Minute) // conservative — MSSTM is cheap but not free
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			itime, err := s.sat.GetSystemTime(ctx)
			if err != nil || itime == nil || !itime.IsValid {
				continue
			}

			// Parse the epoch_utc RFC3339 string.
			satTime, err := time.Parse(time.RFC3339, itime.EpochUTC)
			if err != nil {
				log.Warn().Err(err).Str("raw", itime.EpochUTC).Msg("timesync: msstm parse failed")
				continue
			}

			localNow := time.Now()
			offsetNs := satTime.UnixNano() - localNow.UnixNano()

			// MSSTM resolution is 90ms ticks.
			cb("msstm", 1, offsetNs, 90_000_000)

			log.Debug().
				Float64("offset_ms", float64(offsetNs)/1e6).
				Msg("timesync: msstm reading")
		}
	}
}

// ---------- Hub NTP Source (Stratum 2) ----------

// HubNTPSource receives time corrections from Hub via MQTT.
// The Hub publishes its NTP-synchronized time on a dedicated topic.
type HubNTPSource struct {
	// HubNTP is passive — it receives readings via InjectReading().
	// Start() just keeps the goroutine alive for context cancellation.
	readings chan hubNTPReading
}

type hubNTPReading struct {
	unixNanos int64
	stratum   int
}

// NewHubNTPSource creates a Hub NTP time source adapter.
func NewHubNTPSource() *HubNTPSource {
	return &HubNTPSource{
		readings: make(chan hubNTPReading, 8),
	}
}

func (s *HubNTPSource) Name() string { return "hub_ntp" }
func (s *HubNTPSource) Stratum() int { return 2 }

func (s *HubNTPSource) Start(ctx context.Context, cb CorrectionCallback) {
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-s.readings:
			localNow := time.Now()
			offsetNs := r.unixNanos - localNow.UnixNano()
			// Hub→bridge MQTT latency adds ~500ms uncertainty.
			cb("hub_ntp", r.stratum+1, offsetNs, 500_000_000)

			log.Debug().
				Float64("offset_ms", float64(offsetNs)/1e6).
				Msg("timesync: hub_ntp reading")
		}
	}
}

// InjectReading allows the MQTT handler to feed time readings from Hub.
func (s *HubNTPSource) InjectReading(unixNanos int64, stratum int) {
	select {
	case s.readings <- hubNTPReading{unixNanos: unixNanos, stratum: stratum}:
	default:
		// Channel full — drop reading.
	}
}
