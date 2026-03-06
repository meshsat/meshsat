package database

import "fmt"

// migrations is an append-only list. Never edit existing entries.
var migrations = []string{
	// v1: Initial schema — messages, telemetry, positions, gateway config
	`CREATE TABLE IF NOT EXISTS messages (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		packet_id    INTEGER NOT NULL,
		from_node    TEXT NOT NULL,
		to_node      TEXT NOT NULL,
		channel      INTEGER NOT NULL DEFAULT 0,
		portnum      INTEGER NOT NULL,
		portnum_name TEXT NOT NULL DEFAULT '',
		decoded_text TEXT DEFAULT '',
		rx_snr       REAL DEFAULT 0,
		rx_time      INTEGER DEFAULT 0,
		hop_limit    INTEGER DEFAULT 0,
		hop_start    INTEGER DEFAULT 0,
		direction    TEXT NOT NULL DEFAULT 'rx',
		transport    TEXT NOT NULL DEFAULT 'radio',
		created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_messages_from ON messages(from_node);
	CREATE INDEX IF NOT EXISTS idx_messages_created ON messages(created_at);
	CREATE INDEX IF NOT EXISTS idx_messages_transport ON messages(transport);
	CREATE INDEX IF NOT EXISTS idx_messages_packet ON messages(packet_id);

	CREATE TABLE IF NOT EXISTS telemetry (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		node_id        TEXT NOT NULL,
		battery_level  INTEGER DEFAULT 0,
		voltage        REAL DEFAULT 0,
		channel_util   REAL DEFAULT 0,
		air_util_tx    REAL DEFAULT 0,
		temperature    REAL,
		humidity       REAL,
		pressure       REAL,
		uptime_seconds INTEGER DEFAULT 0,
		created_at     DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_telemetry_node ON telemetry(node_id);
	CREATE INDEX IF NOT EXISTS idx_telemetry_created ON telemetry(created_at);

	CREATE TABLE IF NOT EXISTS positions (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		node_id        TEXT NOT NULL,
		latitude       REAL NOT NULL,
		longitude      REAL NOT NULL,
		altitude       INTEGER DEFAULT 0,
		sats_in_view   INTEGER DEFAULT 0,
		ground_speed   INTEGER DEFAULT 0,
		ground_track   INTEGER DEFAULT 0,
		created_at     DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_positions_node ON positions(node_id);
	CREATE INDEX IF NOT EXISTS idx_positions_created ON positions(created_at);

	CREATE TABLE IF NOT EXISTS gateway_config (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		type       TEXT NOT NULL UNIQUE,
		enabled    BOOLEAN NOT NULL DEFAULT 0,
		config     TEXT NOT NULL DEFAULT '{}',
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER NOT NULL
	);
	INSERT INTO schema_version (version) VALUES (1);`,

	// v2: Dead-letter queue for failed satellite (Iridium) sends
	`CREATE TABLE IF NOT EXISTS dead_letters (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		packet_id    INTEGER NOT NULL,
		payload      BLOB NOT NULL,
		retries      INTEGER NOT NULL DEFAULT 0,
		max_retries  INTEGER NOT NULL DEFAULT 3,
		next_retry   DATETIME NOT NULL,
		status       TEXT NOT NULL DEFAULT 'pending',
		last_error   TEXT DEFAULT '',
		created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_dead_letters_status ON dead_letters(status);
	CREATE INDEX IF NOT EXISTS idx_dead_letters_next_retry ON dead_letters(next_retry);`,

	// v3: Add priority to dead-letter queue (0=critical, 1=normal, 2=low)
	`ALTER TABLE dead_letters ADD COLUMN priority INTEGER NOT NULL DEFAULT 1;
	CREATE INDEX IF NOT EXISTS idx_dead_letters_priority ON dead_letters(priority, next_retry);`,

	// v4: Forwarding rules, preset messages, delivery tracking, credit usage
	`CREATE TABLE IF NOT EXISTS forwarding_rules (
		id                  INTEGER PRIMARY KEY AUTOINCREMENT,
		name                TEXT NOT NULL,
		enabled             INTEGER NOT NULL DEFAULT 1,
		priority            INTEGER NOT NULL DEFAULT 1,
		source_type         TEXT NOT NULL DEFAULT 'any',
		source_channels     TEXT,
		source_nodes        TEXT,
		source_portnums     TEXT,
		source_keyword      TEXT,
		dest_type           TEXT NOT NULL,
		sat_priority        INTEGER NOT NULL DEFAULT 1,
		sat_max_delay_sec   INTEGER NOT NULL DEFAULT 0,
		sat_include_pos     INTEGER NOT NULL DEFAULT 0,
		sat_max_text_len    INTEGER NOT NULL DEFAULT 320,
		position_precision  INTEGER NOT NULL DEFAULT 32,
		rate_limit_per_min  INTEGER NOT NULL DEFAULT 0,
		rate_limit_window   INTEGER NOT NULL DEFAULT 60,
		match_count         INTEGER NOT NULL DEFAULT 0,
		last_match_at       TEXT,
		created_at          TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS preset_messages (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		name        TEXT NOT NULL,
		text        TEXT NOT NULL,
		destination TEXT NOT NULL DEFAULT 'broadcast',
		icon        TEXT,
		sort_order  INTEGER NOT NULL DEFAULT 0,
		created_at  TEXT NOT NULL DEFAULT (datetime('now'))
	);

	INSERT INTO preset_messages (name, text, destination, sort_order) VALUES
		('I''m OK', 'All good, checking in on schedule.', 'broadcast', 1),
		('Need Assistance', 'Requesting non-emergency assistance at current position.', 'broadcast', 2),
		('Returning', 'Heading back to base. ETA will follow.', 'broadcast', 3),
		('Position Report', '[GPS]', 'broadcast', 4);

	ALTER TABLE messages ADD COLUMN delivery_status TEXT NOT NULL DEFAULT 'received';
	ALTER TABLE messages ADD COLUMN delivery_error TEXT;
	ALTER TABLE messages ADD COLUMN composed_at TEXT;
	ALTER TABLE messages ADD COLUMN satellite_cost INTEGER DEFAULT 0;
	CREATE INDEX IF NOT EXISTS idx_messages_delivery ON messages(delivery_status);

	CREATE TABLE IF NOT EXISTS credit_usage (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		rule_id     INTEGER,
		credits     INTEGER NOT NULL,
		message_id  INTEGER,
		date        TEXT NOT NULL DEFAULT (date('now')),
		created_at  TEXT NOT NULL DEFAULT (datetime('now')),
		FOREIGN KEY (rule_id) REFERENCES forwarding_rules(id) ON DELETE SET NULL
	);
	CREATE INDEX IF NOT EXISTS idx_credit_usage_date ON credit_usage(date);`,

	// v5: Signal history, system config, Iridium locations, TLE cache
	`CREATE TABLE IF NOT EXISTS signal_history (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		source    TEXT NOT NULL DEFAULT 'iridium',
		timestamp INTEGER NOT NULL,
		value     REAL NOT NULL,
		UNIQUE(source, timestamp)
	);
	CREATE INDEX IF NOT EXISTS idx_signal_history_ts ON signal_history(source, timestamp);

	CREATE TABLE IF NOT EXISTS system_config (
		key        TEXT PRIMARY KEY,
		value      TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS iridium_locations (
		id      INTEGER PRIMARY KEY AUTOINCREMENT,
		name    TEXT NOT NULL,
		lat     REAL NOT NULL,
		lon     REAL NOT NULL,
		alt_m   REAL NOT NULL DEFAULT 0,
		builtin INTEGER NOT NULL DEFAULT 0
	);
	INSERT INTO iridium_locations (name, lat, lon, alt_m, builtin) VALUES
		('Leiden, NL', 52.1601, 4.4970, 0, 1),
		('Thessaloniki, GR', 40.6401, 22.9444, 0, 1);

	CREATE TABLE IF NOT EXISTS iridium_tle_cache (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		satellite_name TEXT NOT NULL,
		line1          TEXT NOT NULL,
		line2          TEXT NOT NULL,
		fetched_at     INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_tle_cache_fetched ON iridium_tle_cache(fetched_at);`,

	// v6: Inbound rule support — destination channel and target node for mesh injection
	`ALTER TABLE forwarding_rules ADD COLUMN dest_channel INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE forwarding_rules ADD COLUMN dest_node TEXT;`,

	// v7: Queue direction tracking for relay visibility
	`ALTER TABLE dead_letters ADD COLUMN direction TEXT NOT NULL DEFAULT 'outbound';`,

	// v8: Store plaintext preview alongside binary payload for display
	`ALTER TABLE dead_letters ADD COLUMN text_preview TEXT NOT NULL DEFAULT '';`,

	// v9: Pass quality log for smart scheduler — tracks actual signal during predicted passes
	`CREATE TABLE IF NOT EXISTS pass_quality_log (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		satellite       TEXT NOT NULL,
		aos             INTEGER NOT NULL,
		los             INTEGER NOT NULL,
		peak_elev_deg   REAL NOT NULL,
		actual_bars_avg REAL,
		actual_bars_max INTEGER,
		mo_attempts     INTEGER NOT NULL DEFAULT 0,
		mo_successes    INTEGER NOT NULL DEFAULT 0,
		created_at      DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_pass_quality_elev ON pass_quality_log(peak_elev_deg);`,

	// v10: Iridium geolocation log — AT-MSGEO readings from the satellite modem
	`CREATE TABLE IF NOT EXISTS iridium_geolocation (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		source      TEXT NOT NULL DEFAULT 'iridium',
		lat         REAL NOT NULL,
		lon         REAL NOT NULL,
		alt_km      REAL NOT NULL DEFAULT 0,
		accuracy_km REAL NOT NULL DEFAULT 100,
		timestamp   INTEGER NOT NULL,
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_iridium_geo_ts ON iridium_geolocation(timestamp);
	CREATE INDEX IF NOT EXISTS idx_iridium_geo_source ON iridium_geolocation(source, timestamp);`,

	// v11: Cellular signal history for 4G/LTE modem
	`CREATE TABLE IF NOT EXISTS cellular_signal_history (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp  INTEGER NOT NULL,
		bars       INTEGER NOT NULL,
		dbm        INTEGER NOT NULL,
		technology TEXT NOT NULL DEFAULT 'unknown',
		operator   TEXT NOT NULL DEFAULT '',
		UNIQUE(timestamp)
	);
	CREATE INDEX IF NOT EXISTS idx_cell_signal_ts ON cellular_signal_history(timestamp);`,

	// v12: Neighbor info tracking and range test log (DO NOT EDIT)
	`CREATE TABLE IF NOT EXISTS neighbor_info (
		id                  INTEGER PRIMARY KEY AUTOINCREMENT,
		node_id             INTEGER NOT NULL,
		neighbor_node_id    INTEGER NOT NULL,
		snr                 REAL NOT NULL DEFAULT 0,
		last_rx_time        INTEGER NOT NULL DEFAULT 0,
		broadcast_interval  INTEGER NOT NULL DEFAULT 0,
		created_at          DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_neighbor_info_node ON neighbor_info(node_id);
	CREATE INDEX IF NOT EXISTS idx_neighbor_info_created ON neighbor_info(created_at);

	CREATE TABLE IF NOT EXISTS range_tests (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		from_node   TEXT NOT NULL,
		to_node     TEXT NOT NULL DEFAULT '',
		text        TEXT NOT NULL DEFAULT '',
		rx_snr      REAL DEFAULT 0,
		rx_rssi     INTEGER DEFAULT 0,
		hop_limit   INTEGER DEFAULT 0,
		hop_start   INTEGER DEFAULT 0,
		direction   TEXT NOT NULL DEFAULT 'rx',
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_range_tests_from ON range_tests(from_node);
	CREATE INDEX IF NOT EXISTS idx_range_tests_created ON range_tests(created_at);`,

	// v13: SMS contacts (address book) and webhook activity log
	`CREATE TABLE IF NOT EXISTS sms_contacts (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL,
		phone      TEXT NOT NULL UNIQUE,
		notes      TEXT NOT NULL DEFAULT '',
		auto_fwd   INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_sms_contacts_phone ON sms_contacts(phone);

	CREATE TABLE IF NOT EXISTS webhook_log (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		direction  TEXT NOT NULL DEFAULT 'outbound',
		url        TEXT NOT NULL DEFAULT '',
		method     TEXT NOT NULL DEFAULT 'POST',
		status     INTEGER NOT NULL DEFAULT 0,
		payload    TEXT NOT NULL DEFAULT '',
		response   TEXT NOT NULL DEFAULT '',
		error      TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_webhook_log_created ON webhook_log(created_at);
	CREATE INDEX IF NOT EXISTS idx_webhook_log_direction ON webhook_log(direction);`,

	// v14: Message delivery ledger — unified delivery tracking across all channels
	`CREATE TABLE IF NOT EXISTS message_deliveries (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		msg_ref      TEXT NOT NULL,
		rule_id      INTEGER,
		channel      TEXT NOT NULL,
		status       TEXT NOT NULL DEFAULT 'queued',
		priority     INTEGER NOT NULL DEFAULT 1,
		payload      BLOB,
		text_preview TEXT NOT NULL DEFAULT '',
		retries      INTEGER NOT NULL DEFAULT 0,
		max_retries  INTEGER NOT NULL DEFAULT 3,
		next_retry   DATETIME,
		last_error   TEXT DEFAULT '',
		channel_ref  TEXT DEFAULT '',
		cost         INTEGER DEFAULT 0,
		created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_deliveries_msg ON message_deliveries(msg_ref);
	CREATE INDEX IF NOT EXISTS idx_deliveries_channel_status ON message_deliveries(channel, status);
	CREATE INDEX IF NOT EXISTS idx_deliveries_retry ON message_deliveries(status, next_retry);

	-- Migrate existing dead_letters to message_deliveries
	INSERT INTO message_deliveries (msg_ref, channel, status, priority, payload, text_preview, retries, max_retries, next_retry, last_error, created_at, updated_at)
		SELECT
			COALESCE(CAST(id AS TEXT), ''),
			'iridium',
			CASE WHEN status = 'dead' THEN 'dead' WHEN status = 'pending' THEN 'queued' ELSE status END,
			COALESCE(priority, 1),
			payload,
			COALESCE(text_preview, ''),
			COALESCE(retries, 0),
			COALESCE(max_retries, 3),
			next_retry,
			COALESCE(last_error, ''),
			created_at,
			COALESCE(updated_at, created_at)
		FROM dead_letters
		WHERE EXISTS (SELECT 1 FROM sqlite_master WHERE type='table' AND name='dead_letters');`,

	// v15: Astrocast LEO satellite TLE cache (separate from Iridium)
	`CREATE TABLE IF NOT EXISTS astrocast_tle_cache (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		satellite_name TEXT NOT NULL,
		line1          TEXT NOT NULL,
		line2          TEXT NOT NULL,
		fetched_at     INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_astro_tle_cache_fetched ON astrocast_tle_cache(fetched_at);`,
}

func (db *DB) migrate() error {
	// Ensure schema_version table exists for fresh DBs
	var tableExists int
	err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_version'").Scan(&tableExists)
	if err != nil {
		return fmt.Errorf("check schema_version: %w", err)
	}

	currentVersion := 0
	if tableExists > 0 {
		if err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&currentVersion); err != nil {
			return fmt.Errorf("read schema version: %w", err)
		}
	}

	for i := currentVersion; i < len(migrations); i++ {
		if _, err := db.Exec(migrations[i]); err != nil {
			return fmt.Errorf("migration v%d: %w", i+1, err)
		}
		if i > 0 { // v1 inserts its own version row
			if _, err := db.Exec("UPDATE schema_version SET version = ?", i+1); err != nil {
				return fmt.Errorf("update schema version to %d: %w", i+1, err)
			}
		}
	}
	return nil
}
