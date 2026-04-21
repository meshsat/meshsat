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

	// v23: Unified contacts — one entity, many transport addresses (CUBEOS-73).
	// Replaces the single-transport sms_contacts with a multi-address model.
	// Existing sms_contacts are migrated: each becomes a contact + SMS address.
	`CREATE TABLE IF NOT EXISTS contacts (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		display_name TEXT NOT NULL,
		notes        TEXT NOT NULL DEFAULT '',
		created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS contact_addresses (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		contact_id     INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
		type           TEXT NOT NULL,
		address        TEXT NOT NULL,
		label          TEXT NOT NULL DEFAULT '',
		encryption_key TEXT NOT NULL DEFAULT '',
		is_primary     INTEGER NOT NULL DEFAULT 0,
		auto_fwd       INTEGER NOT NULL DEFAULT 0,
		created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(type, address)
	);
	CREATE INDEX IF NOT EXISTS idx_contact_addresses_contact ON contact_addresses(contact_id);
	CREATE INDEX IF NOT EXISTS idx_contact_addresses_lookup ON contact_addresses(type, address);

	-- Migrate existing sms_contacts into unified contacts
	INSERT INTO contacts (display_name, notes, created_at, updated_at)
		SELECT name, COALESCE(notes, ''), created_at, updated_at FROM sms_contacts;

	INSERT INTO contact_addresses (contact_id, type, address, label, is_primary, auto_fwd, created_at)
		SELECT c.id, 'sms', sc.phone, 'Phone', 1, sc.auto_fwd, sc.created_at
		FROM sms_contacts sc
		JOIN contacts c ON c.display_name = sc.name AND c.created_at = sc.created_at;`,

	// v24: Reticulum-inspired routing — destination table for announce discovery (MESHSAT-20).
	// Stores known remote identities discovered via announce packets.
	// dest_hash is the 16-byte truncated SHA-256(signing_pub || encryption_pub), hex-encoded.
	`CREATE TABLE IF NOT EXISTS routing_destinations (
		dest_hash      TEXT PRIMARY KEY,
		signing_pub    BLOB NOT NULL,
		encryption_pub BLOB NOT NULL,
		app_data       BLOB,
		hop_count      INTEGER NOT NULL DEFAULT 0,
		source_iface   TEXT NOT NULL DEFAULT '',
		first_seen     TEXT NOT NULL DEFAULT (datetime('now')),
		last_seen      TEXT NOT NULL DEFAULT (datetime('now')),
		announce_count INTEGER NOT NULL DEFAULT 1
	);
	CREATE INDEX IF NOT EXISTS idx_routing_dest_iface ON routing_destinations(source_iface);
	CREATE INDEX IF NOT EXISTS idx_routing_dest_seen ON routing_destinations(last_seen);`,

	// v25: Routing links — tracks established encrypted links between nodes (MESHSAT-20).
	// Each link represents a 3-packet X25519 → ECDH → AES-256 handshake.
	`CREATE TABLE IF NOT EXISTS routing_links (
		link_id        TEXT PRIMARY KEY,
		dest_hash      TEXT NOT NULL DEFAULT '',
		state          TEXT NOT NULL DEFAULT 'pending',
		is_initiator   INTEGER NOT NULL DEFAULT 0,
		created_at     TEXT NOT NULL DEFAULT (datetime('now')),
		last_activity  TEXT NOT NULL DEFAULT (datetime('now')),
		closed_at      TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_routing_links_dest ON routing_links(dest_hash);
	CREATE INDEX IF NOT EXISTS idx_routing_links_state ON routing_links(state);`,

	// v26: Device registry — tracks physical devices by IMEI (MESHSAT-98).
	`CREATE TABLE IF NOT EXISTS devices (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		imei       TEXT NOT NULL UNIQUE,
		label      TEXT NOT NULL DEFAULT '',
		type       TEXT NOT NULL DEFAULT '',
		notes      TEXT NOT NULL DEFAULT '',
		last_seen  TEXT,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_devices_imei ON devices(imei);`,

	// v27: Per-device YAML configuration versioning (MESHSAT-99).
	// Stores immutable config snapshots per device with auto-incrementing version numbers.
	// Each save creates a new version; rollback creates a new version from an old snapshot.
	`CREATE TABLE IF NOT EXISTS device_config_versions (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		device_id  INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
		version    INTEGER NOT NULL,
		yaml       TEXT NOT NULL,
		comment    TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		UNIQUE(device_id, version)
	);
	CREATE INDEX IF NOT EXISTS idx_device_config_device ON device_config_versions(device_id, version DESC);`,

	// v28: Per-device satellite rate limiting (MESHSAT-124).
	// satellite_rate_limits: token bucket config + daily/monthly caps per device.
	// satellite_usage: daily usage counters for enforcing caps.
	`CREATE TABLE IF NOT EXISTS satellite_rate_limits (
		device_id     INTEGER PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
		daily_limit   INTEGER NOT NULL DEFAULT 50,
		monthly_limit INTEGER NOT NULL DEFAULT 1000,
		burst_size    INTEGER NOT NULL DEFAULT 10,
		refill_rate   REAL NOT NULL DEFAULT 0.0167,
		enabled       INTEGER NOT NULL DEFAULT 1,
		override_until TEXT,
		updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE TABLE IF NOT EXISTS satellite_usage (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		device_id INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
		day       TEXT NOT NULL,
		sends     INTEGER NOT NULL DEFAULT 0,
		credits   INTEGER NOT NULL DEFAULT 0,
		UNIQUE(device_id, day)
	);
	CREATE INDEX IF NOT EXISTS idx_sat_usage_device_day ON satellite_usage(device_id, day DESC);`,

	// v29: Per-device constellation preferences (MESHSAT-121).
	// Stores which satellite constellation a device prefers and the selection strategy
	// when multiple constellations are available. Used by ConstellationManager.
	`CREATE TABLE IF NOT EXISTS device_constellation_prefs (
		device_id                INTEGER PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
		preferred_constellation  TEXT NOT NULL DEFAULT '',
		strategy                 TEXT NOT NULL DEFAULT 'availability',
		updated_at               TEXT NOT NULL DEFAULT (datetime('now'))
	);`,

	// v30: Cloudloop credit balance polling (MESHSAT-100).
	// Stores credit balance snapshots fetched from Ground Control/Cloudloop API.
	`CREATE TABLE IF NOT EXISTS credit_balance (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		balance    INTEGER NOT NULL,
		currency   TEXT NOT NULL DEFAULT 'credits',
		source     TEXT NOT NULL DEFAULT 'cloudloop',
		fetched_at TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE INDEX IF NOT EXISTS idx_credit_balance_fetched ON credit_balance(fetched_at);`,

	// v31: Hub outbox — offline message queue for store-and-forward (MESHSAT-289).
	// When the Hub MQTT broker is unreachable, hub-bound messages are queued
	// locally and replayed in FIFO order when the connection is restored.
	`CREATE TABLE IF NOT EXISTS hub_outbox (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		topic       TEXT NOT NULL,
		payload     TEXT NOT NULL,
		qos         INTEGER NOT NULL DEFAULT 1,
		retry_count INTEGER NOT NULL DEFAULT 0,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_hub_outbox_created ON hub_outbox(created_at);`,

	// v32: Add iridium_imt interface for RockBLOCK 9704 (IMT/JSPR) [MESHSAT-244].
	// SBD (9603) and IMT (9704) are fundamentally different protocols with different
	// message sizes, costs, and capabilities. They need separate interface IDs so the
	// delivery worker can route to the correct gateway.
	`INSERT OR IGNORE INTO interfaces (id, channel_type, label, enabled, config)
		VALUES ('iridium_imt_0', 'iridium_imt', 'Iridium IMT (9704)', 1, '{}');`,

	// v33: Multi-instance gateway support — xN modems of same type [MESHSAT-335].
	// Recreate gateway_config with instance_id column. The original table had
	// UNIQUE(type) which prevents multiple modems of the same type. The new table
	// uses UNIQUE(type, instance_id) instead. Backfill existing rows with "{type}_0".
	`CREATE TABLE IF NOT EXISTS gateway_config_new (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		type        TEXT NOT NULL,
		instance_id TEXT NOT NULL DEFAULT '',
		enabled     BOOLEAN NOT NULL DEFAULT 0,
		config      TEXT NOT NULL DEFAULT '{}',
		updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(type, instance_id)
	);
	INSERT OR IGNORE INTO gateway_config_new (id, type, instance_id, enabled, config, updated_at)
		SELECT id, type, type || '_0', enabled, config, updated_at FROM gateway_config;
	DROP TABLE gateway_config;
	ALTER TABLE gateway_config_new RENAME TO gateway_config;`,

	// v34: Track last mo_status per DLQ entry [MESHSAT-341].
	// Prevents false-positive "already transmitted" when MO buffer is empty
	// after the bridge cleared it following a failed SBDIX (mo_status=32/36).
	// -1 = no SBDIX attempted yet.
	`ALTER TABLE dead_letters ADD COLUMN last_mo_status INTEGER NOT NULL DEFAULT -1;`,

	// v35: Received resources table for Reticulum resource transfer [MESHSAT-199].
	// Stores files/firmware received via chunked Reticulum delivery.
	`CREATE TABLE IF NOT EXISTS received_resources (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		hash TEXT NOT NULL UNIQUE,
		filename TEXT NOT NULL DEFAULT '',
		content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
		size INTEGER NOT NULL,
		data BLOB NOT NULL,
		source_iface TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`,

	// v36: Key bundles table for cross-platform key exchange.
	// Stores AES-256 keys wrapped with master key envelope encryption.
	`CREATE TABLE IF NOT EXISTS key_bundles (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		channel_type  TEXT NOT NULL,
		address       TEXT NOT NULL,
		encrypted_key BLOB NOT NULL,
		key_version   INTEGER NOT NULL DEFAULT 1,
		status        TEXT NOT NULL DEFAULT 'active',
		expires_at    DATETIME,
		created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(channel_type, address, key_version)
	);
	CREATE INDEX IF NOT EXISTS idx_key_bundles_lookup ON key_bundles(channel_type, address, status);`,

	// v37: Credential cache for centralized cert/credential management [MESHSAT-356].
	// Stores TLS certificates and provider credentials encrypted with bridge master key.
	// Mirrors hub credentials table; source tracks local upload vs hub-distributed.
	`CREATE TABLE IF NOT EXISTS credential_cache (
		id             TEXT PRIMARY KEY,
		provider       TEXT NOT NULL,
		name           TEXT NOT NULL,
		cred_type      TEXT NOT NULL,
		encrypted_data BLOB NOT NULL,
		cert_not_after TEXT,
		cert_subject   TEXT NOT NULL DEFAULT '',
		cert_fingerprint TEXT NOT NULL DEFAULT '',
		version        INTEGER NOT NULL DEFAULT 1,
		source         TEXT NOT NULL DEFAULT 'local',
		applied        INTEGER NOT NULL DEFAULT 0,
		received_at    TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at     TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE INDEX IF NOT EXISTS idx_cred_cache_provider ON credential_cache(provider);`,

	// v38: DTN concepts — custody transfer columns, bundle fragments, held packets [MESHSAT-408].
	`ALTER TABLE message_deliveries ADD COLUMN custodian_hash TEXT DEFAULT '';
	ALTER TABLE message_deliveries ADD COLUMN custody_id TEXT DEFAULT '';
	ALTER TABLE message_deliveries ADD COLUMN custody_accepted_at TEXT;
	ALTER TABLE message_deliveries ADD COLUMN custody_source_hash TEXT DEFAULT '';

	CREATE TABLE IF NOT EXISTS bundle_fragments (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		bundle_id      TEXT NOT NULL,
		fragment_index INTEGER NOT NULL,
		fragment_total INTEGER NOT NULL,
		total_size     INTEGER NOT NULL,
		payload        BLOB NOT NULL,
		source_iface   TEXT NOT NULL DEFAULT '',
		delivery_id    INTEGER,
		created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
		expires_at     TEXT,
		UNIQUE(bundle_id, fragment_index)
	);
	CREATE INDEX IF NOT EXISTS idx_bundle_frag_bundle ON bundle_fragments(bundle_id);
	CREATE INDEX IF NOT EXISTS idx_bundle_frag_expires ON bundle_fragments(expires_at);

	CREATE TABLE IF NOT EXISTS held_packets (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		dest_hash    TEXT NOT NULL,
		packet       BLOB NOT NULL,
		source_iface TEXT NOT NULL DEFAULT '',
		ttl_seconds  INTEGER NOT NULL DEFAULT 300,
		created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
		expires_at   TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_held_packets_dest ON held_packets(dest_hash);
	CREATE INDEX IF NOT EXISTS idx_held_packets_expires ON held_packets(expires_at);`,

	// v39: Time sync state for GPS-denied clock synchronization [MESHSAT-410].
	`CREATE TABLE IF NOT EXISTS time_sync_state (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		source         TEXT NOT NULL,
		stratum        INTEGER NOT NULL DEFAULT 5,
		offset_ns      INTEGER NOT NULL DEFAULT 0,
		uncertainty_ns INTEGER NOT NULL DEFAULT 0,
		last_sync      TEXT NOT NULL DEFAULT (datetime('now')),
		peer_hash      TEXT NOT NULL DEFAULT '',
		UNIQUE(source, peer_hash)
	);
	CREATE INDEX IF NOT EXISTS idx_time_sync_source ON time_sync_state(source);`,

	// v40: HeMB bond groups and members — interface bonding for multi-path delivery [MESHSAT-421].
	// bond_groups: defines a bonding group with cost budget and minimum reliability constraints.
	// bond_members: maps interfaces into bond groups with priority ordering.
	// message_deliveries: hemb_stream_id and hemb_gen_id for HeMB stream tracking.
	`CREATE TABLE IF NOT EXISTS bond_groups (
		id              TEXT PRIMARY KEY,
		label           TEXT NOT NULL DEFAULT '',
		cost_budget     REAL NOT NULL DEFAULT 0,
		min_reliability REAL NOT NULL DEFAULT 0,
		created_at      TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS bond_members (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		group_id     TEXT NOT NULL,
		interface_id TEXT NOT NULL,
		priority     INTEGER NOT NULL DEFAULT 0,
		created_at   TEXT NOT NULL DEFAULT (datetime('now')),
		FOREIGN KEY (group_id) REFERENCES bond_groups(id) ON DELETE CASCADE,
		FOREIGN KEY (interface_id) REFERENCES interfaces(id) ON DELETE CASCADE,
		UNIQUE(group_id, interface_id)
	);
	CREATE INDEX IF NOT EXISTS idx_bond_members_group ON bond_members(group_id, priority);

	ALTER TABLE message_deliveries ADD COLUMN hemb_stream_id TEXT DEFAULT '';
	ALTER TABLE message_deliveries ADD COLUMN hemb_gen_id TEXT DEFAULT '';`,

	// v41: HeMB event history for persistent observability [MESHSAT-440].
	// Stores HeMB events (symbol sent/received, generation decoded/failed, etc.)
	// for retrospective debugging and matrix inspection.
	// Retention: configurable via MESHSAT_HEMB_EVENT_RETENTION_DAYS (default 7).
	`CREATE TABLE IF NOT EXISTS hemb_events (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		ts            TEXT NOT NULL DEFAULT (datetime('now')),
		event_type    TEXT NOT NULL,
		stream_id     INTEGER,
		generation_id INTEGER,
		bearer_idx    INTEGER,
		payload       TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_hemb_events_ts ON hemb_events(ts);
	CREATE INDEX IF NOT EXISTS idx_hemb_events_stream_gen ON hemb_events(stream_id, generation_id);`,

	// v42: Zigbee device manager — persistent device metadata + sensor time-series
	// + per-device routing config [MESHSAT-509].
	// Replaces the in-memory-only DirectZigBeeTransport.devices map so paired
	// devices, user-given aliases, and sensor history survive container restarts.
	// zigbee_devices: one row per paired device (keyed by IEEE 64-bit address).
	// zigbee_sensor_readings: append-only time series (cluster + attribute keyed).
	// zigbee_device_routing: per-device routing rules — where sensor events fan out
	// (tak/mesh/hub/log) and the CoT type override for TAK markers.
	`CREATE TABLE IF NOT EXISTS zigbee_devices (
		ieee_addr      TEXT PRIMARY KEY,
		short_addr     INTEGER NOT NULL,
		alias          TEXT NOT NULL DEFAULT '',
		manufacturer   TEXT NOT NULL DEFAULT '',
		model          TEXT NOT NULL DEFAULT '',
		device_type    TEXT NOT NULL DEFAULT '',
		endpoint       INTEGER NOT NULL DEFAULT 0,
		first_seen     TEXT NOT NULL DEFAULT (datetime('now')),
		last_seen      TEXT NOT NULL DEFAULT (datetime('now')),
		lqi            INTEGER NOT NULL DEFAULT 0,
		battery_pct    INTEGER NOT NULL DEFAULT -1,
		last_temp      REAL,
		last_humidity  REAL,
		last_onoff     INTEGER NOT NULL DEFAULT -1,
		message_count  INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_zigbee_devices_short ON zigbee_devices(short_addr);
	CREATE INDEX IF NOT EXISTS idx_zigbee_devices_lastseen ON zigbee_devices(last_seen DESC);

	CREATE TABLE IF NOT EXISTS zigbee_sensor_readings (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		ts          TEXT NOT NULL DEFAULT (datetime('now')),
		ieee_addr   TEXT NOT NULL,
		cluster     INTEGER NOT NULL,
		attribute   INTEGER NOT NULL,
		value_num   REAL,
		value_text  TEXT,
		unit        TEXT NOT NULL DEFAULT '',
		lqi         INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_zigbee_readings_dev_ts ON zigbee_sensor_readings(ieee_addr, ts DESC);
	CREATE INDEX IF NOT EXISTS idx_zigbee_readings_cluster ON zigbee_sensor_readings(ieee_addr, cluster, attribute, ts DESC);

	CREATE TABLE IF NOT EXISTS zigbee_device_routing (
		ieee_addr     TEXT PRIMARY KEY,
		to_tak        INTEGER NOT NULL DEFAULT 1,
		to_mesh       INTEGER NOT NULL DEFAULT 0,
		to_hub        INTEGER NOT NULL DEFAULT 1,
		to_log        INTEGER NOT NULL DEFAULT 1,
		cot_type      TEXT NOT NULL DEFAULT '',
		min_interval  INTEGER NOT NULL DEFAULT 0,
		updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
	);`,

	// v43: Track last IAS Zone status per device — surfaces alarm/tamper/
	// battery-low state in the device manager without needing to query the
	// time-series table for every list render. -1 = unknown (no IAS Zone
	// frame ever received). [MESHSAT-509]
	`ALTER TABLE zigbee_devices ADD COLUMN last_zone_status INTEGER NOT NULL DEFAULT -1;`,

	// v44: Unified directory — Phase 1 foundation [MESHSAT-534].
	// SCIM-shaped contact entity with string IDs (128-bit random hex) so
	// the same identity round-trips Bridge ↔ Hub ↔ Android without collision.
	// Old v23 contacts are backfilled; the legacy tables stay readable as
	// a shim for one release then are dropped in v50 per MESHSAT-542.
	`CREATE TABLE IF NOT EXISTS directory_contacts (
		id                TEXT PRIMARY KEY,
		tenant_id         TEXT NOT NULL DEFAULT '',
		display_name      TEXT NOT NULL,
		given_name        TEXT NOT NULL DEFAULT '',
		family_name       TEXT NOT NULL DEFAULT '',
		org               TEXT NOT NULL DEFAULT '',
		role              TEXT NOT NULL DEFAULT '',
		team              TEXT NOT NULL DEFAULT '',
		sidc              TEXT NOT NULL DEFAULT '',
		notes             TEXT NOT NULL DEFAULT '',
		trust_level       INTEGER NOT NULL DEFAULT 0,
		trust_verified_at TEXT,
		trust_verified_by TEXT NOT NULL DEFAULT '',
		hub_version       INTEGER NOT NULL DEFAULT 0,
		hub_etag          TEXT NOT NULL DEFAULT '',
		origin            TEXT NOT NULL DEFAULT 'local',
		legacy_contact_id INTEGER,
		created_at        TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at        TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE INDEX IF NOT EXISTS idx_dir_contacts_tenant ON directory_contacts(tenant_id);
	CREATE INDEX IF NOT EXISTS idx_dir_contacts_team ON directory_contacts(team);
	CREATE INDEX IF NOT EXISTS idx_dir_contacts_name ON directory_contacts(display_name);
	CREATE INDEX IF NOT EXISTS idx_dir_contacts_legacy ON directory_contacts(legacy_contact_id);

	-- Backfill every v23 contacts row as a directory_contacts row.
	INSERT INTO directory_contacts (id, display_name, notes, origin, legacy_contact_id, created_at, updated_at)
	SELECT
		lower(hex(randomblob(16))),
		display_name,
		notes,
		'local',
		id,
		created_at,
		updated_at
	FROM contacts;`,

	// v45: Multi-valued transport addresses (SCIM-style). One row per
	// (contact, bearer-kind, value). Kind is UPPER_SNAKE canonical:
	// SMS, MESHTASTIC, APRS, IRIDIUM_SBD, IRIDIUM_IMT, CELLULAR, TAK,
	// RETICULUM, ZIGBEE, BLE, WEBHOOK, EMAIL, MQTT. Legacy
	// contact_addresses are backfilled with kind normalisation
	// (mesh → MESHTASTIC, iridium → IRIDIUM_SBD, others UPPER()). Legacy
	// encryption_key values are intentionally NOT backfilled; keys are
	// handled per-contact via directory_contact_keys (v46) and the
	// dual-read transform (MESHSAT-548 / S2-05). [MESHSAT-534]
	`CREATE TABLE IF NOT EXISTS directory_addresses (
		id             TEXT PRIMARY KEY,
		contact_id     TEXT NOT NULL REFERENCES directory_contacts(id) ON DELETE CASCADE,
		kind           TEXT NOT NULL,
		value          TEXT NOT NULL,
		subvalue       TEXT NOT NULL DEFAULT '',
		label          TEXT NOT NULL DEFAULT '',
		primary_rank   INTEGER NOT NULL DEFAULT 0,
		verified       INTEGER NOT NULL DEFAULT 0,
		bearer_hint    INTEGER NOT NULL DEFAULT 50,
		max_cost_cents INTEGER,
		created_at     TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at     TEXT NOT NULL DEFAULT (datetime('now')),
		UNIQUE(kind, value)
	);
	CREATE INDEX IF NOT EXISTS idx_dir_addr_contact ON directory_addresses(contact_id);
	CREATE INDEX IF NOT EXISTS idx_dir_addr_kind ON directory_addresses(kind, value);

	-- Backfill legacy contact_addresses, uppercasing the kind. Rank 0 for
	-- legacy is_primary=1, rank 1 for everything else.
	INSERT INTO directory_addresses (id, contact_id, kind, value, label, primary_rank, created_at)
	SELECT
		lower(hex(randomblob(16))),
		dc.id,
		UPPER(ca.type),
		ca.address,
		ca.label,
		CASE WHEN ca.is_primary = 1 THEN 0 ELSE 1 END,
		ca.created_at
	FROM contact_addresses ca
	JOIN directory_contacts dc ON dc.legacy_contact_id = ca.contact_id;

	-- Normalise legacy kind names.
	UPDATE directory_addresses SET kind = 'MESHTASTIC' WHERE kind = 'MESH';
	UPDATE directory_addresses SET kind = 'IRIDIUM_SBD' WHERE kind = 'IRIDIUM';`,

	// v46: Per-contact key material. Supersedes the per-channel
	// (channel_type, address) keys in key_bundles for new writes; existing
	// key_bundles remain valid for the 30-day dual-read grace (S2-05).
	// public_data holds asymmetric pubkey bytes; encrypted_priv holds the
	// master-key-wrapped private bytes or the wrapped symmetric key. No
	// backfill: legacy contact_addresses.encryption_key is plaintext hex
	// and we refuse to rewrite it here without the master-key envelope.
	// The Go package (MESHSAT-535) will migrate any outstanding legacy
	// keys on first encrypted write. [MESHSAT-534]
	`CREATE TABLE IF NOT EXISTS directory_contact_keys (
		id             TEXT PRIMARY KEY,
		contact_id     TEXT NOT NULL REFERENCES directory_contacts(id) ON DELETE CASCADE,
		kind           TEXT NOT NULL,
		version        INTEGER NOT NULL DEFAULT 1,
		status         TEXT NOT NULL DEFAULT 'active',
		public_data    BLOB,
		encrypted_priv BLOB,
		valid_from     TEXT NOT NULL DEFAULT (datetime('now')),
		valid_until    TEXT,
		rotated_at     TEXT,
		trust_anchor   TEXT NOT NULL DEFAULT 'local',
		created_at     TEXT NOT NULL DEFAULT (datetime('now')),
		UNIQUE(contact_id, kind, version)
	);
	CREATE INDEX IF NOT EXISTS idx_dir_keys_contact ON directory_contact_keys(contact_id);
	CREATE INDEX IF NOT EXISTS idx_dir_keys_status ON directory_contact_keys(status);`,

	// v47: Directory groups (teams, roles, distribution lists, MLS groups)
	// + member join table. Tenant-scoped via directory_groups.tenant_id
	// (empty for local-only). MLS groups carry mls_group_id when Phase 6
	// ships RFC 9420 (MESHSAT-572). [MESHSAT-534]
	`CREATE TABLE IF NOT EXISTS directory_groups (
		id           TEXT PRIMARY KEY,
		tenant_id    TEXT NOT NULL DEFAULT '',
		display_name TEXT NOT NULL,
		kind         TEXT NOT NULL,
		sidc         TEXT NOT NULL DEFAULT '',
		mls_group_id TEXT NOT NULL DEFAULT '',
		hub_version  INTEGER NOT NULL DEFAULT 0,
		hub_etag     TEXT NOT NULL DEFAULT '',
		created_at   TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at   TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE INDEX IF NOT EXISTS idx_dir_groups_tenant ON directory_groups(tenant_id);
	CREATE INDEX IF NOT EXISTS idx_dir_groups_kind ON directory_groups(kind);

	CREATE TABLE IF NOT EXISTS directory_group_members (
		group_id   TEXT NOT NULL REFERENCES directory_groups(id) ON DELETE CASCADE,
		contact_id TEXT NOT NULL REFERENCES directory_contacts(id) ON DELETE CASCADE,
		role       TEXT NOT NULL DEFAULT '',
		added_at   TEXT NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY (group_id, contact_id)
	);
	CREATE INDEX IF NOT EXISTS idx_dir_group_members_contact ON directory_group_members(contact_id);`,

	// v48: Dispatch policy per scope (contact / group / precedence /
	// default). Strategy drives Dispatcher.SendToRecipient (MESHSAT-544 /
	// S2-01) which resolves caller opts → contact policy → group policy
	// → precedence-default → global default. Seeds sane STANAG 4406
	// defaults: Flash and Override bond every reachable bearer, Immediate
	// races them, Priority falls through ordered primary/secondary,
	// Routine and Deferred use the primary only. [MESHSAT-534]
	`CREATE TABLE IF NOT EXISTS directory_dispatch_policy (
		id                  TEXT PRIMARY KEY,
		scope_type          TEXT NOT NULL,
		scope_id            TEXT NOT NULL DEFAULT '',
		strategy            TEXT NOT NULL,
		max_cost_cents      INTEGER,
		max_latency_ms      INTEGER,
		allow_bearers       TEXT NOT NULL DEFAULT '[]',
		deny_bearers        TEXT NOT NULL DEFAULT '[]',
		precedence_override TEXT NOT NULL DEFAULT '{}',
		created_at          TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at          TEXT NOT NULL DEFAULT (datetime('now')),
		UNIQUE(scope_type, scope_id)
	);
	CREATE INDEX IF NOT EXISTS idx_dir_policy_scope ON directory_dispatch_policy(scope_type, scope_id);

	-- Seed STANAG 4406 precedence defaults. These are the out-of-box
	-- choices used when no contact- or group-specific policy matches.
	INSERT OR IGNORE INTO directory_dispatch_policy (id, scope_type, scope_id, strategy) VALUES
		(lower(hex(randomblob(16))), 'default',    '',          'PRIMARY_ONLY'),
		(lower(hex(randomblob(16))), 'precedence', 'Override',  'HEMB_BONDED'),
		(lower(hex(randomblob(16))), 'precedence', 'Flash',     'HEMB_BONDED'),
		(lower(hex(randomblob(16))), 'precedence', 'Immediate', 'ANY_REACHABLE'),
		(lower(hex(randomblob(16))), 'precedence', 'Priority',  'ORDERED_FALLBACK'),
		(lower(hex(randomblob(16))), 'precedence', 'Routine',   'PRIMARY_ONLY'),
		(lower(hex(randomblob(16))), 'precedence', 'Deferred',  'PRIMARY_ONLY');`,

	// v49: STANAG 4406 Edition 2 precedence on every delivery row
	// [MESHSAT-543 / S1-10]. The column is plumbed through
	// MessageDelivery now; the queue-by-precedence + FLASH-preempts-
	// ROUTINE semantics land in MESHSAT-546 / S2-03. Valid values are
	// Override / Flash / Immediate / Priority / Routine / Deferred —
	// see internal/types/precedence.go. Default Routine matches the
	// behaviour of every pre-v49 delivery.
	`ALTER TABLE message_deliveries ADD COLUMN precedence TEXT NOT NULL DEFAULT 'Routine';
	CREATE INDEX IF NOT EXISTS idx_deliveries_precedence ON message_deliveries(precedence, status);`,

	// v50: Pair-mode tables — paired_clients stores every device
	// that has claimed a pair (browser, Android, CLI) with its
	// ECDH-derived identity + the JWT signing key it mints tokens
	// from; pair_modes is a short-lived row armed from the touch-
	// display UI (Settings → Devices → Arm pair mode) that holds
	// the pairing secret until it's consumed or times out. 6-digit
	// PIN + 90-second TTL is the field-kit grammar; revoke wipes
	// both tables selectively. [MESHSAT-593]
	`CREATE TABLE IF NOT EXISTS pair_modes (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		pin          TEXT NOT NULL,                       -- 6-digit, rotates on every Arm
		pairing_key  TEXT NOT NULL,                       -- 32-byte hex, ephemeral ECDH server-side
		armed_at     TEXT NOT NULL DEFAULT (datetime('now')),
		expires_at   TEXT NOT NULL,                       -- armed_at + 90s (first build; tunable)
		consumed_at  TEXT,                                -- set on successful claim
		consumed_by  TEXT,                                -- client_id once paired
		armed_by     TEXT NOT NULL DEFAULT ''             -- operator identifier
	);
	CREATE INDEX IF NOT EXISTS idx_pair_modes_expires ON pair_modes(expires_at);

	CREATE TABLE IF NOT EXISTS paired_clients (
		id             TEXT PRIMARY KEY,                   -- uuid
		name           TEXT NOT NULL DEFAULT '',           -- operator-facing label
		kind           TEXT NOT NULL DEFAULT 'browser',    -- browser / android / cli / hub
		public_key     TEXT NOT NULL,                      -- Ed25519 hex, used to verify JWTs
		cert_pem       TEXT NOT NULL DEFAULT '',           -- X.509 leaf (base64 PEM) for mTLS
		cert_expires_at TEXT,                              -- 90d from issuance
		claimed_at     TEXT NOT NULL DEFAULT (datetime('now')),
		last_seen_at   TEXT,
		revoked_at     TEXT,
		revoke_reason  TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_paired_clients_revoked ON paired_clients(revoked_at);`,

	// v51: trusted_peers — the durable cross-bearer identity + address
	// book that drives auto-federation (MESHSAT-634 epic / MESHSAT-636).
	// One row per paired MeshSat kit we've exchanged a signed capability
	// manifest with.  Per-bearer addresses live in existing subsystem
	// tables (sms_contacts, contacts, ...); this row is the cross-bearer
	// index that ties them together via signer_id.
	`CREATE TABLE IF NOT EXISTS trusted_peers (
		signer_id           TEXT PRIMARY KEY,            -- Ed25519 pub hex
		routing_identity    TEXT NOT NULL DEFAULT '',    -- destination hash
		alias               TEXT NOT NULL DEFAULT '',    -- operator-facing name
		manifest_json       TEXT NOT NULL DEFAULT '',    -- latest full manifest
		manifest_verified_at TEXT,                       -- last successful verify
		auto_federate       INTEGER NOT NULL DEFAULT 1,  -- 1=on, 0=off
		created_at          TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE INDEX IF NOT EXISTS idx_trusted_peers_updated ON trusted_peers(updated_at DESC);`,

	// v52: spectrum scan + transition history for the RF monitor
	// [MESHSAT-650]. On deploy/restart the in-memory 100-row ring was
	// lost and every waterfall panel started black for ~2.5 min. These
	// two tables give us a durable log the UI can (a) prefill from on
	// page load so panels show the last 5 min instantly and (b) use to
	// render a per-band detail view covering 6-24 h with zoom + alert
	// markers. Retention is bounded by the retention goroutine in
	// internal/spectrum (MESHSAT_SPECTRUM_RETENTION_HOURS, default 24,
	// hard cap 168). At 5 bands × 1 row/~30 s × ~45 bins × 8 B, 7 days
	// of storage is well under 40 MB; the index on (band, ts DESC)
	// keeps range queries cheap.
	//
	// `powers` is the raw per-bin dB trace stored as a JSON array (as
	// opposed to a separate per-bin row table) because every read is
	// "give me the whole row" and the blob compresses naturally in
	// SQLite's page cache. `baseline_mean` / `baseline_std` are copied
	// on each write so a historical row can be palette-normalised
	// without a join to the live baseline (which has moved on).
	`CREATE TABLE IF NOT EXISTS spectrum_scans (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		band          TEXT    NOT NULL,
		ts_ms         INTEGER NOT NULL,
		state         TEXT    NOT NULL,
		avg_db        REAL    NOT NULL,
		max_db        REAL    NOT NULL,
		baseline_mean REAL    NOT NULL,
		baseline_std  REAL    NOT NULL,
		powers        TEXT    NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_spectrum_scans_band_ts ON spectrum_scans(band, ts_ms DESC);
	CREATE INDEX IF NOT EXISTS idx_spectrum_scans_ts ON spectrum_scans(ts_ms);

	CREATE TABLE IF NOT EXISTS spectrum_transitions (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		band          TEXT    NOT NULL,
		ts_ms         INTEGER NOT NULL,
		old_state     TEXT    NOT NULL,
		new_state     TEXT    NOT NULL,
		peak_db       REAL    NOT NULL DEFAULT 0,
		peak_freq_hz  INTEGER NOT NULL DEFAULT 0,
		baseline_mean REAL    NOT NULL DEFAULT 0,
		baseline_std  REAL    NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_spectrum_transitions_band_ts ON spectrum_transitions(band, ts_ms DESC);
	CREATE INDEX IF NOT EXISTS idx_spectrum_transitions_ts ON spectrum_transitions(ts_ms);`,
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
