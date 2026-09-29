package engine

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// The mesh transport's position event carries the node itself (user_id, num,
// latitude, ...); reading a "node_id" field out of it stored every position
// without a node and the tracks had nothing to draw. [MESHSAT-1397]
func TestPositionOf_TakesTheNodeIDFromTheNode(t *testing.T) {
	pos := positionOf(json.RawMessage(`{"num":1389068775,"user_id":"!52cb81e7","long_name":"meshsat-pinephone-pro","latitude":52.3731,"longitude":4.8932,"altitude":3,"sats":7}`))
	if pos == nil {
		t.Fatal("no position")
	}
	if pos.NodeID != "!52cb81e7" || pos.Latitude != 52.3731 || pos.Longitude != 4.8932 || pos.Altitude != 3 || pos.SatsInView != 7 {
		t.Fatalf("position row %+v", pos)
	}
}

func TestPositionOf_FallsBackToTheNumberAndNodeID(t *testing.T) {
	if pos := positionOf(json.RawMessage(`{"num":1389068775,"latitude":1.5,"longitude":2.5}`)); pos == nil || pos.NodeID != "!52cb81e7" {
		t.Fatalf("from the number: %+v", pos)
	}
	if pos := positionOf(json.RawMessage(`{"node_id":"!a1b3c2ec","latitude":1.5,"longitude":2.5}`)); pos == nil || pos.NodeID != "!a1b3c2ec" {
		t.Fatalf("from node_id: %+v", pos)
	}
}

func TestPositionOf_NothingWithoutAPositionOrANode(t *testing.T) {
	if pos := positionOf(json.RawMessage(`{"user_id":"!52cb81e7","latitude":0,"longitude":0}`)); pos != nil {
		t.Fatalf("a zero position gave %+v", pos)
	}
	if pos := positionOf(json.RawMessage(`{"latitude":1,"longitude":1}`)); pos != nil {
		t.Fatalf("no node gave %+v", pos)
	}
	if pos := positionOf(json.RawMessage(`not json`)); pos != nil {
		t.Fatalf("bad json gave %+v", pos)
	}
}

// A mesh position event goes through the zones. [MESHSAT-1414]
func TestHandlePosition_ChecksTheZones(t *testing.T) {
	db, err := database.New(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := NewProcessor(db, nil)
	g := NewGeofenceMonitor()
	p.SetGeofenceMonitor(g)
	g.AddZone(GeofenceZone{ID: "z", Name: "Dam Square", AlertOn: "enter",
		Polygon: []LatLon{{52.372, 4.892}, {52.372, 4.894}, {52.374, 4.894}, {52.374, 4.892}}})
	p.handlePosition(transport.MeshEvent{Type: "position", Data: json.RawMessage(`{"num":2712912620,"user_id":"!a1b3c2ec","latitude":52.3731,"longitude":4.8932}`)})
	ev := g.Events(0)
	if len(ev) != 1 || ev[0].NodeID != "!a1b3c2ec" || ev[0].Event != "enter" || ev[0].ZoneName != "Dam Square" {
		t.Fatalf("crossings after a position inside: %+v", ev)
	}
}
