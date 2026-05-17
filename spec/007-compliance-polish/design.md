# Design — Compliance + Polish (Phase 6)

## Goal

Phases 1-5 land the core feature set. Phase 6 closes the compliance + accessibility + standards gaps so the bridge can credibly target: regulated procurement (VPAT for EN 301 549 + Section 508), encrypted-group operations (RFC 9420 MLS opt-in), federal-grade crypto (FIPS-140-3 via BoringSSL), and US/NATO operator workflows (full USMTF template library beyond the 3 Phase 4 skeletons).

## CI accessibility stage (REQ-600..602, REQ-617)

`.gitlab-ci.yml` gains a stage that:
1. Builds the SPA + serves it via a headless test harness.
2. Runs `axe-core` against each primary view's snapshot URL.
3. Runs `pa11y` for additional WCAG checks.
4. Fails the pipeline on any AAA violation.
5. Produces HTML report artifact.
6. Emits Prometheus counter `hub_accessibility_violations_total{view, severity}` via a script that diffs vs the previous run.

VPAT generation (REQ-611/612) consumes the same axe-core JSON output, so the published doc stays honest.

## MLS group encryption (REQ-607/608)

RFC 9420 MLS is enterprise-grade group key agreement. Per-tenant flag because:
- Not every operator wants the operational burden (MLS introduces commit/welcome rounds + key rotation events).
- Some tenants need it for compliance.

Implementation: thin wrapper around an existing Go MLS library (likely `cisco-open/go-mls` or similar). The wrapper integrates with the Dispatcher so group messages are encrypted-by-MLS BEFORE entering the transform pipeline. Per-bearer key references continue working for 1:1.

## FIPS build target (REQ-609/610)

Default build: pure-Go, CGO_ENABLED=0, modernc.org/sqlite (per Constitution Article II).

FIPS build (`make build-fips`): explicit opt-in via `MESHSAT_FIPS=1`. Links against BoringSSL via cgo. Tagged differently in the binary (`runtime/debug.ReadBuildInfo` shows `MESHSAT_FIPS=1`).

This is the ONE permitted exception to Article II — explicitly scoped to a separate build target, doesn't pollute the default path. The article's prohibition stands for default builds.

## USMTF library (REQ-604)

20 additional templates beyond the 3 Phase 4 skeletons. Each lives as a Vue component under `web/src/components/templates/` with `<TemplateName>Template.vue`. Same packing helper from spec/005 generates slash-delimited bodies.

## Out of scope

- MLS UI for visualising group membership / key rotation events — deferred.
- Per-template field-validation rules — deferred.
- Cross-Phase-6/8 integration of accessibility tests with kiosk shell — covered in spec/008.
