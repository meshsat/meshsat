# Steering — Repo conventions (MeshSat Bridge)

## Branch naming + workflow

- **Default workflow** (CLAUDE.md L374): push directly to main. No branches, no MRs. Pipeline deploys automatically to parallax01 + tesseract01.
- **Parallel-dev exception** (Constitution Article XV): a wave gets ONE short-lived `merge/<feature_id>` branch, ONE MR per feature opened by `merge-coordinator.sh`, auto-deleted after merge. Worker branches `parallel-dev/<feature>/<task>` are intermediates — squashed via patch-apply, never merged directly.
- Snapshot branches: `snapshot/<YYYY-MM-DD>-<purpose>` for rollback baselines.

## Commit messages

- Format: `type(scope): description [MESHSAT-NNN]`
- Type: `feat | fix | refactor | test | docs | chore | perf | build | ci`
- Always reference a YouTrack issue ID. CI rule 7 warns on missing.
- Workers' commits get auto-appended `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>`.

## File layout

- `cmd/<binary>/` — entry points (only `meshsat` + `jspr-helper` today)
- `internal/<subsystem>/` — all production code
- `proto/` — shared protobuf wire formats
- `sidecar/` — out-of-process helpers (llama-zip, msvqsc)
- `web/` — Vue 3 SPA, embedded via `go:embed`
- `deploy/kiosk/` — Pi5 systemd units, labwc autostart
- `scripts/` — operator-facing scripts (ci-deploy, install-kiosk, e2e_validate)
- `test/{integration,e2e,e2e_live}/` — test sources
- `docs/` — Hugo-rendered documentation site

## YouTrack discipline

- All 3 meshsat-family repos share the `MESHSAT` YouTrack project. Disambiguation in commits/issues:
  - Bridge (this repo): no `tag:hub` AND no `tag:android` — default
  - Hub: `tag:hub`
  - Android: `tag:android`
- Parallel-dev Planner uses this tag-based disambiguation to route tasks correctly.

## CI

5 stages: `validate → build → package → deploy → pages`. Deploy is matrix-parallel SSH to primary + alt addresses with `allow_failure: true` so deploy failures don't block the pipeline.

## Pre-commit checks

- `go vet ./... && golangci-lint run ./...` (matches CI lint)
- `go test -count=1 -timeout 900s ./...` (matches CI test)
- `make consistency-check` (CI rules 1-7)
- `gofmt -w .`
