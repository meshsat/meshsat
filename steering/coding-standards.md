# Steering — Coding standards (MeshSat Bridge)

## Go

- Go version: **1.24** (`go.mod:3`). Bump only via ADR.
- `CGO_ENABLED=0` everywhere (Article II). Use pure-Go alternatives.
- Format: `gofmt -w .` (Makefile target `make fmt`). CI fails on drift.
- Linter: `go vet ./... && golangci-lint run ./...` (Makefile `make lint`).
- Module structure: `cmd/<binary>/` for entries, `internal/<subsystem>/` for everything else, `proto/` for shared protobuf, `sidecar/` for out-of-process helpers.
- Test file naming: `_test.go` colocated with source. Integration tests in `test/integration/`. E2E in `test/e2e/`. Live-hardware sanity in `test/e2e_live/`.
- Concurrent code: prefer channels over mutexes for orchestration; mutexes for state-machine protection (e.g. SBDIX serial lock per Article VII).
- Error handling: wrap with `fmt.Errorf("context: %w", err)`. Never `panic()` in handler code — that crashes the whole bridge.
- Logging: structured `log/slog` (JSON in prod). Include `device_id`, `bridge_id`, `tenant_id` (if applicable), correlation IDs. NEVER log private keys, master keys, or full JWT tokens.

## Vue 3 SPA (`web/`)

- Vue 3 Composition API only (no Options API).
- Vite for build, embedded into Go binary via `go:embed`.
- Tailwind for styling — no other CSS framework.
- 13 dashboard views in `web/src/views/`. New views go there + `App.vue` router.
- Bundle watcher reload every 4h kiosk-mode safety net — don't break it.

## Comments + docs

- Swagger annotations REQUIRED on new HTTP handlers (`@Summary`, `@Tags`, `@Param`, `@Success`, `@Failure`). CI rule 4 warns if missing.
- Subsystem-level docs live in `docs/` (Hugo-rendered for the Pages job).
- Per-package `doc.go` for non-obvious subsystems (reticulum, hemb, engine).
- CLAUDE.md + `.claude/rules/*.md` are gitignored operator-only context — never reference them from committed code.
