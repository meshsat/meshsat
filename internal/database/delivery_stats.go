package database

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// Delivery outcome statistics (HeMB-NG stage 3, S3-3 as amended in contract
// r2): what became of each attempt of a delivery intent, per bearer,
// interface and planned size, over a stated window. The unit is the attempt
// (an intent's row in the delivery ledger), and every attempt is counted in
// exactly one stage, so the stages of a row add up to its attempts, the
// denominator:
//
//   - completed: a receipt matched to this attempt's transmission alone and
//     classified as completing it (by kind: delivered_device, delivered_app,
//     ...), whether or not it was the receipt that closed the intent;
//   - accepted: the bearer accepted it and no receipt came (yet);
//   - unknown: it may have been sent; nothing says either way;
//   - not_sent: it ended refused, never having left;
//   - withdrawn: superseded or cancelled (a retry row superseded after an
//     earlier transmission of it counts here too);
//   - unresolved: still queued, held, waiting for a retry or sending; of
//     those, handed_off already went to the bearer and waits for its answer;
//   - other: a status none of the above names, kept by name rather than
//     filed as in flight.
//
// None of this is radio loss: an unknown is not proven data loss, and a
// local refusal never reached the air. retries_spent sums the attempts'
// retry counters: transmissions that ended not sent and spent a retry, the
// last of them included even when no further try followed (a refusal before
// any transmission spends none).
//
// History (r23 R23-R1): the retention prune removes a closed intent with its
// plan, transmissions and evidence and keeps its attempt rows. It first
// writes each attempt's facts (the classification below) and the intent's
// outcome to delivery_attempt_facts and delivery_intent_facts (v78), and the
// statistics read those as they read live rows. An attempt whose intent was
// pruned before v78 has no facts and cannot be placed: it is counted as
// unavailable, never in a stage, and the window's coverage is incomplete.
//
// Window (r23 R23-R2): [since, until). The ledger keeps whole seconds, so a
// bound with a fraction is taken to its next whole second for the query,
// which keeps the membership the requested bounds give; the answer states
// the bounds as requested. [MESHSAT-1584]

// OutcomeStats is one row: its attempts and the stage each reached.
type OutcomeStats struct {
	Bearer         string         `json:"bearer,omitempty"`
	InterfaceID    string         `json:"interface_id,omitempty"`
	SizeBucket     string         `json:"planned_size_bucket,omitempty"` // the plan's estimate, not framed or billed bytes
	Attempts       int            `json:"attempts"`
	Completed      map[string]int `json:"completed"` // by receipt kind
	CompletedTotal int            `json:"completed_total"`
	Accepted       int            `json:"accepted"`
	Unknown        int            `json:"unknown"`
	NotSent        int            `json:"not_sent"`
	Withdrawn      int            `json:"withdrawn"`
	Unresolved     int            `json:"unresolved"`
	HandedOff      int            `json:"unresolved_handed_off"` // part of Unresolved
	Other          map[string]int `json:"other,omitempty"`       // by status
	RetriesSpent   int            `json:"retries_spent"`
}

// IntentOutcomeStats counts the intents made in the window by state and
// outcome.
type IntentOutcomeStats struct {
	Intents   int            `json:"intents"`
	Active    int            `json:"active"`
	ByOutcome map[string]int `json:"by_outcome"`
}

// StatsCoverage says how much of the window the figures cover.
type StatsCoverage struct {
	// Complete: every attempt and intent of the window is counted.
	Complete bool `json:"complete"`
	// UnavailableAttempts were made in the window, but their intent was
	// pruned before its facts were kept (v78): they are in no stage.
	UnavailableAttempts int `json:"unavailable_attempts"`
	// UnavailableIntents are those intents, placed in the window by their
	// first attempt.
	UnavailableIntents int `json:"unavailable_intents"`
	// ArchivedAttempts and ArchivedIntents were counted from the facts the
	// prune kept.
	ArchivedAttempts int `json:"archived_attempts"`
	ArchivedIntents  int `json:"archived_intents"`
}

