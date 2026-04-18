// Package types holds small, dependency-free value types shared
// across the bridge — enums, value objects, and similar primitives
// that don't belong in a larger domain package. Nothing in this
// package imports anything outside the standard library.
package types

import (
	"fmt"
	"strings"
)

// Precedence is the STANAG 4406 Edition 2 six-level message
// precedence scheme. The bridge stores precedence as a string on
// every [database.MessageDelivery] and plumbs it through the
// dispatcher; Phase 2 (MESHSAT-544 + MESHSAT-546) wires the queue-
// by-precedence + FLASH-preempts-ROUTINE behaviour.
//
// In ACP-127 short form:
//
//	Override  — (no ACP-127 prosign; reserved for command override)
//	Flash     — Z
//	Immediate — O
//	Priority  — P
//	Routine   — R   (default)
//	Deferred  — M
//
// Override is higher than Flash; it is used only for the most
// exceptional operational conditions (e.g. SOS auto-fanout).
// [MESHSAT-543]
type Precedence string

// The six valid precedence values, lowest rank (highest urgency)
// first in the constant block.
const (
	PrecedenceOverride  Precedence = "Override"
	PrecedenceFlash     Precedence = "Flash"
	PrecedenceImmediate Precedence = "Immediate"
	PrecedencePriority  Precedence = "Priority"
	PrecedenceRoutine   Precedence = "Routine"
	PrecedenceDeferred  Precedence = "Deferred"
)

// DefaultPrecedence is the level assumed when no caller supplies one.
const DefaultPrecedence = PrecedenceRoutine

// AllPrecedences returns the six valid levels in rank order
// (most urgent first). The slice is a copy; callers may mutate it
// freely.
func AllPrecedences() []Precedence {
	return []Precedence{
		PrecedenceOverride,
		PrecedenceFlash,
		PrecedenceImmediate,
		PrecedencePriority,
		PrecedenceRoutine,
		PrecedenceDeferred,
	}
}

// Valid reports whether p is one of the six recognised levels.
// The empty string is NOT valid — callers that want a default should
// compare against [DefaultPrecedence] or call [ParsePrecedence]("")
// which returns DefaultPrecedence explicitly.
func (p Precedence) Valid() bool {
	switch p {
	case PrecedenceOverride, PrecedenceFlash, PrecedenceImmediate,
		PrecedencePriority, PrecedenceRoutine, PrecedenceDeferred:
		return true
	}
	return false
}

// Rank returns the numeric rank of p: 0 for Override (most urgent)
// through 5 for Deferred (least urgent). An unrecognised value
// returns -1 so callers can distinguish "not set" / "invalid" from
// valid rows.
//
// Rank is stable wire format for ordering; callers that need to
// sort by precedence should sort ascending by Rank (Override first).
func (p Precedence) Rank() int {
	switch p {
	case PrecedenceOverride:
		return 0
	case PrecedenceFlash:
		return 1
	case PrecedenceImmediate:
		return 2
	case PrecedencePriority:
		return 3
	case PrecedenceRoutine:
		return 4
	case PrecedenceDeferred:
		return 5
	}
	return -1
}

// Prosign returns the ACP-127 one-letter short form, or the empty
// string when the level has no assigned prosign (Override).
func (p Precedence) Prosign() string {
	switch p {
	case PrecedenceFlash:
		return "Z"
	case PrecedenceImmediate:
		return "O"
	case PrecedencePriority:
		return "P"
	case PrecedenceRoutine:
		return "R"
	case PrecedenceDeferred:
		return "M"
	}
	return ""
}

// ParsePrecedence accepts any of the forms we surface in the API
// and returns the canonical value. Specifically:
//
//   - Full name, any case: "Flash", "FLASH", "flash".
//   - ACP-127 prosign, any case: "Z", "o", "P".
//   - Empty string → [DefaultPrecedence] (Routine) — convenience for
//     callers whose REST input makes the field optional.
//
// "Override" has no prosign — only the full name works. Surrounding
// whitespace is trimmed. Unknown values return an error.
func ParsePrecedence(s string) (Precedence, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return DefaultPrecedence, nil
	}
	switch strings.ToLower(trimmed) {
	case "override":
		return PrecedenceOverride, nil
	case "flash", "z":
		return PrecedenceFlash, nil
	case "immediate", "o":
		return PrecedenceImmediate, nil
	case "priority", "p":
		return PrecedencePriority, nil
	case "routine", "r":
		return PrecedenceRoutine, nil
	case "deferred", "m":
		return PrecedenceDeferred, nil
	}
	return "", fmt.Errorf("types: unknown precedence %q (expected Override/Flash/Immediate/Priority/Routine/Deferred or ACP-127 Z/O/P/R/M)", s)
}

// CanonicalOrMust returns p unchanged if valid, else
// DefaultPrecedence. Useful when reading a column that might contain
// a historical value we no longer understand; never panics.
func (p Precedence) CanonicalOrMust() Precedence {
	if p.Valid() {
		return p
	}
	return DefaultPrecedence
}
