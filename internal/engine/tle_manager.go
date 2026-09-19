package engine

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/akhenakh/sgp4"
	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

const defaultCelestrakURL = "https://celestrak.org/NORAD/elements/gp.php?GROUP=iridium-NEXT&FORMAT=3le"

// defaultTLEAPIURL is the fallback source when Celestrak cannot be reached (it drops some
// addresses outright): the same public element sets, searchable by name, paged. The page
// number is appended.
const defaultTLEAPIURL = "https://tle.ivanstanojevic.me/api/tle/?search=IRIDIUM&page-size=100&page="

const (
	tleUserAgent    = "MeshSat-Bridge (+https://meshsat.net)"
	tleAPIMaxPages  = 6
	tleMaxBodyBytes = 4 << 20
)

// iridiumNextName matches the Iridium NEXT satellites ("IRIDIUM 100".."IRIDIUM 199"); a
// name search also returns the retired first generation and debris.
var iridiumNextName = regexp.MustCompile(`^IRIDIUM 1\d\d$`)

// bundledTLEs is an Iridium NEXT snapshot shipped in the binary, so passes are predicted
// with no network at all even on a kit that never downloaded any. Iridium orbits drift
// slowly: a snapshot a few weeks old still places a pass within seconds.
//
//go:embed tledata/iridium-next.3le
var bundledTLEs string

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
	source string
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

	// Predict from the newer of the cached download and the shipped snapshot, so a kit
	// that never reached the internet still has passes.
	cached, _ := m.db.GetTLECache()
	tles, source := chooseTLEs(cached, parse3LE(bundledTLEs, 0))
	if len(tles) > 0 {
		m.mu.Lock()
		m.tles, m.source = tles, source
		m.mu.Unlock()
		log.Info().Int("count", len(tles)).Str("source", source).Msg("TLE manager: loaded TLEs")
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

// RefreshTLEs fetches TLEs, from Celestrak or else the TLE API, and stores them in the
// database. On failure the elements in use stay as they are.
func (m *TLEManager) RefreshTLEs(ctx context.Context) error {
	entries, err := fetchCelestrak(ctx)
	source := "celestrak"
	if err != nil {
		log.Warn().Err(err).Msg("TLE manager: Celestrak failed, trying the TLE API")
		var apiErr error
		entries, apiErr = fetchTLEAPI(ctx)
		if apiErr != nil {
			return fmt.Errorf("celestrak: %v; TLE API: %w", err, apiErr)
		}
		source = "tle-api"
	}

	if err := m.db.ReplaceTLECache(entries); err != nil {
		return fmt.Errorf("store TLEs: %w", err)
	}

	m.mu.Lock()
	m.tles, m.source = entries, source
	m.mu.Unlock()

	log.Info().Int("count", len(entries)).Str("source", source).Msg("TLE manager: refreshed TLEs")
	return nil
}

func tleGet(ctx context.Context, url string) (io.ReadCloser, error) {
	// #nosec G704 -- the URL is the kit's own configuration (env) or a constant, never request input
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", tleUserAgent)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req) // #nosec G704 -- see above
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// fetchCelestrak downloads the iridium-NEXT group in 3-line format.
func fetchCelestrak(ctx context.Context) ([]database.TLECacheEntry, error) {
	url := os.Getenv("CELESTRAK_IRIDIUM_URL")
	if url == "" {
		url = defaultCelestrakURL
	}
	body, err := tleGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch TLEs: %w", err)
	}
	defer func() { _ = body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(body, tleMaxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read TLEs: %w", err)
	}
	entries := parse3LE(string(raw), time.Now().Unix())
	if len(entries) == 0 {
		return nil, fmt.Errorf("no TLEs parsed from response")
	}
	return entries, nil
}

// tleAPIPage is one page of the TLE API's JSON-LD collection.
type tleAPIPage struct {
	Member []struct {
		Name  string `json:"name"`
		Line1 string `json:"line1"`
		Line2 string `json:"line2"`
	} `json:"member"`
	View struct {
		Next string `json:"next"`
	} `json:"view"`
}

// fetchTLEAPI pages through the TLE API's name search and keeps the Iridium NEXT sets.
func fetchTLEAPI(ctx context.Context) ([]database.TLECacheEntry, error) {
	base := os.Getenv("TLE_API_URL")
	if base == "" {
		base = defaultTLEAPIURL
	}
	now := time.Now().Unix()
	var entries []database.TLECacheEntry
	for page := 1; page <= tleAPIMaxPages; page++ {
		body, err := tleGet(ctx, base+strconv.Itoa(page))
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", page, err)
		}
		var p tleAPIPage
		err = json.NewDecoder(io.LimitReader(body, tleMaxBodyBytes)).Decode(&p)
		_ = body.Close()
		if err != nil {
			return nil, fmt.Errorf("page %d: decode: %w", page, err)
		}
		for _, m := range p.Member {
			name := strings.TrimSpace(m.Name)
			if !iridiumNextName.MatchString(name) {
				continue
			}
			entries = append(entries, database.TLECacheEntry{
				SatelliteName: name, Line1: strings.TrimSpace(m.Line1), Line2: strings.TrimSpace(m.Line2), FetchedAt: now,
			})
		}
		if p.View.Next == "" {
			break
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no Iridium NEXT elements in the response")
	}
	return entries, nil
}

// parse3LE reads name/line1/line2 triples.
func parse3LE(text string, fetchedAt int64) []database.TLECacheEntry {
	var entries []database.TLECacheEntry
	scanner := bufio.NewScanner(strings.NewReader(text))
	var lines []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lines = append(lines, line)
		if len(lines) == 3 {
			entries = append(entries, database.TLECacheEntry{
				SatelliteName: lines[0], Line1: lines[1], Line2: lines[2], FetchedAt: fetchedAt,
			})
			lines = nil
		}
	}
	return entries
}

// tleEpochUnix reads the epoch (columns 19-32, YYDDD.DDDDDDDD) of a TLE line 1.
func tleEpochUnix(line1 string) int64 {
	if len(line1) < 32 {
		return 0
	}
	yy, err1 := strconv.Atoi(strings.TrimSpace(line1[18:20]))
	day, err2 := strconv.ParseFloat(strings.TrimSpace(line1[20:32]), 64)
	if err1 != nil || err2 != nil {
		return 0
	}
	year := 2000 + yy
	if yy >= 57 {
		year = 1900 + yy
	}
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	return start.Add(time.Duration((day - 1) * float64(24*time.Hour))).Unix()
}

func newestEpoch(entries []database.TLECacheEntry) int64 {
	var newest int64
	for _, e := range entries {
		if t := tleEpochUnix(e.Line1); t > newest {
			newest = t
		}
	}
	return newest
}

// chooseTLEs prefers the downloaded cache unless the shipped snapshot is newer.
func chooseTLEs(cached, bundled []database.TLECacheEntry) ([]database.TLECacheEntry, string) {
	switch {
	case len(cached) > 0 && newestEpoch(cached) >= newestEpoch(bundled):
		return cached, "downloaded"
	case len(bundled) > 0:
		return bundled, "bundled"
	default:
		return nil, "none"
	}
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
	// Clamp altitude to sane ground-level max (10km).
	// AT-MSGEO can return satellite altitude (~780-5000km) which breaks SGP4.
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

// DataInfo says where the elements in use came from ("downloaded", "bundled", "celestrak",
// "tle-api" or "none") and how old the newest one is, in seconds (-1 without data).
func (m *TLEManager) DataInfo() (string, int64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.tles) == 0 {
		return "none", -1
	}
	return m.source, time.Now().Unix() - newestEpoch(m.tles)
}
