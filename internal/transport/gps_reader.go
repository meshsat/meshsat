package transport

// GPSReader reads NMEA sentences from a u-blox GPS receiver via USB serial
// and stores positions in the database. Runs as a background goroutine.

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	gpsBaud        = 9600
	gpsReadTimeout = 2 * time.Second
	gpsPollPeriod  = 30 * time.Second // store a fix every 30s
)

// GPSPosition holds a parsed GPS fix.
type GPSPosition struct {
	Lat       float64
	Lon       float64
	AltM      float64
	Sats      int
	Fix       bool
	Timestamp time.Time
}

// GPSStore is the interface for persisting GPS positions.
type GPSStore interface {
	InsertGeolocation(source string, lat, lon, altKm, accuracyKm float64, timestamp int64) error
}

// GPSReader reads NMEA from a u-blox GPS serial port.
// GPSStatus holds the latest GPS fix metadata for API consumers.
type GPSStatus struct {
	Fix  bool
	Sats int
	AltM float64
	Lat  float64
	Lon  float64
	Time time.Time
}

// GPSReader reads NMEA from a u-blox GPS serial port.
type GPSReader struct {
	port         string // "auto" or explicit path
	excludePorts []func() string
	store        GPSStore

	mu     sync.RWMutex
	status GPSStatus
}

// NewGPSReader creates a GPS reader. Pass "auto" for port to use VID:PID detection.
func NewGPSReader(port string, store GPSStore) *GPSReader {
	return &GPSReader{
		port:  port,
		store: store,
	}
}

// SetExcludePortFuncs sets functions that return ports to exclude from auto-detect
// (e.g., Meshtastic and Iridium ports).
func (g *GPSReader) SetExcludePortFuncs(fns []func() string) {
	g.excludePorts = fns
}

// GetStatus returns the latest GPS fix metadata (thread-safe).
func (g *GPSReader) GetStatus() GPSStatus {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.status
}

