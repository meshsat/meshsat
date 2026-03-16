package engine

import (
	"sync"

	"github.com/rs/zerolog/log"
)

// LatLon represents a geographic coordinate.
type LatLon struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// GeofenceZone defines a polygonal geographic area with alert triggers.
type GeofenceZone struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Polygon []LatLon `json:"polygon"` // ordered vertices
	AlertOn string   `json:"alert_on"` // "enter", "exit", "both"
	Message string   `json:"message"`  // alert message template
}

// GeofenceEvent records a node entering or exiting a zone.
type GeofenceEvent struct {
	Zone   GeofenceZone `json:"zone"`
	NodeID string       `json:"node_id"`
	Event  string       `json:"event"` // "enter" or "exit"
}

// GeofenceMonitor tracks node positions against configured geofence zones
// and detects enter/exit transitions.
type GeofenceMonitor struct {
	zones    []GeofenceZone
	inside   map[string]map[string]bool // zone_id -> node_id -> was_inside
	callback func(zone GeofenceZone, nodeID string, event string)
	mu       sync.RWMutex
}

// NewGeofenceMonitor creates a new geofence monitor.
func NewGeofenceMonitor() *GeofenceMonitor {
	return &GeofenceMonitor{
		inside: make(map[string]map[string]bool),
	}
}

// AddZone adds a geofence zone.
func (g *GeofenceMonitor) AddZone(zone GeofenceZone) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.zones = append(g.zones, zone)
	g.inside[zone.ID] = make(map[string]bool)
	log.Info().Str("id", zone.ID).Str("name", zone.Name).Int("vertices", len(zone.Polygon)).Msg("geofence zone added")
}

// RemoveZone removes a geofence zone by ID.
func (g *GeofenceMonitor) RemoveZone(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, z := range g.zones {
		if z.ID == id {
			g.zones = append(g.zones[:i], g.zones[i+1:]...)
			delete(g.inside, id)
			log.Info().Str("id", id).Msg("geofence zone removed")
			return
		}
	}
}

// GetZones returns a copy of all configured zones.
func (g *GeofenceMonitor) GetZones() []GeofenceZone {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]GeofenceZone, len(g.zones))
	copy(result, g.zones)
	return result
}

// CheckPosition evaluates a node's position against all zones and returns
// any enter/exit events. Transitions are detected by comparing current
// inside/outside state against the previous state for each zone.
func (g *GeofenceMonitor) CheckPosition(nodeID string, lat, lon float64) []GeofenceEvent {
	g.mu.Lock()
	defer g.mu.Unlock()

	var events []GeofenceEvent

	for _, zone := range g.zones {
		nowInside := pointInPolygon(lat, lon, zone.Polygon)
		wasInside := g.inside[zone.ID][nodeID]

		if nowInside && !wasInside {
			// Enter transition
			if zone.AlertOn == "enter" || zone.AlertOn == "both" {
				event := GeofenceEvent{Zone: zone, NodeID: nodeID, Event: "enter"}
				events = append(events, event)
				if g.callback != nil {
					g.callback(zone, nodeID, "enter")
				}
			}
			if g.inside[zone.ID] == nil {
				g.inside[zone.ID] = make(map[string]bool)
			}
			g.inside[zone.ID][nodeID] = true
		} else if !nowInside && wasInside {
			// Exit transition
			if zone.AlertOn == "exit" || zone.AlertOn == "both" {
				event := GeofenceEvent{Zone: zone, NodeID: nodeID, Event: "exit"}
				events = append(events, event)
				if g.callback != nil {
					g.callback(zone, nodeID, "exit")
				}
			}
			g.inside[zone.ID][nodeID] = false
		}
	}

	return events
}

// SetCallback sets the function called when a geofence event occurs.
func (g *GeofenceMonitor) SetCallback(fn func(zone GeofenceZone, nodeID string, event string)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.callback = fn
}

// pointInPolygon implements the ray casting algorithm to determine if a point
// is inside a polygon defined by its vertices.
func pointInPolygon(lat, lon float64, polygon []LatLon) bool {
	n := len(polygon)
	if n < 3 {
		return false
	}

	inside := false
	j := n - 1

	for i := 0; i < n; i++ {
		yi, xi := polygon[i].Lat, polygon[i].Lon
		yj, xj := polygon[j].Lat, polygon[j].Lon

		// Ray casting: check if a horizontal ray from (lat, lon) going east
		// crosses the edge between vertices i and j.
		if ((yi > lat) != (yj > lat)) &&
			(lon < (xj-xi)*(lat-yi)/(yj-yi)+xi) {
			inside = !inside
		}
		j = i
	}

	return inside
}
