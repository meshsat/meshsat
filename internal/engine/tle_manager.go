package engine

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/akhenakh/sgp4"
	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

const defaultCelestrakURL = "https://celestrak.org/NORAD/elements/gp.php?GROUP=iridium-NEXT&FORMAT=3le"

// PassSummary describes a single satellite pass.
type PassSummary struct {
	Satellite   string  `json:"satellite"`
	AOS         int64   `json:"aos"`
	LOS         int64   `json:"los"`
	DurationMin float64 `json:"duration_min"`
	PeakElevDeg float64 `json:"peak_elev_deg"`
	PeakAzimuth float64 `json:"peak_azimuth"`
	IsActive    bool    `json:"is_active"`
}

// TLEManager handles daily TLE refresh from Celestrak and SGP4-based pass prediction.
type TLEManager struct {
	db     *database.DB
	mu     sync.RWMutex
	tles   []database.TLECacheEntry
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewTLEManager creates a new TLE manager.
func NewTLEManager(db *database.DB) *TLEManager {
	return &TLEManager{db: db}
}

// Start launches the daily TLE refresh loop.
func (m *TLEManager) Start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)

	// Load cached TLEs from DB
	if cached, err := m.db.GetTLECache(); err == nil && len(cached) > 0 {
		m.mu.Lock()
		m.tles = cached
		m.mu.Unlock()
		log.Info().Int("count", len(cached)).Msg("TLE manager: loaded cached TLEs")
	}

	m.wg.Add(1)
	go m.refreshLoop(ctx)

	log.Info().Msg("TLE manager started")
}

// Stop cancels the manager and waits for goroutines to exit.
func (m *TLEManager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	log.Info().Msg("TLE manager stopped")
}

func (m *TLEManager) refreshLoop(ctx context.Context) {
	defer m.wg.Done()

	// Check if refresh is needed (> 24h since last fetch)
	age, _ := m.db.GetTLECacheAge()
	if age < 0 || age > 86400 {
		if err := m.RefreshTLEs(ctx); err != nil {
			log.Warn().Err(err).Msg("TLE manager: initial refresh failed")
		}
	}

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.RefreshTLEs(ctx); err != nil {
				log.Warn().Err(err).Msg("TLE manager: daily refresh failed")
			}
		}
	}
}

// RefreshTLEs fetches TLEs from Celestrak and stores them in the database.
func (m *TLEManager) RefreshTLEs(ctx context.Context) error {
	url := os.Getenv("CELESTRAK_IRIDIUM_URL")
	if url == "" {
		url = defaultCelestrakURL
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch TLEs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("celestrak returned %d", resp.StatusCode)
	}

	// Parse 3-line format: name, line1, line2
	var entries []database.TLECacheEntry
	now := time.Now().Unix()
	scanner := bufio.NewScanner(resp.Body)
	var lines []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lines = append(lines, line)
		if len(lines) == 3 {
			entries = append(entries, database.TLECacheEntry{
				SatelliteName: lines[0],
				Line1:         lines[1],
				Line2:         lines[2],
				FetchedAt:     now,
			})
			lines = nil
		}
	}

	if len(entries) == 0 {
		return fmt.Errorf("no TLEs parsed from response")
	}

	if err := m.db.ReplaceTLECache(entries); err != nil {
		return fmt.Errorf("store TLEs: %w", err)
	}

	m.mu.Lock()
	m.tles = entries
	m.mu.Unlock()

	log.Info().Int("count", len(entries)).Msg("TLE manager: refreshed from Celestrak")
	return nil
}

// GeneratePasses computes satellite passes for a ground location.
// altKm is altitude in kilometers.
// startTime, if non-zero, sets the window start (unix seconds); otherwise uses now.
func (m *TLEManager) GeneratePasses(lat, lon, altKm float64, hours int, minElevDeg float64, startTime int64) ([]PassSummary, error) {
	m.mu.RLock()
	tles := m.tles
	m.mu.RUnlock()

	if len(tles) == 0 {
		return nil, fmt.Errorf("no TLE data available — trigger a refresh")
	}

	if hours <= 0 || hours > 72 {
		hours = 24
	}
	if minElevDeg <= 0 {
		minElevDeg = 5.0
	}

	var now time.Time
	if startTime > 0 {
		now = time.Unix(startTime, 0).UTC()
	} else {
		now = time.Now().UTC()
	}
	end := now.Add(time.Duration(hours) * time.Hour)

	var passes []PassSummary

	for _, entry := range tles {
		tleInput := entry.Line1 + "\n" + entry.Line2
		tle, err := sgp4.ParseTLE(tleInput)
		if err != nil {
			continue // skip invalid TLEs
		}

		// Use the library's built-in pass prediction (60s step)
		libPasses, err := tle.GeneratePasses(lat, lon, altKm*1000, now, end, 60)
		if err != nil {
			continue
		}

		for _, p := range libPasses {
			if p.MaxElevation < minElevDeg {
				continue
			}
			passes = append(passes, PassSummary{
				Satellite:   entry.SatelliteName,
				AOS:         p.AOS.Unix(),
				LOS:         p.LOS.Unix(),
				DurationMin: p.Duration.Minutes(),
				PeakElevDeg: p.MaxElevation,
				PeakAzimuth: p.MaxElevationAz,
				IsActive:    p.AOS.Before(time.Now().UTC()) && p.LOS.After(time.Now().UTC()),
			})
		}
	}

	// Sort by AOS
	sort.Slice(passes, func(i, j int) bool { return passes[i].AOS < passes[j].AOS })

	return passes, nil
}

// CacheAge returns the age of the TLE cache in seconds, or -1 if empty.
func (m *TLEManager) CacheAge() (int64, error) {
	return m.db.GetTLECacheAge()
}
