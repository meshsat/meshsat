Feature: Multi-Bridge + NAT Traversal + Hub Relay (meshsat Phase 9, EXECUTION-PLAN §6.9)

  Background:
    Given spec/001-pair-protocol has shipped + an Android client is paired with bridge B
    And the Android paired_bridges Room DB has rns_dest + hub_relay_url for bridge B

  # REQ-800 + REQ-801 — multi-bridge tile + swipe-switch
  Scenario: Android multi-bridge view shows tiles with health dots
    Given Android is paired with 3 bridges B1 (LAN ok), B2 (RNS only), B3 (unreachable)
    When the operator opens the multi-bridge view
    Then 3 tiles are rendered with B1=green, B2=amber, B3=red

  Scenario: Swipe right switches to the next bridge
    Given the operator is viewing bridge B1
    When the operator swipes right
    Then within 2 seconds bridge B2's data is loaded

  # REQ-803 + REQ-804 — unified notifications + source-tagged routing
  Scenario: A new MO on bridge B1 fires a tagged notification
    Given the operator is paired with B1 and B2
    When B1's bridge publishes a new MO event via SSE
    Then a system notification appears tagged with source bridge_id="B1"
    When the operator taps the notification
    Then the app navigates to B1's InboxView

  # REQ-806 + REQ-807 — LAN→RNS fallback
  Scenario: LAN unreachable → Reticulum link picked up
    Given bridge B has lan_ip=192.168.1.50 (currently unreachable) + rns_dest=h'abcd...'
    When the operator opens B's view
    Then the app falls back to a Reticulum link via the dest hash
    And HTTP/2-over-RNS frames carry the request/response
    And the tile health dot shows amber

  # REQ-808 + REQ-809 + REQ-810 — Hub relay tunnel + mTLS
  Scenario: LAN+RNS unreachable → Hub WS relay picked up
    Given bridge B has lan_ip unreachable + rns_dest unreachable + hub_relay_url=wss://hub.meshsat.net/relay
    When the operator opens B's view
    Then the app establishes a WS tunnel on meshsat/relay/B/<client_id>
    And the frames are end-to-end mTLS-encrypted (Hub sees ciphertext only)

  # REQ-811 + REQ-812 — relay isolation + rate limit
  Scenario: Hub rejects cross-client traffic
    Given client_id=A holds a tunnel
    When client_id=A tries to send frames into meshsat/relay/<bridgeB>/B
    Then the Hub rejects the frame

  Scenario: Per-client rate limit kicks in at 100 req/min
    When client_id=X sends 101 requests in 60 seconds
    Then the 101st response is HTTP 429

  # REQ-813 — bridge subscribes + forwards
  Scenario: Bridge forwards relay frames to localhost HTTP
    Given an Android client sends a frame via Hub relay
    When the frame reaches the bridge's meshsat/relay/{bridge_id}/+ subscription
    Then the bridge forwards the inner HTTP request to localhost:6050 as if from LAN

  # REQ-814 — health payload includes directory_version + last_sync
  Scenario: Health payload exposes directory drift
    When the bridge publishes its health payload
    Then the payload includes directory_version (int) + last_sync (unix epoch seconds)

  # REQ-815 — unreachable banner
  Scenario: All 3 tiers fail → unreachable banner with last-seen timestamp
    Given bridge B was last seen at 14:32:11
    And all 3 transports currently fail
    When the operator opens B's view
    Then a "bridge unreachable — last seen at 14:32:11" banner is displayed

  # REQ-816 — diagnostic ring buffer
  Scenario: Transport fallback events are logged
    When LAN → RNS fallback occurs
    Then the diagnostic ring buffer's most recent entry records the transition

  # REQ-817 — end-to-end integration test
  Scenario: Off-LAN Android controls CG-NAT bridge via Hub relay
    Given the Android phone has cellular-only connectivity
    And bridge B is behind CG-NAT
    When the operator sends a Compose message from Android
    Then the message reaches B via Hub WS relay
    And B publishes the outbound message on its normal MQTT topics

  # REQ-818 + REQ-819 — Prometheus
  Scenario: Hub relay metrics increment
    When an Android client opens a tunnel to bridge B and exchanges 100 frames
    Then hub_relay_tunnels_active{bridge_id="B"} is at least 1
    And hub_relay_bytes_total{bridge_id="B", direction="in"} is incremented
    And hub_relay_bytes_total{bridge_id="B", direction="out"} is incremented

  # REQ-820 — paired_bridges row populates all 3 transport fields
  Scenario: New pairing populates rns_dest + hub_relay_url
    When the operator completes a fresh pairing per spec/001-pair-protocol
    Then the new paired_bridges row has rns_dest and hub_relay_url populated