// DeliveryOutcomeStats is GET /api/delivery/stats.
type DeliveryOutcomeStats struct {
	Since    string             `json:"since"` // as requested
	Until    string             `json:"until"` // as requested, exclusive
	Unit     string             `json:"unit"`
	Coverage StatsCoverage      `json:"coverage"`
	Routes   []OutcomeStats     `json:"routes"`
	Total    OutcomeStats       `json:"total"`
	Intents  IntentOutcomeStats `json:"intents"`
}

// SizeBuckets are the upper bounds of the planned-size buckets, in bytes:
// one SBD credit, the mesh MTU default, one SMS, an SBD MO message, 1 KiB.
var SizeBuckets = []int{50, 100, 160, 340, 1024}

// SizeBucket names the bucket of n bytes ("1-50" ... ">1024").
func SizeBucket(n int) string {
	lo := 1
	for _, hi := range SizeBuckets {
		if n <= hi {
			return fmt.Sprintf("%d-%d", lo, hi)
		}
		lo = hi + 1
	}
	return fmt.Sprintf(">%d", SizeBuckets[len(SizeBuckets)-1])
}

// attemptFact is what the statistics need of one attempt: what the live
// query derives, and what the prune keeps (delivery_attempt_facts).
type attemptFact struct {
	AttemptID    int64  `db:"attempt_id"`
	IntentID     string `db:"intent_id"`
	CreatedAt    string `db:"created_at"`
	Bearer       string `db:"bearer"`
	InterfaceID  string `db:"interface_id"`
	PlannedBytes int    `db:"planned_bytes"`
	Status       string `db:"status"`
	Completes    string `db:"completes"`
	HandedOff    bool   `db:"handed_off"`
	Retries      int    `db:"retries"`
}

// liveAttemptFacts derives the facts of the attempts whose intent is still
// in the ledger; where filters on d (message_deliveries) and i
// (delivery_intents).
func liveAttemptFacts(q sqlx.Queryer, where string, args ...any) ([]attemptFact, error) {
	var rows []struct {
		AttemptID  int64  `db:"attempt_id"`
		IntentID   string `db:"intent_id"`
		CreatedAt  string `db:"created_at"`
		RouteIndex int    `db:"route_index"`
		Channel    string `db:"channel"`
		Status     string `db:"status"`
		Retries    int    `db:"retries"`
		PayloadLen int    `db:"payload_len"`
		HandedOff  int    `db:"handed_off"`
		Completes  string `db:"completes"`
		RoutePlan  string `db:"route_plan"`
	}
	if err := sqlx.Select(q, &rows, `
		SELECT d.id AS attempt_id, d.intent_id, d.created_at, d.route_index, d.channel, d.status, d.retries,
			COALESCE(length(d.payload), 0) AS payload_len,
			(SELECT COUNT(*) FROM delivery_transmissions t WHERE t.attempt_id = d.id AND t.state = 'handed_off') AS handed_off,
			COALESCE((SELECT e.completes FROM delivery_evidence e WHERE e.attempt_id = d.id AND e.matched = 1
				AND e.ambiguous = 0 AND e.completes != '' ORDER BY e.id LIMIT 1), '') AS completes,
			i.route_plan
		FROM message_deliveries d JOIN delivery_intents i ON i.id = d.intent_id `+where, args...); err != nil {
		return nil, err
	}
	plans := map[string]map[int]RoutePlanEntry{}
	out := make([]attemptFact, 0, len(rows))
	for _, a := range rows {
		plan, ok := plans[a.IntentID]
		if !ok {
			var entries []RoutePlanEntry
			_ = json.Unmarshal([]byte(a.RoutePlan), &entries)
			plan = make(map[int]RoutePlanEntry, len(entries))
			for _, e := range entries {
				plan[e.RouteIndex] = e
			}
			plans[a.IntentID] = plan
		}
		route := plan[a.RouteIndex]
		f := attemptFact{AttemptID: a.AttemptID, IntentID: a.IntentID, CreatedAt: a.CreatedAt, Bearer: route.Kind,
			InterfaceID: route.InterfaceID, PlannedBytes: route.EstWireBytes, Status: a.Status, Completes: a.Completes,
			HandedOff: a.HandedOff > 0, Retries: a.Retries}
		if f.InterfaceID == "" {
			f.InterfaceID = a.Channel
		}
		if f.PlannedBytes <= 0 {
			f.PlannedBytes = a.PayloadLen
		}
		out = append(out, f)
	}
	return out, nil
}

