package database

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Delivery outcome statistics (HeMB-NG stage 3, S3-3 as amended in contract
// r2): what became of each attempt of a delivery intent, per bearer,
// interface and size bucket, over a stated window. The unit is the attempt
// (an intent's row in the delivery ledger), and every attempt is counted in
// exactly one stage, so the stages of a row add up to its attempts, the
// denominator:
//
//   - completed: a receipt matched to this attempt's transmission alone and
//     classified as completing it (by kind: delivered_device, delivered_app,
//     ...), whether or not it was the receipt that closed the intent;
//   - accepted: the bearer accepted it and no receipt came (yet);
//   - unknown: it may have been sent; nothing says either way;
//   - not_sent: refused before it left (a local refusal, not over-air loss);
//   - withdrawn: superseded or cancelled before it was tried;
//   - unresolved: still queued, held, waiting for a retry or sending; of
//     those, handed_off already went to the bearer and waits for its answer;
//   - other: a status none of the above names, kept by name rather than
//     filed as in flight.
//
// None of this is radio loss: an unknown is not proven data loss, and a
// local refusal never reached the air. retries_spent counts the retries the
// attempts spent: transmissions that ended not sent and were put back for
// another go (a refusal before any transmission spends none); they are not
// attempts of their own.
// [MESHSAT-1584]

// OutcomeStats is one row: its attempts and the stage each reached.
type OutcomeStats struct {
	Bearer         string         `json:"bearer,omitempty"`
	InterfaceID    string         `json:"interface_id,omitempty"`
	SizeBucket     string         `json:"size_bucket,omitempty"`
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

// DeliveryOutcomeStats is GET /api/delivery/stats.
type DeliveryOutcomeStats struct {
	Since   string             `json:"since"`
	Until   string             `json:"until"`
	Unit    string             `json:"unit"`
	Routes  []OutcomeStats     `json:"routes"`
	Total   OutcomeStats       `json:"total"`
	Intents IntentOutcomeStats `json:"intents"`
}

// SizeBuckets are the upper bounds of the wire-size buckets, in bytes: one
// SBD credit, the mesh MTU default, one SMS, an SBD MO message, 1 KiB.
var SizeBuckets = []int{50, 100, 160, 340, 1024}

// SizeBucket names the bucket of n wire bytes ("1-50" ... ">1024").
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

// DeliveryOutcomeStatsBetween counts the attempts and intents created in
// [since, until).
func (db *DB) DeliveryOutcomeStatsBetween(since, until time.Time) (DeliveryOutcomeStats, error) {
	const layout = "2006-01-02 15:04:05"
	from, to := since.UTC().Format(layout), until.UTC().Format(layout)
	out := DeliveryOutcomeStats{Since: since.UTC().Format(time.RFC3339), Until: until.UTC().Format(time.RFC3339), Unit: "attempt",
		Routes: []OutcomeStats{}, Total: OutcomeStats{Completed: map[string]int{}},
		Intents: IntentOutcomeStats{ByOutcome: map[string]int{}}}

	var attempts []struct {
		IntentID   string `db:"intent_id"`
		RouteIndex int    `db:"route_index"`
		Channel    string `db:"channel"`
		Status     string `db:"status"`
		Retries    int    `db:"retries"`
		PayloadLen int    `db:"payload_len"`
		HandedOff  int    `db:"handed_off"`
		Completes  string `db:"completes"`
		RoutePlan  string `db:"route_plan"`
	}
	if err := db.Select(&attempts, `
		SELECT d.intent_id, d.route_index, d.channel, d.status, d.retries, COALESCE(length(d.payload), 0) AS payload_len,
			(SELECT COUNT(*) FROM delivery_transmissions t WHERE t.attempt_id = d.id AND t.state = 'handed_off') AS handed_off,
			COALESCE((SELECT e.completes FROM delivery_evidence e WHERE e.attempt_id = d.id AND e.matched = 1
				AND e.ambiguous = 0 AND e.completes != '' ORDER BY e.id LIMIT 1), '') AS completes,
			i.route_plan
		FROM message_deliveries d JOIN delivery_intents i ON i.id = d.intent_id
		WHERE d.created_at >= ? AND d.created_at < ?`, from, to); err != nil {
		return out, fmt.Errorf("delivery stats: %w", err)
	}
	plans := map[string]map[int]RoutePlanEntry{}
	rows := map[string]*OutcomeStats{}
	for _, a := range attempts {
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
		iface := route.InterfaceID
		if iface == "" {
			iface = a.Channel
		}
		size := route.EstWireBytes
		if size <= 0 {
			size = a.PayloadLen
		}
		key := route.Kind + "\x00" + iface + "\x00" + SizeBucket(size)
		r := rows[key]
		if r == nil {
			r = &OutcomeStats{Bearer: route.Kind, InterfaceID: iface, SizeBucket: SizeBucket(size), Completed: map[string]int{}}
			rows[key] = r
		}
		countAttempt(r, a.Status, a.Completes, a.HandedOff > 0, a.Retries)
		countAttempt(&out.Total, a.Status, a.Completes, a.HandedOff > 0, a.Retries)
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

	var intents []struct {
		State   string `db:"state"`
		Outcome string `db:"outcome"`
		N       int    `db:"n"`
	}
	if err := db.Select(&intents, `SELECT state, outcome, COUNT(*) AS n FROM delivery_intents
		WHERE created_at >= ? AND created_at < ? GROUP BY state, outcome`, from, to); err != nil {
		return out, fmt.Errorf("delivery stats: intents: %w", err)
	}
	for _, i := range intents {
		out.Intents.Intents += i.N
		if i.State == IntentActive {
			out.Intents.Active += i.N
			continue
		}
		out.Intents.ByOutcome[i.Outcome] += i.N
	}
	return out, nil
}

// countAttempt puts one attempt in exactly one stage of r.
func countAttempt(r *OutcomeStats, status, completes string, handedOff bool, retries int) {
	r.Attempts++
	r.RetriesSpent += retries
	switch {
	case completes != "":
		r.Completed[completes]++
		r.CompletedTotal++
	case status == AttemptAccepted:
		r.Accepted++
	case status == AttemptUnknown:
		r.Unknown++
	case status == AttemptNotSent:
		r.NotSent++
	case status == AttemptSuperseded || status == AttemptCancelled:
		r.Withdrawn++
	case status == "queued" || status == "retry" || status == "held" || status == "sending":
		r.Unresolved++
		if handedOff {
			r.HandedOff++
		}
	default:
		if r.Other == nil {
			r.Other = map[string]int{}
		}
		r.Other[status]++
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
