# ADR-0003 — Parallel-dev workflow override

## Status

Accepted — 2026-05-17

## Context

CLAUDE.md L374 declares the default workflow: **"Push directly to main. No branches, no MRs. Pipeline deploys automatically."** This optimises for a small team's velocity on a stable codebase.

Parallel-dev (IFRNLLEI01PRD-922 infrastructure on the gateway side) fundamentally requires:
- One short-lived branch per worker (git worktree isolation)
- One MR per feature (merge-coordinator squashes worker patches)
- Tests + lint gated BEFORE merging

These two workflows are incompatible. Either we deny parallel-dev to meshsat, or we override the default rule for parallel-dev waves only.

## Decision

**Override the "push to main" rule for parallel-dev waves only.** Direct human commits still push to main as before. Parallel-dev waves follow:

1. Planner creates a synthetic YT epic + N child tasks (per spec/<feature>/tasks.json)
2. Distribute allocates N worktrees, each on `parallel-dev/<feature_id>/<task_id>` branched from main
3. Each worker commits to its own branch (NEVER pushed to remote — local-only)
4. Merge-coordinator creates `merge/<feature_id>` branch from origin/main, applies worker patches via `git apply` (commits with original task context), runs lint + tests, opens ONE MR
5. Operator (or auto-merge if low-risk per classify-feature-risk.py) merges the MR
6. The `merge/<feature_id>` branch is auto-deleted on merge (no long-lived branches preserved)

## Consequences

**Positive:**
- Parallel-dev becomes available without abandoning the simple default.
- Each parallel-dev wave is fully traceable via the single MR (auditable diff + risk classification + reviewer trail).
- Human direct-push workflow continues unchanged for normal feature work.

**Negative:**
- Slight branch-naming-collision risk if a human happens to use `merge/` or `parallel-dev/` prefixes. Mitigated by the validate-project-spec.py + planner-decompose.py rejecting prefixes that look like reserved.
- Reviewers must understand TWO workflows exist. Documented in `steering/repo-conventions.md`.

## Alternatives considered

- **Deny parallel-dev to meshsat entirely** (rejected — meshsat is the canary product per the IFRNLLEI01PRD-929 plan)
- **Convert meshsat to MR-only workflow** (rejected — too disruptive for a 2-contributor codebase)
- **Use a separate "parallel-dev fork"** (rejected — adds remote-juggling complexity for marginal benefit)
