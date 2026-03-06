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

const defaultAstrocastCelestrakURL = "https://celestrak.org/NORAD/elements/gp.php?GROUP=astrocast&FORMAT=3le"

// AstrocastTLEManager handles daily TLE refresh for the Astrocast LEO constellation.
type AstrocastTLEManager struct {
	db     *database.DB
	mu     sync.RWMutex
	tles   []database.TLECacheEntry
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewAstrocastTLEManager creates a new Astrocast TLE manager.
func NewAstrocastTLEManager(db *database.DB) *AstrocastTLEManager {
	return &AstrocastTLEManager{db: db}
}

// Start launches the daily TLE refresh loop.
func (m *AstrocastTLEManager) Start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)

	if cached, err := m.db.GetAstrocastTLECache(); err == nil && len(cached) > 0 {
		m.mu.Lock()
		m.tles = cached
		m.mu.Unlock()
		log.Info().Int("count", len(cached)).Msg("astrocast TLE manager: loaded cached TLEs")
	}

	m.wg.Add(1)
	go m.refreshLoop(ctx)

	log.Info().Msg("astrocast TLE manager started")
}

// Stop cancels the manager.
func (m *AstrocastTLEManager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	log.Info().Msg("astrocast TLE manager stopped")
}

func (m *AstrocastTLEManager) refreshLoop(ctx context.Context) {
	defer m.wg.Done()

	age, _ := m.db.GetAstrocastTLECacheAge()
	if age < 0 || age > 86400 {
		if err := m.RefreshTLEs(ctx); err != nil {
			log.Warn().Err(err).Msg("astrocast TLE manager: initial refresh failed")
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
				log.Warn().Err(err).Msg("astrocast TLE manager: daily refresh failed")
			}
		}
	}
}

// RefreshTLEs fetches Astrocast TLEs from Celestrak and stores them.
func (m *AstrocastTLEManager) RefreshTLEs(ctx context.Context) error {
	url := os.Getenv("CELESTRAK_ASTROCAST_URL")
	if url == "" {
		url = defaultAstrocastCelestrakURL
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch Astrocast TLEs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("celestrak returned %d", resp.StatusCode)
	}

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
		return fmt.Errorf("no Astrocast TLEs parsed from response")
	}

	if err := m.db.ReplaceAstrocastTLECache(entries); err != nil {
		return fmt.Errorf("store Astrocast TLEs: %w", err)
	}

	m.mu.Lock()
	m.tles = entries
	m.mu.Unlock()

	log.Info().Int("count", len(entries)).Msg("astrocast TLE manager: refreshed from Celestrak")
	return nil
}

// GeneratePasses computes Astrocast satellite passes for a ground location.
func (m *AstrocastTLEManager) GeneratePasses(lat, lon, altKm float64, hours int, minElevDeg float64, startTime int64) ([]PassSummary, error) {
	m.mu.RLock()
	tles := m.tles
	m.mu.RUnlock()

	if len(tles) == 0 {
		return nil, fmt.Errorf("no Astrocast TLE data available — trigger a refresh")
	}

	if hours <= 0 || hours > 72 {
		hours = 24
	}
	if minElevDeg <= 0 {
		minElevDeg = 5.0
	}
	if altKm > 10.0 || altKm < 0 {
		altKm = 0.0
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
			continue
		}

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

	sort.Slice(passes, func(i, j int) bool { return passes[i].AOS < passes[j].AOS })

	return passes, nil
}

// CacheAge returns the age of the Astrocast TLE cache in seconds, or -1 if empty.
func (m *AstrocastTLEManager) CacheAge() (int64, error) {
	return m.db.GetAstrocastTLECacheAge()
}
