# Privacy and security baseline

What the MeshSat Bridge keeps about people, for how long, and how its code is checked on every build. SIDN fonds project 261042, deliverable D6. This page is generated from the code (`internal/database/dataclass.go` and `retention.go`) by a test that fails when the two disagree.

## Reporting a security problem

Write to security@meshsat.net, or use GitHub's private vulnerability reporting on this repository. The policy is at https://meshsat.net/security/ and in [SECURITY.md](../SECURITY.md); the machine-readable contact is https://meshsat.net/.well-known/security.txt.

## What the Bridge keeps

The Bridge keeps its data in one SQLite database on the kit. Of its 94 tables, 34 can hold personal data, 29 hold the operator's own setup and address book, and 31 hold none.

- **Personal data** is what people wrote, where they were, their phone numbers, addresses and radio ids. Every such table is cleared by a rule below or is a named exception.
- **The operator's setup** (contacts, peers, keys, rules, settings) stays until the operator deletes it.
- **No personal data**: radio measurements, caches and counters.

## Retention

The retention period is `MESHSAT_RETENTION_DAYS`, 30 days unless set. All rules run when the Bridge starts and then once a day; one rule failing does not stop the others.

| Rule | Tables | What happens |
|---|---|---|
| `messages` | `messages`, `telemetry`, `positions` | deleted after the retention period |
| `intents` | `delivery_intents`, `delivery_transmissions`, `delivery_evidence`, `delivery_submissions` | closed intents with their transmissions and evidence deleted after the retention period |
| `ledger` | `message_deliveries` | a delivery that ended longer ago than the retention period keeps its time, channel and status and loses its payload, text, destination and error |
| `lxmf-mids` | `lxmf_mids` | message ids deleted after their own horizon of 30 days |
| `dead-letters` | `dead_letters` | deleted after the retention period, whatever their state |
| `sms` | `sms_messages` | deleted after the retention period |
| `cell-peers` | `cell_peers` | a phone on the kit's own 2G cell is forgotten once it has not been seen for the retention period |
| `webhook-log` | `webhook_log` | deleted after the retention period |
| `range-tests` | `range_tests` | deleted after the retention period |
| `neighbor-info` | `neighbor_info` | deleted after the retention period |
| `oob-log` | `oob_log` | deleted after the retention period |
| `received-resources` | `received_resources` | deleted after the retention period |
| `lxmf-attachments` | `lxmf_attachments` | deleted after the retention period, with the messages they came with |
| `cell-info` | `cell_info` | deleted after the retention period |
| `iridium-geolocation` | `iridium_geolocation` | deleted after the retention period |
| `hub-outbox` | `hub_outbox` | deleted after the retention period (the Hub link also keeps it to 7 days while it runs) |
| `dtn` | `bundle_fragments`, `held_packets` | deleted at their own expiry, and in any case after the retention period |
| `post` | `post_messages`, `post_replies` | a Community Post message that ended (sent, answered, refused or failed) is deleted with its replies after the retention period, also while the Post is open; closing the Post deletes everything |
| `post-board` | `post_board` | a Safe and well line is deleted at its expiry |
| `post-sessions` | `post_sessions` | a phone's session key is deleted after the retention period |
| `hub-route` | `hub_inbox`, `hub_ops`, `hub_op_events`, `hub_op_missing` | deleted once past their deadline, also when the Hub route is off |
| `hub-commands` | `hub_command_log` | deleted after 24 hours |
| `tak-chat` | `tak_chat_threads` | deleted after 7 days without a message |
| `audit-redact` | `audit_log` | kept, for the hash chain; after the retention period the personal fields of a row (message text, numbers, addresses, IP addresses) are redacted in place, after a signed checkpoint row records that the chain verified intact |
| `zigbee-readings` | `zigbee_sensor_readings` | deleted after the retention period |

## Every table

