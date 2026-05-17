# Steering — Test strategy (MeshSat Bridge)

## Pyramid

- Unit tests colocated (`*_test.go`). 1058 tests currently in CI.
- Integration tests in `test/integration/` (run via `go test -tags=integration`).
- E2E tests in `test/e2e/` (Playwright against the SPA + REST API).
- Live-hardware tests in `test/e2e_live/` (skipped in CI; run manually against parallax01 / tesseract01).
- Sidecar gRPC tests in `proto/*` + sidecar test mode (out-of-process).

## CI

```bash
go test -count=1 -timeout 900s ./...      # gates every deploy
```

900s timeout (was 300s; raised in MESHSAT-551). Headroom for SBDIX + Reticulum link tests.

## Coverage gaps (today, 2026-05-17)

| Subsystem | Test files / total | % |
|---|---|---|
| `engine/` | 19/41 | ~46% |
| `routing/` | 18/40 | ~45% |
| `reticulum/` | 10/21 | ~48% |
| `transport/` | 8/30 | ~27% |
| `gateway/` | 7/51 | **~14% — WEAK** |
| `api/` | 17/87 | ~20% |
| `pair/` | 1/2 | ~50% (NEW) |

**`internal/gateway/` is the weakest subsystem.** Any new feature touching gateways must add coverage targets, not relax them.

## TUN tests

Skip in CI via `CI` env var (`if os.Getenv("CI") != "" { t.Skip(...) }`) — the GitLab runner container lacks `/dev/net/tun`.

## Parallel-dev workers

Workers must run the FULL `go test -count=1 -timeout 900s ./...` as part of their `acceptance_test`. Per-package tests are insufficient — they miss cross-package integration regressions.

## Aspirational (not retroactive)

This constitution declares test-first as Article V. The existing codebase has uneven coverage (gateway 14%). New code shall be test-first; retrofitting old code is opportunistic.
