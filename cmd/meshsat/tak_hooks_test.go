package main

import (
	"context"
	"errors"
	"testing"

	"meshsat/internal/gateway"
	"meshsat/internal/transport"
)

// takHookMesh is a mesh transport with a status and a node table.
type takHookMesh struct {
	transport.MeshTransport
	status *transport.MeshStatus
	nodes  []transport.MeshNode
	err    error
}

func (m *takHookMesh) GetStatus(context.Context) (*transport.MeshStatus, error) {
	return m.status, m.err
}

func (m *takHookMesh) GetNodes(context.Context) ([]transport.MeshNode, error) {
	return m.nodes, m.err
}

// takHookLocalMesh also knows its node id without I/O, as the direct transport.
type takHookLocalMesh struct {
	takHookMesh
	local string
}

func (m *takHookLocalMesh) LocalNodeID() string { return m.local }

// The TAK gateway's own identity and position come from the local node in the
// node table, until the self-position store replaces the position. [MESHSAT-1421]
func TestLocalNodeTAKHooks(t *testing.T) {
	ctx := context.Background()
	nodes := []transport.MeshNode{
		{Num: 0x11223344, Latitude: 48.1, Longitude: 11.5},
		{Num: 0xaabbccdd, Latitude: 52.3676, Longitude: 4.9041, Altitude: 12},
	}

	// From the status (the HAL transport).
	h := localNodeTAKHooks(ctx, &takHookMesh{status: &transport.MeshStatus{NodeID: "!aabbccdd"}, nodes: nodes})
	if id := h.NodeID(); id != "aabbccdd" {
		t.Errorf("node id %q", id)
	}
	if got := gateway.OwnTAKCallsign(h.NodeID(), "MESHSAT"); got != "MESHSAT-CCDD" {
		t.Errorf("own callsign %q", got)
	}
	if lat, lon, alt, ok := h.SelfPosition(); !ok || lat != 52.3676 || lon != 4.9041 || alt != 12 {
		t.Errorf("position %v %v %v %v", lat, lon, alt, ok)
	}
	if h.Hub != nil {
		t.Error("the Hub hook is the caller's")
	}

	// From the transport itself (the direct transport), before the status.
	h = localNodeTAKHooks(ctx, &takHookLocalMesh{
		takHookMesh: takHookMesh{status: &transport.MeshStatus{NodeID: "!aabbccdd"}, nodes: nodes},
		local:       "!11223344",
	})
	if id := h.NodeID(); id != "11223344" {
		t.Errorf("node id from the transport %q", id)
	}
	if lat, _, _, ok := h.SelfPosition(); !ok || lat != 48.1 {
		t.Errorf("position of the transport's node %v %v", lat, ok)
	}

	// A local node without a position, no local node, a failing transport.
	for name, mesh := range map[string]transport.MeshTransport{
		"at 0,0":           &takHookMesh{status: &transport.MeshStatus{NodeID: "!aabbccdd"}, nodes: []transport.MeshNode{{Num: 0xaabbccdd}}},
		"not in the table": &takHookMesh{status: &transport.MeshStatus{NodeID: "!0000beef"}, nodes: nodes},
		"no node yet":      &takHookLocalMesh{local: ""},
		"transport error":  &takHookMesh{err: errors.New("hal down")},
	} {
		h := localNodeTAKHooks(ctx, mesh)
		if _, _, _, ok := h.SelfPosition(); ok {
			t.Errorf("%s: a position", name)
		}
	}
	if id := localNodeTAKHooks(ctx, &takHookLocalMesh{local: ""}).NodeID(); id != "" {
		t.Errorf("no node yet: node id %q", id)
	}

	// No mesh at all: no hooks.
	if h := localNodeTAKHooks(ctx, nil); h.NodeID != nil || h.SelfPosition != nil {
		t.Error("hooks without a mesh transport")
	}
}
