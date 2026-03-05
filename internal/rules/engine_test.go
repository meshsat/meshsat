package rules

import (
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/ratelimit"
)

func strPtr(s string) *string { return &s }

func TestEvaluateRoute_AnySource(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "Forward All", Enabled: true, Priority: 1, SourceType: "any", DestType: "iridium"},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	msg := RouteMessage{From: "!12345678", PortNum: 1, Channel: 0}
	results := e.EvaluateRoute("mesh", msg)
	if len(results) != 1 {
		t.Fatalf("expected 1 match, got %d", len(results))
	}
	if results[0].Rule.ID != 1 {
		t.Errorf("expected rule 1, got %d", results[0].Rule.ID)
	}
}

func TestEvaluateRoute_ChannelFilter(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "Emergency Only", Enabled: true, Priority: 1, SourceType: "channel",
				SourceChannels: strPtr("[0,2]"), DestType: "iridium"},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	// Match channel 0
	msg := RouteMessage{From: "!00001234", PortNum: 1, Channel: 0}
	if results := e.EvaluateRoute("mesh", msg); len(results) != 1 {
		t.Error("expected match for channel 0")
	}

	// Match channel 2
	msg.Channel = 2
	if results := e.EvaluateRoute("mesh", msg); len(results) != 1 {
		t.Error("expected match for channel 2")
	}

	// No match for channel 1
	msg.Channel = 1
	if results := e.EvaluateRoute("mesh", msg); len(results) != 0 {
		t.Error("expected no match for channel 1")
	}
}

func TestEvaluateRoute_NodeFilter(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "Node Filter", Enabled: true, Priority: 1, SourceType: "node",
				SourceNodes: strPtr(`["!00001234"]`), DestType: "mqtt"},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	msg := RouteMessage{From: "!00001234", PortNum: 1}
	if results := e.EvaluateRoute("mesh", msg); len(results) != 1 {
		t.Error("expected match for node !00001234")
	}

	msg.From = "!00005678"
	if results := e.EvaluateRoute("mesh", msg); len(results) != 0 {
		t.Error("expected no match for node !00005678")
	}
}

func TestEvaluateRoute_PortnumFilter(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "Text+Position", Enabled: true, Priority: 1, SourceType: "portnum",
				SourcePortnums: strPtr("[1,3]"), DestType: "iridium"},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	msg := RouteMessage{From: "!00001234", PortNum: 1}
	if results := e.EvaluateRoute("mesh", msg); len(results) != 1 {
		t.Error("expected match for portnum 1")
	}

	msg.PortNum = 67
	if results := e.EvaluateRoute("mesh", msg); len(results) != 0 {
		t.Error("expected no match for portnum 67")
	}
}

func TestEvaluateRoute_DisabledRule(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "Disabled", Enabled: false, Priority: 1, SourceType: "any", DestType: "iridium"},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	msg := RouteMessage{From: "!00001234", PortNum: 1}
	if results := e.EvaluateRoute("mesh", msg); len(results) != 0 {
		t.Error("expected no match for disabled rule")
	}
}

func TestEvaluateRoute_MultiMatch(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "First", Enabled: true, Priority: 1, SourceType: "any", DestType: "mqtt"},
			{ID: 2, Name: "Second", Enabled: true, Priority: 2, SourceType: "any", DestType: "iridium"},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	msg := RouteMessage{From: "!00001234", PortNum: 1}
	results := e.EvaluateRoute("mesh", msg)
	if len(results) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(results))
	}
}

func TestEvaluateRoute_NoRules(t *testing.T) {
	e := &Engine{
		rules: nil,
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	msg := RouteMessage{From: "!00001234", PortNum: 1}
	if results := e.EvaluateRoute("mesh", msg); len(results) != 0 {
		t.Error("expected no match with empty rules")
	}
}

func TestEvaluateRoute_SelfLoopPrevention(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "Iridium→Iridium", Enabled: true, SourceType: "any", DestType: "iridium"},
			{ID: 2, Name: "Iridium→MQTT", Enabled: true, SourceType: "any", DestType: "mqtt"},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	msg := RouteMessage{Text: "hello", From: "sat"}
	results := e.EvaluateRoute("iridium", msg)
	if len(results) != 1 {
		t.Fatalf("expected 1 match (self-loop prevented), got %d", len(results))
	}
	if results[0].Rule.DestType != "mqtt" {
		t.Fatalf("expected mqtt dest, got %s", results[0].Rule.DestType)
	}
}

