---
title: "Contributing"
---

# Contributing

MeshSat is GPLv3 open source. Contributions are welcome.

## Repository

- **Source**: [github.com/cubeos-app/meshsat](https://github.com/cubeos-app/meshsat)
- **Issues**: [YouTrack MESHSAT project](https://youtrack.nuclearlighters.net/projects/MESHSAT)
- **License**: GPL-3.0

## Development Setup

```bash
git clone https://github.com/cubeos-app/meshsat.git
cd meshsat

# Backend
make build    # Build Go binary
make test     # Run tests
make fmt      # Format code

# Frontend
cd web && npm ci && npm run dev   # Vue dev server at :5173
```

## Code Guidelines

- **Go**: Effective Go, `context.Context` first parameter, table-driven tests
- **Vue**: Composition API with `<script setup>`, Pinia for state, Tailwind CSS only
- **No CGO**: All Go code must compile with `CGO_ENABLED=0`
- **Swagger**: All HTTP handlers must include Swagger annotations
- **Migrations**: Append-only — never edit existing migration entries

## Architecture

Read the architecture section in the main README or browse `internal/`:

- `gateway/` — per-channel gateway implementations
- `engine/` — message processing, dispatch, scheduling
- `api/` — REST handlers + embedded Vue SPA
- `database/` — SQLite via modernc.org/sqlite
- `transport/` — hardware abstraction interfaces
- `rules/` — access control evaluator
- `routing/` — Reticulum-inspired routing protocol
