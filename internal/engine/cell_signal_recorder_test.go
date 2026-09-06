package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// The SMS history must show what the peer's operator typed, not the
// smaz2+AES-GCM+base64 wire text the modem handed over. [MESHSAT-822]

const smsTestTransforms = `[{"type": "smaz2"}, {"type": "encrypt", "params": {"key_ref": "sms:shared"}}, {"type": "base64"}]`

// newSMSRecorderHarness returns a recorder wired to a processor whose
// dispatcher carries a transform pipeline with a fixed key, plus the
// pipeline itself so the test can produce ciphertext the way a peer kit
// would. cellular_0 is inserted with the given ingress transforms.
func newSMSRecorderHarness(t *testing.T, ingress string) (*CellSignalRecorder, *database.DB, *TransformPipeline) {
	t.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`INSERT OR IGNORE INTO interfaces (id, channel_type, label, enabled, config, ingress_transforms, egress_transforms)
		VALUES ('cellular_0', 'cellular', 'cellular_0', 1, '{}', ?, ?)`, ingress, ingress); err != nil {
		t.Fatal(err)
	}

	tp := NewTransformPipeline()
	tp.SetKeyResolver(stubKeyResolver{hexKey: strings.Repeat("5a", 32)})

	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	disp := NewDispatcher(db, reg, nil, nil)
	disp.SetTransformPipeline(tp)

	proc := NewProcessor(db, nil)
	proc.SetDispatcher(disp)

	rec := NewCellSignalRecorder(db, nil)
	rec.SetProcessor(proc)
	return rec, db, tp
}

func smsEvent(t *testing.T, sender, text string) transport.CellEvent {
	t.Helper()
	data, err := json.Marshal(transport.SMSMessage{Sender: sender, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return transport.CellEvent{Type: "sms_received", Message: text, Data: data}
}

func lastSMS(t *testing.T, db *database.DB) database.SMSMessageRecord {
	t.Helper()
	rows, err := db.GetSMSMessages(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one sms row, got %d", len(rows))
	}
	return rows[0]
}

func TestCellRecorder_EncryptedInboundStoredAsPlaintext(t *testing.T) {
	rec, db, tp := newSMSRecorderHarness(t, smsTestTransforms)

	const plain = "MeshSat test TP1 14:13:40Z"
	wire, err := tp.ApplyEgress([]byte(plain), smsTestTransforms)
	if err != nil {
		t.Fatal(err)
	}
	if string(wire) == plain {
		t.Fatal("egress did not transform the text; test cannot prove anything")
	}

	rec.handleSMSReceived(smsEvent(t, "+31653618463", string(wire)))

	row := lastSMS(t, db)
	if row.Direction != "rx" || row.Phone != "+31653618463" || row.Status != "delivered" {
		t.Fatalf("row: %+v", row)
	}
	if row.Text != plain {
		t.Fatalf("history text = %q, want plaintext %q", row.Text, plain)
	}
}

func TestCellRecorder_ClearInboundOnEncryptedInterfaceKeepsRaw(t *testing.T) {
	rec, db, _ := newSMSRecorderHarness(t, smsTestTransforms)

	// A clear SMS from a phone that has no key fails the decrypt step and
	// must land in history as sent, never be dropped or garbled.
	const clear = "hello from a plain phone"
	rec.handleSMSReceived(smsEvent(t, "+31600000000", clear))

	if got := lastSMS(t, db).Text; got != clear {
		t.Fatalf("history text = %q, want raw %q", got, clear)
	}
}

func TestCellRecorder_NoTransformsStoresRaw(t *testing.T) {
	rec, db, _ := newSMSRecorderHarness(t, "[]")

	const text = "no transforms configured"
	rec.handleSMSReceived(smsEvent(t, "+31600000000", text))

	if got := lastSMS(t, db).Text; got != text {
		t.Fatalf("history text = %q, want %q", got, text)
	}
}

func TestCellRecorder_NoProcessorStoresRaw(t *testing.T) {
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// The recorder runs even when no gateway or processor is wired; a nil
	// processor must not panic and must store the wire text.
	rec := NewCellSignalRecorder(db, nil)
	rec.handleSMSReceived(smsEvent(t, "+31600000000", "wire text"))

	if got := lastSMS(t, db).Text; got != "wire text" {
		t.Fatalf("history text = %q", got)
	}
}

func TestCellRecorder_DedupOnDecodedText(t *testing.T) {
	rec, db, tp := newSMSRecorderHarness(t, smsTestTransforms)

	const plain = "same message twice"
	wire, err := tp.ApplyEgress([]byte(plain), smsTestTransforms)
	if err != nil {
		t.Fatal(err)
	}

	// A modem that re-raises +CMTI hands over the identical ciphertext;
	// the second copy is suppressed on the decoded text.
	rec.handleSMSReceived(smsEvent(t, "+31653618463", string(wire)))
	rec.handleSMSReceived(smsEvent(t, "+31653618463", string(wire)))

	rows, err := db.GetSMSMessages(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected the duplicate to be suppressed, got %d rows", len(rows))
	}
}
