package hubreporter

import (
	"context"
	"encoding/json"
	"testing"
)

// The Hub's management commands go through CommandDeps.Mgmt, which main.go
// wires to the OOB executor; here a recording fake stands in. [MESHSAT-756]
func TestMgmtCommands(t *testing.T) {
	healthFn := func() BridgeHealth { return BridgeHealth{} }
	ch := NewCommandHandler(nil, "test-bridge", healthFn)

	var got []MgmtRequest
	ch.SetDeps(CommandDeps{
		Mgmt: func(ctx context.Context, req MgmtRequest) (MgmtResult, error) {
			got = append(got, req)
			return MgmtResult{Code: 0, Result: "ok", Body: "u1h"}, nil
		},
	})

	cases := []struct {
		name    string
		handler string
		payload string
		wantCmd string
		wantErr bool
		wantRun bool
	}{
		{"ping", "mgmt_ping", ``, "PING", false, true},
		{"status", "mgmt_status", `{}`, "STATUS-NET", false, true},
		{"log", "mgmt_log", `{"unit":"docker","lines":5}`, "LOG", false, true},
		{"reset", "mgmt_reset", `{"target":"mesh","level":2}`, "RESET", false, true},
		{"bearer", "mgmt_bearer", `{"target":"aprs","state":"off"}`, "BEARER", false, true},
		{"restart", "mgmt_restart", `{}`, "RESTART", false, true},
		{"reboot_without_confirm_refused", "reboot", `{"delay":10}`, "", false, false},
		{"reboot_with_confirm", "reboot", `{"delay":10,"confirm":true}`, "REBOOT", false, true},
		{"bad_payload", "mgmt_reset", `{"level":"three"}`, "", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := len(got)
			h, ok := ch.handlers[c.handler]
			if !ok {
				t.Fatalf("handler %s not registered", c.handler)
			}
			out, err := h(Command{Protocol: ProtocolVersion, Cmd: c.handler, RequestID: "r1", Payload: json.RawMessage(c.payload)})
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, c.wantErr)
			}
			ran := len(got) > before
			if ran != c.wantRun {
				t.Fatalf("ran=%v wantRun=%v", ran, c.wantRun)
			}
			if ran {
				if got[len(got)-1].Cmd != c.wantCmd {
					t.Fatalf("cmd %q want %q", got[len(got)-1].Cmd, c.wantCmd)
				}
				var res MgmtResult
				if err := json.Unmarshal(out, &res); err != nil || res.Result != "ok" {
					t.Fatalf("result %s %v", out, err)
				}
			} else if !c.wantErr && !json.Valid(out) {
				t.Fatalf("refusal must still be JSON: %s", out)
			}
		})
	}
	if got[3].Target != "mesh" || got[3].Level != 2 || got[4].State != "off" || got[2].Lines != 5 {
		t.Fatalf("payload fields lost: %+v", got)
	}

	// Without a Mgmt dep the commands fail closed.
	ch2 := NewCommandHandler(nil, "test-bridge", healthFn)
	if _, err := ch2.handlers["mgmt_ping"](Command{Protocol: ProtocolVersion, Cmd: "mgmt_ping"}); err == nil {
		t.Fatal("mgmt without dep must error")
	}
}
