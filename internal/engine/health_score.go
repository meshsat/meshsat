package engine

import (
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// HealthScore represents the composite health of a transport interface.
type HealthScore struct {
	InterfaceID string  `json:"interface_id"`
	Score       int     `json:"score"`        // 0-100
	Signal      int     `json:"signal"`       // 0-100 normalized signal
	SuccessRate float64 `json:"success_rate"` // delivery success ratio
	Latency     int     `json:"latency_ms"`   // avg delivery latency
	Cost        int     `json:"cost_score"`   // 100 = free, 0 = expensive
	Available   bool    `json:"available"`    // interface online?
}

// HealthScorer computes composite health scores for transport interfaces.
type HealthScorer struct {
	db *database.DB
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

	// Signal: get latest signal value from signal_history, normalize to 0-100
	hs.Signal = h.getSignalScore(interfaceID)

	// Success rate: sent / (sent + failed) from message_deliveries in last 24h
	hs.SuccessRate = h.getSuccessRate(interfaceID)

	// Latency: average time from queued to sent in last 24h
	hs.Latency = h.getAvgLatency(interfaceID)

	// Cost score based on channel type
	hs.Cost = channelCostScore(iface.ChannelType)

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

func (h *HealthScorer) getSignalScore(interfaceID string) int {
	point, err := h.db.GetLatestSignal(interfaceID)
	if err != nil {
		return 0
	}
	// signal_history value is typically 0-5 bars; normalize to 0-100
	val := int(point.Value * 20)
	if val > 100 {
		val = 100
	}
	if val < 0 {
		val = 0
	}
	return val
}

func (h *HealthScorer) getSuccessRate(interfaceID string) float64 {
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
		return 1.0 // no data = assume healthy
	}
	return float64(sent) / float64(total)
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
// 100 = free, 0 = expensive.
func channelCostScore(channelType string) int {
	switch channelType {
	case "mesh", "mqtt", "webhook":
		return 100
	case "cellular", "sms":
		return 60
	case "iridium":
		return 30
	default:
		return 50
	}
}
