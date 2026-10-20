package database

import (
	"fmt"
	"testing"
	"time"
)

// statsIntent writes one intent with one round-1 attempt on a route of kind
// on iface, est wire bytes est, payload payload; returns the attempt id.
func statsIntent(t *testing.T, db *DB, id, kind, iface string, est int, payload string) int64 {
	t.Helper()
	in := DeliveryIntent{ID: id, ContactID: "c", Precedence: "Routine", Class: DeliveryClassMessage, Payload: []byte(payload),
		PayloadSHA256: "x", EffectiveStrategy: "PRIMARY_ONLY", Currency: "EUR",
		RoutePlan: []RoutePlanEntry{{RouteIndex: 1, Kind: kind, InterfaceID: iface, EstWireBytes: est, Admitted: true}}}
	rows := []MessageDelivery{{MsgRef: id, Channel: iface, Priority: 1, MaxRetries: 3, QoSLevel: 1,
		Payload: []byte(payload), IntentID: id, Round: 1, AttemptNo: 1, RouteIndex: 1}}
	_, ids, err := db.CreateSubmission(DeliverySubmission{ID: "sub-" + id, Source: "api", RequestSHA256: id},
		[]NewIntent{{Intent: in, FirstRows: rows}})
	if err != nil {
		t.Fatal(err)
	}
	return ids[0][0]
}

