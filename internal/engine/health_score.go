package engine

import (
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// HealthScore represents the composite health of a transport interface.
type HealthScore struct {
	InterfaceID string `json:"interface_id"`
	Score       int    `json:"score"`  // 0-100
	Signal      int    `json:"signal"` // 0-100 normalized signal
	// SignalSource is where Signal came from ("sbd", "imt", "cellular"),
	// empty when the link has no signal input or no fresh reading.
	SignalSource string  `json:"signal_source,omitempty"`
	SuccessRate  float64 `json:"success_rate"` // delivery success ratio
	// Samples is how many finished deliveries SuccessRate rests on (24 h);
	// 0 means no data, and the rate then reads 1.0 by design.
	Samples   int  `json:"samples"`
	Latency   int  `json:"latency_ms"` // avg delivery latency
	Cost      int  `json:"cost_score"` // 100 = free, 0 = expensive
	Available bool `json:"available"`  // interface online?
}

// SpectrumChecker checks if an interface is currently jammed.
type SpectrumChecker interface {
	IsJammed(interfaceID string) bool
}

// ReceiveChecker reports an interface whose receiver is known to be deaf
// (the APRS receive watchdog, MESHSAT-814). A deaf receiver scores 0 so the
// dispatcher's failover groups route around it.
type ReceiveChecker interface {
	ReceiveDeaf(interfaceID string) bool
}

// ReceiveCheckers composes several checkers: an interface is deaf when any
// of them says so (the APRS receive watchdog and the device health
// watchdog share the single HealthScorer hook). [MESHSAT-817]
type ReceiveCheckers []ReceiveChecker

// ReceiveDeaf implements ReceiveChecker.
func (c ReceiveCheckers) ReceiveDeaf(interfaceID string) bool {
	for _, rc := range c {
		if rc != nil && rc.ReceiveDeaf(interfaceID) {
			return true
		}
	}
	return false
}

// HealthScorer computes composite health scores for transport interfaces.
type HealthScorer struct {
	db       *database.DB
	mu       sync.RWMutex // the checkers are set late in main, read by the intents
	spectrum SpectrumChecker
	receive  ReceiveChecker
}

// SetSpectrumChecker sets the spectrum monitor for jamming-aware health scoring.
func (h *HealthScorer) SetSpectrumChecker(sc SpectrumChecker) {
	h.mu.Lock()
	h.spectrum = sc
	h.mu.Unlock()
}

// SetReceiveChecker sets the receive watchdog for deaf-aware health scoring.
func (h *HealthScorer) SetReceiveChecker(rc ReceiveChecker) {
	h.mu.Lock()
	h.receive = rc
	h.mu.Unlock()
}

// RouteExcluded implements RouteGate: a link whose band is jammed (a fresh
// verdict; the monitor reads a stale one as unknown) or whose receiver is
// deaf is not tried. Nothing else excludes, the composite score least of
// all. [MESHSAT-1650]
func (h *HealthScorer) RouteExcluded(interfaceID string) (bool, string) {
	h.mu.RLock()
	sc, rc := h.spectrum, h.receive
	h.mu.RUnlock()
	if sc != nil && sc.IsJammed(interfaceID) {
		return true, "jammed"
	}
	if rc != nil && rc.ReceiveDeaf(interfaceID) {
		return true, "deaf"
	}
	return false, ""
}

// NewHealthScorer creates a new health scorer.
func NewHealthScorer(db *database.DB) *HealthScorer {
	return &HealthScorer{db: db}
}

// Score computes the health score for a single interface.
func (h *HealthScorer) Score(interfaceID string) HealthScore {
	hs := HealthScore{
		InterfaceID: interfaceID,
	}

	// Check interface availability
	iface, err := h.db.GetInterface(interfaceID)
	if err != nil {
		log.Debug().Err(err).Str("interface", interfaceID).Msg("health score: interface not found")
		return hs
	}
	hs.Available = iface.Enabled

	// Signal: the link's modem reading, normalized to 0-100
	hs.Signal, hs.SignalSource = h.getSignalScore(iface.ChannelType)

	// Success rate: sent / (sent + failed) from message_deliveries in last 24h
	hs.SuccessRate, hs.Samples = h.getSuccessRate(interfaceID)

	// Latency: average time from queued to sent in last 24h
	hs.Latency = h.getAvgLatency(interfaceID)

	// Cost score based on channel type
	hs.Cost = channelCostScore(iface.ChannelType)

	// A jammed band or a deaf receiver scores 0: as useless as each other
	if excluded, _ := h.RouteExcluded(interfaceID); excluded {
		hs.Signal = 0
		hs.Score = 0
		return hs
	}

	// Composite score
	if !hs.Available {
		hs.Score = 0
	} else {
		latencyScore := 100 - min(hs.Latency/1000, 100)
		hs.Score = int(
			float64(hs.Signal)*0.3 +
				hs.SuccessRate*100*0.3 +
				float64(latencyScore)*0.2 +
				float64(hs.Cost)*0.2,
		)
	}

	return hs
}

// ScoreAll computes health scores for all registered interfaces.
func (h *HealthScorer) ScoreAll() []HealthScore {
	ifaces, err := h.db.GetAllInterfaces()
	if err != nil {
		log.Error().Err(err).Msg("health score: failed to list interfaces")
		return nil
	}

	scores := make([]HealthScore, 0, len(ifaces))
	for _, iface := range ifaces {
		scores = append(scores, h.Score(iface.ID))
	}
	return scores
}

// getSignalScore reads the link's modem signal from where it is recorded,
// never under the interface id (nothing writes that): the SignalRecorder
// writes bars per modem in signal_history ("sbd", or "iridium" before the
// modem split, and "imt"), the cellular recorder writes
// cellular_signal_history. The GSS rows there are session outcomes, not
// bars. A link with no signal input, or no reading in ten minutes, scores
// 0 with no source. [MESHSAT-1653]
func (h *HealthScorer) getSignalScore(channelType string) (int, string) {
	var bars float64
	var source string
	switch channelType {
	case "iridium", "iridium_imt":
		sources := []string{"sbd", "iridium"}
		if channelType == "iridium_imt" {
			sources = []string{"imt"}
		}
		p, err := h.db.GetLatestSignal(sources...)
		if err != nil {
			return 0, ""
		}
		bars, source = p.Value, p.Source
	case "cellular", "sms":
		p, err := h.db.GetLatestCellularSignal()
		if err != nil {
			return 0, ""
		}
		bars, source = float64(p.Bars), "cellular"
	default:
		return 0, ""
	}
	// bars are 0-5; normalize to 0-100
	return min(max(int(bars*20), 0), 100), source
}

// getSuccessRate is sent / (sent + failed) over 24 h and how many
// deliveries it rests on; with none it is 1.0 (a quiet link isn't a
// failing one).
func (h *HealthScorer) getSuccessRate(interfaceID string) (float64, int) {
	cutoff := time.Now().Add(-24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	var sent, failed int
	_ = h.db.QueryRow(
		`SELECT COALESCE(SUM(CASE WHEN status='sent' OR status='delivered' THEN 1 ELSE 0 END), 0),
		        COALESCE(SUM(CASE WHEN status='failed' OR status='dead' THEN 1 ELSE 0 END), 0)
		 FROM message_deliveries WHERE channel = ? AND created_at >= ?`,
		interfaceID, cutoff,
	).Scan(&sent, &failed)

	total := sent + failed
	if total == 0 {
		return 1.0, 0 // no data = assume healthy
	}
	return float64(sent) / float64(total), total
}

func (h *HealthScorer) getAvgLatency(interfaceID string) int {
	cutoff := time.Now().Add(-24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	var avgMs float64
	err := h.db.QueryRow(
		`SELECT COALESCE(AVG(
			(julianday(updated_at) - julianday(created_at)) * 86400000
		), 0)
		 FROM message_deliveries
		 WHERE channel = ? AND status IN ('sent','delivered') AND created_at >= ?`,
		interfaceID, cutoff,
	).Scan(&avgMs)
	if err != nil {
		return 0
	}
	return int(avgMs)
}

// channelCostScore returns a cost score based on channel type.
// 100 = free, 0 = expensive. The metered links are the ones the intent
// engine bills (Iridium SBD and IMT, cellular SMS); every other radio is
// free, the MeshSat 2G cell included (our own network). [MESHSAT-1653]
func channelCostScore(channelType string) int {
	switch channelType {
	case "mesh", "mqtt", "webhook", "tetra", "aprs", "ax25", "zigbee", "ble", "tcp",
		"lxmf", "hf", "tak", "hub", "cell", "rnode", "udp", "auto", "kiss":
		return 100
	case "cellular", "sms":
		return 60
	case "iridium", "iridium_imt":
		return 30
	default:
		return 50
	}
}
