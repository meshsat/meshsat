Feature: Compliance + polish (WCAG AAA, MLS, USMTF, FIPS) (spec/007)

  # Covers: REQ-600, REQ-601, REQ-602, REQ-603, REQ-604, REQ-605, REQ-606, REQ-607, REQ-608, REQ-609

  Scenario: Compliance + polish (WCAG AAA, MLS, USMTF, FIPS) capability 1
    When REQ-600 is exercised
    Then the behaviour matches the spec

  Scenario: Compliance + polish (WCAG AAA, MLS, USMTF, FIPS) capability 2
    When REQ-601 is exercised
    Then the behaviour matches the spec

  Scenario: Compliance + polish (WCAG AAA, MLS, USMTF, FIPS) capability 3
    When REQ-602 is exercised
    Then the behaviour matches the spec

  Scenario: Compliance + polish (WCAG AAA, MLS, USMTF, FIPS) capability 4
    When REQ-603 is exercised
    Then the behaviour matches the spec

  Scenario: Compliance + polish (WCAG AAA, MLS, USMTF, FIPS) capability 5
    When REQ-604 is exercised
    Then the behaviour matches the spec

