package main

import (
	"context"
	"fmt"
	"time"

	"meshsat/internal/selfpos"
	"meshsat/internal/transport"
)

// selfPositionResolver returns the resolver of the device's own position:
// the app's fix from store while it is younger than selfpos.MaxAge, else the
// local node's position from the mesh node table, else the GPS reader's fix.
// The APRS-IS position beacon and filter read it through the gateway
// manager, GET /api/position/self through the API. [MESHSAT-1421]
func selfPositionResolver(ctx context.Context, store *selfpos.Store, mesh transport.MeshTransport, gps *transport.GPSReader) func() (selfpos.Fix, bool) {
	fallback := func() (selfpos.Fix, bool) {
		if f, ok := localNodeFix(ctx, mesh); ok {
			return f, true
		}
		if gps != nil {
			if st := gps.GetStatus(); st.Fix && (st.Lat != 0 || st.Lon != 0) {
				return selfpos.Fix{Latitude: st.Lat, Longitude: st.Lon, AltitudeM: st.AltM, At: st.Time}, true
			}
		}
		return selfpos.Fix{}, false
	}
	return func() (selfpos.Fix, bool) {
		return selfpos.Resolve(store, fallback, selfpos.MaxAge)
	}
}

// localNodeFix is the local radio's own position from the mesh node table,
// if the mesh knows it.
func localNodeFix(ctx context.Context, mesh transport.MeshTransport) (selfpos.Fix, bool) {
	if mesh == nil {
		return selfpos.Fix{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	id := ""
	if lp, ok := mesh.(transport.LocalNodeProvider); ok {
		id = lp.LocalNodeID()
	}
	if id == "" {
		st, err := mesh.GetStatus(ctx)
		if err != nil || st == nil {
			return selfpos.Fix{}, false
		}
		id = st.NodeID
	}
	if id == "" {
		return selfpos.Fix{}, false
	}
	nodes, err := mesh.GetNodes(ctx)
	if err != nil {
		return selfpos.Fix{}, false
	}
	for _, n := range nodes {
		if fmt.Sprintf("!%08x", n.Num) != id {
			continue
		}
		if n.Latitude == 0 && n.Longitude == 0 {
			return selfpos.Fix{}, false
		}
		return selfpos.Fix{Latitude: n.Latitude, Longitude: n.Longitude, AltitudeM: float64(n.Altitude), At: time.Now()}, true
	}
	return selfpos.Fix{}, false
}