// Start begins the GPS reader background loop. Blocks until ctx is cancelled.
func (g *GPSReader) Start(ctx context.Context) {
	for {
		port := g.resolvePort()
		if port == "" {
			log.Debug().Msg("gps: no GPS device found, retrying in 60s")
			select {
			case <-ctx.Done():
				return
			case <-time.After(60 * time.Second):
				continue
			}
		}

		log.Info().Str("port", port).Msg("gps: opening serial port")
		g.readLoop(ctx, port)

		// If readLoop returned, the port was lost — retry after delay
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func (g *GPSReader) resolvePort() string {
	if g.port != "" && g.port != "auto" {
		return g.port
	}
	return g.autoDetectGPS()
}

func (g *GPSReader) autoDetectGPS() string {
	excludes := make(map[string]bool)
	for _, fn := range g.excludePorts {
		if p := fn(); p != "" {
			excludes[p] = true
		}
	}

	var ports []string
	if matches, err := filepath.Glob("/dev/ttyACM*"); err == nil {
		ports = append(ports, matches...)
	}
	if matches, err := filepath.Glob("/dev/ttyUSB*"); err == nil {
		ports = append(ports, matches...)
	}

	for _, port := range ports {
		if excludes[port] {
			continue
		}
		vidpid := findUSBVIDPID(port)
		if gpsVIDPIDs[vidpid] {
			log.Info().Str("port", port).Str("vidpid", vidpid).Msg("gps: auto-detected by VID:PID")
			return port
		}
	}
	return ""
}

func (g *GPSReader) readLoop(ctx context.Context, portPath string) {
	sp, err := openSerial(portPath, gpsBaud)
	if err != nil {
		log.Error().Err(err).Str("port", portPath).Msg("gps: failed to open serial")
		return
	}
	defer sp.Close()

	// Set read timeout so scanner returns periodically for ctx/ticker checks.
	// When Read returns n=0,err=nil (timeout), Scanner.Scan() returns false
	// with nil error — we recreate the scanner and loop.
	sp.SetReadTimeout(gpsReadTimeout)
	scanner := bufio.NewScanner(sp)
	storeTicker := time.NewTicker(gpsPollPeriod)
	defer storeTicker.Stop()

	var lastFix GPSPosition
	var hasFix bool

	for {
		select {
		case <-ctx.Done():
			return
		case <-storeTicker.C:
			if hasFix && lastFix.Fix {
				g.storeFix(lastFix)
			}
		default:
		}

		if !scanner.Scan() {
			if ctx.Err() != nil {
				return
			}
			if err := scanner.Err(); err != nil {
				log.Warn().Err(err).Msg("gps: serial read error")
				return
			}
			// Scan returned false with nil error = read timeout (no data)
			scanner = bufio.NewScanner(sp)
			continue
		}

		line := scanner.Text()
		if pos, ok := parseNMEA(line); ok {
			lastFix = pos
			hasFix = true
			g.mu.Lock()
			g.status = GPSStatus{
				Fix:  pos.Fix,
				Sats: pos.Sats,
				AltM: pos.AltM,
				Lat:  pos.Lat,
				Lon:  pos.Lon,
				Time: pos.Timestamp,
			}
			g.mu.Unlock()
		}
	}
}

func (g *GPSReader) storeFix(pos GPSPosition) {
	altKm := pos.AltM / 1000.0
	// HDOP-based accuracy: assume ~2.5m per satellite with good geometry
	accuracyKm := 0.005 // default 5m
	if pos.Sats > 0 && pos.Sats < 4 {
		accuracyKm = 0.015 // poor fix
	}

	if err := g.store.InsertGeolocation("gps", pos.Lat, pos.Lon, altKm, accuracyKm, pos.Timestamp.Unix()); err != nil {
		log.Warn().Err(err).Msg("gps: failed to store position")
		return
	}
	log.Debug().
		Float64("lat", pos.Lat).
		Float64("lon", pos.Lon).
		Float64("alt_m", pos.AltM).
		Int("sats", pos.Sats).
		Msg("gps: position stored")
}

// parseNMEA parses a single NMEA sentence. Returns a GPSPosition if valid fix found.
func parseNMEA(line string) (GPSPosition, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "$") {
		return GPSPosition{}, false
	}

	// Validate checksum
	if !validateNMEAChecksum(line) {
		return GPSPosition{}, false
	}

	// Strip checksum for parsing
	if idx := strings.IndexByte(line, '*'); idx >= 0 {
		line = line[:idx]
	}

	fields := strings.Split(line, ",")
	if len(fields) < 2 {
		return GPSPosition{}, false
	}

	switch {
	case strings.HasSuffix(fields[0], "GGA"):
		return parseGGA(fields)
	case strings.HasSuffix(fields[0], "RMC"):
		return parseRMC(fields)
	}
	return GPSPosition{}, false
}

// parseGGA parses a $GPGGA / $GNGGA sentence.
// $GPGGA,hhmmss.ss,lat,N,lon,E,fix,sats,hdop,alt,M,geoid,M,,*cs
func parseGGA(f []string) (GPSPosition, bool) {
	if len(f) < 15 {
		return GPSPosition{}, false
	}

	fix, _ := strconv.Atoi(f[6])
	if fix == 0 {
		return GPSPosition{}, false // no fix
	}

	lat, ok1 := parseNMEACoord(f[2], f[3])
	lon, ok2 := parseNMEACoord(f[4], f[5])
	if !ok1 || !ok2 {
		return GPSPosition{}, false
	}

	alt, _ := strconv.ParseFloat(f[9], 64)
	sats, _ := strconv.Atoi(f[7])
	ts := parseNMEATime(f[1], "")

	return GPSPosition{
		Lat:       lat,
		Lon:       lon,
		AltM:      alt,
		Sats:      sats,
		Fix:       true,
		Timestamp: ts,
	}, true
}

// parseRMC parses a $GPRMC / $GNRMC sentence.
// $GPRMC,hhmmss.ss,A,lat,N,lon,E,speed,course,ddmmyy,...*cs
func parseRMC(f []string) (GPSPosition, bool) {
	if len(f) < 12 {
		return GPSPosition{}, false
	}

	if f[2] != "A" {
		return GPSPosition{}, false // V = void (no fix)
	}

	lat, ok1 := parseNMEACoord(f[3], f[4])
	lon, ok2 := parseNMEACoord(f[5], f[6])
	if !ok1 || !ok2 {
		return GPSPosition{}, false
	}

	ts := parseNMEATime(f[1], f[9])

	return GPSPosition{
		Lat:       lat,
		Lon:       lon,
		Fix:       true,
		Timestamp: ts,
	}, true
}

// parseNMEACoord parses "ddmm.mmmm" + "N/S/E/W" into decimal degrees.
func parseNMEACoord(raw, dir string) (float64, bool) {
	if raw == "" || dir == "" {
		return 0, false
	}

	// Find the degrees/minutes split point
	dotIdx := strings.IndexByte(raw, '.')
	if dotIdx < 3 {
		return 0, false
	}

	degStr := raw[:dotIdx-2]
	minStr := raw[dotIdx-2:]

	deg, err := strconv.ParseFloat(degStr, 64)
	if err != nil {
		return 0, false
	}
	min, err := strconv.ParseFloat(minStr, 64)
	if err != nil {
		return 0, false
	}

	result := deg + min/60.0

	if dir == "S" || dir == "W" {
		result = -result
	}

	// Sanity check
	if math.Abs(result) > 180 {
		return 0, false
	}

	return result, true
}

// parseNMEATime parses time (hhmmss.ss) and optional date (ddmmyy) into time.Time.
func parseNMEATime(timeStr, dateStr string) time.Time {
	if len(timeStr) < 6 {
		return time.Now().UTC()
	}

	h, _ := strconv.Atoi(timeStr[0:2])
	m, _ := strconv.Atoi(timeStr[2:4])
	s, _ := strconv.Atoi(timeStr[4:6])

	if dateStr != "" && len(dateStr) >= 6 {
		day, _ := strconv.Atoi(dateStr[0:2])
		mon, _ := strconv.Atoi(dateStr[2:4])
		yr, _ := strconv.Atoi(dateStr[4:6])
		yr += 2000
		return time.Date(yr, time.Month(mon), day, h, m, s, 0, time.UTC)
	}

	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), h, m, s, 0, time.UTC)
}

