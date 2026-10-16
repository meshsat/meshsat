package database

import (
	"path/filepath"
	"testing"
)

func intentTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(filepath.Join(t.TempDir(), "intents.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newTestIntent writes a submission with one intent and n round-1 rows.
func newTestIntent(t *testing.T, db *DB, id string, n int, capMinor *int64) []int64 {
	t.Helper()
	in := DeliveryIntent{ID: id, ContactID: "c", Precedence: "Routine", Class: DeliveryClassMessage, Payload: []byte("hi"),
		PayloadSHA256: "x", EffectiveStrategy: "ALL_BEARERS", Currency: "EUR", CapMinor: capMinor,
		RoutePlan: []RoutePlanEntry{{RouteIndex: 1, Admitted: true, InterfaceID: "mesh_0"}}}
	var rows []MessageDelivery
	for i := 1; i <= n; i++ {
		rows = append(rows, MessageDelivery{MsgRef: id, Channel: "mesh_0", Priority: 1, MaxRetries: 3, QoSLevel: 1,
			Payload: []byte("hi"), IntentID: id, Round: 1, AttemptNo: i, RouteIndex: 1})
	}
	_, ids, err := db.CreateSubmission(DeliverySubmission{ID: "sub-" + id, Source: "api", RequestSHA256: id},
		[]NewIntent{{Intent: in, FirstRows: rows}})
	if err != nil {
		t.Fatal(err)
	}
	return ids[0]
}

func claim(t *testing.T, db *DB, id int64) int {
	t.Helper()
	ok, err := db.ClaimDeliveryForSending(id)
	if err != nil || !ok {
		t.Fatalf("claim %d: %v %v", id, ok, err)
	}
	d, _ := db.GetDelivery(id)
	return d.ClaimGen
}

// S16 (the requirements' added assertion, G1): after a restart that
// follows a fully charged, an unknown and an authorized-only transmission,
// the intent's aggregates equal the sums over its transmissions: nothing
// reserved on any; incurred 10, possible 10, nothing, respectively.
func TestIntentAggregatesEqualTransmissionSums(t *testing.T) {
	db := intentTestDB(t)
	capMinor := int64(100)
	rows := newTestIntent(t, db, "agg", 3, &capMinor)

	var tx [3]int64
	for i, r := range rows {
		gen := claim(t, db, r)
		id, err := db.AuthorizeTransmission(r, gen, "unit", 1, 10)
		if err != nil {
			t.Fatal(err)
		}
		tx[i] = id
	}
	in, _ := db.GetIntent("agg")
	if in.ReservedMinor != 30 {
		t.Fatalf("reserved %d after three authorizations; want 30", in.ReservedMinor)
	}
	// A fourth reservation of 80 does not fit the cap of 100.
	_, _ = db.Exec(`UPDATE message_deliveries SET status = 'sending' WHERE id = ?`, rows[0])
	if _, err := db.AuthorizeTransmission(rows[0], 1, "unit", 1, 80); err != ErrOverCap {
		t.Fatalf("over the cap: %v; want ErrOverCap", err)
	}
	_ = db.MarkTransmissionHandedOff(tx[0])
	if _, err := db.FinalizeTransmission(tx[0], TransmissionResult{Class: ClassAccepted, ChargedUnits: 1, ChargedMinor: 10, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	_ = db.MarkTransmissionHandedOff(tx[1]) // then the stop
	st, err := db.RecoverIntentDeliveries()
	if err != nil {
		t.Fatal(err)
	}
	if st.HandedOff != 1 || st.Authorized != 1 {
		t.Fatalf("recovery %+v; want one handed off, one authorized", st)
	}
	snap, _ := db.LoadIntentSnapshot("agg")
	var reserved, incurred, possible int64
	for _, x := range snap.Transmissions {
		reserved += x.ReservedMinor
		incurred += x.ChargedMinor
		possible += x.PossibleMinor
	}
	got := snap.Intent
	if got.ReservedMinor != 0 || got.IncurredMinor != 10 || got.PossibleMinor != 10 {
		t.Fatalf("aggregates reserved %d incurred %d possible %d; want 0, 10, 10", got.ReservedMinor, got.IncurredMinor, got.PossibleMinor)
	}
	if reserved != got.ReservedMinor || incurred != got.IncurredMinor || possible != got.PossibleMinor {
		t.Fatalf("sums reserved %d incurred %d possible %d differ from the aggregates", reserved, incurred, possible)
	}
	want := map[int64][3]int64{tx[0]: {0, 10, 0}, tx[1]: {0, 0, 10}, tx[2]: {0, 0, 0}}
	for _, x := range snap.Transmissions {
		w := want[x.ID]
		if x.ReservedMinor != w[0] || x.ChargedMinor != w[1] || x.PossibleMinor != w[2] {
			t.Fatalf("transmission %d: reserved %d charged %d possible %d; want %v", x.ID, x.ReservedMinor, x.ChargedMinor, x.PossibleMinor, w)
		}
	}
}

// The claim fence (r3 section 3a): an intent's row is claimed only while
// its intent is active and it is of the active round; a legacy row is
// claimed as before.
func TestIntentClaimFence(t *testing.T) {
	db := intentTestDB(t)
	rows := newTestIntent(t, db, "fence", 2, nil)
	if _, err := db.Exec(`UPDATE message_deliveries SET round = 2 WHERE id = ?`, rows[1]); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.ClaimDeliveryForSending(rows[1]); ok {
		t.Fatal("a row of another round was claimed")
	}
	if _, err := db.Exec(`UPDATE delivery_intents SET state = 'closed' WHERE id = 'fence'`); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.ClaimDeliveryForSending(rows[0]); ok {
		t.Fatal("a row of a closed intent was claimed")
	}
	legacy, err := db.InsertDelivery(MessageDelivery{MsgRef: "l", Channel: "mesh_0", Status: "queued", Priority: 1, MaxRetries: 3})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.ClaimDeliveryForSending(legacy); !ok {
		t.Fatal("a legacy row was not claimed")
	}
	// A finalization belongs to its own claim: a transmission of an
	// earlier generation never moves the row of a later claim.
	_, _ = db.Exec(`UPDATE delivery_intents SET state = 'active' WHERE id = 'fence'`)
	g1 := claim(t, db, rows[0])
	t1, _ := db.AuthorizeTransmission(rows[0], g1, "", 0, 0)
	_, _ = db.Exec(`UPDATE message_deliveries SET status = 'retry' WHERE id = ?`, rows[0])
	g2 := claim(t, db, rows[0])
	if _, err := db.AuthorizeTransmission(rows[0], g1, "", 0, 0); err != ErrClaimLost {
		t.Fatalf("authorizing under the old claim: %v; want ErrClaimLost", err)
	}
	_ = db.MarkTransmissionHandedOff(t1)
	if _, err := db.FinalizeTransmission(t1, TransmissionResult{Class: ClassAccepted}); err != nil {
		t.Fatal(err)
	}
	d, _ := db.GetDelivery(rows[0])
	if d.Status != "sending" || d.ClaimGen != g2 {
		t.Fatalf("row %s gen %d; the old generation's finalization moved the new claim", d.Status, d.ClaimGen)
	}
}

// F-4: wire timestamps are distinct and never go back, within one
// millisecond, with the clock stepping back, and across a reopen.
func TestAllocateWireTimestampMs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.db")
	db, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, now := range []int64{1000, 1000, 1000, 900, 5000} {
		v, err := db.AllocateWireTimestampMs(now)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	want := []int64{1000, 1001, 1002, 1003, 5000}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("allocations %v; want %v", got, want)
		}
	}
	_ = db.Close()
	db, err = New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if v, _ := db.AllocateWireTimestampMs(10); v != 5001 {
		t.Fatalf("after a reopen: %d; want 5001", v)
	}
}
