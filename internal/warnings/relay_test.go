package warnings

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshsat/internal/database"
)

// MESHSAT-1734: the fan-out, through fake senders standing in for the
// delivery ledger.

type send struct{ key, addressee, text string }

type fakeDeps struct {
	alerts, texts, bulletins []send
	aprsUp                   bool
	fail                     error
}

func (f *fakeDeps) deps(pub []byte) Deps {
	return Deps{
		Key: func() ([]byte, error) { return pub, nil },
		SendAlert: func(key, text string) error {
			if f.fail != nil {
				return f.fail
			}
			f.alerts = append(f.alerts, send{key, "", text})
			return nil
		},
		SendText: func(key, text string) error {
			f.texts = append(f.texts, send{key, "", text})
			return nil
		},
		SendBulletin: func(key, addressee, text string) error {
			f.bulletins = append(f.bulletins, send{key, addressee, text})
			return nil
		},
		APRSUp: func() bool { return f.aprsUp },
	}
}

func relayRig(t *testing.T) (*Relay, *fakeDeps, *database.DB) {
	t.Helper()
	db, err := database.New(filepath.Join(t.TempDir(), "kit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := &fakeDeps{aprsUp: true}
	r := New(db, f.deps(testPub(t)))
	r.now = func() time.Time { return time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC) }
	return r, f, db
}

func alertDigest() *Digest {
	return &Digest{MsgType: TypeAlert, Source: SourceMeteoAlarmNL, ID: idOf("nl-1"),
		Sent: time.Date(2026, 10, 28, 10, 11, 41, 0, time.UTC), Expires: time.Date(2026, 10, 29, 10, 11, 41, 0, time.UTC),
		Severity: 2, Urgency: 2, Certainty: 4, Category: 1, Event: 1, Language: "nl",
		Geocodes: []Geocode{{GeocodeEMMAID, "NL804"}}, Text: "Kans op windstoten tot 90 km/u"}
}

func TestAnAlertGoesOutOnEveryBearerOnce(t *testing.T) {
	r, f, db := relayRig(t)
	wire := encode(t, alertDigest(), testKey(t))
	if err := r.Handle(context.Background(), wire, "hub"); err != nil {
		t.Fatal(err)
	}
	if len(f.alerts) != 1 || len(f.texts) != 1 || len(f.bulletins) != 1 {
		t.Fatalf("sends: alerts %d texts %d bulletins %d", len(f.alerts), len(f.texts), len(f.bulletins))
	}
	mesh := f.alerts[0].text
	if mesh != f.texts[0].text {
		t.Fatalf("the alert and the text differ: %q / %q", mesh, f.texts[0].text)
	}
	if !strings.HasPrefix(mesh, "WARNING Wind Moderate until 10:11 UTC: Kans op windstoten tot 90 km/u\n") ||
		!strings.Contains(mesh, "Relay of KNMI via MeteoAlarm warning, issued 28 Oct 10:11 UTC via MeshSat, not an official NL-Alert") {
		t.Fatalf("mesh text: %q", mesh)
	}
	if len(mesh) > MeshMaxBytes {
		t.Fatalf("mesh text %d bytes", len(mesh))
	}
	idHex := alertDigest().IDHex()
	if f.alerts[0].key != "warn:"+idHex+":alert:alert" || f.texts[0].key != "warn:"+idHex+":alert:text" ||
		f.bulletins[0].key != "warn:"+idHex+":alert:aprs" {
		t.Fatalf("keys: %q %q %q", f.alerts[0].key, f.texts[0].key, f.bulletins[0].key)
	}
	b := f.bulletins[0]
	if !strings.HasPrefix(b.addressee, "BLN") || len(b.addressee) != 4 || len(b.text) > APRSMaxChars ||
		!strings.HasSuffix(b.text, " -MeshSat relay") || !strings.HasPrefix(b.text, "WIND MODERATE til 10:11: Kans op windstoten") {
		t.Fatalf("bulletin: %+v", b)
	}
	// Kept, with its bearers.
	list, err := db.ListWarnings(10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	w := list[0]
	if w.ID != idHex || w.MsgType != "Alert" || w.Source != "meteoalarm-nl" || w.Event != "Wind" || w.Severity != "Moderate" ||
		w.Via != "hub" || w.Cancelled || w.Bearers != `["mesh-alert","mesh-text","aprs"]` || w.Text != "Kans op windstoten tot 90 km/u" {
		t.Fatalf("row: %+v", w)
	}
	// The same digest again (a QoS 1 redelivery, the satellite copy): nothing more.
	if err := r.Handle(context.Background(), wire, "iridium_0"); !errors.Is(err, ErrSeen) {
		t.Fatalf("second delivery: %v", err)
	}
	if len(f.alerts) != 1 || len(f.texts) != 1 || len(f.bulletins) != 1 {
		t.Fatal("a seen digest was relayed again")
	}
}

func TestUpdateAndCancelHonourTheReference(t *testing.T) {
	r, f, db := relayRig(t)
	key := testKey(t)
	first := alertDigest()
	if err := r.Handle(context.Background(), encode(t, first, key), "hub"); err != nil {
		t.Fatal(err)
	}
	upd := alertDigest()
	upd.MsgType, upd.ID, upd.Ref, upd.Severity, upd.Text = TypeUpdate, idOf("nl-2"), first.ID, 3, "Zware windstoten tot 110 km/u"
	if err := r.Handle(context.Background(), encode(t, upd, key), "hub"); err != nil {
		t.Fatal(err)
	}
	if len(f.alerts) != 2 || !strings.HasPrefix(f.alerts[1].text, "UPDATE: WARNING Wind Severe") {
		t.Fatalf("update: %+v", f.alerts)
	}
	old, _ := db.GetWarning(first.IDHex())
	if old == nil || !old.Cancelled {
		t.Fatalf("the replaced message is still current: %+v", old)
	}
	cur, _ := db.GetWarning(upd.IDHex())
	if cur == nil || cur.Cancelled || cur.Ref != first.IDHex() || cur.MsgType != "Update" {
		t.Fatalf("the update: %+v", cur)
	}

	cancel := &Digest{MsgType: TypeCancel, Source: SourceMeteoAlarmNL, ID: idOf("nl-3"), Ref: upd.ID, Sent: upd.Sent.Add(time.Hour),
		Expires: upd.Expires, Event: 1, Language: "nl"}
	if err := r.Handle(context.Background(), encode(t, cancel, key), "hub"); err != nil {
		t.Fatal(err)
	}
	if len(f.alerts) != 3 || !strings.HasPrefix(f.alerts[2].text, "CANCELLED: Wind\n") || !strings.HasPrefix(f.bulletins[2].text, "CANCELLED WIND -MeshSat") {
		t.Fatalf("cancel: %+v / %+v", f.alerts[2], f.bulletins[2])
	}
	cur, _ = db.GetWarning(upd.IDHex())
	if cur == nil || !cur.Cancelled {
		t.Fatalf("the cancelled message is still current: %+v", cur)
	}
	// A cancel of something never held still goes out: the mesh may have it.
	orphan := &Digest{MsgType: TypeCancel, Source: SourceDWD, ID: idOf("de-9"), Ref: idOf("never"), Sent: upd.Sent, Event: 2}
	if err := r.Handle(context.Background(), encode(t, orphan, key), "hub"); err != nil || len(f.alerts) != 4 {
		t.Fatalf("orphan cancel: %v, %d alerts", err, len(f.alerts))
	}
}

func TestATestIsMarkedAndAnExpiredOneIsKeptNotRelayed(t *testing.T) {
	r, f, _ := relayRig(t)
	key := testKey(t)
	test := alertDigest()
	test.Test, test.Source, test.ID = true, SourceTest, idOf("test-1")
	if err := r.Handle(context.Background(), encode(t, test, key), "hub"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.alerts[0].text, "TEST WARNING Wind") || !strings.Contains(f.alerts[0].text, "Relay of MeshSat test warning") ||
		!strings.HasPrefix(f.bulletins[0].text, "TEST WIND") {
		t.Fatalf("test marks: %q / %q", f.alerts[0].text, f.bulletins[0].text)
	}
	expired := alertDigest()
	expired.ID, expired.Expires = idOf("old"), time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC)
	if err := r.Handle(context.Background(), encode(t, expired, key), "hub"); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	if len(f.alerts) != 1 {
		t.Fatal("an expired warning was relayed")
	}
	row, _ := r.db.GetWarning(expired.IDHex())
	if row == nil || row.Bearers != "[]" {
		t.Fatalf("the expired warning is not kept with no bearer: %+v", row)
	}
}

func TestRefusalsAndTheAPRSGate(t *testing.T) {
	r, f, db := relayRig(t)
	key := testKey(t)
	// No key pinned.
	r.deps.Key = func() ([]byte, error) { return nil, nil }
	if err := r.Handle(context.Background(), encode(t, alertDigest(), key), "hub"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key: %v", err)
	}
	// A wrong key.
	r.deps.Key = func() ([]byte, error) { return []byte{0x30, 0x00}, nil }
	if err := r.Handle(context.Background(), encode(t, alertDigest(), key), "hub"); err == nil {
		t.Fatal("a bad pinned key verified")
	}
	// The right key, a forged signature.
	r.deps.Key = func() ([]byte, error) { return testPub(t), nil }
	wire := encode(t, alertDigest(), key)
	wire[len(wire)-1] ^= 1
	if err := r.Handle(context.Background(), wire, "hub"); !errors.Is(err, ErrSignature) {
		t.Fatalf("forged: %v", err)
	}
	if n, _ := db.ListWarnings(10); len(n) != 0 || len(f.alerts) != 0 {
		t.Fatal("a refused digest was kept or relayed")
	}
	// APRS gateway down: no bulletin, the mesh still goes.
	f.aprsUp = false
	if err := r.Handle(context.Background(), encode(t, alertDigest(), key), "hub"); err != nil {
		t.Fatal(err)
	}
	if len(f.alerts) != 1 || len(f.bulletins) != 0 {
		t.Fatalf("aprs down: alerts %d bulletins %d", len(f.alerts), len(f.bulletins))
	}
	row, _ := db.GetWarning(alertDigest().IDHex())
	if row.Bearers != `["mesh-alert","mesh-text"]` {
		t.Fatalf("bearers: %s", row.Bearers)
	}
}

func TestHandleSatelliteConsumesOnlyDigests(t *testing.T) {
	r, f, _ := relayRig(t)
	if r.HandleSatellite(context.Background(), "iridium_0", []byte("hello from the RockBLOCK web form")) {
		t.Fatal("a text was eaten")
	}
	if r.HandleSatellite(context.Background(), "iridium_0", append([]byte{Magic, Version}, make([]byte, 20)...)) {
		t.Fatal("a short frame starting like a digest was eaten")
	}
	wire := encode(t, alertDigest(), testKey(t))
	if !r.HandleSatellite(context.Background(), "iridium_0", wire) || len(f.alerts) != 1 {
		t.Fatal("a digest over satellite was not relayed")
	}
	row, _ := r.db.GetWarning(alertDigest().IDHex())
	if row.Via != "iridium_0" {
		t.Fatalf("via: %s", row.Via)
	}
	// Even a forged one is consumed (and refused): it is not a text for the rules.
	wire[10] ^= 1
	if !r.HandleSatellite(context.Background(), "iridium_0", wire) {
		t.Fatal("a forged digest reached the rules")
	}
}

func TestTextsStayWithinTheBearers(t *testing.T) {
	long := &Digest{MsgType: TypeAlert, Source: SourceDWD, ID: idOf("x"), Sent: time.Unix(1793187780, 0).UTC(),
		Expires: time.Unix(1793200000, 0).UTC(), Severity: 4, Event: 4, Language: "de",
		Text: strings.Repeat("Orkanböen und Hagel: amtliche Unwetterwarnung vor schwerem Gewitter ", 3)}
	mesh := MeshText(long)
	if len(mesh) > MeshMaxBytes || !strings.Contains(mesh, "not an official NL-Alert") {
		t.Fatalf("mesh %d bytes: %q", len(mesh), mesh)
	}
	aprs := APRSText(long)
	if len(aprs) > APRSMaxChars || !strings.HasSuffix(aprs, aprsLabel) || strings.ContainsAny(aprs, "|~{") || !strings.Contains(aprs, "Orkanboeen") {
		t.Fatalf("aprs %d: %q", len(aprs), aprs)
	}
	for i := 0; i < len(aprs); i++ {
		if aprs[i] < 0x20 || aprs[i] > 0x7E {
			t.Fatalf("aprs has a non-ASCII byte: %q", aprs)
		}
	}
	if a, b := BulletinAddressee(&Digest{ID: [8]byte{7: 13}}), BulletinAddressee(&Digest{ID: [8]byte{7: 23}}); a != "BLN3" || b != "BLN3" {
		t.Fatalf("addressees: %s %s", a, b)
	}
}
