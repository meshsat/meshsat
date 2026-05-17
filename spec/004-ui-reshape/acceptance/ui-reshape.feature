Feature: UI Reshape (meshsat Phase 3, EXECUTION-PLAN §6.3)

  Background:
    Given Phase 1 (unified directory) and Phase 2 (contact dispatcher) have shipped
    And the SPA is built with the Phase 3 components

  # REQ-301 — viewport-driven default
  Scenario: Desktop viewport defaults to Engineer mode
    Given window.innerWidth=1920
    When the SPA loads with no localStorage state
    Then store.operatorMode equals false

  Scenario: Mobile viewport defaults to Operator mode
    Given window.innerWidth=400
    When the SPA loads with no localStorage state
    Then store.operatorMode equals true

  Scenario: ?shell=kiosk forces Operator
    Given the URL contains ?shell=kiosk
    When the SPA loads
    Then store.operatorMode equals true regardless of viewport

  # REQ-303 + REQ-304 — 5-item nav
  Scenario: Primary nav shows exactly 5 items + More
    When the operator views the SPA
    Then the nav contains exactly 5 visible items: Compose, Inbox, Map, People, Radios
    And a "⋮ More" overflow control is present

  Scenario: More overflow exposes Engineer views
    When the operator clicks ⋮ More
    Then the menu lists Dashboard, Bridge, Interfaces, Passes, Topology, Settings, Audit, Help, About

  # REQ-305 + REQ-306 — responsive nav
  Scenario: <768px nav is a bottom-tab bar
    Given window.innerWidth=400
    When the operator views the SPA
    Then the nav is rendered at the bottom with 5 tabs reachable by thumb

  Scenario: 768-1024px nav hides text labels
    Given window.innerWidth=900
    When the operator views the SPA
    Then the nav icons are visible without text labels

  # REQ-307 — v-show preserves state
  Scenario: Toggling Engineer mode does NOT remount components
    Given the operator is in ComposeView with typed-in body text
    When the operator toggles to Engineer mode
    Then the typed body text is preserved

  # REQ-308 + REQ-309 — Compose flow
  Scenario: Operator composes via contact picker + Routine precedence
    When the operator opens Compose
    And types "alice" into the contact picker
    Then a typeahead dropdown shows contacts matching "alice"
    When the operator picks alice and selects precedence="Routine" and submits
    Then POST /api/messages/send-to-contact is sent with body containing contact_id=alice + precedence="Routine"

  Scenario: Engineer mode reveals per-bearer override row
    Given the operator is in Compose and toggles to Engineer mode
    Then a per-bearer override row appears
    And the operator can pick exact bearers + strategy_override

  # REQ-310 + REQ-311 — Inbox bubble colours + ticks
  Scenario: Mesh message bubble is blue
    Given an inbox message arrived via the meshtastic interface
    When the operator views Inbox
    Then the bubble is rendered with the blue Mesh class

  Scenario: Sent-but-not-delivered message shows ✓
    Given a message's delivery state is "sent"
    When the operator views Inbox
    Then the bubble shows a single ✓ tick

  # REQ-313 — People list with trust dots
  Scenario: PeopleView shows trust dots leading
    Given alice has trust_level=2
    When the operator opens People
    Then alice's row shows 2 of 3 trust dots filled

  # REQ-316 — persistent status strip
  Scenario: Status strip is visible across all views
    When the operator navigates Compose → Inbox → Map → People → Radios → Settings
    Then a persistent status strip with mesh/sat/cell/Hub/battery/GPS/sync indicators is visible on every view

  # REQ-317 + REQ-318 — Settings recategorization + tab overflow
  Scenario: Operator-mode Settings shows 5 tabs
    Given operatorMode=true
    When the operator opens Settings
    Then the tab strip shows exactly Radio, Channels, Position, Satellite, Cellular

  Scenario: Engineer-mode drawer reveals 12 more tabs
    Given operatorMode=false
    When the operator opens Settings
    Then 17 tabs total are reachable (5 visible + 12 in drawer or extended bar)

  # REQ-319 + REQ-320 — Night/NVIS theme + backlight
  Scenario: NVIS toggle dims the panel via API
    When the operator activates the NVIS theme toggle
    Then POST /api/system/backlight is called with body {"value":32}
    And the SPA renders the Green A palette

  # REQ-324 + REQ-325 — keyboard shortcuts
  Scenario: Engineer "g c" navigates to Compose
    Given operatorMode=false
    When the operator presses "g" then "c"
    Then the SPA navigates to /compose

  Scenario: Operator mode ignores keyboard shortcuts
    Given operatorMode=true
    When the operator presses "g" then "c"
    Then the SPA does NOT navigate
