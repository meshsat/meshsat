# ADR-0002 — `CGO_ENABLED=0` is mandatory

## Status

Accepted — 2026-05-17 (codifies long-standing rule from CLAUDE.md + CI rule 2)

## Context

MeshSat Bridge ships as a multi-arch Docker image deployed to Raspberry Pi 5 (Ubuntu Server 24.04, ARM64) running as the sole privileged container. The bridge must build deterministically across:
- GitLab CI runner (x86_64 Ubuntu)
- Operator workstation (mixed)
- Pi5 native build (rare, ARM64)

CGO introduces a C toolchain dependency + libc-version coupling + cross-compilation complexity that has historically broken the multi-arch build.

## Decision

`CGO_ENABLED=0` is set EVERYWHERE: Makefile (`-e CGO_ENABLED=0`), `.gitlab-ci.yml` (env), Dockerfile builders, local dev. The CI `lint:consistency` stage rule 2 actively rejects any `import "C"` in Go source files.

## Consequences

**Positive:**
- Pure-Go compile is portable across x86_64 + ARM64 + ARM6 without cross-toolchain pain.
- No libc-version surprise (musl vs glibc).
- Faster CI (no apk install of build-essential).
- Smaller binary (no libc dynamic link).

**Negative:**
- Some libraries are CGO-only. We've accepted pure-Go alternatives:
  - SQLite → `modernc.org/sqlite` (pure-Go, slower but adequate at our QPS)
  - Serial → `go.bug.st/serial` (pure-Go)
  - GPIO → `github.com/warthog618/go-gpiocdev` (chardev, pure-Go)
- Sidecar processes (`sidecar/llama-zip`, `sidecar/msvqsc`) use CGO/C internally — they're out-of-process so the rule doesn't apply.
- One unavoidable C binary: `cmd/jspr-helper/main.c` (static GCC cross-compiled in Dockerfile). Statically linked, no dynamic libc dependency. Acceptable.

## Alternatives considered

- **Allow CGO for specific subsystems** (rejected — slippery slope; every dep becomes a fight)
- **Build two images, one CGO one pure-Go** (rejected — doubles deploy complexity)
- **Vendor cross-compile toolchains in CI** (rejected — bloats CI image to ~3GB and breaks every Alpine update)

## Operational note

Future ADR will be needed if FIPS-140-3 certification becomes a requirement — that forces BoringSSL which requires CGO. That decision will explicitly weigh certification value vs build-determinism cost.
