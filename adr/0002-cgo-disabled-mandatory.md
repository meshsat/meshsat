# 2. CGO_ENABLED=0 mandatory

Date: 2026-05-18

## Status
Accepted

## Context
Cross-compile Pi ARM64 + x86_64 from one build host; pure-Go SQLite via modernc.org/sqlite.

## Decision
Never `import "C"`. CGC-verified.
