package rules

import (
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/ratelimit"
)

func TestAccessEvaluator_BasicIngress(t *testing.T) {
	eval := &AccessEvaluator{
		rates:  make(map[int64]*ratelimit.TokenBucket),
		groups: make(map[string][]string),
		rules: []database.AccessRule{
			{
				ID:          1,
				InterfaceID: "mesh_0",
				Direction:   "ingress",
				Priority:    10,
				Name:        "mesh-to-iridium",
				Enabled:     true,
				Action:      "forward",
				ForwardTo:   "iridium_0",
				Filters:     "{}",
			},
		},
	}

	msg := RouteMessage{Text: "hello world", From: "!abc123", PortNum: 1}
	results := eval.EvaluateIngress("mesh_0", msg)

	if len(results) != 1 {
		t.Fatalf("expected 1 match, got %d", len(results))
	}
	if results[0].ForwardTo != "iridium_0" {
		t.Errorf("expected forward to iridium_0, got %s", results[0].ForwardTo)
	}
}

func TestAccessEvaluator_ImplicitDeny(t *testing.T) {
	eval := &AccessEvaluator{
		rates:  make(map[int64]*ratelimit.TokenBucket),
		groups: make(map[string][]string),
		rules: []database.AccessRule{
			{
				ID:          1,
				InterfaceID: "mesh_0",
				Direction:   "ingress",
				Enabled:     true,
				Action:      "forward",
				ForwardTo:   "iridium_0",
				Filters:     "{}",
			},
		},
	}

	// Message from a different interface — no matching rules → implicit deny
	msg := RouteMessage{Text: "hello", From: "!abc", PortNum: 1}
	results := eval.EvaluateIngress("cellular_0", msg)

	if len(results) != 0 {
		t.Fatalf("expected 0 matches (implicit deny), got %d", len(results))
	}
}

func TestAccessEvaluator_SelfLoopPrevention(t *testing.T) {
	eval := &AccessEvaluator{
		rates:  make(map[int64]*ratelimit.TokenBucket),
		groups: make(map[string][]string),
		rules: []database.AccessRule{
			{
				ID:          1,
				InterfaceID: "mesh_0",
				Direction:   "ingress",
				Enabled:     true,
				Action:      "forward",
				ForwardTo:   "mesh_0", // self-loop
				Filters:     "{}",
			},
		},
	}

	msg := RouteMessage{Text: "hello", From: "!abc", PortNum: 1}
	results := eval.EvaluateIngress("mesh_0", msg)

	if len(results) != 0 {
		t.Fatalf("expected 0 matches (self-loop prevention), got %d", len(results))
	}
}

func TestAccessEvaluator_KeywordFilter(t *testing.T) {
	eval := &AccessEvaluator{
		rates:  make(map[int64]*ratelimit.TokenBucket),
		groups: make(map[string][]string),
		rules: []database.AccessRule{
			{
				ID:          1,
				InterfaceID: "mesh_0",
				Direction:   "ingress",
				Enabled:     true,
				Action:      "forward",
				ForwardTo:   "iridium_0",
				Filters:     `{"keyword":"urgent","channels":"","nodes":"","portnums":""}`,
			},
		},
	}

	// No keyword match
	msg := RouteMessage{Text: "hello", From: "!abc", PortNum: 1}
	results := eval.EvaluateIngress("mesh_0", msg)
	if len(results) != 0 {
		t.Fatalf("expected 0 matches for keyword miss, got %d", len(results))
	}

	// Keyword match (case insensitive)
	msg.Text = "This is URGENT please help"
	results = eval.EvaluateIngress("mesh_0", msg)
	if len(results) != 1 {
		t.Fatalf("expected 1 match for keyword hit, got %d", len(results))
	}
}

