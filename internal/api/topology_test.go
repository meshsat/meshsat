package api

import "testing"

// The store writes node ids as "!a1b3c2ec" and the graph keys them as
// "a1b3c2ec"; before MESHSAT-1397 the link inference compared the two forms as
// they were and never found a link.
func TestTopologyNodeID_NormalisesEveryForm(t *testing.T) {
	cases := map[string]string{
		"!a1b3c2ec":   "a1b3c2ec",
		"a1b3c2ec":    "a1b3c2ec",
		"!A1B3C2EC":   "a1b3c2ec",
		" !a1b3c2ec ": "a1b3c2ec",
		"!ffffffff":   "ffffffff",
		"":            "",
		"!":           "",
		"a1b3":        "",
		"!zzzzzzzz":   "",
		"broadcast":   "",
	}
	for in, want := range cases {
		if got := topologyNodeID(in); got != want {
			t.Errorf("topologyNodeID(%q) = %q, want %q", in, got, want)
		}
	}
}
