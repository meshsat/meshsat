// Package demo is the public demonstration of the Bridge (cmd/meshsat-demo):
// the Bridge's own web app over made-up API answers that always read as
// now, and the Community Post's real portal in its demo mode. It holds
// nothing that reaches a radio, a host, a modem or the network: no route of
// internal/api, no transport, no gateway. [MESHSAT-1845]
package demo

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io/fs"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed fixtures/*.json
var fixtureFiles embed.FS

// FixtureEpoch is the moment the fixtures describe: every time in them is
// shifted by now minus this, so "2 min ago" stays two minutes ago.
var FixtureEpoch = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

// Fixtures are the made-up API answers, one per API path with "/" written
// as "_" (the accessibility harness's naming, test/a11y/serve.mjs).
type Fixtures struct {
	raw map[string][]byte
}

// LoadFixtures reads the embedded fixtures and refuses one that is not JSON.
func LoadFixtures() (*Fixtures, error) {
	return loadFixtures(fixtureFiles, "fixtures")
}

func loadFixtures(fsys fs.FS, dir string) (*Fixtures, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	f := &Fixtures{raw: map[string][]byte{}}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		b, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return nil, err
		}
		if !json.Valid(b) {
			return nil, fmt.Errorf("fixture %s is not JSON", name)
		}
		f.raw[strings.TrimSuffix(name, ".json")] = b
	}
	return f, nil
}

// Keys lists the fixture keys, sorted.
func (f *Fixtures) Keys() []string {
	out := make([]string, 0, len(f.raw))
	for k := range f.raw {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Has answers whether a fixture exists for key.
func (f *Fixtures) Has(key string) bool { _, ok := f.raw[key]; return ok }

// At is the fixture for key with every time in it moved to now. The
// Iridium passes are not a fixture: they are made for now (Passes).
func (f *Fixtures) At(key string, now time.Time) ([]byte, bool) {
	if key == "iridium_passes" {
		b, err := json.Marshal(Passes(now))
		return b, err == nil
	}
	b, ok := f.raw[key]
	if !ok {
		return nil, false
	}
	out, err := Shift(b, now.Truncate(time.Second).Sub(FixtureEpoch))
	if err != nil {
		return b, true
	}
	return out, true
}

var isoTime = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)$`)

// epochKeys hold Unix seconds; a number in them inside the plausible range
// moves with the clock. Other numbers are left alone.
var epochKeys = map[string]bool{"aos": true, "los": true, "timestamp": true, "rx_time": true, "ts": true, "time": true}

func isEpochKey(k string) bool {
	return epochKeys[k] || strings.HasPrefix(k, "last_") || strings.HasSuffix(k, "_at") || strings.HasSuffix(k, "_time")
}

const (
	minEpoch = 1.6e9 // 2020
	maxEpoch = 2.1e9 // 2036
)

// Shift moves every time in a JSON document by d: RFC 3339 strings
// (written back in the same form) and Unix seconds under the time keys.
// Go's zero time and anything before 2000 stay as they are.
func Shift(doc []byte, d time.Duration) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	v = shiftValue(v, "", d)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func shiftValue(v any, key string, d time.Duration) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = shiftValue(e, k, d)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = shiftValue(e, key, d)
		}
		return x
	case string:
		if !isoTime.MatchString(x) {
			return x
		}
		t, err := time.Parse(time.RFC3339Nano, x)
		if err != nil || t.Year() < 2000 {
			return x
		}
		layout := time.RFC3339
		if strings.Contains(x, ".") {
			layout = time.RFC3339Nano
		}
		return t.Add(d).In(t.Location()).Format(layout)
	case json.Number:
		if !isEpochKey(key) {
			return x
		}
		n, err := x.Int64()
		if err != nil || n < minEpoch || n > maxEpoch {
			return x
		}
		return json.Number(fmt.Sprint(n + int64(d/time.Second)))
	}
	return v
}

// Pass is one Iridium pass, as GET /api/iridium/passes answers it.
type Pass struct {
	Satellite   string  `json:"satellite"`
	AOS         int64   `json:"aos"`
	LOS         int64   `json:"los"`
	DurationMin float64 `json:"duration_min"`
	PeakElevDeg float64 `json:"peak_elev_deg"`
	PeakAzimuth float64 `json:"peak_azimuth"`
	IsActive    bool    `json:"is_active"`
}

// PassesAnswer is the whole answer of GET /api/iridium/passes.
type PassesAnswer struct {
	Passes     []Pass `json:"passes"`
	TLESource  string `json:"tle_source"`
	TLEAgeSec  int    `json:"tle_age_sec"`
	CacheAgeSc int    `json:"cache_age_sec"`
}

// passSlot is the spacing of the made-up passes: about one usable pass
// every 40 minutes, as a kit under an open sky sees above 10 degrees.
const passSlot = 40 * time.Minute

// Passes makes the Iridium passes under way or to come in the next day.
// Each 40-minute slot has one pass whose start, length, elevation and
// azimuth come from a hash of the slot, so every request agrees with the
// last and the schedule never runs out.
func Passes(now time.Time) PassesAnswer {
	now = now.UTC()
	first := now.Add(-time.Hour).Unix() / int64(passSlot/time.Second)
	last := now.Add(24*time.Hour).Unix() / int64(passSlot/time.Second)
	out := PassesAnswer{TLESource: "bundled", TLEAgeSec: 3600, CacheAgeSc: 60, Passes: []Pass{}}
	for slot := first; slot <= last; slot++ {
		h := fnv.New64a()
		fmt.Fprintf(h, "pass-%d", slot)
		r := h.Sum64()
		pick := func(lo, hi float64, shift uint) float64 {
			return lo + float64((r>>shift)&0xffff)/65535*(hi-lo)
		}
		start := slot*int64(passSlot/time.Second) + int64(pick(0, 25*60, 0))
		dur := pick(6.5, 11, 16)
		aos := start
		los := start + int64(dur*60)
		p := Pass{
			Satellite:   fmt.Sprintf("IRIDIUM %d", 100+int((r>>32)%80)),
			AOS:         aos,
			LOS:         los,
			DurationMin: math.Round(dur*10) / 10,
			PeakElevDeg: math.Round(pick(12, 78, 40)*10) / 10,
			PeakAzimuth: math.Round(pick(0, 359, 48)*10) / 10,
		}
		if los <= now.Unix() {
			continue // over; the API lists what is still to come or under way
		}
		p.IsActive = now.Unix() >= aos
		out.Passes = append(out.Passes, p)
	}
	return out
}
