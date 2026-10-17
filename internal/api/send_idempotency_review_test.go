package api

// The closure review of 5bef352 (17 Oct 2026 16:51 CEST), its API cases as
// supplied: a keyed ORDERED_FALLBACK send replayed after delivery intents
// were turned off (B10-R4 residual), a partial send whose failed address
// shares a bearer with a queued one (B10-R6), and 16 concurrent requests
// under one key. [MESHSAT-1584]

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"meshsat/internal/directory"
)

func TestReviewCumulative2ReplayOrderedFallbackAfterFlagOff(t *testing.T) {
	s, id := keyedServer(t)
	withIntents(t, s, true)
	body := fmt.Sprintf(`{"contact_id":%q,"text":"unchanged","strategy":"ORDERED_FALLBACK"}`, id)
	firstCode, first := keyedPost(t, s, body, "ordered-flag-change")
	if firstCode != http.StatusOK || first.SubmissionID == "" {
		t.Fatalf("initial send: %d %+v", firstCode, first)
	}
	withIntents(t, s, false)
	retryCode, retry := keyedPost(t, s, body, "ordered-flag-change")
	t.Logf("first HTTP=%d submission=%s; retry HTTP=%d submission=%s existing=%v; rows=%d submissions=%d", firstCode, first.SubmissionID, retryCode, retry.SubmissionID, retry.Existing, rowCount(t, s, "message_deliveries"), rowCount(t, s, "delivery_submissions"))
	if retryCode != http.StatusOK || retry.SubmissionID != first.SubmissionID || !retry.Existing {
		t.Fatal("the existing keyed operation cannot be resolved after disabling intents")
	}
}

func TestReviewCumulative2PartialSameBearerKeepsError(t *testing.T) {
	for _, keyed := range []bool{false, true} {
		t.Run(fmt.Sprintf("keyed_%v", keyed), func(t *testing.T) {
			s, id := keyedServer(t)
			withIntents(t, s, false)
			store := directory.NewSQLStore(s.db.DB)
			if err := store.AddAddress(t.Context(), &directory.Address{ContactID: id, Kind: directory.KindMeshtastic, Value: "!zzzzzzzz", PrimaryRank: 1}); err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"contact_id":%q,"text":"partial","strategy":"ALL_BEARERS"}`, id)
			key := ""
			if keyed {
				key = "partial-key"
			}
			code, result := keyedPost(t, s, body, key)
			hasError := len(result.PerBearer) == 1 && result.PerBearer[0].Error != ""
			t.Logf("first: keyed=%v HTTP=%d per_bearer=%+v rows=%d", keyed, code, result.PerBearer, rowCount(t, s, "message_deliveries"))
			if code != http.StatusMultiStatus || !hasError {
				t.Error("one rejected address was hidden by another successful address of the same bearer")
			}
			if keyed {
				code, retry := keyedPost(t, s, body, key)
				t.Logf("replay: HTTP=%d existing=%v per_bearer=%+v rows=%d", code, retry.Existing, retry.PerBearer, rowCount(t, s, "message_deliveries"))
				if code != http.StatusMultiStatus || len(retry.PerBearer) != 1 || retry.PerBearer[0].Error == "" {
					t.Error("the saved replay lost the partial failure")
				}
				if rowCount(t, s, "message_deliveries") != 1 {
					t.Error("the retry queued new work")
				}
			}
		})
	}
}

func TestReviewCumulative2ConcurrentLegacyKeyControl(t *testing.T) {
	s, id := keyedServer(t)
	withIntents(t, s, false)
	body := fmt.Sprintf(`{"contact_id":%q,"text":"concurrent","strategy":"PRIMARY_ONLY"}`, id)
	start := make(chan struct{})
	type answer struct {
		code   int
		result sendToContactResponse
	}
	results := make(chan answer, 16)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c, r := keyedPost(t, s, body, "same-concurrent-key")
			results <- answer{c, r}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	ids := map[string]bool{}
	firsts := 0
	for a := range results {
		if a.code != http.StatusOK || a.result.SubmissionID == "" {
			t.Fatalf("reply %d %+v", a.code, a.result)
		}
		ids[a.result.SubmissionID] = true
		if !a.result.Existing {
			firsts++
		}
	}
	rows, subs := rowCount(t, s, "message_deliveries"), rowCount(t, s, "delivery_submissions")
	t.Logf("16 concurrent calls: first=%d submission IDs=%d rows=%d submissions=%d", firsts, len(ids), rows, subs)
	if firsts != 1 || len(ids) != 1 || rows != 1 || subs != 1 {
		t.Fatal("concurrent requests did not converge on one operation")
	}
}
