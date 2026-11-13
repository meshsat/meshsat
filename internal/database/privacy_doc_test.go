package database

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updatePrivacyDoc = flag.Bool("update", false, "rewrite docs/privacy-baseline.md from dataclass.go and retention.go")

// privacyBaselineDoc renders docs/privacy-baseline.md from the table
// classification and the retention rules, so the published page cannot
// drift from the code: SIDN fonds project 261042, deliverable D6
// [MESHSAT-1137].
func privacyBaselineDoc() string {
	var b strings.Builder
	count := map[DataClass]int{}
	for _, t := range Tables {
		count[t.Class]++
	}
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("# Privacy and security baseline\n\n")
	w("What the MeshSat Bridge keeps about people, for how long, and how its code is checked on every build. SIDN fonds project 261042, deliverable D6. This page is generated from the code (`internal/database/dataclass.go` and `retention.go`) by a test that fails when the two disagree.\n\n")
	w("## Reporting a security problem\n\n")
	w("Write to security@meshsat.net, or use GitHub's private vulnerability reporting on this repository. The policy is at https://meshsat.net/security/ and in [SECURITY.md](../SECURITY.md); the machine-readable contact is https://meshsat.net/.well-known/security.txt.\n\n")
	w("## What the Bridge keeps\n\n")
	w("The Bridge keeps its data in one SQLite database on the kit. Of its %d tables, %d can hold personal data, %d hold the operator's own setup and address book, and %d hold none.\n\n",
		len(Tables), count[ClassPersonal], count[ClassOperator], count[ClassNone])
	w("- **Personal data** is what people wrote, where they were, their phone numbers, addresses and radio ids. Every such table is cleared by a rule below or is a named exception.\n")
	w("- **The operator's setup** (contacts, peers, keys, rules, settings) stays until the operator deletes it.\n")
	w("- **No personal data**: radio measurements, caches and counters.\n\n")
	w("## Retention\n\n")
	w("The retention period is `MESHSAT_RETENTION_DAYS`, 30 days unless set. All rules run when the Bridge starts and then once a day; one rule failing does not stop the others.\n\n")
	w("| Rule | Tables | What happens |\n|---|---|---|\n")
	for _, r := range RetentionRules {
		w("| `%s` | %s | %s |\n", r.ID, "`"+strings.Join(r.Tables, "`, `")+"`", r.Describe)
	}
	w("\n## Every table\n\n")
	w("| Table | Holds personal data | What | How it is cleared |\n|---|---|---|---|\n")
	for _, t := range Tables {
		kind := map[DataClass]string{ClassPersonal: "yes", ClassOperator: "the operator's own", ClassNone: "no"}[t.Class]
		how := ""
		switch {
		case t.Rule != "" && t.Exception != "":
			how = "`" + t.Rule + "`: " + t.Exception
		case t.Rule != "":
			how = "`" + t.Rule + "`"
		case t.Exception != "":
			how = "exception: " + t.Exception
		case t.Class == ClassOperator:
			how = "by the operator"
		default:
			how = "-"
		}
		w("| `%s` | %s | %s | %s |\n", t.Table, kind, t.Holds, how)
	}
	w("\n## The audit trail\n\n")
	w("The audit trail is a signed, hash-chained record of what the Bridge did: each row carries the SHA-256 of the row before it, so a changed or removed row breaks the chain. Deleting old rows would break it too, so the audit trail is the one exception to deletion. Instead:\n\n")
	w("- a new row never holds a message's text, a phone number, an email address, a long device number or an IP address: the text becomes its length, the delivery id says which message;\n")
	w("- after the retention period, what older rows still hold of those is redacted in place, field by field. Before each redaction the Bridge verifies the whole chain and writes a checkpoint row signed with its own Ed25519 key, naming the verified head. If the chain does not verify, nothing is redacted;\n")
	w("- a redacted row keeps its stored hash and its link to the row before. Its link is checked as before; its hash no longer recomputes from its text. `GET /api/audit/verify` reports both counts.\n\n")
	w("The licence record of the kit's own 2G cell (`cell_tx_log`) is the other exception: the radio licence requires it, and it names only the licence holder.\n\n")
	w("## Scanning on every build\n\n")
	w("Every pipeline, on every branch, runs:\n\n")
	w("- **gitleaks** over the working tree and the last 25 commits. A leaked secret stops the build, and with it the deploy to the field kits. `.gitleaks.toml` allows only reviewed test fixtures, each with its reason.\n")
	w("- **govulncheck** on the Go version that builds the Bridge (Go 1.26). A known vulnerability that the code actually reaches stops the build.\n")
	w("- **osv-scanner** on the web app's packages, and **trivy** on the kit image after it is built. Their findings show in the pipeline and do not stop the deploy. Most are in build tools and in the Debian packages the kit image is built on, many with no fixed version from Debian yet; they are worked through as a task of their own.\n\n")
	w("## How this stays true\n\n")
	w("`internal/database/retention_registry_test.go` fails the build when a table is added without a classification, when a table with personal data has neither a rule nor a named exception, and when a rule does not clear an old row while keeping a fresh one. `internal/engine/audit_privacy_test.go` holds the audit trail to the rules above.\n")
	return b.String()
}

func TestPrivacyBaselineDoc(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "privacy-baseline.md")
	want := privacyBaselineDoc()
	if *updatePrivacyDoc {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v: run go test ./internal/database -run TestPrivacyBaselineDoc -update", err)
	}
	if string(got) != want {
		t.Fatal("docs/privacy-baseline.md is out of step with dataclass.go and retention.go: run go test ./internal/database -run TestPrivacyBaselineDoc -update")
	}
}
