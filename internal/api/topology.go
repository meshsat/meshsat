package api

import (
	"fmt"
	"net/http"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// TopologyNode represents a node in the mesh topology graph.
type TopologyNode struct {
	ID       string  `json:"id"`    // node ID (hex)
	Label    string  `json:"label"` // long name or short name
	Lat      float64 `json:"lat,omitempty"`
	Lon      float64 `json:"lon,omitempty"`
	Battery  int     `json:"battery"`   // percent, 0 if unknown
	SNR      float64 `json:"snr"`       // last known SNR
	LastSeen int64   `json:"last_seen"` // unix timestamp
	Online   bool    `json:"online"`    // seen in last 15 minutes
}

// TopologyLink represents a connection between two nodes.
type TopologyLink struct {
	Source string  `json:"source"` // node ID
	Target string  `json:"target"` // node ID
	SNR    float64 `json:"snr"`    // link quality
	Hops   int     `json:"hops"`   // hop count
}

// TopologyGraph is the full mesh topology.
type TopologyGraph struct {
	Nodes []TopologyNode `json:"nodes"`
	Links []TopologyLink `json:"links"`
	Stats TopologyStats  `json:"stats"`
}

// TopologyStats holds aggregate topology statistics.
type TopologyStats struct {
	TotalNodes  int     `json:"total_nodes"`
	OnlineNodes int     `json:"online_nodes"`
	TotalLinks  int     `json:"total_links"`
	AvgSNR      float64 `json:"avg_snr"`
}

// handleGetTopology returns the mesh topology as a graph of nodes and links.
// @Summary Get mesh topology
// @Description Returns nodes and inferred links for topology visualization
// @Tags topology
// @Success 200 {object} TopologyGraph
// @Failure 503 {object} map[string]string "mesh transport unavailable"
// @Router /api/topology [get]
func (s *Server) handleGetTopology(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	// Get nodes from the mesh transport.
	meshNodes, err := s.mesh.GetNodes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to get nodes: "+err.Error())
		return
	}
	if meshNodes == nil {
		meshNodes = []transport.MeshNode{}
	}

	now := time.Now().Unix()
	onlineThreshold := int64(15 * 60) // 15 minutes

	// Build topology nodes.
	nodes := make([]TopologyNode, 0, len(meshNodes))
	nodeIDs := make(map[string]bool)
	for _, mn := range meshNodes {
		id := fmt.Sprintf("%08x", mn.Num)
		label := mn.LongName
		if label == "" {
			label = mn.ShortName
		}
		online := (now - mn.LastHeard) < onlineThreshold
		nodes = append(nodes, TopologyNode{
			ID:       id,
			Label:    label,
			Lat:      mn.Latitude,
			Lon:      mn.Longitude,
			Battery:  mn.BatteryLevel,
			SNR:      float64(mn.SNR),
			LastSeen: mn.LastHeard,
			Online:   online,
		})
		nodeIDs[id] = true
	}

	// Build links from recent messages in the database.
	links := make([]TopologyLink, 0)
	linkSeen := make(map[string]bool)

	if s.db != nil {
		// Query recent messages (last 2 hours) to infer links.
		since := time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
		filter := database.MessageFilter{
			Since: since,
			Limit: 1000,
		}
		msgs, _, err := s.db.GetMessages(filter)
		if err == nil {
			for _, msg := range msgs {
				src := msg.FromNode
				dst := msg.ToNode
				if src == "" || dst == "" {
					continue
				}
				// Skip broadcast destination "ffffffff".
				if dst == "ffffffff" || dst == "FFFFFFFF" {
					continue
				}
				// Only include links where both nodes are known.
				if !nodeIDs[src] || !nodeIDs[dst] {
					continue
				}
				// Deduplicate links (treat A->B and B->A as the same link).
				key := src + ":" + dst
				rev := dst + ":" + src
				if linkSeen[key] || linkSeen[rev] {
					continue
				}
				linkSeen[key] = true

				hops := 0
				if msg.HopStart > 0 && msg.HopLimit >= 0 {
					hops = msg.HopStart - msg.HopLimit
					if hops < 0 {
						hops = 0
					}
				}

				links = append(links, TopologyLink{
					Source: src,
					Target: dst,
					SNR:    float64(msg.RxSNR),
					Hops:   hops,
				})
			}
		}
	}

	// Calculate stats.
	onlineCount := 0
	var snrSum float64
	snrCount := 0
	for _, n := range nodes {
		if n.Online {
			onlineCount++
		}
		if n.SNR != 0 {
			snrSum += n.SNR
			snrCount++
		}
	}
	avgSNR := 0.0
	if snrCount > 0 {
		avgSNR = snrSum / float64(snrCount)
	}

	graph := TopologyGraph{
		Nodes: nodes,
		Links: links,
		Stats: TopologyStats{
			TotalNodes:  len(nodes),
			OnlineNodes: onlineCount,
			TotalLinks:  len(links),
			AvgSNR:      avgSNR,
		},
	}

	writeJSON(w, http.StatusOK, graph)
}
