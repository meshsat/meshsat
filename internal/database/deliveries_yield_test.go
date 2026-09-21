package database

import (
	"testing"
	"time"
)

// The MT poll gives way only to a message that outranks it and is due now.
// [MESHSAT-1282]
func TestHasDueDeliveryAboveDeferred(t *testing.T) {
	future := time.Now().Add(10 * time.Minute).UTC()
	for _, tc := range []struct {
		name string
		row  MessageDelivery
		want bool
	}{
		{"nothing but the poll itself", MessageDelivery{Status: "sending", Precedence: "Deferred"}, false},
		{"another Deferred row", MessageDelivery{Status: "queued", Precedence: "Deferred"}, false},
		{"a Routine message queued", MessageDelivery{Status: "queued", Precedence: "Routine"}, true},
		{"no precedence counts as Routine", MessageDelivery{Status: "queued"}, true},
		{"a Flash message due for retry", MessageDelivery{Status: "retry", Precedence: "Flash"}, true},
		{"a Routine retry not due yet", MessageDelivery{Status: "retry", Precedence: "Routine", NextRetry: &future}, false},
		{"a Routine message on another channel", MessageDelivery{Channel: "cellular_0", Status: "queued", Precedence: "Routine"}, false},
		{"a Routine message already sent", MessageDelivery{Status: "sent", Precedence: "Routine"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			row := tc.row
			if row.Channel == "" {
				row.Channel = "iridium_imt_0"
			}
			row.MsgRef = "test"
			id, err := db.InsertDelivery(row)
			if err != nil {
				t.Fatal(err)
			}
			if row.Status != "queued" {
				if _, err := db.Exec(`UPDATE message_deliveries SET status = ? WHERE id = ?`, row.Status, id); err != nil {
					t.Fatal(err)
				}
			}
			got, err := db.HasDueDeliveryAboveDeferred("iridium_imt_0")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
