package hubreporter

import "testing"

// Public warnings, the Bridge's side of the Hub (MESHSAT-1734).

func TestWarnInForIsTheBridgesOwnSubtree(t *testing.T) {
	if got := platformTopics.WarnInFor("kit+1"); got != "meshsat/bridge/kit%2B1/warn/in" {
		t.Errorf("platform: %q", got)
	}
	tenant := Topics{root: "meshsat/t-42"}
	if got := tenant.WarnInFor("kit"); got != "meshsat/t-42/bridge/kit/warn/in" {
		t.Errorf("tenant: %q", got)
	}
}

func TestADigestOnWarnInGoesToTheWarningHandlerAlone(t *testing.T) {
	r := NewHubReporter(
		ReporterConfig{HubURL: "tcp://127.0.0.1:1", BridgeID: "parallax01"},
		func() BridgeBirth { return BridgeBirth{BridgeID: "parallax01"} },
		func() BridgeHealth { return BridgeHealth{BridgeID: "parallax01"} },
	)
	var got [][]byte
	var chats int
	r.SetTAKChatHandler(func([]byte) { chats++ })
	// No handler: nothing happens, no panic.
	r.handleWarnIn([]byte{0x57, 0x01})
	r.SetWarningHandler(func(wire []byte) { got = append(got, wire) })
	payload := []byte{0x57, 0x01, 0x01, 0x02}
	r.handleWarnIn(payload)
	payload[3] = 0xFF // the handler got its own copy
	if len(got) != 1 || got[0][3] != 0x02 || chats != 0 {
		t.Fatalf("handler got %v, chats %d", got, chats)
	}
}