// validateNMEAChecksum verifies the XOR checksum after '*'.
func validateNMEAChecksum(line string) bool {
	starIdx := strings.IndexByte(line, '*')
	if starIdx < 0 || starIdx+3 > len(line) {
		return false
	}

	// XOR all bytes between $ and *
	var cksum byte
	for i := 1; i < starIdx; i++ {
		cksum ^= line[i]
	}

	expected, err := strconv.ParseUint(line[starIdx+1:starIdx+3], 16, 8)
	if err != nil {
		return false
	}

	return cksum == byte(expected)
}

// autoDetectGPSPort is a package-level helper for use from main.go.
func autoDetectGPSPort(excludePorts []func() string) string {
	excludes := make(map[string]bool)
	for _, fn := range excludePorts {
		if p := fn(); p != "" {
			excludes[p] = true
		}
	}

	var ports []string
	if matches, err := filepath.Glob("/dev/ttyACM*"); err == nil {
		ports = append(ports, matches...)
	}
	if matches, err := filepath.Glob("/dev/ttyUSB*"); err == nil {
		ports = append(ports, matches...)
	}

	for _, port := range ports {
		if excludes[port] {
			continue
		}
		vidpid := findUSBVIDPID(port)
		if gpsVIDPIDs[vidpid] {
			return port
		}
	}
	return ""
}

// FormatCoord formats a decimal degree coordinate for display.
func FormatCoord(deg float64, isLat bool) string {
	dir := "N"
	if !isLat {
		dir = "E"
	}
	if deg < 0 {
		deg = -deg
		if isLat {
			dir = "S"
		} else {
			dir = "W"
		}
	}
	return fmt.Sprintf("%.6f°%s", deg, dir)
}
