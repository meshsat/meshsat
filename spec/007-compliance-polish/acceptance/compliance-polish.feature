Feature: Compliance + Polish (meshsat Phase 6, EXECUTION-PLAN §6.6)

  Background:
    Given Phases 1-5 have shipped

  # REQ-600 + REQ-601 — accessibility CI gate
  Scenario: WCAG AAA violation fails the CI pipeline
    Given a developer adds a Vue component with an unlabeled button
    When the CI accessibility stage runs
    Then the stage exits non-zero
    And the HTML report artifact is published

  # REQ-602 + REQ-603 + REQ-618 — team broadcast
  Scenario: Compose team broadcast dispatches per group policy
    Given group "ops-team" has 4 members
    When the operator opens Compose, picks group "ops-team", sets body, submits
    Then SendResult contains delivery_ids for all 4 members (per their addresses)
    And the InboxView renders the broadcast as one message with 4 sub-deliveries

  Scenario: Empty group broadcast shows inline error
    Given group "empty-team" has 0 members
    When the operator picks "empty-team" and tries to submit
    Then an inline error appears
    And no POST is made

  # REQ-604 — USMTF library
  Scenario: USMTF template library shows 23+ templates
    When the operator opens Compose → Templates dropdown
    Then at least 23 templates are listed (3 from spec/005 + 20 added here)

  # REQ-605 — map clustering
  Scenario: MapView clusters dense areas
    Given 50 contacts have positions within a 1km radius
    When the operator opens Map zoomed out
    Then a single cluster marker showing "50" is rendered (no individual overlapping markers)

  # REQ-609 + REQ-610 — FIPS build target
  Scenario: make build-fips requires MESHSAT_FIPS=1
    When the developer runs `make build-fips` without MESHSAT_FIPS set
    Then the build prints an error referencing the required env var
    And the build exits non-zero

  Scenario: make build-fips with env opt-in produces FIPS-linked binary
    Given MESHSAT_FIPS=1 is set
    When `make build-fips` runs
    Then the resulting binary's runtime/debug.ReadBuildInfo reports MESHSAT_FIPS=1
    And the binary links against BoringSSL

  # REQ-607 + REQ-608 — MLS opt-in
  Scenario: MLS-enabled tenant cannot send unencrypted to group
    Given tenant-A has mls_enabled=true
    When the operator tries to send an unencrypted message to a group
    Then the dispatch is rejected
    And the operator sees a "MLS-required" error

  Scenario: MLS-disabled tenant can send unencrypted (default behavior)
    Given tenant-B has mls_enabled=false (default)
    When the operator sends a message to a group
    Then the dispatch proceeds normally

  # REQ-611 + REQ-612 — VPAT
  Scenario: make vpat regenerates the VPAT document
    When `make vpat` runs
    Then docs/compliance/vpat.md is regenerated reflecting the latest axe-core findings
    And the file maps every primary view against EN 301 549 + Section 508 sections

  # REQ-613 + REQ-614 — Android-to-Android handoff
  Scenario: Operator A on Android shows a contact QR for in-person handoff
    Given operator A has alice in their People view
    When operator A taps "Share contact" → "via QR"
    Then a QR is displayed with TTL=300s embedding alice's data + source bridge pubkey

  # REQ-615 + REQ-616 — Playwright operator suite
  Scenario: Playwright operator suite runs in CI with video artifacts on failure
    When the CI Playwright stage runs against the SPA
    Then test scenarios for Compose, Inbox, People, Map, Radios execute
    And on any failure a video artifact is captured

  # REQ-617 — Prometheus counter
  Scenario: Accessibility violations emit Prometheus counter
    Given a Settings view has 2 minor + 1 critical violation
    When the accessibility stage finishes
    Then hub_accessibility_violations_total{view="settings", severity="minor"} is incremented by 2
    And hub_accessibility_violations_total{view="settings", severity="critical"} is incremented by 1
