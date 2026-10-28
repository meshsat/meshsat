package database

import (
	"testing"
	"time"
)

// The relayed warnings table (MESHSAT-1734).

func TestWarningsAreKeptByIDAndTakenBack(t *testing.T) {
	db := testDB(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	if seen, err := db.WarningSeen("aa", "Alert"); err != nil || seen {
		t.Fatalf("unknown: %v %v", seen, err)
	}
	w := &Warning{ID: "aa", MsgType: "Alert", Source: "dwd", Sent: now.Add(-time.Hour), Expires: now.Add(time.Hour),
		Severity: "Severe", Event: "Wind", Text: "Sturm", Raw: []byte{0x57, 1}, Via: "hub", ReceivedAt: now}
	if err := db.InsertWarning(w); err != nil {
		t.Fatal(err)
	}
	if seen, _ := db.WarningSeen("aa", "Alert"); !seen {
		t.Fatal("not seen after insert")
	}
	if seen, _ := db.WarningSeen("aa", "Update"); seen {
		t.Fatal("an update of the same id counts as seen")
	}
	if err := db.SetWarningBearers("aa", `["mesh-alert"]`); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetWarning("aa")
	if err != nil || got == nil || got.Bearers != `["mesh-alert"]` || got.Cancelled || !got.Expires.Equal(w.Expires) || got.Via != "hub" {
		t.Fatalf("get: %+v %v", got, err)
	}
	if had, err := db.CancelWarning("aa"); err != nil || !had {
		t.Fatalf("cancel: %v %v", had, err)
	}
	if had, _ := db.CancelWarning("nothing"); had {
		t.Fatal("cancelled a row that is not there")
	}
	// A second row, newer; the list is newest first; the old one is cancelled.
	if err := db.InsertWarning(&Warning{ID: "bb", Ref: "aa", MsgType: "Update", Source: "dwd", Sent: now, ReceivedAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListWarnings(10)
	if err != nil || len(list) != 2 || list[0].ID != "bb" || !list[1].Cancelled || list[0].Ref != "aa" || list[0].Bearers != "[]" {
		t.Fatalf("list: %+v %v", list, err)
	}
	// Rows older than 30 days go on the next insert.
	if err := db.InsertWarning(&Warning{ID: "cc", MsgType: "Alert", Sent: now, ReceivedAt: now.Add(31 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if list, _ := db.ListWarnings(10); len(list) != 1 || list[0].ID != "cc" {
		t.Fatalf("after the prune: %+v", list)
	}
}
