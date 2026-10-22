package database

import (
	"testing"
	"time"
)

// A message id is taken once per origin, and forgotten after the horizon. [MESHSAT-1641]
func TestClaimLXMFMid(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	for i, c := range []struct {
		origin, mid string
		want        bool
	}{
		{"aa", "m1", true}, {"aa", "m1", false}, {"bb", "m1", true}, {"aa", "m2", true},
	} {
		got, err := db.ClaimLXMFMid(c.origin, c.mid, now)
		if err != nil || got != c.want {
			t.Fatalf("%d: %v %v, want %v", i, got, err, c.want)
		}
	}
	if _, err := db.ClaimLXMFMid("cc", "old", now.Add(-LXMFMidRetention-time.Minute)); err != nil {
		t.Fatal(err)
	}
	n, err := db.PruneLXMFMids(now)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d %v, want 1", n, err)
	}
	if got, _ := db.ClaimLXMFMid("cc", "old", now); !got {
		t.Fatal("a pruned id is still remembered")
	}
	if got, _ := db.ClaimLXMFMid("aa", "m1", now); got {
		t.Fatal("a live id was forgotten")
	}
}