// handOff claims, authorizes and hands off the attempt with a correlation;
// returns the transmission.
func handOff(t *testing.T, db *DB, attempt int64, corr string) int64 {
	t.Helper()
	gen := claim(t, db, attempt)
	tx, err := db.AuthorizeTransmission(attempt, gen, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkTransmissionHandedOffWith(tx, Correlation{Kind: "pkt", Value: corr, Scope: "s"}); err != nil {
		t.Fatal(err)
	}
	return tx
}

func finalize(t *testing.T, db *DB, tx int64, class, corr string) {
	t.Helper()
	if _, err := db.FinalizeTransmission(tx, TransmissionResult{Class: class, CorrKind: "pkt", CorrValue: corr, CorrScope: "s"}); err != nil {
		t.Fatal(err)
	}
}

func receipt(t *testing.T, db *DB, corr, completes string) DeliveryEvidence {
	t.Helper()
	e, err := db.RecordEvidence(DeliveryEvidence{Source: "test", Event: "receipt", CorrKind: "pkt", CorrValue: corr, CorrScope: "s"}, 0,
		func(DeliveryTransmission) (string, string, string, string) {
			return "receipt", "test", "authenticated", completes
		})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// S3-3: every attempt of the window lands in exactly one stage, per bearer,
// interface and size bucket; receipts complete only the attempt they match
// alone; attempts outside the window are not counted. [MESHSAT-1584]
func TestDeliveryOutcomeStats(t *testing.T) {
	db := intentTestDB(t)

	// completed: accepted, then a receipt (delivered_device)
	a := statsIntent(t, db, "done", "MESHTASTIC", "mesh_0", 30, "hi")
	finalize(t, db, handOff(t, db, a, "1"), ClassAccepted, "1")
	if e := receipt(t, db, "1", OutcomeDeliveredDevice); !e.Matched {
		t.Fatal("setup: the receipt did not match")
	}
	// completed by the Hub app, larger message
	a = statsIntent(t, db, "app", "MQTT", "hub_0", 120, "hello there")
	finalize(t, db, handOff(t, db, a, "2"), ClassAccepted, "2")
	receipt(t, db, "2", OutcomeDeliveredApp)
	// a receipt for an attempt that ended unknown still completes it
	a = statsIntent(t, db, "late", "MESHTASTIC", "mesh_0", 30, "hi")
	finalize(t, db, handOff(t, db, a, "3"), ClassUnknown, "3")
	receipt(t, db, "3", OutcomeDeliveredDevice)
	// accepted, no receipt
	a = statsIntent(t, db, "acc", "MESHTASTIC", "mesh_0", 30, "hi")
	finalize(t, db, handOff(t, db, a, "4"), ClassAccepted, "4")
	// unknown
	a = statsIntent(t, db, "unk", "MESHTASTIC", "mesh_0", 30, "hi")
	finalize(t, db, handOff(t, db, a, "5"), ClassUnknown, "5")
	// two attempts whose correlation is the same: a receipt for it is
	// ambiguous and completes neither
	a = statsIntent(t, db, "amb1", "MESHTASTIC", "mesh_0", 30, "hi")
	finalize(t, db, handOff(t, db, a, "6"), ClassAccepted, "6")
	a = statsIntent(t, db, "amb2", "MESHTASTIC", "mesh_0", 30, "hi")
	finalize(t, db, handOff(t, db, a, "6"), ClassAccepted, "6")
	if e := receipt(t, db, "6", OutcomeDeliveredDevice); !e.Ambiguous {
		t.Fatal("setup: the receipt was not ambiguous")
	}
	// not sent: a transmission that ended not sent and spent a retry, then
	// a refusal before any transmission (which spends none)
	a = statsIntent(t, db, "ns", "MESHTASTIC", "mesh_0", 30, "hi")
	if _, err := db.FinalizeTransmission(handOff(t, db, a, "8"), TransmissionResult{Class: ClassNotSent, Retryable: true,
		ConsumeRetry: true, RetryAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	gen := claim(t, db, a)
	if err := db.EndAttemptNotSent(a, gen, "oversize", "", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	// unresolved: one queued, one handed off with no answer yet
	statsIntent(t, db, "q", "SMS", "cellular_0", 0, "a text of twelve")
	handOff(t, db, statsIntent(t, db, "ho", "SMS", "cellular_0", 0, "a text of twelve"), "7")
	// withdrawn, and a status none of the stages names (set by hand: no
	// ledger call writes them in a single step)
	a = statsIntent(t, db, "sup", "MESHTASTIC", "mesh_0", 30, "hi")
	_, _ = db.Exec(`UPDATE message_deliveries SET status = 'superseded' WHERE id = ?`, a)
	a = statsIntent(t, db, "odd", "MESHTASTIC", "mesh_0", 30, "hi")
	_, _ = db.Exec(`UPDATE message_deliveries SET status = 'failed' WHERE id = ?`, a)
	// outside the window
	a = statsIntent(t, db, "old", "MESHTASTIC", "mesh_0", 30, "hi")
	_, _ = db.Exec(`UPDATE message_deliveries SET created_at = '2020-01-01 00:00:00' WHERE id = ?`, a)
	_, _ = db.Exec(`UPDATE delivery_intents SET created_at = '2020-01-01 00:00:00' WHERE id = 'old'`)

	st, err := db.DeliveryOutcomeStatsBetween(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]OutcomeStats{}
	for _, r := range st.Routes {
		byKey[r.Bearer+"/"+r.InterfaceID+"/"+r.SizeBucket] = r
		if sum := sumStages(r); sum != r.Attempts {
			t.Errorf("%s/%s/%s: stages add up to %d, attempts %d", r.Bearer, r.InterfaceID, r.SizeBucket, sum, r.Attempts)
		}
	}
	mesh := byKey["MESHTASTIC/mesh_0/1-50"]
	want := OutcomeStats{Attempts: 9, CompletedTotal: 2, Accepted: 3, Unknown: 1, NotSent: 1, Withdrawn: 1, RetriesSpent: 1}
	if mesh.Attempts != want.Attempts || mesh.CompletedTotal != want.CompletedTotal || mesh.Completed[OutcomeDeliveredDevice] != 2 ||
		mesh.Accepted != want.Accepted || mesh.Unknown != want.Unknown || mesh.NotSent != want.NotSent ||
		mesh.Withdrawn != want.Withdrawn || mesh.Other["failed"] != 1 || mesh.Unresolved != 0 || mesh.RetriesSpent != want.RetriesSpent {
		t.Errorf("mesh row %+v", mesh)
	}
	if app := byKey["MQTT/hub_0/101-160"]; app.Attempts != 1 || app.Completed[OutcomeDeliveredApp] != 1 {
		t.Errorf("hub row %+v", app)
	}
	// No estimate: the payload's length (16 bytes) decides the bucket.
	if sms := byKey["SMS/cellular_0/1-50"]; sms.Attempts != 2 || sms.Unresolved != 2 || sms.HandedOff != 1 {
		t.Errorf("sms row %+v", sms)
	}
	if mesh.HandedOff != 0 || st.Total.HandedOff != 1 {
		t.Errorf("handed off counted outside the unresolved: mesh %d, total %d", mesh.HandedOff, st.Total.HandedOff)
	}
	if st.Total.Attempts != 12 || sumStages(st.Total) != 12 {
		t.Errorf("total %+v", st.Total)
	}
	if st.Intents.Intents != 12 || st.Intents.Active+sumMap(st.Intents.ByOutcome) != 12 {
		t.Errorf("intents %+v", st.Intents)
	}
	if len(st.Routes) != 3 || st.Unit != "attempt" {
		t.Errorf("%d rows, unit %q", len(st.Routes), st.Unit)
	}
}

func sumStages(r OutcomeStats) int {
	return r.CompletedTotal + r.Accepted + r.Unknown + r.NotSent + r.Withdrawn + r.Unresolved + sumMap(r.Other)
}

func sumMap(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func TestSizeBucket(t *testing.T) {
	for n, want := range map[int]string{0: "1-50", 1: "1-50", 50: "1-50", 51: "51-100", 160: "101-160", 161: "161-340",
		340: "161-340", 1024: "341-1024", 1025: ">1024"} {
		if got := SizeBucket(n); got != want {
			t.Errorf("%d: %s, want %s", n, got, want)
		}
	}
	var order []int
	for _, b := range []string{"1-50", "51-100", "101-160", "161-340", "341-1024", ">1024"} {
		order = append(order, bucketOrder(b))
	}
	if fmt.Sprint(order) != "[0 1 2 3 4 5]" {
		t.Errorf("bucket order %v", order)
	}
}
