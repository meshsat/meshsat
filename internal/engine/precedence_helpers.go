package engine

import "meshsat/internal/types"

// precedenceRankFromName maps a STANAG 4406 precedence name to its
// numeric rank, matching the CASE expression in the deliveries SQL
// (Override=0, Flash=1, Immediate=2, Priority=3, Routine=4,
// Deferred=5). Unknown / empty values fall back to Routine (4).
// [MESHSAT-546]
func precedenceRankFromName(name string) int {
	p := types.Precedence(name)
	if rank := p.Rank(); rank >= 0 {
		return rank
	}
	return int(types.PrecedenceRoutine.Rank())
}

// isStrongerThanQueued reports whether a new arrival with the given
// precedence rank + legacy priority should preempt the weakest
// queued delivery (newPrecRank < oldPrecRank, OR same rank but
// strictly lower priority number). Returns false for ties — ties
// are the only signal we have that the operator intended a flat
// FIFO among equal-urgency traffic. [MESHSAT-546]
func isStrongerThanQueued(newPrecRank, newPriority, oldPrecRank, oldPriority int) bool {
	if newPrecRank < oldPrecRank {
		return true
	}
	if newPrecRank > oldPrecRank {
		return false
	}
	return newPriority < oldPriority
}

// routineDefault returns s if it's a valid precedence name, else
// "Routine". Sanity-cap for fields that travel without explicit
// precedence (legacy rule-based dispatch) until MESHSAT-544 plumbs
// the field end-to-end.
func routineDefault(s string) string {
	if types.Precedence(s).Valid() {
		return s
	}
	return string(types.PrecedenceRoutine)
}
