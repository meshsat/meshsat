package engine

import (
	"testing"
	"time"
)

// A simple square polygon around (0,0): corners at (-1,-1), (-1,1), (1,1), (1,-1)
var testSquare = []LatLon{
	{Lat: -1, Lon: -1},
	{Lat: -1, Lon: 1},
	{Lat: 1, Lon: 1},
	{Lat: 1, Lon: -1},
}

func TestPointInPolygon_Inside(t *testing.T) {
	if !pointInPolygon(0, 0, testSquare) {
		t.Fatal("center point should be inside square")
	}
	if !pointInPolygon(0.5, 0.5, testSquare) {
		t.Fatal("(0.5, 0.5) should be inside square")
	}
}

func TestPointInPolygon_Outside(t *testing.T) {
	if pointInPolygon(2, 2, testSquare) {
		t.Fatal("(2, 2) should be outside square")
	}
	if pointInPolygon(0, 2, testSquare) {
		t.Fatal("(0, 2) should be outside square")
	}
}

func TestPointInPolygon_TooFewVertices(t *testing.T) {
	if pointInPolygon(0, 0, []LatLon{{0, 0}, {1, 1}}) {
		t.Fatal("polygon with fewer than 3 vertices should return false")
	}
}

func TestGeofenceMonitor_EnterTransition(t *testing.T) {
	gm := NewGeofenceMonitor()
	gm.AddZone(GeofenceZone{
		ID:      "zone1",
		Name:    "Test Zone",
		Polygon: testSquare,
		AlertOn: "enter",
	})

	// First check: outside — no event
	events := gm.CheckPosition("node1", 5, 5)
	if len(events) != 0 {
		t.Fatalf("expected 0 events outside, got %d", len(events))
	}

	// Move inside — should trigger enter
	events = gm.CheckPosition("node1", 0, 0)
	if len(events) != 1 {
		t.Fatalf("expected 1 enter event, got %d", len(events))
	}
	if events[0].Event != "enter" {
		t.Fatalf("expected 'enter' event, got %q", events[0].Event)
	}

	// Stay inside — no new event
	events = gm.CheckPosition("node1", 0.5, 0.5)
	if len(events) != 0 {
		t.Fatalf("expected 0 events while staying inside, got %d", len(events))
	}
}

func TestGeofenceMonitor_ExitTransition(t *testing.T) {
	gm := NewGeofenceMonitor()
	gm.AddZone(GeofenceZone{
		ID:      "zone1",
		Name:    "Test Zone",
		Polygon: testSquare,
		AlertOn: "exit",
	})

	// Start inside
	gm.CheckPosition("node1", 0, 0)

	// Move outside — should trigger exit
	events := gm.CheckPosition("node1", 5, 5)
	if len(events) != 1 {
		t.Fatalf("expected 1 exit event, got %d", len(events))
	}
	if events[0].Event != "exit" {
		t.Fatalf("expected 'exit' event, got %q", events[0].Event)
	}
}

func TestGeofenceMonitor_BothAlertOn(t *testing.T) {
	gm := NewGeofenceMonitor()
	gm.AddZone(GeofenceZone{
		ID:      "zone1",
		Name:    "Both Zone",
		Polygon: testSquare,
		AlertOn: "both",
	})

	// Enter
	events := gm.CheckPosition("node1", 0, 0)
	if len(events) != 1 || events[0].Event != "enter" {
		t.Fatal("expected enter event")
	}

	// Exit
	events = gm.CheckPosition("node1", 5, 5)
	if len(events) != 1 || events[0].Event != "exit" {
		t.Fatal("expected exit event")
	}
}

func TestGeofenceMonitor_MultipleZones(t *testing.T) {
	gm := NewGeofenceMonitor()

	// Zone 1: around origin
	gm.AddZone(GeofenceZone{
		ID:      "zone1",
		Name:    "Zone 1",
		Polygon: testSquare,
		AlertOn: "both",
	})

	// Zone 2: around (10, 10)
	zone2Polygon := []LatLon{
		{Lat: 9, Lon: 9},
		{Lat: 9, Lon: 11},
		{Lat: 11, Lon: 11},
		{Lat: 11, Lon: 9},
	}
	gm.AddZone(GeofenceZone{
		ID:      "zone2",
		Name:    "Zone 2",
		Polygon: zone2Polygon,
		AlertOn: "both",
	})

	// Enter zone 1 only
	events := gm.CheckPosition("node1", 0, 0)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Zone.ID != "zone1" {
		t.Fatalf("expected zone1 event, got %s", events[0].Zone.ID)
	}

	// Move to zone 2 (exit zone 1, enter zone 2)
	events = gm.CheckPosition("node1", 10, 10)
	if len(events) != 2 {
		t.Fatalf("expected 2 events (exit z1 + enter z2), got %d", len(events))
	}
}

