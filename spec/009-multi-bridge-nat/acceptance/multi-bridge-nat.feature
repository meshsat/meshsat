Feature: Multi-bridge + NAT + Hub relay (spec/009)

  # Covers: REQ-800, REQ-801, REQ-802, REQ-803, REQ-804, REQ-805, REQ-806, REQ-807, REQ-808, REQ-809

  Scenario: Multi-bridge + NAT + Hub relay capability 1
    When REQ-800 is exercised
    Then the behaviour matches the spec

  Scenario: Multi-bridge + NAT + Hub relay capability 2
    When REQ-801 is exercised
    Then the behaviour matches the spec

  Scenario: Multi-bridge + NAT + Hub relay capability 3
    When REQ-802 is exercised
    Then the behaviour matches the spec

  Scenario: Multi-bridge + NAT + Hub relay capability 4
    When REQ-803 is exercised
    Then the behaviour matches the spec

  Scenario: Multi-bridge + NAT + Hub relay capability 5
    When REQ-804 is exercised
    Then the behaviour matches the spec

