package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/engine"
	"meshsat/internal/hubreporter"
)

// The Hub's directory transcript through the real bridge (r19 C7-R1,
// C7-R3): testdata/hub-directory-rotation-transcript.json holds the commands
// the Hub's real pusher sent one bridge, signed by the Hub's real keys and
// rotations (meshsat-hub internal/bridge TestDirectoryTranscriptForTheBridge
// writes it), with the answer the Hub's model of a bridge gave each. Here
// every command goes through CommandHandler.HandleCommand with the real
// trust store, applier, command log and database, once straight and once
// with a restart before every command. The bridge's own answers (read back
// from its command log) must equal the model's, and its rows the Hub's: the
// snapshot of the last rotation's version applies, a lost rotation answer
// and a replica that signed with the old key do not strand it, and an empty
// snapshot takes the last rows back. [MESHSAT-1584]

type transcriptAnswer struct {
	Applied        bool   `json:"applied"`
	Reason         string `json:"reason"`
	AppliedVersion *int64 `json:"applied_version"`
	KeyVersion     *int64 `json:"key_version"`
	KeyID          string `json:"key_id"`
}

type transcriptAddress struct {
	Kind, Value, Contact string
}

type transcriptScenario struct {
	Name       string `json:"name"`
	Tenant     string `json:"tenant"`
	InitialKey []byte `json:"initial_key_pkix"`
	Steps      []struct {
		Cmd     string            `json:"cmd"`
		Payload json.RawMessage   `json:"payload"`
		Lost    bool              `json:"lost"`
		Expect  *transcriptAnswer `json:"expect"`
	} `json:"steps"`
	KeyID      string              `json:"key_id"`
	Version    int64               `json:"version"`
	KeyVersion int64               `json:"key_version"`
	Contacts   map[string]string   `json:"contacts"`
	Addresses  []transcriptAddress `json:"addresses"`
	Gone       []transcriptAddress `json:"gone"`
	Groups     map[string][]string `json:"groups"`
}

type transcriptBridge struct {
	db    *database.DB
	ch    *hubreporter.CommandHandler
	store *directory.SQLStore
}

func openTranscriptBridge(t *testing.T, path string) *transcriptBridge {
	t.Helper()
	db, err := database.New(path)
	if err != nil {
		t.Fatal(err)
	}
	d := engine.NewDispatcher(db, channel.NewRegistry(), nil, nil)
	store := directory.NewSQLStore(db.DB)
	ch := hubreporter.NewCommandHandler(nil, "kit", nil)
	ch.SetTrustAnchorStore(&bridgeDirectoryTrustStore{db: db})
	ch.SetDirectoryApplier(&bridgeDirectoryApplier{dir: store, supported: d.StrategySupported})
	ch.SetCommandLog(hubCommandLog{db})
	return &transcriptBridge{db: db, ch: ch, store: store}
}

