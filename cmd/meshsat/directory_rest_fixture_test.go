package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"meshsat/internal/directory"
	"meshsat/internal/hubreporter"
)

// fixtures-v1.2's directory_v2_rest.json (r40 review, decision 1) through
// the real bridge: the Hub's own store, signer and rotation made every
// envelope, and each goes through CommandHandler.HandleCommand with the real
// trust store, applier and database. The bridge must end each case holding
// the contacts (names and addresses), groups (memberships), versions and key
// the fixture names, refuse what it refuses with the state unchanged, and
// skip the rows it skips by kind, id and reason. [MESHSAT-1642]

type restFixtureExpect struct {
	Applied    bool   `json:"applied"`
	Reason     string `json:"reason"`
	Pinned     string `json:"pinned"`
	Version    int64  `json:"version"`
	KeyVersion int64  `json:"key_version"`
	Contacts   []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		Addresses   []struct {
			Kind  string `json:"kind"`
			Value string `json:"value"`
		} `json:"addresses"`
	} `json:"contacts"`
	Groups []struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		Kind      string   `json:"kind"`
		MemberIDs []string `json:"member_ids"`
	} `json:"groups"`
	Skipped []hubreporter.DirectorySkip `json:"skipped"`
}

type restFixtureCase struct {
	Name       string             `json:"name"`
	Kind       string             `json:"kind"`
	TrustedKey string             `json:"trusted_key"`
	Envelope   json.RawMessage    `json:"envelope"`
	Verify     bool               `json:"verify"`
	Expect     *restFixtureExpect `json:"expect"`
}

type restFixture struct {
	Tenant string `json:"tenant"`
	Keys   map[string]struct {
		KeyID   string `json:"key_id"`
		PKIXB64 []byte `json:"pkix_b64"`
	} `json:"keys"`
	Sequence []restFixtureCase `json:"sequence"`
	Refused  []restFixtureCase `json:"refused"`
	Rows     []restFixtureCase `json:"rows"`
}

func loadRestFixture(t *testing.T) restFixture {
	t.Helper()
	raw, err := os.ReadFile("../../internal/hembfixtures/testdata/v1/directory_v2_rest.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx restFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Sequence) != 3 || len(fx.Refused) != 5 || len(fx.Rows) != 5 {
		t.Fatalf("%d/%d/%d cases", len(fx.Sequence), len(fx.Refused), len(fx.Rows))
	}
	return fx
}

// restAnswer is what the bridge answers a directory command with.
type restAnswer struct {
	Applied bool                        `json:"applied"`
	Reason  string                      `json:"reason"`
	Skipped []hubreporter.DirectorySkip `json:"skipped"`
}

