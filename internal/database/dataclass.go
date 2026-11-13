package database

// Every table the Bridge keeps, classified: SIDN fonds project 261042,
// deliverable D6 [MESHSAT-1137]. retention_registry_test.go holds this
// list to the migrated schema (a new table fails the build until it is
// classified here) and holds every personal-data table to a retention rule
// in retention.go or a named exception. docs/privacy-baseline.md is
// generated from it (go test ./internal/database -run TestPrivacyBaselineDoc -update).

// DataClass says whose data a table holds.
type DataClass string

const (
	// ClassPersonal: data about people: what they wrote, where they were,
	// their numbers, addresses and node ids. Cleared by a retention rule,
	// or a named exception.
	ClassPersonal DataClass = "personal"
	// ClassOperator: the operator's own setup and address book (contacts,
	// peers, keys, rules). Kept until the operator deletes it.
	ClassOperator DataClass = "operator"
	// ClassNone: no personal data (radio measurements, caches, counters).
	ClassNone DataClass = "none"
)

// TableInfo is one table's classification.
type TableInfo struct {
	Table     string
	Class     DataClass
	Holds     string // what it holds about people, in a few words
	Rule      string // the RetentionRules ID that clears it
	Exception string // why a personal table has no age rule
}

// Tables is every table, alphabetical.
var Tables = []TableInfo{
	{Table: "access_rules", Class: ClassOperator, Holds: "forwarding rules, may name nodes or numbers"},
	{Table: "astrocast_tle_cache", Class: ClassNone},
	{Table: "audit_log", Class: ClassPersonal, Holds: "signed record of events; older rows may name numbers or quote text",
		Rule: "audit-redact", Exception: "kept for the hash chain; personal fields are redacted in place after the retention period"},
	{Table: "bundle_fragments", Class: ClassPersonal, Holds: "fragments of messages in transit", Rule: "dtn"},
	{Table: "cell_broadcasts", Class: ClassNone, Holds: "public warning broadcasts"},
	{Table: "cell_info", Class: ClassPersonal, Holds: "the cell the kit is in, so roughly where it is", Rule: "cell-info"},
	{Table: "cell_peers", Class: ClassPersonal, Holds: "numbers of phones on the kit's own 2G cell", Rule: "cell-peers"},
	{Table: "cell_tx_log", Class: ClassPersonal, Holds: "the licence holder's name on each transmission",
		Exception: "the transmission record the radio licence requires; it names only the licence holder"},
	{Table: "cellular_signal_history", Class: ClassNone},
	{Table: "contact_addresses", Class: ClassOperator, Holds: "the operator's address book"},
	{Table: "contacts", Class: ClassOperator, Holds: "the operator's address book"},
	{Table: "credential_cache", Class: ClassOperator, Holds: "certificates and keys for the operator's services"},
	{Table: "credit_balance", Class: ClassNone},
	{Table: "credit_usage", Class: ClassNone},
	{Table: "dead_letters", Class: ClassPersonal, Holds: "messages that could not be sent", Rule: "dead-letters"},
	{Table: "delivery_attempt_facts", Class: ClassNone, Holds: "counts and times of delivery attempts"},
	{Table: "delivery_evidence", Class: ClassPersonal, Holds: "proof of delivery of a message", Rule: "intents"},
	{Table: "delivery_history_coverage", Class: ClassNone},
	{Table: "delivery_intent_facts", Class: ClassNone, Holds: "counts and times of delivered intents"},
	{Table: "delivery_intents", Class: ClassPersonal, Holds: "messages to a contact and their routes", Rule: "intents"},
	{Table: "delivery_submissions", Class: ClassPersonal, Holds: "messages handed in for delivery", Rule: "intents"},
	{Table: "delivery_tariffs", Class: ClassNone},
	{Table: "delivery_transmissions", Class: ClassPersonal, Holds: "each sending of a message", Rule: "intents"},
	{Table: "device_config_versions", Class: ClassOperator, Holds: "configuration history of the operator's devices"},
	{Table: "device_constellation_prefs", Class: ClassNone},
	{Table: "devices", Class: ClassOperator, Holds: "the operator's satellite devices and their ids"},
	{Table: "directory_address_conflicts", Class: ClassOperator, Holds: "the operator's address book"},
	{Table: "directory_addresses", Class: ClassOperator, Holds: "the operator's address book"},
	{Table: "directory_contact_keys", Class: ClassOperator, Holds: "contacts' public keys"},
	{Table: "directory_contacts", Class: ClassOperator, Holds: "the operator's address book"},
	{Table: "directory_dispatch_policy", Class: ClassNone},
	{Table: "directory_group_members", Class: ClassOperator, Holds: "the operator's address book"},
	{Table: "directory_groups", Class: ClassOperator, Holds: "the operator's address book"},
	{Table: "directory_hub_state", Class: ClassNone},
	{Table: "dispatch_work", Class: ClassNone},
	{Table: "failover_groups", Class: ClassNone},
	{Table: "failover_members", Class: ClassNone},
	{Table: "gateway_config", Class: ClassOperator, Holds: "the operator's settings, may hold numbers"},
	{Table: "held_packets", Class: ClassPersonal, Holds: "packets held for a destination", Rule: "dtn"},
	{Table: "hub_command_log", Class: ClassPersonal, Holds: "answers to Hub commands", Rule: "hub-commands"},
	{Table: "hub_inbox", Class: ClassPersonal, Holds: "recipients of messages from the Hub", Rule: "hub-route"},
	{Table: "hub_inbox_handoff", Class: ClassNone},
	{Table: "hub_op_events", Class: ClassPersonal, Holds: "events of messages from the Hub", Rule: "hub-route"},
	{Table: "hub_op_missing", Class: ClassNone, Rule: "hub-route"},
	{Table: "hub_ops", Class: ClassPersonal, Holds: "messages from the Hub and their recipients", Rule: "hub-route"},
	{Table: "hub_outbox", Class: ClassPersonal, Holds: "positions and messages waiting for the Hub", Rule: "hub-outbox"},
	{Table: "interfaces", Class: ClassOperator, Holds: "the operator's links"},
	{Table: "iridium_geolocation", Class: ClassPersonal, Holds: "where the satellite network placed the kit", Rule: "iridium-geolocation"},
	{Table: "iridium_locations", Class: ClassNone},
	{Table: "iridium_tle_cache", Class: ClassNone},
	{Table: "key_bundles", Class: ClassOperator, Holds: "keys shared with the operator's peers"},
	{Table: "kit_power", Class: ClassNone},
	{Table: "lxmf_attachments", Class: ClassPersonal, Holds: "voice notes and photos", Rule: "lxmf-attachments"},
	{Table: "lxmf_mids", Class: ClassPersonal, Holds: "message ids and sender hashes", Rule: "lxmf-mids"},
	{Table: "message_deliveries", Class: ClassPersonal, Holds: "messages, destinations and errors", Rule: "ledger"},
	{Table: "messages", Class: ClassPersonal, Holds: "messages, senders and recipients", Rule: "messages"},
	{Table: "neighbor_info", Class: ClassPersonal, Holds: "which radios heard which", Rule: "neighbor-info"},
	{Table: "object_groups", Class: ClassOperator, Holds: "the operator's lists of nodes or numbers"},
	{Table: "oob_log", Class: ClassPersonal, Holds: "management commands and their senders", Rule: "oob-log"},
	{Table: "oob_peers", Class: ClassOperator, Holds: "the operator's management peers and their numbers"},
	{Table: "pair_modes", Class: ClassOperator, Holds: "pairing sessions of the operator's phones"},
	{Table: "paired_clients", Class: ClassOperator, Holds: "the operator's paired phones"},
	{Table: "pass_quality_log", Class: ClassNone},
	{Table: "positions", Class: ClassPersonal, Holds: "positions of radios and people", Rule: "messages"},
	{Table: "post_board", Class: ClassPersonal, Holds: "Safe and well lines: names and places", Rule: "post-board"},
	{Table: "post_messages", Class: ClassPersonal, Holds: "Community Post messages, recipients, names", Rule: "post"},
	{Table: "post_replies", Class: ClassPersonal, Holds: "replies to Community Post messages", Rule: "post"},
	{Table: "post_sessions", Class: ClassPersonal, Holds: "a key per phone that used the Community Post", Rule: "post-sessions"},
	{Table: "preset_messages", Class: ClassOperator, Holds: "the operator's canned messages"},
	{Table: "range_tests", Class: ClassPersonal, Holds: "range test messages between radios", Rule: "range-tests"},
	{Table: "received_resources", Class: ClassPersonal, Holds: "files received over Reticulum", Rule: "received-resources"},
	{Table: "rns_paths", Class: ClassOperator, Holds: "paths to Reticulum destinations, may hold display names"},
	{Table: "routing_destinations", Class: ClassOperator, Holds: "known Reticulum destinations, may hold display names"},
	{Table: "routing_ifaces", Class: ClassNone},
	{Table: "routing_links", Class: ClassOperator, Holds: "links to the operator's peers"},
	{Table: "satellite_rate_limits", Class: ClassNone},
	{Table: "satellite_usage", Class: ClassNone},
	{Table: "schema_version", Class: ClassNone},
	{Table: "signal_history", Class: ClassNone},
	{Table: "sim_cards", Class: ClassOperator, Holds: "the operator's SIM cards and their numbers"},
	{Table: "sms_contacts", Class: ClassOperator, Holds: "the operator's address book"},
	{Table: "sms_messages", Class: ClassPersonal, Holds: "text messages and phone numbers", Rule: "sms"},
	{Table: "spectrum_scans", Class: ClassNone},
	{Table: "spectrum_transitions", Class: ClassNone},
	{Table: "system_config", Class: ClassOperator, Holds: "the operator's settings, including alert numbers"},
	{Table: "tak_chat_threads", Class: ClassPersonal, Holds: "TAK chat partners and callsigns", Rule: "tak-chat"},
	{Table: "telemetry", Class: ClassPersonal, Holds: "readings from radios", Rule: "messages"},
	{Table: "time_sync_state", Class: ClassNone},
	{Table: "trusted_peers", Class: ClassOperator, Holds: "the operator's trusted peers"},
	{Table: "warnings", Class: ClassNone, Holds: "public warnings"},
	{Table: "webhook_log", Class: ClassPersonal, Holds: "payloads sent to and from webhooks", Rule: "webhook-log"},
	{Table: "zigbee_device_routing", Class: ClassOperator, Holds: "the operator's sensor routing"},
	{Table: "zigbee_devices", Class: ClassOperator, Holds: "the operator's sensors"},
	{Table: "zigbee_sensor_readings", Class: ClassNone, Holds: "sensor readings", Rule: "zigbee-readings"},
}
