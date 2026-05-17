Feature: Contact-aware Dispatcher (meshsat Phase 2, EXECUTION-PLAN §6.2)

  Background:
    Given Phase 1 has shipped (directory_contacts/addresses/dispatch_policy populated)
    And the DeliveryWorker is running

  # REQ-203 — PRIMARY_ONLY
  Scenario: PRIMARY_ONLY strategy sends to the contact's first address only
    Given contact "alice" has addresses [MESHTASTIC:!aabbccdd, SMS:+1-555-0100]
    And alice's DispatchPolicy.strategy="PRIMARY_ONLY"
    When Dispatcher.SendToRecipient is called for alice
    Then exactly 1 delivery is enqueued for MESHTASTIC:!aabbccdd
    And NO delivery is enqueued for SMS:+1-555-0100

  # REQ-204 — ANY_REACHABLE
  Scenario: ANY_REACHABLE picks the first healthy bearer
    Given alice has [MESHTASTIC:!aabbccdd, SMS:+1-555-0100, IRIDIUM_SBD:300258060902280]
    And health scores: MESHTASTIC=20, SMS=90, IRIDIUM_SBD=60
    And alice's policy strategy="ANY_REACHABLE"
    When Dispatcher.SendToRecipient is called
    Then exactly 1 delivery is enqueued for SMS:+1-555-0100

  # REQ-205 — ORDERED_FALLBACK
  Scenario: ORDERED_FALLBACK iterates bearer_order until one accepts
    Given alice's policy has bearer_order=[IRIDIUM_SBD, MESHTASTIC, SMS]
    And IRIDIUM_SBD is offline AND MESHTASTIC is rate-limited
    When Dispatcher.SendToRecipient is called
    Then exactly 1 delivery succeeds via SMS

  # REQ-207 — ALL_BEARERS
  Scenario: ALL_BEARERS enqueues a delivery per address
    Given alice has 3 addresses
    And alice's policy strategy="ALL_BEARERS"
    When Dispatcher.SendToRecipient is called
    Then exactly 3 deliveries are enqueued (one per address)

  # REQ-208 + REQ-218 — REST endpoint returns per-bearer delivery IDs
  Scenario: POST /api/messages/send-to-contact returns per-bearer SendResult
    When POST /api/messages/send-to-contact is called with {contact_id="alice",body="hi",precedence="Routine"}
    Then the response status is 200
    And the response body contains delivery_ids[] (length>=1), bearers_attempted[], strategy_used, precedence_used

  # REQ-209 — unreachable contact returns 503
  Scenario: Contact with no reachable bearers returns 503 + per_bearer_status
    Given contact "bob" has 2 addresses both offline + rate-limited
    When POST /api/messages/send-to-contact is called for bob
    Then the response status is 503
    And the response body field per_bearer_status[] describes each attempted bearer

  # REQ-211 — precedence-ordered queue
  Scenario: DeliveryWorker drains higher-precedence first
    Given queue has [Routine, Routine, Flash, Priority] queued in that order
    When DeliveryWorker.processBatch picks the next message
    Then the picked message has precedence="Flash"
    When the next-next message is picked
    Then the next-next has precedence="Priority"

  # REQ-212 + REQ-213 — preemption with audit + counter
  Scenario: Flash message preempts saturated interface
    Given the meshtastic queue is full of 10 Routine messages
    When a Flash message arrives for meshtastic
    Then 1 Routine message is moved to queue tail
    And an audit_log entry of type "delivery.preempted" exists for the evicted message
    And the counter meshsat_delivery_preempted_total{evicted_precedence="Routine"} is incremented by 1

  # REQ-214 — precedence-default policies seeded on first boot
  Scenario: First boot seeds 6 precedence-default policies
    When the bridge starts after this phase
    Then directory_dispatch_policy contains 6 rows with scope_type="default"
    And the rows map: Override→HEMB_BONDED, Flash→HEMB_BONDED, Immediate→ANY_REACHABLE, Priority→ANY_REACHABLE, Routine→PRIMARY_ONLY, Deferred→PRIMARY_ONLY

  # REQ-216 — 30-day key grace
  Scenario: Both sms: and contact: key refs resolve during grace window
    Given a transform rule has key_ref="sms:+1-555-0100"
    And a directory_contacts row resolves +1-555-0100 to contact "alice"
    And alice has a derived contact AES key
    When the transform pipeline runs
    Then it uses the contact-derived key (NOT the legacy channel key)
