package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"meshsat/internal/database"
)

// The audit trail as the published, field-level redacted exception to
// retention: SIDN fonds project 261042, deliverable D6 [MESHSAT-1137].

func TestMinimizeAuditDetail_NoTextNumberOrAddressInANewRow(t *testing.T) {
	cases := []struct{ event, in, want string }{
		{"deliver", "SOS: Kyriakos needs help at the harbour", "39 characters"},
		{"dispatch", "hello", "5 characters"},
		{"drop", "sms to +44 7700 900123 failed", "sms to [phone] failed"},
		{"oob_command", "from a@example.org imei 300234067500220", "from [email] imei [number]"},
		{"device_health", "mesh: mesh recovered after step 1 (serial reconnect)", "mesh: mesh recovered after step 1 (serial reconnect)"},
	}
	for _, c := range cases {
		if got := MinimizeAuditDetail(c.event, c.in); got != c.want {
			t.Errorf("%s %q: %q, want %q", c.event, c.in, got, c.want)
		}
	}
	sos := MinimizeAuditDetail("sos_activated", `{"message":"SOS: Kyriakos needs help","started_at":"2026-11-13T10:00:00Z","trigger":"hold"}`)
	var m map[string]any
	if err := json.Unmarshal([]byte(sos), &m); err != nil || m["message"] != nil || m["message_chars"] != float64(24) || m["trigger"] != "hold" {
		t.Errorf("sos_activated: %s", sos)
	}
	tok := MinimizeAuditDetail("api_token_set", `{"remote":"192.0.2.7","via":"local"}`)
	if strings.Contains(tok, "192.0.2.7") || !strings.Contains(tok, `"via":"local"`) {
		t.Errorf("api_token_set: %s", tok)
	}
}

// legacyRow writes a row as the Bridge did before MESHSAT-1137: the raw
// detail, hashed and chained.
func legacyRow(t *testing.T, db *database.DB, ss *SigningService, ts, event, detail string) {
	t.Helper()
	h := sha256.New()
	h.Write([]byte(ss.lastHash))
	h.Write([]byte(ts))
	h.Write([]byte(event))
	h.Write([]byte(detail))
	hash := hex.EncodeToString(h.Sum(nil))
	if _, err := db.InsertAuditLog(&database.AuditLogEntry{Timestamp: ts, EventType: event, Detail: detail, PrevHash: ss.lastHash, Hash: hash}); err != nil {
		t.Fatal(err)
	}
	ss.lastHash = hash
}

func TestRedactOlderThan_KeepsTheChainAndSignsACheckpoint(t *testing.T) {
	db := testDB(t)
	ss, err := NewSigningService(db)
	if err != nil {
		t.Fatal(err)
	}
	legacyRow(t, db, ss, "2026-09-01T10:00:00Z", "deliver", "SOS: Kyriakos needs help at the harbour")
	legacyRow(t, db, ss, "2026-09-01T10:00:01Z", "drop", "sms to +44 7700 900123 failed")
	legacyRow(t, db, ss, "2026-09-01T10:00:02Z", "device_health", "mesh: mesh recovered after step 1 (serial reconnect)")
	legacyRow(t, db, ss, "2026-11-12T10:00:00Z", "deliver", "a message from yesterday")
	ss.AuditEvent("dispatch", nil, nil, nil, nil, "today's text")

	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	n, err := ss.RedactOlderThan(now.AddDate(0, 0, -30), now)
	if err != nil || n != 2 {
		t.Fatalf("redacted %d (%v), want the two old rows with personal data", n, err)
	}
	rows, _ := db.AuditRowsAll()
	if rows[0].Detail != "[redacted]" || rows[0].RedactedAt == nil || rows[1].Detail != "sms to [phone] failed" ||
		rows[2].RedactedAt != nil || rows[3].Detail != "a message from yesterday" || rows[4].Detail != "12 characters" {
		for _, r := range rows {
			t.Logf("%d %s %q redacted=%v", r.ID, r.EventType, r.Detail, r.RedactedAt != nil)
		}
		t.Fatal("wrong rows redacted")
	}
	last := rows[len(rows)-1]
	if last.EventType != "audit_checkpoint" {
		t.Fatalf("no checkpoint row; last is %s", last.EventType)
	}
	var cp struct {
		VerifiedRows int    `json:"verified_rows"`
		HeadID       int64  `json:"head_id"`
		HeadHash     string `json:"head_hash"`
		Signature    string `json:"signature"`
	}
	if err := json.Unmarshal([]byte(last.Detail), &cp); err != nil {
		t.Fatal(err)
	}
	sig, _ := hex.DecodeString(cp.Signature)
	if cp.VerifiedRows != 5 || cp.HeadID != rows[4].ID || cp.HeadHash != rows[4].Hash || !Verify(ss.SignerID(), []byte(cp.HeadHash), sig) {
		t.Fatalf("checkpoint %+v does not attest the verified head", cp)
	}

	rep, err := ss.VerifyAll()
	if err != nil || rep.BrokenAt != -1 || rep.Redacted != 2 || rep.HashesChecked != 4 {
		t.Fatalf("after redaction: %+v (%v), want intact, 2 redacted, 4 hashes checked", rep, err)
	}
	if _, broken := ss.VerifyChain(100); broken != -1 {
		t.Fatalf("VerifyChain reports a break at %d after redaction", broken)
	}
	// Nothing more to do the next day.
	if n, err := ss.RedactOlderThan(now.AddDate(0, 0, -29), now); err != nil || n != 0 {
		t.Fatalf("second run redacted %d (%v)", n, err)
	}

	// A tampered row is still caught, and so is a deleted one.
	if _, err := db.Exec(`UPDATE audit_log SET detail = 'changed' WHERE id = ?`, rows[3].ID); err != nil {
		t.Fatal(err)
	}
	if rep, _ := ss.VerifyAll(); rep.BrokenAt != rows[3].ID || rep.Reason != "hash" {
		t.Fatalf("a changed row: %+v", rep)
	}
	if _, err := db.Exec(`UPDATE audit_log SET detail = ? WHERE id = ?`, "a message from yesterday", rows[3].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM audit_log WHERE id = ?`, rows[1].ID); err != nil {
		t.Fatal(err)
	}
	if rep, _ := ss.VerifyAll(); rep.BrokenAt != rows[2].ID || rep.Reason != "link to the row before" {
		t.Fatalf("a deleted redacted row: %+v", rep)
	}
}

// A redaction never covers a break: with the chain broken nothing is
// redacted and no checkpoint is written.
func TestRedactOlderThan_RefusesOnABrokenChain(t *testing.T) {
	db := testDB(t)
	ss, err := NewSigningService(db)
	if err != nil {
		t.Fatal(err)
	}
	legacyRow(t, db, ss, "2026-09-01T10:00:00Z", "deliver", "text one")
	legacyRow(t, db, ss, "2026-09-01T10:00:01Z", "deliver", "text two")
	if _, err := db.Exec(`UPDATE audit_log SET detail = 'forged' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	if n, err := ss.RedactOlderThan(now.AddDate(0, 0, -30), now); err == nil || n != 0 {
		t.Fatalf("redacted %d over a broken chain (err %v)", n, err)
	}
	rows, _ := db.AuditRowsAll()
	if len(rows) != 2 || rows[1].Detail != "text two" {
		t.Fatalf("rows changed: %+v", rows)
	}
}
