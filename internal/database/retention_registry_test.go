package database

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// The privacy baseline's regression test: SIDN fonds project 261042,
// deliverable D6 [MESHSAT-1137]. It fails when a table holding personal
// data has no retention rule, when a new table is not classified, and when
// a rule does not clear what it names.

func liveTables(t *testing.T, db *DB) []string {
	t.Helper()
	var names []string
	if err := db.Select(&names, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`); err != nil {
		t.Fatal(err)
	}
	return names
}

func ruleByID(id string) *RetentionRule {
	for i := range RetentionRules {
		if RetentionRules[i].ID == id {
			return &RetentionRules[i]
		}
	}
	return nil
}

// Every table the migrations make is classified, and nothing classified is
// missing: a new table fails here until it is added to dataclass.go.
func TestEveryTableIsClassified(t *testing.T) {
	db := testDB(t)
	live := liveTables(t, db)
	classified := map[string]bool{}
	for _, ti := range Tables {
		if classified[ti.Table] {
			t.Errorf("%s is classified twice", ti.Table)
		}
		classified[ti.Table] = true
		if !slices.Contains(live, ti.Table) {
			t.Errorf("%s is classified but no migration makes it", ti.Table)
		}
		switch ti.Class {
		case ClassPersonal, ClassOperator, ClassNone:
		default:
			t.Errorf("%s: unknown class %q", ti.Table, ti.Class)
		}
	}
	for _, name := range live {
		if !classified[name] {
			t.Errorf("table %s is not classified in dataclass.go: say whether it holds personal data and how it is cleared", name)
		}
	}
}

// The regression test the contract names: a table that holds personal data
// and has no retention rule fails the build. An exception has to be named
// with its reason.
func TestEveryPersonalTableHasARetentionRule(t *testing.T) {
	for _, ti := range Tables {
		if ti.Class != ClassPersonal {
			continue
		}
		if ti.Rule == "" && ti.Exception == "" {
			t.Errorf("%s holds personal data (%s) and has no retention rule and no named exception", ti.Table, ti.Holds)
			continue
		}
		if ti.Rule == "" {
			continue
		}
		r := ruleByID(ti.Rule)
		if r == nil {
			t.Errorf("%s names retention rule %q, which does not exist", ti.Table, ti.Rule)
			continue
		}
		if !slices.Contains(r.Tables, ti.Table) {
			t.Errorf("rule %q does not list %s", r.ID, ti.Table)
		}
	}
	for _, r := range RetentionRules {
		if r.Describe == "" {
			t.Errorf("rule %q says nothing about what it does", r.ID)
		}
		for _, name := range r.Tables {
			found := false
			for _, ti := range Tables {
				if ti.Table == name {
					found = true
				}
			}
			if !found {
				t.Errorf("rule %q names %s, which is not classified", r.ID, name)
			}
		}
	}
}

// Each rule clears an old row and keeps a fresh one.
func TestRetentionClearsOldRowsAndKeepsNewOnes(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -60)
	fresh := now.Add(-time.Hour)
	sql := func(t time.Time) string { return t.Format("2006-01-02 15:04:05") }
	rfc := func(t time.Time) string { return t.Format(time.RFC3339Nano) }

	cases := []struct {
		table  string
		insert string // with one %s for the time
		format func(time.Time) string
	}{
		{"sms_messages", `INSERT INTO sms_messages (phone, text, timestamp, created_at) VALUES ('+44 7700 900123', 'hi', 0, '%s')`, sql},
		{"webhook_log", `INSERT INTO webhook_log (url, payload, created_at) VALUES ('https://example.org', 'x', '%s')`, sql},
		{"range_tests", `INSERT INTO range_tests (from_node, text, created_at) VALUES ('!a11ce001', 'seq 1', '%s')`, sql},
		{"neighbor_info", `INSERT INTO neighbor_info (node_id, neighbor_node_id, created_at) VALUES (1, 2, '%s')`, sql},
		{"oob_log", `INSERT INTO oob_log (ts, direction, kind, from_addr) VALUES ('%s', 'in', 'cmd', '+44 7700 900123')`, sql},
		{"received_resources", `INSERT INTO received_resources (hash, size, data, created_at) VALUES (hex(randomblob(8)), 1, x'00', '%s')`, sql},
		{"lxmf_attachments", `INSERT INTO lxmf_attachments (message_id, lxmf_hash, idx, kind, size, data, received_at) VALUES (abs(random()), 'h', 0, 'audio', 1, x'00', '%s')`, sql},
		{"cell_info", `INSERT INTO cell_info (cell_id, timestamp, created_at) VALUES ('1', 0, '%s')`, sql},
		{"iridium_geolocation", `INSERT INTO iridium_geolocation (lat, lon, timestamp, created_at) VALUES (52.1, 4.4, 0, '%s')`, sql},
		{"hub_outbox", `INSERT INTO hub_outbox (topic, payload, created_at) VALUES ('t', 'p', '%s')`, sql},
		{"dead_letters", `INSERT INTO dead_letters (packet_id, payload, next_retry, status, updated_at) VALUES (1, x'00', '2026-01-01 00:00:00', 'pending', '%s')`, rfc},
		{"cell_peers", `INSERT INTO cell_peers (msisdn, first_seen_at, last_rx_at) VALUES (hex(randomblob(4)), '%[1]s', '%[1]s')`, sql},
		{"tak_chat_threads", `INSERT INTO tak_chat_threads (node, peer_uid, last_at) VALUES (hex(randomblob(4)), 'u', '%s')`, rfc},
		{"zigbee_sensor_readings", `INSERT INTO zigbee_sensor_readings (ts, ieee_addr, cluster, attribute) VALUES ('%s', 'a', 1, 1)`, sql},
		{"post_sessions", `INSERT INTO post_sessions (client_key, created_at, last_seen_at) VALUES (hex(randomblob(4)), '%[1]s', '%[1]s')`, rfc},
	}
	for _, tc := range cases {
		t.Run(tc.table, func(t *testing.T) {
			db := testDB(t)
			for _, at := range []time.Time{old, fresh} {
				if _, err := db.Exec(fmt.Sprintf(tc.insert, tc.format(at))); err != nil {
					t.Fatalf("insert: %v", err)
				}
			}
			for _, r := range db.RunRetention(now, 30) {
				if r.Err != nil {
					t.Fatalf("rule %s: %v", r.Rule, r.Err)
				}
			}
			var n int
			if err := db.Get(&n, `SELECT COUNT(*) FROM `+tc.table); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("%d rows left, want the fresh one only", n)
			}
		})
	}

	t.Run("hub_command_log", func(t *testing.T) {
		db := testDB(t)
		for i, at := range []time.Time{now.Add(-48 * time.Hour), fresh} {
			if _, err := db.Exec(`INSERT INTO hub_command_log (request_id, cmd, received_at) VALUES (?, 'ping', ?)`, fmt.Sprint(i), at.Unix()); err != nil {
				t.Fatal(err)
			}
		}
		db.RunRetention(now, 30)
		var n int
		db.Get(&n, `SELECT COUNT(*) FROM hub_command_log`)
		if n != 1 {
			t.Fatalf("%d rows left, want one", n)
		}
	})

	t.Run("post: an ended message goes with its replies, a waiting one stays", func(t *testing.T) {
		db := testDB(t)
		for _, p := range []struct {
			code, status string
			at           time.Time
		}{
			{"OLDSENT", PostStatusSent, old},
			{"OLDWAIT", PostStatusPending, old},
			{"NEWSENT", PostStatusSent, fresh},
		} {
			if _, err := db.Exec(`INSERT INTO post_messages (code, session, recipient, text, status, created_at, updated_at) VALUES (?, 's', '+44 7700 900123', 'hi', ?, ?, ?)`,
				p.code, p.status, rfc(p.at), rfc(p.at)); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO post_replies (code, from_addr, text, received_at) VALUES (?, '+44 7700 900123', 'ok', ?)`, p.code, rfc(p.at)); err != nil {
				t.Fatal(err)
			}
		}
		db.RunRetention(now, 30)
		var codes []string
		db.Select(&codes, `SELECT code FROM post_messages ORDER BY code`)
		var replies int
		db.Get(&replies, `SELECT COUNT(*) FROM post_replies`)
		if !slices.Equal(codes, []string{"NEWSENT", "OLDWAIT"}) || replies != 2 {
			t.Fatalf("posts %v, replies %d; want NEWSENT and OLDWAIT with their two replies", codes, replies)
		}
	})
}
