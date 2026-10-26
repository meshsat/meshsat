package hubreporter

import (
	"encoding/json"
	"strings"
	"testing"
)

// TAK direct messages, the Bridge's side of the Hub (MESHSAT-1705).

func TestTAKCoTInForIsTheBridgesOwnSubtree(t *testing.T) {
	if got := platformTopics.TAKCoTInFor("kit+1"); got != "meshsat/bridge/kit%2B1/tak/cot/in" {
		t.Errorf("platform: %q", got)
	}
	tenant := Topics{root: "meshsat/t-42"}
	if got := tenant.TAKCoTInFor("kit"); got != "meshsat/t-42/bridge/kit/tak/cot/in" {
		t.Errorf("tenant: %q", got)
	}
	// Not a broadcast filter: the Hub publishes it for this bridge alone.
	for _, f := range platformTopics.TAKCoTInFilters() {
		if f == platformTopics.TAKCoTInFor("kit") {
			t.Errorf("the chat topic is one of the broadcast filters: %q", f)
		}
	}
}

func TestATAKChatTheHubRoutedHereGoesToTheChatHandler(t *testing.T) {
	r := NewHubReporter(
		ReporterConfig{HubURL: "tcp://127.0.0.1:1", BridgeID: "parallax01"},
		func() BridgeBirth { return BridgeBirth{BridgeID: "parallax01"} },
		func() BridgeHealth { return BridgeHealth{BridgeID: "parallax01"} },
	)
	var chats, broadcast [][]byte
	r.SetTAKChatHandler(func(xml []byte) { chats = append(chats, xml) })
	r.SetTAKCoTHandler(func(xml []byte) { broadcast = append(broadcast, xml) })
	doc := []byte(`<event uid="GeoChat.a.b.c" type="b-t-f"><point lat="0" lon="0"/></event>`)

	r.handleTAKChat(doc)
	if len(chats) != 1 || len(broadcast) != 0 {
		t.Fatalf("chat handler %d, broadcast handler %d", len(chats), len(broadcast))
	}
	// The broadcast path still reaches its own handler (positive control).
	r.handleTAKCoT("meshsat/broadcast/tak/cot/in", doc)
	if len(broadcast) != 1 || len(chats) != 1 {
		t.Errorf("after a broadcast: chat %d, broadcast %d", len(chats), len(broadcast))
	}
	if n := r.TAKRelayStats().MessagesIn; n != 2 {
		t.Errorf("messages in %d, want 2", n)
	}
}

type sendCall struct{ path, request, iface, dest, text string }

func sendTextHandler(calls *[]sendCall, withTo bool) *CommandHandler {
	ch := NewCommandHandler(nil, "test-bridge", func() BridgeHealth { return BridgeHealth{} })
	deps := CommandDeps{
		SendText: func(iface, text string) (int64, string, error) {
			*calls = append(*calls, sendCall{"broadcast", "", iface, "", text})
			return 1, "ref-1", nil
		},
	}
	if withTo {
		deps.SendTextTo = func(iface, dest, text string) (int64, string, error) {
			*calls = append(*calls, sendCall{"to", "", iface, dest, text})
			return 2, "ref-2", nil
		}
		deps.SendTextToKeyed = func(req, _, iface, dest, text string) (int64, string, error) {
			*calls = append(*calls, sendCall{"to-keyed", req, iface, dest, text})
			return 3, "ref-3", nil
		}
	}
	ch.SetDeps(deps)
	return ch
}

func TestSendTextReachesTheOneNodeItNames(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		iface   string
		request string
		want    sendCall
		wantErr string
	}{
		{"node alone goes to mesh_0", "!A1B3C3A4", "", "", sendCall{"to", "", "mesh_0", "!a1b3c3a4", "hi"}, ""},
		{"node on a named mesh interface", "!a1b3c3a4", "mesh_0", "", sendCall{"to", "", "mesh_0", "!a1b3c3a4", "hi"}, ""},
		{"keyed by the command", "!a1b3c3a4", "", "req-9", sendCall{"to-keyed", "req-9", "mesh_0", "!a1b3c3a4", "hi"}, ""},
		{"no target: the interface as before", "", "iridium_0", "", sendCall{"broadcast", "", "iridium_0", "", "hi"}, ""},
		{"a node on a satellite interface", "!a1b3c3a4", "iridium_imt_0", "", sendCall{}, "not a mesh interface"},
		{"not a node id", "a1b3c3a4", "", "", sendCall{}, "not a mesh node id"},
		{"a short id", "!a1b3", "", "", sendCall{}, "not a mesh node id"},
		{"no target, no interface", "", "", "", sendCall{}, "interface_id is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []sendCall
			ch := sendTextHandler(&calls, true)
			payload, _ := json.Marshal(sendTextPayload{InterfaceID: c.iface, Text: "hi"})
			out, err := ch.handleSendText(Command{Cmd: "send_text", TargetDevice: c.target, RequestID: c.request, Payload: payload})
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err %v, want %q", err, c.wantErr)
				}
				if len(calls) != 0 {
					t.Fatalf("a refused command still queued %+v", calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 || calls[0] != c.want {
				t.Fatalf("queued %+v, want %+v", calls, c.want)
			}
			if c.want.dest != "" && !strings.Contains(string(out), `"destination":"`+c.want.dest+`"`) {
				t.Errorf("the response does not name the node: %s", out)
			}
		})
	}
	// A bridge that cannot address a node says so instead of broadcasting.
	var calls []sendCall
	ch := sendTextHandler(&calls, false)
	payload, _ := json.Marshal(sendTextPayload{Text: "hi"})
	if _, err := ch.handleSendText(Command{Cmd: "send_text", TargetDevice: "!a1b3c3a4", Payload: payload}); err == nil || len(calls) != 0 {
		t.Errorf("err %v, calls %+v: a text meant for one node went elsewhere", err, calls)
	}
}

func TestSendTextIsSafeWithoutTheLogOnlyWhenBothWaysAreKeyed(t *testing.T) {
	ch := NewCommandHandler(nil, "b", func() BridgeHealth { return BridgeHealth{} })
	keyed := func(string, string, string, string) (int64, string, error) { return 0, "", nil }
	to := func(string, string, string) (int64, string, error) { return 0, "", nil }
	toKeyed := func(string, string, string, string, string) (int64, string, error) { return 0, "", nil }

	ch.SetDeps(CommandDeps{SendTextKeyed: keyed})
	if !ch.safeWithoutLog("send_text") {
		t.Error("keyed send_text with no node path is not safe")
	}
	ch.SetDeps(CommandDeps{SendTextKeyed: keyed, SendTextTo: to})
	if ch.safeWithoutLog("send_text") {
		t.Error("send_text whose node path is not keyed was taken as safe")
	}
	ch.SetDeps(CommandDeps{SendTextKeyed: keyed, SendTextTo: to, SendTextToKeyed: toKeyed})
	if !ch.safeWithoutLog("send_text") {
		t.Error("send_text keyed both ways is not safe")
	}
}