func TestGeofenceMonitor_Callback(t *testing.T) {
	gm := NewGeofenceMonitor()
	gm.AddZone(GeofenceZone{
		ID:      "zone1",
		Name:    "Test Zone",
		Polygon: testSquare,
		AlertOn: "both",
	})

	var cbEvents []string
	gm.SetCallback(func(zone GeofenceZone, nodeID string, event string) {
		cbEvents = append(cbEvents, event)
	})

	gm.CheckPosition("node1", 0, 0) // enter
	gm.CheckPosition("node1", 5, 5) // exit

	if len(cbEvents) != 2 {
		t.Fatalf("expected 2 callback events, got %d", len(cbEvents))
	}
	if cbEvents[0] != "enter" || cbEvents[1] != "exit" {
		t.Fatalf("expected [enter, exit], got %v", cbEvents)
	}
}

func TestGeofenceMonitor_RemoveZone(t *testing.T) {
	gm := NewGeofenceMonitor()
	gm.AddZone(GeofenceZone{
		ID:      "zone1",
		Name:    "Test Zone",
		Polygon: testSquare,
		AlertOn: "both",
	})

	gm.RemoveZone("zone1")
	zones := gm.GetZones()
	if len(zones) != 0 {
		t.Fatalf("expected 0 zones after removal, got %d", len(zones))
	}

	// Check should produce no events
	events := gm.CheckPosition("node1", 0, 0)
	if len(events) != 0 {
		t.Fatalf("expected 0 events after zone removal, got %d", len(events))
	}
}

func TestGeofenceMonitor_EnterOnlyNoExitEvent(t *testing.T) {
	gm := NewGeofenceMonitor()
	gm.AddZone(GeofenceZone{
		ID:      "zone1",
		Name:    "Enter Only",
		Polygon: testSquare,
		AlertOn: "enter",
	})

	// Enter
	events := gm.CheckPosition("node1", 0, 0)
	if len(events) != 1 {
		t.Fatal("expected enter event")
	}

	// Exit — should not generate event for "enter" only zone
	events = gm.CheckPosition("node1", 5, 5)
	if len(events) != 0 {
		t.Fatalf("expected 0 events on exit for enter-only zone, got %d", len(events))
	}
}

// Every crossing is kept, the zone's name with it (it outlives the zone), and
// the newest 50 are served newest first. [MESHSAT-1414]
func TestGeofenceMonitor_LogsCrossingsNewestFirst(t *testing.T) {
	g := NewGeofenceMonitor()
	clock := time.Unix(1790000000, 0)
	g.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	square := []LatLon{{46.99, -122.01}, {46.99, -121.99}, {47.01, -121.99}, {47.01, -122.01}}
	g.AddZone(GeofenceZone{ID: "z1", Name: "Home", Polygon: square, AlertOn: "both"})
	if ev := g.Events(0); len(ev) != 0 {
		t.Fatalf("a log before any crossing: %v", ev)
	}
	g.CheckPosition("!a1b3c2ec", 47.0, -122.0)
	g.CheckPosition("!a1b3c2ec", 48.0, -122.0)
	ev := g.Events(0)
	if len(ev) != 2 || ev[0].Event != "exit" || ev[1].Event != "enter" || ev[0].ZoneName != "Home" || ev[0].NodeID != "!a1b3c2ec" || ev[0].Timestamp <= ev[1].Timestamp {
		t.Fatalf("log = %+v", ev)
	}
	g.RemoveZone("z1")
	if ev := g.Events(0); len(ev) != 2 || ev[0].ZoneName != "Home" {
		t.Fatalf("the log after the zone went: %+v", ev)
	}
	g.AddZone(GeofenceZone{ID: "z2", Name: "Camp", Polygon: square, AlertOn: "both"})
	for i := 0; i < 30; i++ {
		g.CheckPosition("!a1b3c2ec", 47.0, -122.0)
		g.CheckPosition("!a1b3c2ec", 48.0, -122.0)
	}
	if ev := g.Events(0); len(ev) != 50 || ev[0].ZoneName != "Camp" || ev[0].Event != "exit" {
		t.Fatalf("the newest 50: %d, first %+v", len(ev), ev[0])
	}
}
