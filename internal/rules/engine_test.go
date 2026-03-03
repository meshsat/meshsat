package rules

import (
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/ratelimit"
	"meshsat/internal/transport"
)

func strPtr(s string) *string { return &s }

func TestEvaluate_AnySource(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "Forward All", Enabled: true, Priority: 1, SourceType: "any", DestType: "iridium"},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	msg := &transport.MeshMessage{From: 0x12345678, PortNum: 1, Channel: 0}
	result := e.Evaluate(msg)
	if result == nil {
		t.Fatal("expected match for any source")
	}
	if result.Rule.ID != 1 {
		t.Errorf("expected rule 1, got %d", result.Rule.ID)
	}
}

func TestEvaluate_ChannelFilter(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "Emergency Only", Enabled: true, Priority: 1, SourceType: "channel",
				SourceChannels: strPtr("[0,2]"), DestType: "iridium"},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	// Match channel 0
	msg := &transport.MeshMessage{From: 0x1234, PortNum: 1, Channel: 0}
	if result := e.Evaluate(msg); result == nil {
		t.Error("expected match for channel 0")
	}

	// Match channel 2
	msg.Channel = 2
	if result := e.Evaluate(msg); result == nil {
		t.Error("expected match for channel 2")
	}

	// No match for channel 1
	msg.Channel = 1
	if result := e.Evaluate(msg); result != nil {
		t.Error("expected no match for channel 1")
	}
}

func TestEvaluate_NodeFilter(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "Node Filter", Enabled: true, Priority: 1, SourceType: "node",
				SourceNodes: strPtr(`["!00001234"]`), DestType: "mqtt"},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	msg := &transport.MeshMessage{From: 0x1234, PortNum: 1}
	if result := e.Evaluate(msg); result == nil {
		t.Error("expected match for node !00001234")
	}

	msg.From = 0x5678
	if result := e.Evaluate(msg); result != nil {
		t.Error("expected no match for node !00005678")
	}
}

func TestEvaluate_PortnumFilter(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "Text+Position", Enabled: true, Priority: 1, SourceType: "portnum",
				SourcePortnums: strPtr("[1,3]"), DestType: "iridium"},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	msg := &transport.MeshMessage{From: 0x1234, PortNum: 1}
	if result := e.Evaluate(msg); result == nil {
		t.Error("expected match for portnum 1")
	}

	msg.PortNum = 67
	if result := e.Evaluate(msg); result != nil {
		t.Error("expected no match for portnum 67")
	}
}

func TestEvaluate_DisabledRule(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "Disabled", Enabled: false, Priority: 1, SourceType: "any", DestType: "iridium"},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	msg := &transport.MeshMessage{From: 0x1234, PortNum: 1}
	if result := e.Evaluate(msg); result != nil {
		t.Error("expected no match for disabled rule")
	}
}

func TestEvaluate_FirstMatchWins(t *testing.T) {
	e := &Engine{
		rules: []database.ForwardingRule{
			{ID: 1, Name: "First", Enabled: true, Priority: 1, SourceType: "any", DestType: "mqtt"},
			{ID: 2, Name: "Second", Enabled: true, Priority: 2, SourceType: "any", DestType: "iridium"},
		},
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	msg := &transport.MeshMessage{From: 0x1234, PortNum: 1}
	result := e.Evaluate(msg)
	if result == nil {
		t.Fatal("expected match")
	}
	if result.Rule.ID != 1 {
		t.Errorf("expected first rule (ID 1), got %d", result.Rule.ID)
	}
}

func TestEvaluate_NoRules(t *testing.T) {
	e := &Engine{
		rules: nil,
		rates: make(map[int]*ratelimit.TokenBucket),
	}

	msg := &transport.MeshMessage{From: 0x1234, PortNum: 1}
	if result := e.Evaluate(msg); result != nil {
		t.Error("expected no match with empty rules")
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