func TestDirectoryTranscriptThroughTheRealBridge(t *testing.T) {
	raw, err := os.ReadFile("../../internal/hubreporter/testdata/hub-directory-rotation-transcript.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenarios []transcriptScenario
	if err := json.Unmarshal(raw, &scenarios); err != nil {
		t.Fatal(err)
	}
	if len(scenarios) != 4 {
		t.Fatalf("%d scenarios", len(scenarios))
	}
	for _, sc := range scenarios {
		for _, restart := range []bool{false, true} {
			name := sc.Name
			if restart {
				name += ", restarting before every command"
			}
			t.Run(name, func(t *testing.T) { replayTranscript(t, sc, restart) })
		}
	}
}

func replayTranscript(t *testing.T, sc transcriptScenario, restart bool) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bridge.db")
	b := openTranscriptBridge(t, path)
	defer func() { _ = b.db.Close() }()
	if err := (&bridgeDirectoryTrustStore{db: b.db}).SetDirectoryTrustAnchor(sc.InitialKey); err != nil {
		t.Fatal(err)
	}
	for i, step := range sc.Steps {
		if restart && i > 0 {
			_ = b.db.Close()
			b = openTranscriptBridge(t, path)
		}
		id := fmt.Sprintf("step-%d", i)
		cmd, _ := json.Marshal(hubreporter.Command{Protocol: hubreporter.ProtocolVersion, Cmd: step.Cmd, RequestID: id,
			Payload: step.Payload, Timestamp: time.Now().UTC()})
		b.ch.HandleCommand(cmd)
		var stored string
		if err := b.db.QueryRow(`SELECT response FROM hub_command_log WHERE request_id = ?`, id).Scan(&stored); err != nil {
			t.Fatalf("step %d (%s): no reply stored: %v", i, step.Cmd, err)
		}
		var resp hubreporter.CommandResponse
		var got transcriptAnswer
		if err := json.Unmarshal([]byte(stored), &resp); err != nil || resp.Status != "ok" || json.Unmarshal(resp.Result, &got) != nil {
			t.Fatalf("step %d (%s): reply %s", i, step.Cmd, stored)
		}
		want := step.Expect
		if want == nil {
			continue
		}
		bad := got.Applied != want.Applied || got.Reason != want.Reason || got.KeyID != want.KeyID ||
			(want.AppliedVersion != nil && (got.AppliedVersion == nil || *got.AppliedVersion != *want.AppliedVersion)) ||
			(want.KeyVersion != nil && (got.KeyVersion == nil || *got.KeyVersion != *want.KeyVersion))
		if bad {
			t.Fatalf("step %d (%s, lost %v): the bridge answered %s\n the Hub's model answered %+v (applied_version %v, key_version %v)",
				i, step.Cmd, step.Lost, resp.Result, *want, deref(want.AppliedVersion), deref(want.KeyVersion))
		}
	}
	if restart {
		_ = b.db.Close()
		b = openTranscriptBridge(t, path)
	}

	st, err := b.store.GetHubState(ctx)
	if err != nil || st.TenantID != sc.Tenant || st.Version != sc.Version || st.KeyVersion != sc.KeyVersion {
		t.Fatalf("state %+v %v; want %s at %d, key from %d", st, err, sc.Tenant, sc.Version, sc.KeyVersion)
	}
	if pinned, _ := (&bridgeDirectoryTrustStore{db: b.db}).GetDirectoryTrustAnchor(); hubreporter.KeyID(pinned) != sc.KeyID {
		t.Errorf("trusts %s, want %s", hubreporter.KeyID(pinned), sc.KeyID)
	}
	contacts, err := b.store.ListContacts(ctx, directory.ContactFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range contacts {
		if string(c.Origin) == directory.HubOrigin(sc.Tenant) {
			got[c.ID] = c.DisplayName
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(sc.Contacts) {
		t.Errorf("Hub contacts %v\n want %v", got, sc.Contacts)
	}
	for _, a := range sc.Addresses {
		c, err := b.store.FindByAddress(ctx, directory.Kind(a.Kind), a.Value)
		if err != nil || c == nil || c.ID != a.Contact {
			t.Errorf("%s %s resolves to %+v (%v), want %s", a.Kind, a.Value, c, err, a.Contact)
		}
	}
	for _, a := range sc.Gone {
		if c, err := b.store.FindByAddress(ctx, directory.Kind(a.Kind), a.Value); err == nil && c != nil {
			t.Errorf("deleted %s %s still resolves to %s", a.Kind, a.Value, c.ID)
		}
	}
	groups, err := b.store.ListGroups(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	gotGroups := map[string][]string{}
	for _, g := range groups {
		members, err := b.store.ListGroupMembers(ctx, g.ID)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, m := range members {
			ids = append(ids, m.ID)
		}
		sort.Strings(ids)
		gotGroups[g.ID] = ids
	}
	if fmt.Sprint(gotGroups) != fmt.Sprint(sc.Groups) {
		t.Errorf("groups %v\n want %v", gotGroups, sc.Groups)
	}
	var policies int
	if err := b.db.QueryRow(`SELECT COUNT(*) FROM directory_dispatch_policy WHERE origin LIKE 'hub%'`).Scan(&policies); err != nil || policies != 0 {
		t.Errorf("%d Hub policies left (%v); every scenario ends without one", policies, err)
	}
}

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
