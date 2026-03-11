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

	// v16: Default to never-expire for DLQ entries (max_retries=0 means infinite retries).
	// Only update pending/queued entries; leave expired/sent/dead entries as-is.
	`UPDATE dead_letters SET max_retries = 0 WHERE status = 'pending';
	UPDATE message_deliveries SET max_retries = 0 WHERE status = 'queued';`,

	// v17: Add satellite_id to iridium_geolocation for multi-pass visualization.
	// AT-MSGEO returns the satellite sub-point, not the modem position.
	// Storing per-satellite readings enables multi-pass position estimation.
	`ALTER TABLE iridium_geolocation ADD COLUMN satellite_id TEXT NOT NULL DEFAULT '';`,

	// v18: Cellular SMS history, cell broadcast alerts, and cell tower info.
	`CREATE TABLE IF NOT EXISTS sms_messages (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		direction  TEXT NOT NULL DEFAULT 'rx',
		phone      TEXT NOT NULL DEFAULT '',
		text       TEXT NOT NULL DEFAULT '',
		status     TEXT NOT NULL DEFAULT 'delivered',
		error      TEXT NOT NULL DEFAULT '',
		timestamp  INTEGER NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_sms_messages_ts ON sms_messages(timestamp);
	CREATE INDEX IF NOT EXISTS idx_sms_messages_dir ON sms_messages(direction);

	CREATE TABLE IF NOT EXISTS cell_broadcasts (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		serial_number  INTEGER NOT NULL DEFAULT 0,
		message_id     INTEGER NOT NULL DEFAULT 0,
		channel        INTEGER NOT NULL DEFAULT 0,
		severity       TEXT NOT NULL DEFAULT 'unknown',
		text           TEXT NOT NULL DEFAULT '',
		acknowledged   INTEGER NOT NULL DEFAULT 0,
		timestamp      INTEGER NOT NULL,
		created_at     DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_cbs_ts ON cell_broadcasts(timestamp);

	CREATE TABLE IF NOT EXISTS cell_info (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		mcc         TEXT NOT NULL DEFAULT '',
		mnc         TEXT NOT NULL DEFAULT '',
		lac         TEXT NOT NULL DEFAULT '',
		cell_id     TEXT NOT NULL DEFAULT '',
		network_type TEXT NOT NULL DEFAULT '',
		rsrp        INTEGER,
		rsrq        INTEGER,
		timestamp   INTEGER NOT NULL,
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_cell_info_ts ON cell_info(timestamp);`,

	// v19: Interface-based routing model (v0.3.0 foundation — Cisco ASA-style).
	// New tables: interfaces, access_rules, object_groups, failover_groups,
	// failover_members, audit_log. Enhanced message_deliveries with S&F columns.
	// Data migration: gateway_config → interfaces, forwarding_rules → access_rules.
	// Old tables kept for backward compatibility until P4 rewire completes.
	`-- Interfaces: named, hardware-bound communication endpoints
	CREATE TABLE IF NOT EXISTS interfaces (
		id                 TEXT PRIMARY KEY,
		channel_type       TEXT NOT NULL,
		label              TEXT NOT NULL DEFAULT '',
		enabled            INTEGER NOT NULL DEFAULT 1,
		device_id          TEXT NOT NULL DEFAULT '',
		device_port        TEXT NOT NULL DEFAULT '',
		config             TEXT NOT NULL DEFAULT '{}',
		ingress_transforms TEXT NOT NULL DEFAULT '[]',
		egress_transforms  TEXT NOT NULL DEFAULT '[]',
		ingress_seq        INTEGER NOT NULL DEFAULT 0,
		egress_seq         INTEGER NOT NULL DEFAULT 0,
		created_at         TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at         TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE INDEX IF NOT EXISTS idx_interfaces_type ON interfaces(channel_type);

	-- Object groups: reusable filter sets for access rules
	CREATE TABLE IF NOT EXISTS object_groups (
		id         TEXT PRIMARY KEY,
		type       TEXT NOT NULL,
		label      TEXT NOT NULL DEFAULT '',
		members    TEXT NOT NULL DEFAULT '[]',
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	);

	-- Access rules: per-interface ingress/egress forwarding and drop rules
	CREATE TABLE IF NOT EXISTS access_rules (
		id                   INTEGER PRIMARY KEY AUTOINCREMENT,
		interface_id         TEXT NOT NULL,
		direction            TEXT NOT NULL,
		priority             INTEGER NOT NULL DEFAULT 100,
		name                 TEXT NOT NULL DEFAULT '',
		enabled              INTEGER NOT NULL DEFAULT 1,
		action               TEXT NOT NULL DEFAULT 'forward',
		forward_to           TEXT NOT NULL DEFAULT '',
		filters              TEXT NOT NULL DEFAULT '{}',
		filter_node_group    TEXT,
		filter_sender_group  TEXT,
		filter_portnum_group TEXT,
		schedule_type        TEXT NOT NULL DEFAULT 'none',
		schedule_config      TEXT NOT NULL DEFAULT '{}',
		forward_options      TEXT NOT NULL DEFAULT '{}',
		qos_level            INTEGER NOT NULL DEFAULT 1,
		rate_limit_per_min   INTEGER NOT NULL DEFAULT 0,
		rate_limit_window    INTEGER NOT NULL DEFAULT 60,
		match_count          INTEGER NOT NULL DEFAULT 0,
		last_match_at        TEXT,
		created_at           TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at           TEXT NOT NULL DEFAULT (datetime('now')),
		FOREIGN KEY (interface_id) REFERENCES interfaces(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_access_rules_iface_dir ON access_rules(interface_id, direction, priority);
	CREATE INDEX IF NOT EXISTS idx_access_rules_enabled ON access_rules(enabled);

	-- Failover groups: HA groups of same-type interfaces
	CREATE TABLE IF NOT EXISTS failover_groups (
		id         TEXT PRIMARY KEY,
		label      TEXT NOT NULL DEFAULT '',
		mode       TEXT NOT NULL DEFAULT 'failover',
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	);

	-- Failover group membership with priority ordering
	CREATE TABLE IF NOT EXISTS failover_members (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		group_id     TEXT NOT NULL,
		interface_id TEXT NOT NULL,
		priority     INTEGER NOT NULL DEFAULT 0,
		created_at   TEXT NOT NULL DEFAULT (datetime('now')),
		FOREIGN KEY (group_id) REFERENCES failover_groups(id) ON DELETE CASCADE,
		FOREIGN KEY (interface_id) REFERENCES interfaces(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_failover_members_group ON failover_members(group_id, priority);

	-- Audit log: tamper-evident hash-chain event log
	CREATE TABLE IF NOT EXISTS audit_log (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp    TEXT NOT NULL DEFAULT (datetime('now')),
		interface_id TEXT,
		direction    TEXT,
		event_type   TEXT NOT NULL,
		seq_num      INTEGER,
		delivery_id  INTEGER,
		rule_id      INTEGER,
		detail       TEXT NOT NULL DEFAULT '{}',
		prev_hash    TEXT NOT NULL DEFAULT '',
		hash         TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_audit_log_ts ON audit_log(timestamp);
	CREATE INDEX IF NOT EXISTS idx_audit_log_iface ON audit_log(interface_id);
	CREATE INDEX IF NOT EXISTS idx_audit_log_event ON audit_log(event_type);

	-- Enhance message_deliveries with store-and-forward and QoS columns
	ALTER TABLE message_deliveries ADD COLUMN ttl_seconds INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE message_deliveries ADD COLUMN expires_at TEXT;
	ALTER TABLE message_deliveries ADD COLUMN held_at TEXT;
	ALTER TABLE message_deliveries ADD COLUMN visited TEXT NOT NULL DEFAULT '[]';
	ALTER TABLE message_deliveries ADD COLUMN qos_level INTEGER NOT NULL DEFAULT 1;
	ALTER TABLE message_deliveries ADD COLUMN seq_num INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE message_deliveries ADD COLUMN signature BLOB;
	ALTER TABLE message_deliveries ADD COLUMN signer_id TEXT;
	ALTER TABLE message_deliveries ADD COLUMN ack_status TEXT;
	ALTER TABLE message_deliveries ADD COLUMN ack_timestamp TEXT;

	-- Data migration: seed interfaces from gateway_config
	-- mesh_0 always exists (mesh transport is always present)
	INSERT OR IGNORE INTO interfaces (id, channel_type, label, enabled, config)
		VALUES ('mesh_0', 'mesh', 'Meshtastic LoRa', 1, '{}');

	-- Migrate existing gateway configs to interfaces
	INSERT OR IGNORE INTO interfaces (id, channel_type, label, enabled, config)
		SELECT
			type || '_0',
			type,
			CASE type
				WHEN 'iridium' THEN 'Iridium SBD'
				WHEN 'mqtt' THEN 'MQTT Broker'
				WHEN 'webhook' THEN 'Webhook HTTP'
				WHEN 'cellular' THEN 'Cellular SMS'
				WHEN 'astrocast' THEN 'Astrocast'
				ELSE type
			END,
			enabled,
			config
		FROM gateway_config;

	-- Data migration: convert forwarding_rules to access_rules
	-- Each forwarding rule becomes an ingress rule on the source interface
	-- source_type 'any' maps to mesh_0 ingress (most common case)
	INSERT INTO access_rules (interface_id, direction, priority, name, enabled, action, forward_to, filters, qos_level, rate_limit_per_min, rate_limit_window, match_count, last_match_at, created_at, updated_at)
		SELECT
			CASE source_type
				WHEN 'any' THEN 'mesh_0'
				WHEN 'mesh' THEN 'mesh_0'
				WHEN 'channel' THEN 'mesh_0'
				WHEN 'node' THEN 'mesh_0'
				WHEN 'portnum' THEN 'mesh_0'
				WHEN 'iridium' THEN 'iridium_0'
				WHEN 'astrocast' THEN 'astrocast_0'
				WHEN 'cellular' THEN 'cellular_0'
				WHEN 'webhook' THEN 'webhook_0'
				WHEN 'mqtt' THEN 'mqtt_0'
				WHEN 'external' THEN 'mesh_0'
				ELSE 'mesh_0'
			END,
			'ingress',
			priority,
			name,
			enabled,
			'forward',
			dest_type || '_0',
			json_object(
				'keyword', COALESCE(source_keyword, ''),
				'channels', COALESCE(source_channels, ''),
				'nodes', COALESCE(source_nodes, ''),
				'portnums', COALESCE(source_portnums, '')
			),
			1,
			rate_limit_per_min,
			rate_limit_window,
			match_count,
			last_match_at,
			created_at,
			updated_at
		FROM forwarding_rules;`,

	// v20: Drop legacy forwarding_rules table.
	// Data was migrated to access_rules in v19. The legacy rules.Engine
	// gracefully handles the missing table by returning empty results.
	`DROP TABLE IF EXISTS forwarding_rules;`,

	// v21: SIM card management — store per-SIM settings (phone, PIN, label).
	// ICCID (from AT+CCID) uniquely identifies each SIM card across modems.
	// When a saved SIM is inserted, its settings are auto-applied.
	`CREATE TABLE IF NOT EXISTS sim_cards (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		iccid      TEXT NOT NULL UNIQUE,
		label      TEXT NOT NULL DEFAULT '',
		phone      TEXT NOT NULL DEFAULT '',
		pin        TEXT NOT NULL DEFAULT '',
		notes      TEXT NOT NULL DEFAULT '',
		last_seen  DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_sim_cards_iccid ON sim_cards(iccid);`,

	// v22: Set sane max_retries defaults on existing Iridium deliveries.
	// Infinite retries (max_retries=0) on paid satellite channels caused runaway
	// credit waste when SBDIX parsing bugs triggered false retry loops (CUBEOS-72).
	`UPDATE message_deliveries SET max_retries = 10
		WHERE channel = 'iridium' AND max_retries = 0 AND status IN ('queued', 'retry', 'held');
	 UPDATE dead_letters SET max_retries = 10
		WHERE max_retries = 0 AND status = 'pending';`,
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