func TestAccessEvaluator_PortnumFilter(t *testing.T) {
	eval := &AccessEvaluator{
		rates:  make(map[int64]*ratelimit.TokenBucket),
		groups: make(map[string][]string),
		rules: []database.AccessRule{
			{
				ID:          1,
				InterfaceID: "mesh_0",
				Direction:   "ingress",
				Enabled:     true,
				Action:      "forward",
				ForwardTo:   "mqtt_0",
				Filters:     `{"keyword":"","channels":"","nodes":"","portnums":"[1,67]"}`,
			},
		},
	}

	// PortNum 1 (text) — matches
	msg := RouteMessage{Text: "hello", From: "!abc", PortNum: 1}
	results := eval.EvaluateIngress("mesh_0", msg)
	if len(results) != 1 {
		t.Fatalf("expected 1 match for portnum 1, got %d", len(results))
	}

	// PortNum 3 (position) — no match
	msg.PortNum = 3
	results = eval.EvaluateIngress("mesh_0", msg)
	if len(results) != 0 {
		t.Fatalf("expected 0 matches for portnum 3, got %d", len(results))
	}
}

func TestAccessEvaluator_ExplicitDrop(t *testing.T) {
	eval := &AccessEvaluator{
		rates:  make(map[int64]*ratelimit.TokenBucket),
		groups: make(map[string][]string),
		rules: []database.AccessRule{
			{
				ID:          1,
				InterfaceID: "mesh_0",
				Direction:   "ingress",
				Priority:    1,
				Enabled:     true,
				Action:      "drop",
				Filters:     `{"keyword":"spam"}`,
			},
			{
				ID:          2,
				InterfaceID: "mesh_0",
				Direction:   "ingress",
				Priority:    10,
				Enabled:     true,
				Action:      "forward",
				ForwardTo:   "iridium_0",
				Filters:     "{}",
			},
		},
	}

	// Message with "spam" hits drop rule, stops evaluation
	msg := RouteMessage{Text: "spam message", From: "!abc", PortNum: 1}
	results := eval.EvaluateIngress("mesh_0", msg)
	if len(results) != 0 {
		t.Fatalf("expected 0 matches after explicit drop, got %d", len(results))
	}

	// Message without "spam" skips drop rule, hits forward rule
	msg.Text = "hello world"
	results = eval.EvaluateIngress("mesh_0", msg)
	if len(results) != 1 {
		t.Fatalf("expected 1 match for non-spam message, got %d", len(results))
	}
}

func TestAccessEvaluator_MultiMatch(t *testing.T) {
	eval := &AccessEvaluator{
		rates:  make(map[int64]*ratelimit.TokenBucket),
		groups: make(map[string][]string),
		rules: []database.AccessRule{
			{
				ID:          1,
				InterfaceID: "mesh_0",
				Direction:   "ingress",
				Priority:    10,
				Enabled:     true,
				Action:      "forward",
				ForwardTo:   "iridium_0",
				Filters:     "{}",
			},
			{
				ID:          2,
				InterfaceID: "mesh_0",
				Direction:   "ingress",
				Priority:    20,
				Enabled:     true,
				Action:      "forward",
				ForwardTo:   "mqtt_0",
				Filters:     "{}",
			},
		},
	}

	msg := RouteMessage{Text: "hello", From: "!abc", PortNum: 1}
	results := eval.EvaluateIngress("mesh_0", msg)
	if len(results) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(results))
	}
}

func TestAccessEvaluator_DisabledRuleSkipped(t *testing.T) {
	eval := &AccessEvaluator{
		rates:  make(map[int64]*ratelimit.TokenBucket),
		groups: make(map[string][]string),
		rules: []database.AccessRule{
			{
				ID:          1,
				InterfaceID: "mesh_0",
				Direction:   "ingress",
				Enabled:     false,
				Action:      "forward",
				ForwardTo:   "iridium_0",
				Filters:     "{}",
			},
		},
	}

	msg := RouteMessage{Text: "hello", From: "!abc", PortNum: 1}
	results := eval.EvaluateIngress("mesh_0", msg)
	if len(results) != 0 {
		t.Fatalf("expected 0 matches for disabled rule, got %d", len(results))
	}
}

