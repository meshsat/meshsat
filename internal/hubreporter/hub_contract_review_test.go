package hubreporter

import (
	"encoding/json"
	"os"
	"testing"
)

// Reviewer's regression (B5-R2, stage 0b review of e2cacd2): snapshots
// signed by the Hub's own types and signer, with and without contact
// metadata, verify and reach the applier with the per-row report.
// REVIEW_HUB_FIXTURES overrides the reviewer's fixture file. [MESHSAT-1584]
func TestReviewB5ActualHubSignedContract(t *testing.T) {
	path := os.Getenv("REVIEW_HUB_FIXTURES")
	if path == "" {
		path = "testdata/review-b5-hub-fixtures.json"
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name      string          `json:"name"`
		PublicKey []byte          `json:"public_key"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(payload, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 2 {
		t.Fatalf("fixture count = %d", len(fixtures))
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			handler, app, anchor := newHandlerWithDeps(t)
			anchor.pub = fixture.PublicKey
			app.report = DirectoryApplyReport{
				PoliciesApplied:  []string{"supported"},
				PoliciesRejected: []PolicyRejection{{ID: "unsupported", ScopeType: "precedence", ScopeID: "Flash", Strategy: "HEMB_BONDED", Reason: "unsupported_strategy"}},
			}
			response, err := handler.handleDirectoryPush(Command{Cmd: "directory_push", RequestID: "review-fixture", Payload: fixture.Payload})
			t.Logf("error=%v applier_called=%v response=%s", err, app.applied != nil, response)
			if err != nil {
				t.Fatalf("valid Hub-signed snapshot rejected: %v", err)
			}
			if app.applied == nil {
				t.Fatal("applier not called")
			}
			var result struct{ DirectoryApplyReport }
			if err := json.Unmarshal(response, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.PoliciesApplied) != 1 || result.PoliciesApplied[0] != "supported" || len(result.PoliciesRejected) != 1 || result.PoliciesRejected[0].ID != "unsupported" {
				t.Fatalf("per-policy report missing: %s", response)
			}
		})
	}
}
