Feature: Contact-aware dispatcher + STANAG 4406 precedence (spec/003)

  # Covers: REQ-200, REQ-201, REQ-202, REQ-203, REQ-204, REQ-205, REQ-206, REQ-207, REQ-208, REQ-209

  Scenario: Contact-aware dispatcher + STANAG 4406 precedence capability 1
    When REQ-200 is exercised
    Then the behaviour matches the spec

  Scenario: Contact-aware dispatcher + STANAG 4406 precedence capability 2
    When REQ-201 is exercised
    Then the behaviour matches the spec

  Scenario: Contact-aware dispatcher + STANAG 4406 precedence capability 3
    When REQ-202 is exercised
    Then the behaviour matches the spec

  Scenario: Contact-aware dispatcher + STANAG 4406 precedence capability 4
    When REQ-203 is exercised
    Then the behaviour matches the spec

  Scenario: Contact-aware dispatcher + STANAG 4406 precedence capability 5
    When REQ-204 is exercised
    Then the behaviour matches the spec

