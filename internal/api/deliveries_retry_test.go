package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/hubreporter"
)

// POST /api/deliveries/{id}/retry refuses the SOS's Hub frame (class
// hub_uplink at priority 0) with 409: re-queued, it would pass the SBD
// credit budget and raise the SOS at the Hub again with its old time. The
// frame stays dead. The fallback's position frame and an ordinary message
// are re-queued as before. (The web UI and the Linux app offer Retry on
// dead and failed deliveries only; this is the guard.) [MESHSAT-1431]
func TestRetryDelivery_RefusesTheSOSFrame(t *testing.T) {
	s := newTestServerWithDB(t)
	now := time.Unix(1790000000, 0)
	dead := func(class string, priority int, payload []byte) int64 {
		t.Helper()
		id, err := s.db.InsertDelivery(database.MessageDelivery{MsgRef: fmt.Sprintf("r-%s-%d", class, priority), Channel: "iridium_0",
			Status: "dead", Class: class, Priority: priority, Payload: payload, TextPreview: "hub uplink frame"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	sos := dead(database.DeliveryClassHubUplink, 0, hubreporter.EncodeSatSOS("msa-flaneur", "bridge", 52.1601, 4.4970, "SOS: Anna needs help.", now))
	position := dead(database.DeliveryClassHubUplink, 1, hubreporter.EncodeSatPosition("msa-flaneur", 52.1601, 4.4970, 3.9, 1, now))
	text := dead(database.DeliveryClassMessage, 1, []byte("hello"))

	w := post(t, s, fmt.Sprintf("/api/deliveries/%d/retry", sos), "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "not sent again") {
		t.Fatalf("retry of the SOS frame: %d %s, want 409", w.Code, w.Body.String())
	}
	if st, _ := rowStatus(t, s, sos); st != "dead" {
		t.Fatalf("the SOS frame is %s after the refused retry, want dead", st)
	}
	for name, id := range map[string]int64{"the position frame": position, "a message": text} {
		if w := post(t, s, fmt.Sprintf("/api/deliveries/%d/retry", id), ""); w.Code != http.StatusOK {
			t.Fatalf("retry of %s: %d %s", name, w.Code, w.Body.String())
		}
		if st, _ := rowStatus(t, s, id); st != "queued" {
			t.Fatalf("%s is %s after the retry, want queued", name, st)
		}
	}
}
