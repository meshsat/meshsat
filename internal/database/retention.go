package database

import (
	"fmt"
	"time"
)

// Retention: SIDN fonds project 261042, deliverable D6 [MESHSAT-1137].
// Every table that can hold personal data is either cleared by a rule
// below or named as an exception in dataclass.go, and
// retention_registry_test.go fails when a table is neither. The rules run
// together, once at start and then daily (engine.StartRetentionWorker);
// one failing rule no longer stops the others.

// RetentionRule clears one kind of personal data by age or expiry.
type RetentionRule struct {
	ID       string
	Tables   []string
	Describe string // in words, for docs/privacy-baseline.md
	Run      func(db *DB, now time.Time, days int) (int64, error)
}

// RetentionResult is what one rule did in one run.
type RetentionResult struct {
	Rule string
	N    int64
	Err  error
}

// sqliteCutoff is now minus days in the form CURRENT_TIMESTAMP writes;
// julianday() reads it and the RFC 3339 forms alike, so the comparisons
// below hold whichever way a row's time was written.
func sqliteCutoff(now time.Time, d time.Duration) string {
	return now.UTC().Add(-d).Format("2006-01-02 15:04:05")
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

// byAge deletes the rows of table whose column is older than the retention.
func byAge(table, column string) func(db *DB, now time.Time, d int) (int64, error) {
	return func(db *DB, now time.Time, d int) (int64, error) {
		res, err := db.Exec(`DELETE FROM `+table+` WHERE julianday(`+column+`) < julianday(?)`, sqliteCutoff(now, days(d)))
		if err != nil {
			return 0, fmt.Errorf("retention %s: %w", table, err)
		}
		return res.RowsAffected()
	}
}

// RetentionRules is every rule, in the order they run. The intent prune
// runs before the ledger scrub, which archives an intent's facts from its
// rows first. [MESHSAT-1581, MESHSAT-1814]
var RetentionRules = []RetentionRule{
	{ID: "messages", Tables: []string{"messages", "telemetry", "positions"},
		Describe: "deleted after the retention period",
		Run:      func(db *DB, _ time.Time, d int) (int64, error) { return db.PruneOlderThan(d) }},
	{ID: "intents", Tables: []string{"delivery_intents", "delivery_transmissions", "delivery_evidence", "delivery_submissions"},
		Describe: "closed intents with their transmissions and evidence deleted after the retention period",
		Run:      func(db *DB, _ time.Time, d int) (int64, error) { return db.PruneClosedIntents(d) }},
	{ID: "ledger", Tables: []string{"message_deliveries"},
		Describe: "a delivery that ended longer ago than the retention period keeps its time, channel and status and loses its payload, text, destination and error",
		Run:      func(db *DB, _ time.Time, d int) (int64, error) { return db.ScrubEndedDeliveries(d) }},
	{ID: "lxmf-mids", Tables: []string{"lxmf_mids"},
		Describe: "message ids deleted after their own horizon of 30 days",
		Run:      func(db *DB, now time.Time, _ int) (int64, error) { return db.PruneLXMFMids(now) }},
	{ID: "dead-letters", Tables: []string{"dead_letters"},
		Describe: "deleted after the retention period, whatever their state",
		Run:      byAge("dead_letters", "updated_at")},
	{ID: "sms", Tables: []string{"sms_messages"}, Describe: "deleted after the retention period", Run: byAge("sms_messages", "created_at")},
	{ID: "cell-peers", Tables: []string{"cell_peers"},
		Describe: "a phone on the kit's own 2G cell is forgotten once it has not been seen for the retention period",
		Run: func(db *DB, now time.Time, d int) (int64, error) {
			res, err := db.Exec(`DELETE FROM cell_peers WHERE MAX(COALESCE(julianday(first_seen_at), 0),
				COALESCE(julianday(last_rx_at), 0), COALESCE(julianday(last_tx_at), 0)) < julianday(?)`, sqliteCutoff(now, days(d)))
			if err != nil {
				return 0, fmt.Errorf("retention cell_peers: %w", err)
			}
			return res.RowsAffected()
		}},
	{ID: "webhook-log", Tables: []string{"webhook_log"}, Describe: "deleted after the retention period", Run: byAge("webhook_log", "created_at")},
	{ID: "range-tests", Tables: []string{"range_tests"}, Describe: "deleted after the retention period", Run: byAge("range_tests", "created_at")},
	{ID: "neighbor-info", Tables: []string{"neighbor_info"}, Describe: "deleted after the retention period", Run: byAge("neighbor_info", "created_at")},
	{ID: "oob-log", Tables: []string{"oob_log"}, Describe: "deleted after the retention period", Run: byAge("oob_log", "ts")},
	{ID: "received-resources", Tables: []string{"received_resources"}, Describe: "deleted after the retention period", Run: byAge("received_resources", "created_at")},
	{ID: "lxmf-attachments", Tables: []string{"lxmf_attachments"}, Describe: "deleted after the retention period, with the messages they came with", Run: byAge("lxmf_attachments", "received_at")},
	{ID: "cell-info", Tables: []string{"cell_info"}, Describe: "deleted after the retention period", Run: byAge("cell_info", "created_at")},
	{ID: "iridium-geolocation", Tables: []string{"iridium_geolocation"}, Describe: "deleted after the retention period", Run: byAge("iridium_geolocation", "created_at")},
	{ID: "hub-outbox", Tables: []string{"hub_outbox"}, Describe: "deleted after the retention period (the Hub link also keeps it to 7 days while it runs)", Run: byAge("hub_outbox", "created_at")},
	{ID: "dtn", Tables: []string{"bundle_fragments", "held_packets"},
		Describe: "deleted at their own expiry, and in any case after the retention period",
		Run: func(db *DB, now time.Time, d int) (int64, error) {
			a, err := db.CleanExpiredFragments()
			if err != nil {
				return a, err
			}
			b, err := db.CleanExpiredHeldPackets()
			if err != nil {
				return a + b, err
			}
			c, err := byAge("bundle_fragments", "created_at")(db, now, d)
			if err != nil {
				return a + b + c, err
			}
			e, err := byAge("held_packets", "created_at")(db, now, d)
			return a + b + c + e, err
		}},
	{ID: "post", Tables: []string{"post_messages", "post_replies"},
		Describe: "a Community Post message that ended (sent, answered, refused or failed) is deleted with its replies after the retention period, also while the Post is open; closing the Post deletes everything",
		Run: func(db *DB, now time.Time, d int) (int64, error) {
			return db.PrunePostsEnded(now.Add(-days(d)))
		}},
	{ID: "post-board", Tables: []string{"post_board"}, Describe: "a Safe and well line is deleted at its expiry",
		Run: func(db *DB, now time.Time, _ int) (int64, error) { return db.PrunePostBoard(now) }},
	{ID: "post-sessions", Tables: []string{"post_sessions"}, Describe: "a phone's session key is deleted after the retention period",
		Run: func(db *DB, now time.Time, d int) (int64, error) { return 0, db.PrunePostSessions(now.Add(-days(d))) }},
	{ID: "hub-route", Tables: []string{"hub_inbox", "hub_ops", "hub_op_events", "hub_op_missing"},
		Describe: "deleted once past their deadline, also when the Hub route is off",
		Run: func(db *DB, now time.Time, _ int) (int64, error) {
			a, err := db.PruneHubInbox(now)
			if err != nil {
				return int64(a), err
			}
			b, err := db.PruneHubOps(now)
			return int64(a + b), err
		}},
	{ID: "hub-commands", Tables: []string{"hub_command_log"}, Describe: "deleted after 24 hours",
		Run: func(db *DB, now time.Time, _ int) (int64, error) {
			res, err := db.Exec(`DELETE FROM hub_command_log WHERE received_at < ?`, now.Add(-HubCommandWindow).Unix())
			if err != nil {
				return 0, fmt.Errorf("retention hub_command_log: %w", err)
			}
			return res.RowsAffected()
		}},
	{ID: "tak-chat", Tables: []string{"tak_chat_threads"}, Describe: "deleted after 7 days without a message",
		Run: func(db *DB, now time.Time, _ int) (int64, error) {
			res, err := db.Exec(`DELETE FROM tak_chat_threads WHERE julianday(last_at) < julianday(?)`, sqliteCutoff(now, takChatThreadKeep))
			if err != nil {
				return 0, fmt.Errorf("retention tak_chat_threads: %w", err)
			}
			return res.RowsAffected()
		}},
	// The audit trail runs in engine.StartRetentionWorker, which holds the
	// signing key the checkpoint row needs (SigningService.RedactOlderThan).
	{ID: "audit-redact", Tables: []string{"audit_log"},
		Describe: "kept, for the hash chain; after the retention period the personal fields of a row (message text, numbers, addresses, IP addresses) are redacted in place, after a signed checkpoint row records that the chain verified intact",
		Run:      nil},
	{ID: "zigbee-readings", Tables: []string{"zigbee_sensor_readings"}, Describe: "deleted after the retention period",
		Run: func(db *DB, now time.Time, d int) (int64, error) {
			return byAge("zigbee_sensor_readings", "ts")(db, now, d)
		}},
}

// RunRetention runs every rule once and reports each; a rule that fails
// does not stop the next one.
func (db *DB) RunRetention(now time.Time, d int) []RetentionResult {
	out := make([]RetentionResult, 0, len(RetentionRules))
	for _, r := range RetentionRules {
		if r.Run == nil {
			continue // run where its key is (see the rule's comment)
		}
		n, err := r.Run(db, now, d)
		out = append(out, RetentionResult{Rule: r.ID, N: n, Err: err})
	}
	return out
}

// PrunePostsEnded deletes the Community Post messages that ended (sent,
// answered, refused or failed) before before, with their replies. A post
// still waiting (for the operator or for the sky) stays until it ends or
// the operator closes the Post, which deletes everything. [MESHSAT-1137]
func (db *DB) PrunePostsEnded(before time.Time) (int64, error) {
	rows, err := db.Query(`SELECT code, updated_at FROM post_messages WHERE status IN (?, ?, ?, ?)`,
		PostStatusSent, PostStatusReplied, PostStatusRefused, PostStatusFailed)
	if err != nil {
		return 0, fmt.Errorf("ended posts: %w", err)
	}
	var codes []string
	for rows.Next() {
		var code, at string
		if err := rows.Scan(&code, &at); err != nil {
			rows.Close()
			return 0, err
		}
		if t := parsePostTime(at); !t.IsZero() && t.Before(before) {
			codes = append(codes, code)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	var n int64
	for _, code := range codes {
		if _, err := db.Exec(`DELETE FROM post_replies WHERE code = ?`, code); err != nil {
			return n, fmt.Errorf("prune replies of %s: %w", code, err)
		}
		if _, err := db.Exec(`DELETE FROM post_messages WHERE code = ?`, code); err != nil {
			return n, fmt.Errorf("prune post %s: %w", code, err)
		}
		n++
	}
	return n, nil
}
