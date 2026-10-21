package gateway

import (
	"bytes"
	"encoding/csv"
	"os"
	"strconv"
	"strings"
	"testing"

	"meshsat/internal/codec"
)

func TestReviewR26ActualSBDFraming(t *testing.T) {
	g := sbdIntentGW(t, IridiumConfig{}, &fakeSat{})
	for _, n := range []int{41, 50, 100, 101, 237, 255, 256, 273} {
		wire := codec.PrependVersionByte(bytes.Repeat([]byte{'A'}, n-1))
		mo, text, reason, detail := g.sbdMO(wire)
		if reason != "" {
			t.Logf("chain_bytes=%d admission=%s detail=%s", n, reason, detail)
			if n <= 255 {
				t.Fatal("unexpected admission refusal")
			}
			continue
		}
		t.Logf("chain_bytes=%d modem_bytes=%d model_credits=%d actual_credits=%d text_mode=%v", n, len(mo), creditCost(n), creditCost(len(mo)), text)
		if text || len(mo) != n+11 {
			t.Fatal("unexpected compact framing")
		}
	}
}

func TestReviewR26CorpusActualSBDFraming(t *testing.T) {
	path := os.Getenv("REVIEW_R26_CSV")
	if path == "" {
		t.Skip("set REVIEW_R26_CSV to the submitted per-message CSV")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	g := sbdIntentGW(t, IridiumConfig{}, &fakeSat{})
	for col, name := range rows[0] {
		if !strings.HasSuffix(name, " bytes") {
			continue
		}
		admitted, refused, credits, framedBytes := 0, 0, 0, 0
		for _, row := range rows[1:] {
			n, err := strconv.Atoi(row[col])
			if err != nil || n < 1 {
				t.Fatalf("invalid length %q: %v", row[col], err)
			}
			wire := codec.PrependVersionByte(bytes.Repeat([]byte{'A'}, n-1))
			mo, _, reason, _ := g.sbdMO(wire)
			if reason != "" {
				refused++
				continue
			}
			admitted++
			credits += creditCost(len(mo))
			framedBytes += len(mo)
		}
		t.Logf("%s: admitted=%d refused=%d modem_bytes=%d credits=%d", name, admitted, refused, framedBytes, credits)
	}
}

func TestReviewR26SMSIntentWireAccounting(t *testing.T) {
	for _, n := range []int{157, 161, 165, 273} {
		wire := codec.PrependVersionByte(bytes.Repeat([]byte{'A'}, n-1))
		air := smsIntentText(wire, true)
		t.Logf("chain_bytes=%d SMS_characters=%d budget_segments=%d fits_intent=%v", n, len(air), SMSSegments(air), len(air) <= 160)
		if len(air) != n-1 {
			t.Fatal("encrypted SMS must strip the protocol version byte")
		}
	}
}