| Table | Holds personal data | What | How it is cleared |
|---|---|---|---|
| `access_rules` | the operator's own | forwarding rules, may name nodes or numbers | by the operator |
| `astrocast_tle_cache` | no |  | - |
| `audit_log` | yes | signed record of events; older rows may name numbers or quote text | `audit-redact`: kept for the hash chain; personal fields are redacted in place after the retention period |
| `bundle_fragments` | yes | fragments of messages in transit | `dtn` |
| `cell_broadcasts` | no | public warning broadcasts | - |
| `cell_info` | yes | the cell the kit is in, so roughly where it is | `cell-info` |
| `cell_peers` | yes | numbers of phones on the kit's own 2G cell | `cell-peers` |
| `cell_tx_log` | yes | the licence holder's name on each transmission | exception: the transmission record the radio licence requires; it names only the licence holder |
| `cellular_signal_history` | no |  | - |
| `contact_addresses` | the operator's own | the operator's address book | by the operator |
| `contacts` | the operator's own | the operator's address book | by the operator |
| `credential_cache` | the operator's own | certificates and keys for the operator's services | by the operator |
| `credit_balance` | no |  | - |
| `credit_usage` | no |  | - |
| `dead_letters` | yes | messages that could not be sent | `dead-letters` |
| `delivery_attempt_facts` | no | counts and times of delivery attempts | - |
| `delivery_evidence` | yes | proof of delivery of a message | `intents` |
| `delivery_history_coverage` | no |  | - |
| `delivery_intent_facts` | no | counts and times of delivered intents | - |
| `delivery_intents` | yes | messages to a contact and their routes | `intents` |
| `delivery_submissions` | yes | messages handed in for delivery | `intents` |
| `delivery_tariffs` | no |  | - |
| `delivery_transmissions` | yes | each sending of a message | `intents` |
| `device_config_versions` | the operator's own | configuration history of the operator's devices | by the operator |
| `device_constellation_prefs` | no |  | - |
| `devices` | the operator's own | the operator's satellite devices and their ids | by the operator |
| `directory_address_conflicts` | the operator's own | the operator's address book | by the operator |
| `directory_addresses` | the operator's own | the operator's address book | by the operator |
| `directory_contact_keys` | the operator's own | contacts' public keys | by the operator |
| `directory_contacts` | the operator's own | the operator's address book | by the operator |
| `directory_dispatch_policy` | no |  | - |
| `directory_group_members` | the operator's own | the operator's address book | by the operator |
| `directory_groups` | the operator's own | the operator's address book | by the operator |
| `directory_hub_state` | no |  | - |
| `dispatch_work` | no |  | - |
| `failover_groups` | no |  | - |
| `failover_members` | no |  | - |
| `gateway_config` | the operator's own | the operator's settings, may hold numbers | by the operator |
| `held_packets` | yes | packets held for a destination | `dtn` |
| `hub_command_log` | yes | answers to Hub commands | `hub-commands` |
| `hub_inbox` | yes | recipients of messages from the Hub | `hub-route` |
| `hub_inbox_handoff` | no |  | - |
| `hub_op_events` | yes | events of messages from the Hub | `hub-route` |
| `hub_op_missing` | no |  | `hub-route` |
| `hub_ops` | yes | messages from the Hub and their recipients | `hub-route` |
| `hub_outbox` | yes | positions and messages waiting for the Hub | `hub-outbox` |
| `interfaces` | the operator's own | the operator's links | by the operator |
| `iridium_geolocation` | yes | where the satellite network placed the kit | `iridium-geolocation` |
| `iridium_locations` | no |  | - |
| `iridium_tle_cache` | no |  | - |
| `key_bundles` | the operator's own | keys shared with the operator's peers | by the operator |
| `kit_power` | no |  | - |
| `lxmf_attachments` | yes | voice notes and photos | `lxmf-attachments` |
| `lxmf_mids` | yes | message ids and sender hashes | `lxmf-mids` |
| `message_deliveries` | yes | messages, destinations and errors | `ledger` |
| `messages` | yes | messages, senders and recipients | `messages` |
| `neighbor_info` | yes | which radios heard which | `neighbor-info` |
| `object_groups` | the operator's own | the operator's lists of nodes or numbers | by the operator |
| `oob_log` | yes | management commands and their senders | `oob-log` |
| `oob_peers` | the operator's own | the operator's management peers and their numbers | by the operator |
| `pair_modes` | the operator's own | pairing sessions of the operator's phones | by the operator |
| `paired_clients` | the operator's own | the operator's paired phones | by the operator |
| `pass_quality_log` | no |  | - |
| `positions` | yes | positions of radios and people | `messages` |
| `post_board` | yes | Safe and well lines: names and places | `post-board` |
| `post_messages` | yes | Community Post messages, recipients, names | `post` |
| `post_replies` | yes | replies to Community Post messages | `post` |
| `post_sessions` | yes | a key per phone that used the Community Post | `post-sessions` |
| `preset_messages` | the operator's own | the operator's canned messages | by the operator |
| `range_tests` | yes | range test messages between radios | `range-tests` |
| `received_resources` | yes | files received over Reticulum | `received-resources` |
| `rns_paths` | the operator's own | paths to Reticulum destinations, may hold display names | by the operator |
| `routing_destinations` | the operator's own | known Reticulum destinations, may hold display names | by the operator |
| `routing_ifaces` | no |  | - |
| `routing_links` | the operator's own | links to the operator's peers | by the operator |
| `satellite_rate_limits` | no |  | - |
| `satellite_usage` | no |  | - |
| `schema_version` | no |  | - |
| `signal_history` | no |  | - |
| `sim_cards` | the operator's own | the operator's SIM cards and their numbers | by the operator |
| `sms_contacts` | the operator's own | the operator's address book | by the operator |
| `sms_messages` | yes | text messages and phone numbers | `sms` |
| `spectrum_scans` | no |  | - |
| `spectrum_transitions` | no |  | - |
| `system_config` | the operator's own | the operator's settings, including alert numbers | by the operator |
| `tak_chat_threads` | yes | TAK chat partners and callsigns | `tak-chat` |
| `telemetry` | yes | readings from radios | `messages` |
| `time_sync_state` | no |  | - |
| `trusted_peers` | the operator's own | the operator's trusted peers | by the operator |
| `warnings` | no | public warnings | - |
| `webhook_log` | yes | payloads sent to and from webhooks | `webhook-log` |
| `zigbee_device_routing` | the operator's own | the operator's sensor routing | by the operator |
| `zigbee_devices` | the operator's own | the operator's sensors | by the operator |
| `zigbee_sensor_readings` | no | sensor readings | `zigbee-readings` |