func TestEvaluateRoute_MeshSourceFilters(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "Channel 0 Only", Enabled: true, SourceType: "channel",
				SourceChannels: strPtr("[0]"), DestType: "iridium"},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	// Match: mesh source, channel 0
	msg := RouteMessage{Text: "test", From: "!00001234", Channel: 0, PortNum: 1}
	results := e.EvaluateRoute("mesh", msg)
	if len(results) != 1 {
		t.Fatalf("expected 1 match, got %d", len(results))
	}

	// No match: mesh source, channel 1
	msg.Channel = 1
	results = e.EvaluateRoute("mesh", msg)
	if len(results) != 0 {
		t.Fatalf("expected 0 matches for channel 1, got %d", len(results))
	}

	// No match: non-mesh source with channel filter
	results = e.EvaluateRoute("iridium", msg)
	if len(results) != 0 {
		t.Fatalf("expected 0 matches for iridium source with channel filter, got %d", len(results))
	}
}

func TestEvaluateRoute_ExternalSources(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "Iridium→Mesh", Enabled: true, SourceType: "iridium", DestType: "mesh"},
			{ID: 2, Name: "Cellular→Mesh", Enabled: true, SourceType: "cellular", DestType: "mesh"},
			{ID: 3, Name: "External→MQTT", Enabled: true, SourceType: "external", DestType: "mqtt"},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	// Iridium source matches iridium + external rules
	msg := RouteMessage{Text: "sat msg"}
	results := e.EvaluateRoute("iridium", msg)
	if len(results) != 2 {
		t.Fatalf("expected 2 matches for iridium, got %d", len(results))
	}

	// Cellular matches cellular + external
	results = e.EvaluateRoute("cellular", msg)
	if len(results) != 2 {
		t.Fatalf("expected 2 matches for cellular, got %d", len(results))
	}

	// Mesh matches neither iridium nor cellular nor external
	results = e.EvaluateRoute("mesh", msg)
	if len(results) != 0 {
		t.Fatalf("expected 0 matches for mesh, got %d", len(results))
	}
}

func TestEvaluateRoute_KeywordFilter(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "SOS Only", Enabled: true, SourceType: "any", DestType: "iridium",
				SourceKeyword: strPtr("SOS")},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	// Match
	results := e.EvaluateRoute("mesh", RouteMessage{Text: "sos help me"})
	if len(results) != 1 {
		t.Fatalf("expected 1 match, got %d", len(results))
	}

	// No match
	results = e.EvaluateRoute("mesh", RouteMessage{Text: "normal message"})
	if len(results) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(results))
	}
}

func TestEvaluateRoute_PortnumFilterOnAny(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "Text Only", Enabled: true, SourceType: "any", DestType: "webhook",
				SourcePortnums: strPtr("[1]")},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	results := e.EvaluateRoute("mesh", RouteMessage{PortNum: 1, Text: "hi"})
	if len(results) != 1 {
		t.Fatalf("expected 1 match for portnum 1, got %d", len(results))
	}

	results = e.EvaluateRoute("mesh", RouteMessage{PortNum: 67, Text: "telem"})
	if len(results) != 0 {
		t.Fatalf("expected 0 matches for portnum 67, got %d", len(results))
	}
}

func TestEvaluateRoute_AstrocastSource(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "Astrocast→Mesh", Enabled: true, SourceType: "astrocast", DestType: "mesh"},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	results := e.EvaluateRoute("astrocast", RouteMessage{Text: "from space"})
	if len(results) != 1 {
		t.Fatalf("expected 1 match, got %d", len(results))
	}

	results = e.EvaluateRoute("iridium", RouteMessage{Text: "from space"})
	if len(results) != 0 {
		t.Fatalf("expected 0 matches for iridium source, got %d", len(results))
	}
}

func TestMatchesKeyword(t *testing.T) {
	rule := database.ForwardingRule{SourceKeyword: strPtr("emergency")}
	if !MatchesKeyword(rule, "This is an EMERGENCY alert") {
		t.Error("expected case-insensitive keyword match")
	}
	if MatchesKeyword(rule, "Normal message") {
		t.Error("expected no match for non-matching text")
	}

	rule2 := database.ForwardingRule{}
	if !MatchesKeyword(rule2, "anything") {
		t.Error("expected nil keyword to match")
	}
}