// archiveIntentFacts keeps the facts of the intents that cond names (on i,
// with args), and of their attempts, before they are pruned.
func archiveIntentFacts(tx *sqlx.Tx, cond string, args ...any) error {
	facts, err := liveAttemptFacts(tx, "WHERE "+cond, args...)
	if err != nil {
		return fmt.Errorf("attempt facts: %w", err)
	}
	for _, f := range facts {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO delivery_attempt_facts (attempt_id, intent_id, created_at, bearer,
			interface_id, planned_bytes, status, completes, handed_off, retries) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.AttemptID, f.IntentID, f.CreatedAt, f.Bearer, f.InterfaceID, f.PlannedBytes, f.Status, f.Completes,
			f.HandedOff, f.Retries); err != nil {
			return fmt.Errorf("keep attempt %d: %w", f.AttemptID, err)
		}
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO delivery_intent_facts (intent_id, created_at, state, outcome)
		SELECT i.id, i.created_at, i.state, i.outcome FROM delivery_intents i WHERE `+cond, args...); err != nil {
		return fmt.Errorf("keep intents: %w", err)
	}
	return nil
}

// statsSecond is the bound of t for a ledger that keeps whole seconds: t,
// or its next whole second when it has a fraction. A row of second s is in
// [since, until) exactly when it is in [statsSecond(since), statsSecond(until)).
func statsSecond(t time.Time) string {
	t = t.UTC()
	if w := t.Truncate(time.Second); !w.Equal(t) {
		t = w.Add(time.Second)
	}
	return t.Format("2006-01-02 15:04:05")
}

// DeliveryOutcomeStatsBetween counts the attempts and intents created in
// [since, until).
func (db *DB) DeliveryOutcomeStatsBetween(since, until time.Time) (DeliveryOutcomeStats, error) {
	from, to := statsSecond(since), statsSecond(until)
	out := DeliveryOutcomeStats{Since: since.UTC().Format(time.RFC3339Nano), Until: until.UTC().Format(time.RFC3339Nano),
		Unit: "attempt", Routes: []OutcomeStats{}, Total: OutcomeStats{Completed: map[string]int{}},
		Intents: IntentOutcomeStats{ByOutcome: map[string]int{}}}

	live, err := liveAttemptFacts(db, `WHERE d.intent_id IS NOT NULL AND d.created_at >= ? AND d.created_at < ?`, from, to)
	if err != nil {
		return out, fmt.Errorf("delivery stats: %w", err)
	}
	var archived []attemptFact
	if err := db.Select(&archived, `SELECT attempt_id, intent_id, created_at, bearer, interface_id, planned_bytes, status,
		completes, handed_off, retries FROM delivery_attempt_facts WHERE created_at >= ? AND created_at < ?`, from, to); err != nil {
		return out, fmt.Errorf("delivery stats: kept facts: %w", err)
	}
	out.Coverage.ArchivedAttempts = len(archived)
	rows := map[string]*OutcomeStats{}
	for _, f := range append(live, archived...) {
		bucket := SizeBucket(f.PlannedBytes)
		key := f.Bearer + "\x00" + f.InterfaceID + "\x00" + bucket
		r := rows[key]
		if r == nil {
			r = &OutcomeStats{Bearer: f.Bearer, InterfaceID: f.InterfaceID, SizeBucket: bucket, Completed: map[string]int{}}
			rows[key] = r
		}
		countAttempt(r, f)
		countAttempt(&out.Total, f)
	}
	for _, r := range rows {
		out.Routes = append(out.Routes, *r)
	}
	sort.Slice(out.Routes, func(i, j int) bool {
		a, b := out.Routes[i], out.Routes[j]
		if a.Bearer != b.Bearer {
			return a.Bearer < b.Bearer
		}
		if a.InterfaceID != b.InterfaceID {
			return a.InterfaceID < b.InterfaceID
		}
		return bucketOrder(a.SizeBucket) < bucketOrder(b.SizeBucket)
	})

	// What cannot be placed: attempts of the window whose intent is gone
	// with no facts kept (pruned before v78).
	const orphan = `d.intent_id IS NOT NULL AND d.intent_id != ''
		AND NOT EXISTS (SELECT 1 FROM delivery_intents i WHERE i.id = d.intent_id)
		AND NOT EXISTS (SELECT 1 FROM delivery_attempt_facts f WHERE f.attempt_id = d.id)`
	if err := db.Get(&out.Coverage.UnavailableAttempts, `SELECT COUNT(*) FROM message_deliveries d
		WHERE d.created_at >= ? AND d.created_at < ? AND `+orphan, from, to); err != nil {
		return out, fmt.Errorf("delivery stats: unavailable attempts: %w", err)
	}
	if err := db.Get(&out.Coverage.UnavailableIntents, `SELECT COUNT(*) FROM (SELECT MIN(d.created_at) AS first
		FROM message_deliveries d WHERE `+orphan+` GROUP BY d.intent_id) WHERE first >= ? AND first < ?`, from, to); err != nil {
		return out, fmt.Errorf("delivery stats: unavailable intents: %w", err)
	}

	var intents []struct {
		State   string `db:"state"`
		Outcome string `db:"outcome"`
		N       int    `db:"n"`
		Kept    int    `db:"kept"`
	}
	if err := db.Select(&intents, `
		SELECT state, outcome, COUNT(*) AS n, SUM(kept) AS kept FROM (
			SELECT state, outcome, 0 AS kept FROM delivery_intents WHERE created_at >= ? AND created_at < ?
			UNION ALL
			SELECT state, outcome, 1 AS kept FROM delivery_intent_facts WHERE created_at >= ? AND created_at < ?
		) GROUP BY state, outcome`, from, to, from, to); err != nil {
		return out, fmt.Errorf("delivery stats: intents: %w", err)
	}
	for _, i := range intents {
		out.Intents.Intents += i.N
		out.Coverage.ArchivedIntents += i.Kept
		if i.State == IntentActive {
			out.Intents.Active += i.N
			continue
		}
		out.Intents.ByOutcome[i.Outcome] += i.N
	}
	out.Coverage.Complete = out.Coverage.UnavailableAttempts == 0 && out.Coverage.UnavailableIntents == 0
	return out, nil
}

// countAttempt puts one attempt in exactly one stage of r.
func countAttempt(r *OutcomeStats, f attemptFact) {
	r.Attempts++
	r.RetriesSpent += f.Retries
	switch {
	case f.Completes != "":
		r.Completed[f.Completes]++
		r.CompletedTotal++
	case f.Status == AttemptAccepted:
		r.Accepted++
	case f.Status == AttemptUnknown:
		r.Unknown++
	case f.Status == AttemptNotSent:
		r.NotSent++
	case f.Status == AttemptSuperseded || f.Status == AttemptCancelled:
		r.Withdrawn++
	case f.Status == "queued" || f.Status == "retry" || f.Status == "held" || f.Status == "sending":
		r.Unresolved++
		if f.HandedOff {
			r.HandedOff++
		}
	default:
		if r.Other == nil {
			r.Other = map[string]int{}
		}
		r.Other[f.Status]++
	}
}

// bucketOrder sorts the buckets by size.
func bucketOrder(b string) int {
	if strings.HasPrefix(b, ">") {
		return len(SizeBuckets)
	}
	for i, hi := range SizeBuckets {
		if strings.HasSuffix(b, fmt.Sprintf("-%d", hi)) {
			return i
		}
	}
	return len(SizeBuckets) + 1
}