func TestAccessEvaluator_ObjectGroupNodeFilter(t *testing.T) {
	nodeGroup := "trusted_nodes"
	eval := &AccessEvaluator{
		rates: make(map[int64]*ratelimit.TokenBucket),
		groups: map[string][]string{
			"trusted_nodes": {"!abc123", "!def456"},
		},
		rules: []database.AccessRule{
			{
				ID:              1,
				InterfaceID:     "mesh_0",
				Direction:       "ingress",
				Enabled:         true,
				Action:          "forward",
				ForwardTo:       "iridium_0",
				Filters:         "{}",
				FilterNodeGroup: &nodeGroup,
			},
		},
	}

	// Trusted node — match
	msg := RouteMessage{Text: "hello", From: "!abc123", PortNum: 1}
	results := eval.EvaluateIngress("mesh_0", msg)
	if len(results) != 1 {
		t.Fatalf("expected 1 match for trusted node, got %d", len(results))
	}

	// Unknown node — no match
	msg.From = "!unknown"
	results = eval.EvaluateIngress("mesh_0", msg)
	if len(results) != 0 {
		t.Fatalf("expected 0 matches for untrusted node, got %d", len(results))
	}
}

func TestAccessEvaluator_EgressDirection(t *testing.T) {
	eval := &AccessEvaluator{
		rates:  make(map[int64]*ratelimit.TokenBucket),
		groups: make(map[string][]string),
		rules: []database.AccessRule{
			{
				ID:          1,
				InterfaceID: "iridium_0",
				Direction:   "egress",
				Enabled:     true,
				Action:      "forward",
				ForwardTo:   "",
				Filters:     "{}",
			},
		},
	}

	msg := RouteMessage{Text: "hello", From: "!abc", PortNum: 1}

	// Egress evaluation
	results := eval.EvaluateEgress("iridium_0", msg)
	if len(results) != 1 {
		t.Fatalf("expected 1 egress match, got %d", len(results))
	}

	// Ingress evaluation should not match egress rules
	results = eval.EvaluateIngress("iridium_0", msg)
	if len(results) != 0 {
		t.Fatalf("expected 0 ingress matches for egress rule, got %d", len(results))
	}
}

func TestAccessEvaluator_VisitedLoopPrevention(t *testing.T) {
	eval := &AccessEvaluator{
		rates:  make(map[int64]*ratelimit.TokenBucket),
		groups: make(map[string][]string),
		rules: []database.AccessRule{
			{
				ID:          1,
				InterfaceID: "mesh_0",
				Direction:   "ingress",
				Enabled:     true,
				Action:      "forward",
				ForwardTo:   "iridium_0",
				Filters:     "{}",
			},
		},
	}

	// Message with iridium_0 already in visited set — should be blocked (loop prevention)
	msg := RouteMessage{Text: "hello", From: "!abc", PortNum: 1, Visited: []string{"iridium_0"}}
	results := eval.EvaluateIngress("mesh_0", msg)
	if len(results) != 0 {
		t.Fatalf("expected 0 matches (visited loop prevention), got %d", len(results))
	}
}

func TestAccessEvaluator_VisitedAllowsNonVisited(t *testing.T) {
	eval := &AccessEvaluator{
		rates:  make(map[int64]*ratelimit.TokenBucket),
		groups: make(map[string][]string),
		rules: []database.AccessRule{
			{
				ID:          1,
				InterfaceID: "mesh_0",
				Direction:   "ingress",
				Enabled:     true,
				Action:      "forward",
				ForwardTo:   "iridium_0",
				Filters:     "{}",
			},
		},
	}

	// Message with cellular_0 in visited set — iridium_0 is NOT visited, should match
	msg := RouteMessage{Text: "hello", From: "!abc", PortNum: 1, Visited: []string{"cellular_0"}}
	results := eval.EvaluateIngress("mesh_0", msg)
	if len(results) != 1 {
		t.Fatalf("expected 1 match (iridium_0 not in visited set), got %d", len(results))
	}
	if results[0].ForwardTo != "iridium_0" {
		t.Errorf("expected forward to iridium_0, got %s", results[0].ForwardTo)
	}
}