// send hands one case's envelope to the bridge as the Hub would and returns
// its answer, or nil when the command failed.
func (b *transcriptBridge) send(t *testing.T, id string, c restFixtureCase) *restAnswer {
	t.Helper()
	cmdName := map[string]string{"snapshot": "directory_push", "rotation": "directory_rotate"}[c.Kind]
	cmd, _ := json.Marshal(hubreporter.Command{Protocol: hubreporter.ProtocolVersion, Cmd: cmdName, RequestID: id,
		Payload: c.Envelope, Timestamp: time.Now().UTC()})
	b.ch.HandleCommand(cmd)
	var stored string
	if err := b.db.QueryRow(`SELECT response FROM hub_command_log WHERE request_id = ?`, id).Scan(&stored); err != nil {
		t.Fatalf("%s: no reply stored: %v", c.Name, err)
	}
	var resp hubreporter.CommandResponse
	if err := json.Unmarshal([]byte(stored), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "ok" {
		return nil
	}
	var ans restAnswer
	if err := json.Unmarshal(resp.Result, &ans); err != nil {
		t.Fatal(err)
	}
	return &ans
}

// holds checks the bridge's rows, versions and key against want.
func (b *transcriptBridge) holds(t *testing.T, what string, fx restFixture, want *restFixtureExpect) {
	t.Helper()
	ctx := context.Background()
	st, err := b.store.GetHubState(ctx)
	if err != nil || st.Version != want.Version || st.KeyVersion != want.KeyVersion {
		t.Errorf("%s: state %+v %v; want version %d, key version %d", what, st, err, want.Version, want.KeyVersion)
	}
	if pinned, _ := (&bridgeDirectoryTrustStore{db: b.db}).GetDirectoryTrustAnchor(); hubreporter.KeyID(pinned) != fx.Keys[want.Pinned].KeyID {
		t.Errorf("%s: trusts %s, want %s", what, hubreporter.KeyID(pinned), want.Pinned)
	}
	var got, exp []string
	contacts, err := b.store.ListContacts(ctx, directory.ContactFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range contacts {
		full, err := b.store.Resolve(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		var addrs []string
		for _, a := range full.Addresses {
			addrs = append(addrs, string(a.Kind)+" "+a.Value)
		}
		sort.Strings(addrs)
		got = append(got, fmt.Sprintf("%s %q %v", c.ID, c.DisplayName, addrs))
	}
	for _, c := range want.Contacts {
		var addrs []string
		for _, a := range c.Addresses {
			addrs = append(addrs, a.Kind+" "+a.Value)
		}
		sort.Strings(addrs)
		exp = append(exp, fmt.Sprintf("%s %q %v", c.ID, c.DisplayName, addrs))
	}
	sort.Strings(got)
	sort.Strings(exp)
	if strings.Join(got, "\n") != strings.Join(exp, "\n") {
		t.Errorf("%s: contacts\n%s\nwant\n%s", what, strings.Join(got, "\n"), strings.Join(exp, "\n"))
	}
	got, exp = nil, nil
	groups, err := b.store.ListGroups(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
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
		got = append(got, fmt.Sprintf("%s %q %s %v", g.ID, g.DisplayName, g.Kind, ids))
	}
	for _, g := range want.Groups {
		ids := append([]string{}, g.MemberIDs...)
		sort.Strings(ids)
		exp = append(exp, fmt.Sprintf("%s %q %s %v", g.ID, g.Name, g.Kind, ids))
	}
	sort.Strings(got)
	sort.Strings(exp)
	if strings.Join(got, "\n") != strings.Join(exp, "\n") {
		t.Errorf("%s: groups\n%s\nwant\n%s", what, strings.Join(got, "\n"), strings.Join(exp, "\n"))
	}
}

func skips(s []hubreporter.DirectorySkip) string {
	var out []string
	for _, k := range s {
		out = append(out, k.Kind+" "+k.ID+" "+k.Reason)
	}
	sort.Strings(out)
	return strings.Join(out, "; ")
}

func TestDirectoryRestFixtureThroughTheRealBridge(t *testing.T) {
	fx := loadRestFixture(t)
	path := filepath.Join(t.TempDir(), "bridge.db")
	b := openTranscriptBridge(t, path)
	defer func() { _ = b.db.Close() }()
	if err := (&bridgeDirectoryTrustStore{db: b.db}).SetDirectoryTrustAnchor(fx.Keys["old"].PKIXB64); err != nil {
		t.Fatal(err)
	}
	for i, c := range fx.Sequence {
		ans := b.send(t, fmt.Sprintf("seq-%d", i), c)
		if ans == nil || !ans.Applied || ans.Reason != "" {
			t.Fatalf("%s: answer %+v", c.Name, ans)
		}
		b.holds(t, c.Name, fx, c.Expect)
	}
	last := fx.Sequence[len(fx.Sequence)-1].Expect
	for i, c := range fx.Refused {
		ans := b.send(t, fmt.Sprintf("ref-%d", i), c)
		switch {
		case !c.Verify && ans != nil && ans.Applied:
			t.Errorf("%s: applied an envelope that does not verify", c.Name)
		case c.Verify && (ans == nil || ans.Applied || ans.Reason != c.Expect.Reason):
			t.Errorf("%s: answer %+v, want refused %s", c.Name, ans, c.Expect.Reason)
		}
		b.holds(t, c.Name, fx, last)
	}
	for i, c := range fx.Rows {
		t.Run(c.Name, func(t *testing.T) {
			rb := openTranscriptBridge(t, filepath.Join(t.TempDir(), "bridge.db"))
			defer func() { _ = rb.db.Close() }()
			if err := (&bridgeDirectoryTrustStore{db: rb.db}).SetDirectoryTrustAnchor(fx.Keys[c.TrustedKey].PKIXB64); err != nil {
				t.Fatal(err)
			}
			ans := rb.send(t, fmt.Sprintf("row-%d", i), c)
			if ans == nil || !ans.Applied {
				t.Fatalf("answer %+v", ans)
			}
			if skips(ans.Skipped) != skips(c.Expect.Skipped) {
				t.Errorf("skipped %s, want %s", skips(ans.Skipped), skips(c.Expect.Skipped))
			}
			rb.holds(t, c.Name, fx, c.Expect)
		})
	}
}
