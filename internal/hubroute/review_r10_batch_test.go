package hubroute

// The r10 review's reproduction of HM-R6, kept as a regression, unchanged but for gofmt: a
// start asked for its first 200 operations only. On 64a3990a it asks 200 of 201. [MESHSAT-1584]

import (
	"encoding/json"
	"meshsat/internal/database"
	"meshsat/internal/hubmsg"
	"testing"
	"time"
)

type reviewCounter struct{ seen map[string]int }

func (p *reviewCounter) IsConnected() bool { return true }
func (p *reviewCounter) Principal() string { return "bridge:kit-a" }
func (p *reviewCounter) PublishMsg(_ string, b []byte, _ time.Duration) error {
	p.seen[string(b)]++
	return nil
}

func TestReviewR10StartupVisitsAllOperations(t *testing.T) {
	db := newDB(t)
	now := time.Now()
	for i := 0; i < 201; i++ {
		id := hubmsg.NewV7(now.Add(time.Duration(i) * time.Millisecond))
		sub, err := hubmsg.NewSubmission(id, hubmsg.NewV7(now.Add(time.Duration(i+1000)*time.Millisecond)), "bridge:kit-b", "recover", now.Add(2*time.Hour).UnixMilli(), now.Add(time.Hour).UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(sub)
		if err := db.PutHubOp(database.HubOp{OpID: id, Mid: sub.Mid, Recipient: "bridge:kit-b", ExpiresAt: now.Add(time.Hour).UnixMilli(), CreatedAt: now.UnixMilli() + int64(i), Submission: string(raw)}); err != nil {
			t.Fatal(err)
		}
	}
	p := &reviewCounter{seen: map[string]int{}}
	r := New(db, p, Config{})
	for i := 0; i < 5; i++ {
		r.RequestReplays()
	}
	t.Logf("distinct operations asked=%d ", len(p.seen))
	if len(p.seen) != 201 {
		t.Error("startup reconciliation omitted operations beyond first batch")
	}
}
