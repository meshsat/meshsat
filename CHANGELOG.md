# Changelog — meshsat

All notable changes to this project. Format based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) + [Conventional Commits](https://www.conventionalcommits.org/).

Generated from git history + tags by `scripts/sdd-generate-changelog.py` on 2026-05-18.


## [v0.1.0] - 2026-03-04

### Added

- mode-aware CI deploy (direct vs Swarm) (487527f2)
- direct serial transport for standalone mode (no HAL required) (89516f11)
- pass-aware smart scheduler + multi-source location management (fd9b4b93)
- encoding fix, logo, map messages, node mailboxes, signal overlay (8d2046d8)
- Iridium operational dashboard + bridge pipeline builder (048e8a64)
- tactical operations dashboard with 7-panel grid (5bdb5232)
- node management, message stats breakdown, logo (6e541b39)
- forwarding rules engine, presets, SOS, delivery tracking, standalone mode (fcc7c80a)
- Iridium priority queue + offline compose (Priority 3d) (9912bdd5)
- opportunistic DLQ drain on Iridium signal SSE events (edfde6a3)
- Iridium signal display with 10s fast poll + refresh in MeshSat SPA (6b8e6fec)
- MeshSat SPA UX overhaul — dashboard, channels, config forms (f93c3769)
- add default_destination to Iridium gateway config (a231f07a)
- Iridium dead-letter queue for failed SBD sends (cab7c718)
- Phase 4 — MQTT gateway, Iridium satellite bridge, gateway management API + UI (72242125)
- Phase 3 — standalone web UI with embedded SPA (f43b069b)
- Phase 2 — admin commands, radio config, waypoints, signal quality (6b8ee491)
- MeshSat coreapp Phase 1 — scaffold, persistence, event ingestion (424a112a)

### Changed

- add Apache 2.0 license (matches CubeOS repos) (894dc958)
- rewrite README for public release (db2debc4)

### Fixed

- parseSBDIX tolerates 5-field responses (mtQueued optional) (ab044f7d)
- ringAlertListener retry + prevent stale SBDIX on fresh connect (4f199e3c)
- match HAL's serial mutex pattern exactly — eliminate SBDIX binary garbage (2533d82f)
- Iridium gateway shows disconnected despite working transport (492cecad)
- SBDIX serial race + SBDSX gating in direct transport (bb81063a)
- pollWorker initial timer race — start with 5s instead of idle interval (1bb80f56)
- reduce serial contention + deduplicate ring alert handlers (65cb9c99)
- handle plain-text MT messages from external senders (3f3f67f4)
- detect piggybacked MT from outbound sends + add mailbox result logging (f102eae9)
- inbound SBD messages silently dropped when forwarding rules exist (a45e2831)
- opportunistic DLQ drain bypasses backoff, ring alert retries on failure (c90e1e9f)
- chart pass colors indigo, Last TX/RX from queue, bulk stale removal (471cfa89)
- portnum filter on all rule types, historical pass prediction (36b48b8a)
- check MOSuccess after SBD send, redesign signal-vs-passes chart (5a816577)
- plaintext SBD for text messages, reduce poll waste (17617e3f)
- 6 UX/bug fixes — signal, queue direction, inbound SBD, config, message stats, comms (70035ab3)
- queue UI clarity — sent items show as 'delivered' with dimmed style (cd927f44)
- queue not displaying — API returns {queue:[]} not raw array (885afac9)
- inbound node selector, queue shows sent messages (11273d45)
- bridge rules single authority, inbound evaluation, clear UI (43f285f1)
- overhaul Messages view into tactical comms panel (c4b99651)
- message flow Y-fork layout for MQTT and Iridium gateways (3b1c8dbb)
- Iridium SSE backoff — reset on successful connection, lower cap to 5s (2510552b)
- SSE backoff — reset on successful connection, lower cap to 5s (d3e7973b)
- processor forwards to live gateway instances via GatewayProvider (6e814397)
- read HAL_CORE_KEY directly instead of HAL_API_KEY (14ba7013)
- ci-deploy.sh sources secrets.env for compose variable substitution (2822a3ca)
- CI dual-IP failover for deploy target resolution (24675666)
- CI deploy — use service update --force for existing services (fc607c41)
- add placeholder index.html for go:embed in CI builds (f8ebc903)

