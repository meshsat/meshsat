package rnsstack

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/reticulum"
	"meshsat/internal/routing"
)

// A13-R2: the late-proof listener is in place when Build returns, before
// main.go hands the node to the packet ingress, so a proof that is the very
// first packet in is recorded; when the stored hashes cannot be loaded the
// listener still checks every unmatched proof against the database rather
// than letting it go (and then be filtered as a duplicate). [MESHSAT-1584]

// handedOff stores one intent attempt with a handed-off transmission whose
// proof hash and key are stored, and returns a valid implicit proof packet
// for it, signed by the destination, and the transmission id.
func handedOff(t *testing.T, db *database.DB) ([]byte, int64) {
	t.Helper()
	in := database.DeliveryIntent{ID: "late", ContactID: "c", Precedence: "Routine", Class: database.DeliveryClassMessage,
		Payload: []byte("hi"), PayloadSHA256: "x", EffectiveStrategy: "PRIMARY_ONLY", Currency: "EUR",
		RoutePlan: []database.RoutePlanEntry{{RouteIndex: 1, Admitted: true, InterfaceID: "lxmf_0"}}}
	rows := []database.MessageDelivery{{MsgRef: "late", Channel: "lxmf_0", Priority: 1, MaxRetries: 3, QoSLevel: 1,
		Payload: []byte("hi"), IntentID: "late", Round: 1, AttemptNo: 1, RouteIndex: 1, Class: database.DeliveryClassLXMF}}
	_, ids, err := db.CreateSubmission(database.DeliverySubmission{ID: "sub-late", Source: "api", RequestSHA256: "late"},
		[]database.NewIntent{{Intent: in, FirstRows: rows}})
	if err != nil {
		t.Fatal(err)
	}
	row := ids[0][0]
	if ok, err := db.ClaimDeliveryForSending(row); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	d, _ := db.GetDelivery(row)
	txID, err := db.AuthorizeTransmission(row, d.ClaimGen, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkTransmissionHandedOff(txID); err != nil {
		t.Fatal(err)
	}
	dest, err := reticulum.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	var hash [32]byte
	_, _ = rand.Read(hash[:])
	if err := db.SetTransmissionProof(row, d.ClaimGen, hex.EncodeToString(hash[:]), hex.EncodeToString(dest.SigningPublicKey())); err != nil {
		t.Fatal(err)
	}
	var trunc [reticulum.TruncatedHashLen]byte
	copy(trunc[:], hash[:])
	proof := (&reticulum.Header{HeaderType: reticulum.HeaderType1, DestType: reticulum.DestSingle, PacketType: reticulum.PacketProof,
		DestHash: trunc, Context: reticulum.ContextNone, Data: dest.Sign(hash[:])}).Marshal()
	return proof, txID
}

func proofRows(t *testing.T, db *database.DB, txID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM delivery_evidence WHERE transmission_id = ? AND event = 'proof' AND matched = 1`, txID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func buildFor(t *testing.T, db *database.DB) *Stack {
	t.Helper()
	ident, err := routing.NewIdentity(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := Build(ctx, Config{Identity: ident, Registry: routing.NewInterfaceRegistry(), DB: db, LXMF: LXMFConfig{Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestLateProof_FirstPacketAfterBuildIsRecorded(t *testing.T) {
	for _, tc := range []struct {
		name    string
		loadErr bool
	}{{"stored_hashes_loaded", false}, {"stored_hashes_unreadable", true}} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := database.New(filepath.Join(t.TempDir(), "bridge.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			proof, txID := handedOff(t, db)
			if tc.loadErr {
				prev := loadExpectedProofs
				loadExpectedProofs = func(*database.DB) ([]string, error) { return nil, errors.New("test: unreadable") }
				defer func() { loadExpectedProofs = prev }()
			}
			st := buildFor(t, db)
			// What main.go does next: the node is handed to the ingress and
			// the first packet in is the proof; then a copy of it.
			st.Node.Inbound(append([]byte(nil), proof...), "tcp_0")
			st.Node.Inbound(append([]byte(nil), proof...), "tcp_0")
			if n := proofRows(t, db, txID); n != 1 {
				t.Fatalf("%d proof rows; want the first packet recorded once", n)
			}
		})
	}
}
