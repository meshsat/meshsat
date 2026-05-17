# ADR-0001 — Record architecture decisions

## Status

Accepted — 2026-05-17

## Context

MeshSat Bridge has accumulated significant tribal knowledge in CLAUDE.md (374 lines, gitignored) and `.claude/rules/*.md` (also gitignored). Operator-only context can't be auto-loaded by parallel-dev workers or future contributors who don't have the local config. ADRs are the durable record of "why this is the way it is" that ships in the repo.

## Decision

MADR format. One ADR per file under `adr/NNNN-<title>.md`. Each ADR has Status, Context, Decision, Consequences, Alternatives considered.

## Consequences

Positive: every "would argue about this in 6 months" decision becomes searchable + versioned. Workers can read the ADR before making changes that violate the decision.

Negative: small discipline cost. Aspirational, not retroactive.

## Alternatives

- No ADRs: rejected — operator-only knowledge is fragile across contributor turnover.
- Single ARCHITECTURE.md: rejected — monolithic docs don't capture WHEN/WHY.
- Inline code comments: rejected — architectural decisions span files.
