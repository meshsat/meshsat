package gateway

import (
	"sort"
	"sync"
	"time"
)

// HeardStation is a single APRS station observed over-the-air.
type HeardStation struct {
	Callsign    string  `json:"callsign"`
	LastHeard   int64   `json:"last_heard"` // unix seconds
	PacketCount int64   `json:"packet_count"`
	Lat         float64 `json:"lat,omitempty"`
	Lon         float64 `json:"lon,omitempty"`
	Symbol      string  `json:"symbol,omitempty"`
	Path        string  `json:"path,omitempty"`
	PacketType  string  `json:"packet_type,omitempty"`
	DistanceKm  float64 `json:"distance_km,omitempty"`
}

// ActivityBucket is one minute of packet count history.
type ActivityBucket struct {
	Timestamp int64 `json:"timestamp"` // unix seconds, floored to the minute
	RX        int   `json:"rx"`
	TX        int   `json:"tx"`
}

// PacketTypeCount tracks counts per APRS data type.
type PacketTypeCount struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

const activityBuckets = 30 // 30 minutes of history

// APRSTracker tracks heard stations and packet activity in memory.
type APRSTracker struct {
	mu         sync.RWMutex
	stations   map[string]*HeardStation
	activity   [activityBuckets]ActivityBucket
	typeCounts map[string]int64
	ownLat     float64
	ownLon     float64

	// Recent packets for digipeater path display
	recentPkts []recentPacket
}

type recentPacket struct {
	Callsign string `json:"callsign"`
	Path     string `json:"path"`
	Time     int64  `json:"time"`
}

// NewAPRSTracker creates a new tracker.
func NewAPRSTracker() *APRSTracker {
	return &APRSTracker{
		stations:   make(map[string]*HeardStation),
		typeCounts: make(map[string]int64),
	}
}

// SetOwnPosition sets the station's own position for distance calculation.
func (t *APRSTracker) SetOwnPosition(lat, lon float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ownLat = lat
	t.ownLon = lon
}

// RecordRX records a received APRS packet.
func (t *APRSTracker) RecordRX(pkt *APRSPacket) {
	if pkt == nil || pkt.Source == "" {
		return
	}
	now := time.Now()
	nowUnix := now.Unix()

	t.mu.Lock()
	defer t.mu.Unlock()

	// Upsert heard station
	st, ok := t.stations[pkt.Source]
	if !ok {
		st = &HeardStation{Callsign: pkt.Source}
		t.stations[pkt.Source] = st
	}
	st.LastHeard = nowUnix
	st.PacketCount++
	if pkt.Lat != 0 || pkt.Lon != 0 {
		st.Lat = pkt.Lat
		st.Lon = pkt.Lon
	}
	if pkt.Symbol != "" {
		st.Symbol = pkt.Symbol
	}
	if pkt.Path != "" {
		st.Path = pkt.Path
	}
	st.PacketType = classifyAPRSType(pkt.DataType)

	// Distance from own position
	if t.ownLat != 0 && t.ownLon != 0 && st.Lat != 0 && st.Lon != 0 {
		st.DistanceKm = DistanceKm(t.ownLat, t.ownLon, st.Lat, st.Lon)
	}

	// Packet type counter
	label := st.PacketType
	t.typeCounts[label]++

	// Activity bucket
	bucketIdx := int(nowUnix/60) % activityBuckets
	bucketTS := (nowUnix / 60) * 60
	b := &t.activity[bucketIdx]
	if b.Timestamp != bucketTS {
		b.Timestamp = bucketTS
		b.RX = 0
		b.TX = 0
	}
	b.RX++

	// Recent packets for path display (keep last 10)
	if pkt.Path != "" {
		t.recentPkts = append(t.recentPkts, recentPacket{
			Callsign: pkt.Source,
			Path:     pkt.Path,
			Time:     nowUnix,
		})
		if len(t.recentPkts) > 10 {
			t.recentPkts = t.recentPkts[len(t.recentPkts)-10:]
		}
	}
}

// RecordTX records a transmitted packet.
func (t *APRSTracker) RecordTX() {
	now := time.Now().Unix()
	t.mu.Lock()
	defer t.mu.Unlock()

	bucketIdx := int(now/60) % activityBuckets
	bucketTS := (now / 60) * 60
	b := &t.activity[bucketIdx]
	if b.Timestamp != bucketTS {
		b.Timestamp = bucketTS
		b.RX = 0
		b.TX = 0
	}
	b.TX++
}

// GetHeardStations returns all heard stations sorted by last heard (newest first).
func (t *APRSTracker) GetHeardStations() []HeardStation {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]HeardStation, 0, len(t.stations))
	for _, st := range t.stations {
		result = append(result, *st)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastHeard > result[j].LastHeard
	})
	return result
}

// GetActivity returns the last 30 minutes of packet activity, oldest first.
func (t *APRSTracker) GetActivity() []ActivityBucket {
	t.mu.RLock()
	defer t.mu.RUnlock()

	now := time.Now().Unix()
	currentIdx := int(now/60) % activityBuckets
	cutoff := now - int64(activityBuckets*60)

	result := make([]ActivityBucket, 0, activityBuckets)
	for i := 0; i < activityBuckets; i++ {
		idx := (currentIdx + 1 + i) % activityBuckets
		b := t.activity[idx]
		if b.Timestamp >= cutoff {
			result = append(result, b)
		} else {
			// Stale bucket — emit zero
			ts := now - int64((activityBuckets-1-i)*60)
			ts = (ts / 60) * 60
			result = append(result, ActivityBucket{Timestamp: ts})
		}
	}
	return result
}

// GetPacketTypeBreakdown returns packet type counts sorted by count descending.
func (t *APRSTracker) GetPacketTypeBreakdown() []PacketTypeCount {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]PacketTypeCount, 0, len(t.typeCounts))
	for label, count := range t.typeCounts {
		result = append(result, PacketTypeCount{Label: label, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Count > result[j].Count
	})
	return result
}

// GetRecentPaths returns the most recent packets with digipeater paths.
func (t *APRSTracker) GetRecentPaths() []recentPacket {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]recentPacket, len(t.recentPkts))
	copy(result, t.recentPkts)
	return result
}

// classifyAPRSType maps an APRS data type indicator byte to a human label.
func classifyAPRSType(b byte) string {
	switch b {
	case '!', '=', '/', '@':
		return "position"
	case ':':
		return "message"
	case ';':
		return "object"
	case ')':
		return "item"
	case '_':
		return "weather"
	case 'T':
		return "telemetry"
	case '>':
		return "status"
	case '?':
		return "query"
	case '{':
		return "user-defined"
	default:
		return "other"
	}
}
