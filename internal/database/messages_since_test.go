package database

import (
	"errors"
	"testing"
)

// messagesAt stores one message per created_at value, in the form the column holds them
// (CURRENT_TIMESTAMP: "YYYY-MM-DD HH:MM:SS", UTC).
func messagesAt(t *testing.T, db *DB, at ...string) {
	t.Helper()
	for i, ts := range at {
		if err := db.InsertMessage(&Message{FromNode: "!a1", ToNode: "!b2", DecodedText: ts, Direction: "rx", Transport: "iridium_imt", PacketID: uint32(i + 1)}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE messages SET created_at = ? WHERE id = (SELECT MAX(id) FROM messages)", ts); err != nil {
			t.Fatal(err)
		}
	}
}

// TestGetMessagesSinceReadsEveryTimeForm: an RFC 3339 bound used to compare its "T" with the
// stored form's space and find nothing on its own day (live on parallax, 22 Oct 2026: ISO total 0,
// stored form 1088). Every accepted form now gives the same rows. [MESHSAT-1640]
func TestGetMessagesSinceReadsEveryTimeForm(t *testing.T) {
	db := testDB(t)
	messagesAt(t, db, "2026-10-21 23:59:59", "2026-10-22 08:00:00", "2026-10-22 09:30:00", "2026-10-22 12:00:00")
	for _, tc := range []struct {
		since, until string
		want         int
	}{
		{"2026-10-22T00:00:00Z", "", 3},
		{"2026-10-22 00:00:00", "", 3},
		{"2026-10-22", "", 3},
		{"2026-10-22T02:00:00+02:00", "", 3}, // midnight UTC
		{"2026-10-22T09:30:00.250Z", "", 2},  // whole seconds: the 09:30:00 row is in
		{"2026-10-22T08:00:00Z", "2026-10-22T09:30:00Z", 2},
		{"", "2026-10-22T08:00:00Z", 2},
		{"2026-10-21T23:59:59Z", "2026-10-21T23:59:59Z", 1},
		{"2026-10-22T10:00:00+02:00", "2026-10-22T12:00:00+02:00", 2}, // 08:00 to 10:00 UTC, both ends in
	} {
		msgs, total, err := db.GetMessages(MessageFilter{Since: tc.since, Until: tc.until, Limit: 100})
		if err != nil {
			t.Fatalf("since %q until %q: %v", tc.since, tc.until, err)
		}
		if total != tc.want || len(msgs) != tc.want {
			t.Errorf("since %q until %q: total %d rows %d, want %d", tc.since, tc.until, total, len(msgs), tc.want)
		}
	}
}

func TestGetMessagesRefusesABoundThatIsNoTime(t *testing.T) {
	db := testDB(t)
	for _, bad := range []string{"yesterday", "2026-13-01", "22/10/2026", "1729584000"} {
		if _, _, err := db.GetMessages(MessageFilter{Since: bad}); !errors.Is(err, ErrBadTimeBound) {
			t.Errorf("since %q: err %v, want ErrBadTimeBound", bad, err)
		}
		if _, _, err := db.GetMessages(MessageFilter{Until: bad}); !errors.Is(err, ErrBadTimeBound) {
			t.Errorf("until %q: err %v, want ErrBadTimeBound", bad, err)
		}
	}
}
