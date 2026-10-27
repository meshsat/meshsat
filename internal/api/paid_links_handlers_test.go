package api

import (
	"strings"
	"testing"

	"meshsat/internal/rns"
)

// A paid link opened without extra_hops gets the contract's default, so a
// stock client's direct message survives the satellite round trip; an
// explicit value, 0 included, is kept. [MESHSAT-1714]
func TestDecodePaidPolicyRequestExtraHops(t *testing.T) {
	dest := `"0123456789abcdef0123456789abcdef"`
	cases := []struct {
		name string
		body string
		want int
	}{
		{"opened without extra_hops", `{"dests":[` + dest + `],"min_interval_sec":300}`, rns.DefaultPaidExtraHops},
		{"closed without extra_hops", `{"dests":[]}`, 0},
		{"explicit zero kept", `{"dests":[` + dest + `],"extra_hops":0}`, 0},
		{"explicit value kept", `{"dests":[` + dest + `],"extra_hops":12}`, 12},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := decodePaidPolicyRequest(strings.NewReader(c.body))
			if err != nil {
				t.Fatal(err)
			}
			if req.ExtraHops != c.want {
				t.Fatalf("extra_hops = %d, want %d", req.ExtraHops, c.want)
			}
			if _, err := rns.ParsePaidPolicy(req); err != nil {
				t.Fatalf("decoded body does not parse: %v", err)
			}
		})
	}
	if _, err := decodePaidPolicyRequest(strings.NewReader(`{"dests":`)); err == nil {
		t.Fatal("broken JSON accepted")
	}
}

// 20 extra hops cover about 60 s each way by the rule
// 6 x (hops + 3) >= 2 x one-way delay that the Reticulum contract states.
func TestDefaultPaidExtraHopsCoversSixtySeconds(t *testing.T) {
	if 6*(rns.DefaultPaidExtraHops+3) < 2*60 {
		t.Fatalf("default %d extra hops do not cover 60 s each way", rns.DefaultPaidExtraHops)
	}
	if rns.DefaultPaidExtraHops > rns.MaxPaidExtraHops {
		t.Fatal("default above the maximum")
	}
}