## The audit trail

The audit trail is a signed, hash-chained record of what the Bridge did: each row carries the SHA-256 of the row before it, so a changed or removed row breaks the chain. Deleting old rows would break it too, so the audit trail is the one exception to deletion. Instead:

- a new row never holds a message's text, a phone number, an email address, a long device number or an IP address: the text becomes its length, the delivery id says which message;
- after the retention period, what older rows still hold of those is redacted in place, field by field. Before each redaction the Bridge verifies the whole chain and writes a checkpoint row signed with its own Ed25519 key, naming the verified head. If the chain does not verify, nothing is redacted;
- a redacted row keeps its stored hash and its link to the row before. Its link is checked as before; its hash no longer recomputes from its text. `GET /api/audit/verify` reports both counts.

The licence record of the kit's own 2G cell (`cell_tx_log`) is the other exception: the radio licence requires it, and it names only the licence holder.

## Scanning on every build

Every pipeline, on every branch, runs:

- **gitleaks** over the working tree and the last 25 commits. A leaked secret stops the build, and with it the deploy to the field kits. `.gitleaks.toml` allows only reviewed test fixtures, each with its reason.
- **govulncheck** on the Go version that builds the Bridge (Go 1.26). A known vulnerability that the code actually reaches stops the build.
- **osv-scanner** on the web app's packages, and **trivy** on the kit image after it is built. Their findings show in the pipeline and do not stop the deploy. Most are in build tools and in the Debian packages the kit image is built on, many with no fixed version from Debian yet; they are worked through as a task of their own.

## How this stays true

`internal/database/retention_registry_test.go` fails the build when a table is added without a classification, when a table with personal data has neither a rule nor a named exception, and when a rule does not clear an old row while keeping a fresh one. `internal/engine/audit_privacy_test.go` holds the audit trail to the rules above.
