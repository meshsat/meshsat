# Changelog — meshsat

All notable changes to this project. Format based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) + [Conventional Commits](https://www.conventionalcommits.org/).

Generated from git history + tags by `scripts/sdd-generate-changelog.py` on 2026-05-18.


## [v0.1.0] - 2026-03-04

### Added

- mode-aware CI deploy (direct vs Swarm) (8c85eb69)
- direct serial transport for standalone mode (no HAL required) (a52691e8)
- pass-aware smart scheduler + multi-source location management (bfbfb7f7)
- encoding fix, logo, map messages, node mailboxes, signal overlay (29b84034)
- Iridium operational dashboard + bridge pipeline builder (cfd2fa1b)
- tactical operations dashboard with 7-panel grid (dc98c1df)
- node management, message stats breakdown, logo (7adecc29)
- forwarding rules engine, presets, SOS, delivery tracking, standalone mode (1a4fc5c8)
- Iridium priority queue + offline compose (Priority 3d) (08f96fa6)
- opportunistic DLQ drain on Iridium signal SSE events (81bbb916)
- Iridium signal display with 10s fast poll + refresh in MeshSat SPA (03114387)
- MeshSat SPA UX overhaul — dashboard, channels, config forms (9910a8bd)
- add default_destination to Iridium gateway config (118337ec)
- Iridium dead-letter queue for failed SBD sends (98b00047)
- Phase 4 — MQTT gateway, Iridium satellite bridge, gateway management API + UI (fdfaf6f8)
- Phase 3 — standalone web UI with embedded SPA (b15d7395)
- Phase 2 — admin commands, radio config, waypoints, signal quality (52dbdcaa)
- MeshSat coreapp Phase 1 — scaffold, persistence, event ingestion (e55b33cd)

### Changed

- add Apache 2.0 license (matches CubeOS repos) (3862e1f5)
- rewrite README for public release (7958970a)

### Fixed

- parseSBDIX tolerates 5-field responses (mtQueued optional) (675688a4)
- ringAlertListener retry + prevent stale SBDIX on fresh connect (e67288ab)
- match HAL's serial mutex pattern exactly — eliminate SBDIX binary garbage (69fe7785)
- Iridium gateway shows disconnected despite working transport (6a61979e)
- SBDIX serial race + SBDSX gating in direct transport (b6f13f90)
- pollWorker initial timer race — start with 5s instead of idle interval (a9448a31)
- reduce serial contention + deduplicate ring alert handlers (73316864)
- handle plain-text MT messages from external senders (1db2540b)
- detect piggybacked MT from outbound sends + add mailbox result logging (495d0e0c)
- inbound SBD messages silently dropped when forwarding rules exist (6e0f2dd2)
- opportunistic DLQ drain bypasses backoff, ring alert retries on failure (ea87aec4)
- chart pass colors indigo, Last TX/RX from queue, bulk stale removal (0bc6b76d)
- portnum filter on all rule types, historical pass prediction (5ceb37e7)
- check MOSuccess after SBD send, redesign signal-vs-passes chart (252cefe1)
- plaintext SBD for text messages, reduce poll waste (06ffd978)
- 6 UX/bug fixes — signal, queue direction, inbound SBD, config, message stats, comms (4207ebd2)
- queue UI clarity — sent items show as 'delivered' with dimmed style (3ceec670)
- queue not displaying — API returns {queue:[]} not raw array (2a16467c)
- inbound node selector, queue shows sent messages (7f68f55c)
- bridge rules single authority, inbound evaluation, clear UI (790bd0ce)
- overhaul Messages view into tactical comms panel (60e7f582)
- message flow Y-fork layout for MQTT and Iridium gateways (bf6b00f1)
- Iridium SSE backoff — reset on successful connection, lower cap to 5s (8c16da41)
- SSE backoff — reset on successful connection, lower cap to 5s (803f28a7)
- processor forwards to live gateway instances via GatewayProvider (98e066a8)
- read HAL_CORE_KEY directly instead of HAL_API_KEY (41a4cd41)
- ci-deploy.sh sources secrets.env for compose variable substitution (c9ab2b1c)
- CI dual-IP failover for deploy target resolution (035f0f8e)
- CI deploy — use service update --force for existing services (9b96160c)
- add placeholder index.html for go:embed in CI builds (4d55db8b)

