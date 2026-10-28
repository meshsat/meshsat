package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"meshsat/internal/gateway"
	"meshsat/internal/transport"
)

// localNodeTAKHooks are the TAK gateway's hooks read from the mesh: the local
// node's id, which makes the Bridge's own uid and callsign, and the local
// node's position in the node table as the Bridge's own position for its PLI
// and SOS. The Hub hook is set by the caller. [MESHSAT-1421]
//
// main.go replaces SelfPosition with the self-position resolver (the app's fix
// first, then this node's, then the GPS reader); this one serves a wiring
// without it.
func localNodeTAKHooks(ctx context.Context, mesh transport.MeshTransport) gateway.TAKHooks {
	if mesh == nil {
		return gateway.TAKHooks{}
	}
	return gateway.TAKHooks{
		NodeID: func() string {
			if num, ok := localNodeNum(ctx, mesh); ok {
				return fmt.Sprintf("%08x", num)
			}
			return ""
		},
		SelfPosition: func() (lat, lon, altM float64, ok bool) {
			return localNodePosition(ctx, mesh)
		},
		NodeHeardAt: func(node uint32) (time.Time, bool) {
			return meshNodeHeardAt(ctx, mesh, node)
		},
	}
}

// meshNodeHeardAt is when the radio last heard a node, from the node table;
// false for a node it does not list or never heard. [MESHSAT-1750]
func meshNodeHeardAt(ctx context.Context, mesh transport.MeshTransport, node uint32) (time.Time, bool) {
	if node == 0 {
		return time.Time{}, false
	}
	cctx, cancel := context.WithTimeout(ctx, localNodeLookup)
	defer cancel()
	nodes, err := mesh.GetNodes(cctx)
	if err != nil {
		return time.Time{}, false
	}
	for _, n := range nodes {
		if n.Num == node && n.LastHeard > 0 {
			return time.Unix(n.LastHeard, 0), true
		}
	}
	return time.Time{}, false
}

// localNodeLookup bounds a status or node-table read through HAL.
const localNodeLookup = 3 * time.Second

// localNodeNum is the local radio's node number: from the transport when it
// knows it without I/O, else from its status.
func localNodeNum(ctx context.Context, mesh transport.MeshTransport) (uint32, bool) {
	id := ""
	if p, ok := mesh.(transport.LocalNodeProvider); ok {
		id = p.LocalNodeID()
	} else {
		cctx, cancel := context.WithTimeout(ctx, localNodeLookup)
		defer cancel()
		if st, err := mesh.GetStatus(cctx); err == nil && st != nil {
			id = st.NodeID
		}
	}
	id = strings.TrimPrefix(strings.TrimSpace(id), "!")
	if id == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(id, 16, 32)
	if err != nil || v == 0 {
		return 0, false
	}
	return uint32(v), true
}

// heardMeshNodes is every node the radio heard within the given time: the
// crews a TAK chat may be sent to from this Bridge. [MESHSAT-1705]
func heardMeshNodes(ctx context.Context, mesh transport.MeshTransport, within time.Duration) []uint32 {
	if mesh == nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, localNodeLookup)
	defer cancel()
	nodes, err := mesh.GetNodes(cctx)
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-within).Unix()
	var out []uint32
	for _, n := range nodes {
		if n.Num != 0 && n.LastHeard >= cutoff {
			out = append(out, n.Num)
		}
	}
	return out
}

// localNodePosition is the local node's position in the node table. A node
// at 0,0 has none.
func localNodePosition(ctx context.Context, mesh transport.MeshTransport) (lat, lon, altM float64, ok bool) {
	num, ok := localNodeNum(ctx, mesh)
	if !ok {
		return 0, 0, 0, false
	}
	cctx, cancel := context.WithTimeout(ctx, localNodeLookup)
	defer cancel()
	nodes, err := mesh.GetNodes(cctx)
	if err != nil {
		return 0, 0, 0, false
	}
	for _, n := range nodes {
		if n.Num != num {
			continue
		}
		if n.Latitude == 0 && n.Longitude == 0 {
			return 0, 0, 0, false
		}
		return n.Latitude, n.Longitude, float64(n.Altitude), true
	}
	return 0, 0, 0, false
}
