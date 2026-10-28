package warnings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshsat/internal/cell"
	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// MESHSAT-1734: the fan-out, through fake senders standing in for the
// delivery ledger.

type send struct{ key, addressee, text string }

type fakeDeps struct {
	alerts, texts, bulletins []send
	aprsUp                   bool
	fail                     error
	// The cell: what the fake osmo-cbc side was asked, which channels its
	// allow-list refuses, what was cancelled, and what is on air by ref.
	cellUp      bool
	cellReqs    []cell.BroadcastRequest
	cellRefuse  map[int]bool
	cellCancels []string
	cellOnAir   map[string]int
	// The LXMF leg: the peers the node knows, what was queued (addressee
	// is the destination), and a failure to inject.
	peers    []LXMFPeer
	lxmf     []send
	lxmfFail error
	// The event stream.
	events []transport.MeshEvent
}

func (f *fakeDeps) deps(pub []byte) Deps {
	return Deps{
		LXMFPeers: func() []LXMFPeer { return f.peers },
		SendLXMF: func(key, dest, text string) error {
			if f.lxmfFail != nil {
				return f.lxmfFail
			}
			f.lxmf = append(f.lxmf, send{key, dest, text})
			return nil
		},
		Emit:   func(ev transport.MeshEvent) { f.events = append(f.events, ev) },
		CellUp: func() bool { return f.cellUp },
		CellBroadcast: func(ctx context.Context, req cell.BroadcastRequest) error {
			f.cellReqs = append(f.cellReqs, req)
			if f.cellRefuse[req.Channel] {
				return fmt.Errorf("%w: %d", cell.ErrChannelRefused, req.Channel)
			}
			if f.cellOnAir == nil {
				f.cellOnAir = map[string]int{}
			}
			delete(f.cellOnAir, req.Replaces)
			f.cellOnAir[req.Ref] = req.Channel
			return nil
		},
		CellCancel: func(ctx context.Context, ref string) (int, error) {
			f.cellCancels = append(f.cellCancels, ref)
			if _, ok := f.cellOnAir[ref]; ok {
				delete(f.cellOnAir, ref)
				return 1, nil
			}
			return 0, nil
		},
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

// The 2G cell leg (MESHSAT-1734): a test digest goes out on 4380, a real
// one on the identifier the TS 23.041 table gives its severity, certainty
// and urgency, or on the ordinary channel when the cell's allow-list
// refuses that identifier or the severity has none; an Update replaces
// under the first id; a Cancel takes it off the air, radio up or not; a
// radio that is down gets no broadcast and the other bearers still go.
func TestTheCellLeg(t *testing.T) {
	r, f, db := relayRig(t)
	key := testKey(t)
	f.cellUp = true
	f.cellRefuse = map[int]bool{4371: true, 4372: true, 4373: true, 4374: true, 4375: true, 4376: true, 4377: true, 4378: true}

	// A test: 4380, marked as a test run, the mesh text, a minute apart
	// until it expires (22 h 11 min from the rig's noon: 1326 broadcasts).
	test := alertDigest()
	test.Test, test.Source, test.ID = true, SourceTest, idOf("t-1")
	if err := r.Handle(context.Background(), encode(t, test, key), "hub"); err != nil {
		t.Fatal(err)
	}
	if len(f.cellReqs) != 1 {
		t.Fatalf("cell requests %+v", f.cellReqs)
	}
	req := f.cellReqs[0]
	if req.Channel != 4380 || !req.Test || req.Ref != test.IDHex() || req.Replaces != "" || req.Language != "nl" || req.Period != CellRepetitionPeriod ||
		req.Count != 1326 || req.Text != f.alerts[0].text || !strings.HasPrefix(req.Purpose, "warning relay "+test.IDHex()+": Alert from test (Wind Moderate) [test]") {
		t.Fatalf("test request %+v", req)
	}
	row, _ := db.GetWarning(test.IDHex())
	if row.Bearers != `["mesh-alert","mesh-text","aprs","cell"]` {
		t.Fatalf("bearers %s", row.Bearers)
	}

	// A real Extreme, observed, immediate: 4371, refused by the allow-list,
	// so the ordinary channel carries it.
	ext := alertDigest()
	ext.ID, ext.Severity, ext.Certainty, ext.Urgency = idOf("x-1"), 4, 4, 3
	if err := r.Handle(context.Background(), encode(t, ext, key), "hub"); err != nil {
		t.Fatal(err)
	}
	if len(f.cellReqs) != 3 || f.cellReqs[1].Channel != 4371 || f.cellReqs[2].Channel != cell.OrdinaryChannel || f.cellReqs[2].Ref != ext.IDHex() || f.cellReqs[2].Test {
		t.Fatalf("extreme: %+v", f.cellReqs[1:])
	}
	// Its Update, now Severe, likely, expected: 4378 refused, ordinary again, replacing the first.
	upd := alertDigest()
	upd.MsgType, upd.ID, upd.Ref, upd.Severity, upd.Certainty, upd.Urgency = TypeUpdate, idOf("x-2"), ext.ID, 3, 3, 2
	if err := r.Handle(context.Background(), encode(t, upd, key), "hub"); err != nil {
		t.Fatal(err)
	}
	if len(f.cellReqs) != 5 || f.cellReqs[3].Channel != 4378 || f.cellReqs[4].Channel != cell.OrdinaryChannel || f.cellReqs[4].Replaces != ext.IDHex() ||
		!strings.Contains(f.cellReqs[4].Purpose, "Update from meteoalarm-nl (Wind Severe)") {
		t.Fatalf("update: %+v", f.cellReqs[3:])
	}
	if _, on := f.cellOnAir[ext.IDHex()]; on {
		t.Fatal("the replaced broadcast is still on air")
	}
	// A Moderate real warning: no alert identifier, the ordinary channel at once.
	mod := alertDigest()
	mod.ID = idOf("m-1")
	if err := r.Handle(context.Background(), encode(t, mod, key), "hub"); err != nil {
		t.Fatal(err)
	}
	if len(f.cellReqs) != 6 || f.cellReqs[5].Channel != cell.OrdinaryChannel {
		t.Fatalf("moderate: %+v", f.cellReqs[5:])
	}
	// A cleared identifier goes out as it is.
	delete(f.cellRefuse, 4375)
	sev := alertDigest()
	sev.ID, sev.Severity, sev.Certainty, sev.Urgency = idOf("s-1"), 3, 4, 3
	if err := r.Handle(context.Background(), encode(t, sev, key), "hub"); err != nil {
		t.Fatal(err)
	}
	if len(f.cellReqs) != 7 || f.cellReqs[6].Channel != 4375 {
		t.Fatalf("severe: %+v", f.cellReqs[6:])
	}

	// The radio down: nothing on the cell, the mesh and APRS still go; a
	// Cancel still takes the update off the air.
	f.cellUp = false
	quiet := alertDigest()
	quiet.ID = idOf("q-1")
	if err := r.Handle(context.Background(), encode(t, quiet, key), "hub"); err != nil || len(f.cellReqs) != 7 {
		t.Fatalf("radio down: %v, %d requests", err, len(f.cellReqs))
	}
	row, _ = db.GetWarning(quiet.IDHex())
	if row.Bearers != `["mesh-alert","mesh-text","aprs"]` {
		t.Fatalf("bearers with the radio down %s", row.Bearers)
	}
	cancel := &Digest{MsgType: TypeCancel, Source: SourceMeteoAlarmNL, ID: idOf("c-1"), Ref: upd.ID, Sent: upd.Sent.Add(time.Hour), Event: 1}
	if err := r.Handle(context.Background(), encode(t, cancel, key), "hub"); err != nil {
		t.Fatal(err)
	}
	if len(f.cellCancels) != 1 || f.cellCancels[0] != upd.IDHex() || len(f.cellReqs) != 7 {
		t.Fatalf("cancel: %v", f.cellCancels)
	}
	row, _ = db.GetWarning(cancel.IDHex())
	if row.Bearers != `["mesh-alert","mesh-text","aprs","cell-cancel"]` {
		t.Fatalf("cancel bearers %s", row.Bearers)
	}
	// A Cancel of something never on the cell is no cell bearer.
	orphan := &Digest{MsgType: TypeCancel, Source: SourceDWD, ID: idOf("c-2"), Ref: idOf("never"), Sent: upd.Sent, Event: 2}
	if err := r.Handle(context.Background(), encode(t, orphan, key), "hub"); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetWarning(orphan.IDHex())
	if row.Bearers != `["mesh-alert","mesh-text","aprs"]` {
		t.Fatalf("orphan cancel bearers %s", row.Bearers)
	}
}

// The channel table and the repetition count.
func TestCellChannelTable(t *testing.T) {
	cases := []struct {
		sev, cert, urg int
		test           bool
		want           int
	}{
		{4, 4, 3, true, 4380}, {2, 4, 3, true, 4380},
		{4, 4, 3, false, 4371}, {4, 3, 3, false, 4372}, {4, 4, 2, false, 4373}, {4, 3, 2, false, 4374},
		{3, 4, 3, false, 4375}, {3, 3, 3, false, 4376}, {3, 4, 2, false, 4377}, {3, 3, 2, false, 4378},
		{4, 2, 1, false, 4374}, {3, 0, 4, false, 4378},
		{2, 4, 3, false, 50}, {1, 4, 3, false, 50}, {0, 0, 0, false, 50},
	}
	for _, c := range cases {
		d := &Digest{Severity: c.sev, Certainty: c.cert, Urgency: c.urg, Test: c.test}
		if got := CellChannel(d); got != c.want {
			t.Errorf("severity %d certainty %d urgency %d test %v: %d, want %d", c.sev, c.cert, c.urg, c.test, got, c.want)
		}
	}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	if n := CellBroadcasts(&Digest{}, now); n != CellDefaultBroadcasts {
		t.Fatalf("no expiry: %d", n)
	}
	if n := CellBroadcasts(&Digest{Expires: now.Add(2 * time.Hour)}, now); n != 119 {
		t.Fatalf("two hours: %d", n)
	}
	if n := CellBroadcasts(&Digest{Expires: now.Add(time.Minute)}, now); n != CellMinBroadcasts {
		t.Fatalf("a minute: %d", n)
	}
	if n := CellBroadcasts(&Digest{Expires: now.Add(72 * time.Hour)}, now); n != CellMaxBroadcasts {
		t.Fatalf("three days: %d", n)
	}
}

func destHex(i int) string { return fmt.Sprintf("%032x", i) }

// The LXMF leg (MESHSAT-1734): the mesh text goes as one LXMF message to
// each peer heard in the last day on a free link, the most recently heard
// first, under a key of the digest and the peer, so a redelivered digest
// queues nothing twice; an old peer, one never heard and one behind a paid
// link get nothing; an Update and a Cancel go too; no peer, no bearer.
func TestTheLXMFLeg(t *testing.T) {
	r, f, db := relayRig(t)
	key := testKey(t)
	now := r.now()
	f.peers = []LXMFPeer{
		{Dest: destHex(1), LastSeen: now.Add(-30 * time.Minute)},
		{Dest: destHex(2), LastSeen: now.Add(-48 * time.Hour)},        // too old
		{Dest: destHex(3), LastSeen: now.Add(-time.Hour), Paid: true}, // over the satellite
		{Dest: destHex(4)}, // never heard
		{Dest: destHex(5), LastSeen: now.Add(-15 * time.Minute)},             // the most recent
		{Dest: destHex(1), LastSeen: now.Add(-20 * time.Minute)},             // the first again
		{Dest: destHex(6), LastSeen: now.Add(-LXMFPeerWindow + time.Minute)}, // just inside the window
	}
	first := alertDigest()
	if err := r.Handle(context.Background(), encode(t, first, key), "hub"); err != nil {
		t.Fatal(err)
	}
	if len(f.lxmf) != 3 || f.lxmf[0].addressee != destHex(5) || f.lxmf[1].addressee != destHex(1) || f.lxmf[2].addressee != destHex(6) {
		t.Fatalf("lxmf sends: %+v", f.lxmf)
	}
	idHex := first.IDHex()
	for _, s := range f.lxmf {
		if s.key != "warn:"+idHex+":alert:lxmf:"+s.addressee || s.text != f.alerts[0].text {
			t.Fatalf("lxmf send: %+v, mesh text %q", s, f.alerts[0].text)
		}
	}
	row, _ := db.GetWarning(idHex)
	if row.Bearers != `["mesh-alert","mesh-text","aprs","lxmf"]` {
		t.Fatalf("bearers %s", row.Bearers)
	}
	// The same digest again: nothing more to LXMF either.
	if err := r.Handle(context.Background(), encode(t, first, key), "iridium_0"); !errors.Is(err, ErrSeen) || len(f.lxmf) != 3 {
		t.Fatalf("redelivery: %v, %d lxmf sends", err, len(f.lxmf))
	}
	// An Update: the new text to the same peers under the new id.
	upd := alertDigest()
	upd.MsgType, upd.ID, upd.Ref, upd.Severity, upd.Text = TypeUpdate, idOf("nl-2"), first.ID, 3, "Zware windstoten tot 110 km/u"
	if err := r.Handle(context.Background(), encode(t, upd, key), "hub"); err != nil {
		t.Fatal(err)
	}
	if len(f.lxmf) != 6 || !strings.HasPrefix(f.lxmf[3].text, "UPDATE: WARNING Wind Severe") || f.lxmf[3].key != "warn:"+upd.IDHex()+":update:lxmf:"+destHex(5) {
		t.Fatalf("update: %+v", f.lxmf[3:])
	}
	// A Cancel goes too: the peers have the warning.
	cancel := &Digest{MsgType: TypeCancel, Source: SourceMeteoAlarmNL, ID: idOf("nl-3"), Ref: upd.ID, Sent: upd.Sent.Add(time.Hour), Event: 1}
	if err := r.Handle(context.Background(), encode(t, cancel, key), "hub"); err != nil {
		t.Fatal(err)
	}
	if len(f.lxmf) != 9 || !strings.HasPrefix(f.lxmf[6].text, "CANCELLED: Wind\n") {
		t.Fatalf("cancel: %+v", f.lxmf[6:])
	}
	// No peer heard recently: no send, no bearer; the mesh still goes.
	f.peers = []LXMFPeer{{Dest: destHex(2), LastSeen: now.Add(-48 * time.Hour)}}
	quiet := alertDigest()
	quiet.ID = idOf("q-1")
	if err := r.Handle(context.Background(), encode(t, quiet, key), "hub"); err != nil || len(f.lxmf) != 9 {
		t.Fatalf("no peers: %v, %d sends", err, len(f.lxmf))
	}
	row, _ = db.GetWarning(quiet.IDHex())
	if row.Bearers != `["mesh-alert","mesh-text","aprs"]` {
		t.Fatalf("bearers without a peer %s", row.Bearers)
	}
	// The ledger refusing every send: no bearer either.
	f.peers, f.lxmfFail = []LXMFPeer{{Dest: destHex(7), LastSeen: now}}, errors.New("ledger closed")
	failed := alertDigest()
	failed.ID = idOf("f-1")
	if err := r.Handle(context.Background(), encode(t, failed, key), "hub"); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetWarning(failed.IDHex())
	if row.Bearers != `["mesh-alert","mesh-text","aprs"]` {
		t.Fatalf("bearers with the ledger refusing %s", row.Bearers)
	}
	// The cap: sixty recent peers, fifty sends, the most recent first.
	f.lxmfFail, f.peers = nil, nil
	for i := 0; i < 60; i++ {
		f.peers = append(f.peers, LXMFPeer{Dest: destHex(100 + i), LastSeen: now.Add(-time.Duration(i) * time.Minute)})
	}
	many := alertDigest()
	many.ID = idOf("m-1")
	before := len(f.lxmf)
	if err := r.Handle(context.Background(), encode(t, many, key), "hub"); err != nil {
		t.Fatal(err)
	}
	if got := len(f.lxmf) - before; got != LXMFMaxPeers || f.lxmf[before].addressee != destHex(100) || f.lxmf[len(f.lxmf)-1].addressee != destHex(149) {
		t.Fatalf("cap: %d sends, first %s last %s", got, f.lxmf[before].addressee, f.lxmf[len(f.lxmf)-1].addressee)
	}
}

// Every kept digest is one event on the Bridge's event stream: type
// "warning", the headline as the message, the row with its label as the
// data; a redelivery is none; an expired arrival says so.
func TestAKeptDigestIsOneEventOnTheStream(t *testing.T) {
	r, f, _ := relayRig(t)
	key := testKey(t)
	first := alertDigest()
	wire := encode(t, first, key)
	if err := r.Handle(context.Background(), wire, "hub"); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 1 {
		t.Fatalf("events: %+v", f.events)
	}
	ev := f.events[0]
	if ev.Type != "warning" || !strings.HasPrefix(ev.Message, "WARNING Wind Moderate until 10:11 UTC: Kans op windstoten") || ev.Time != "2026-10-28T12:00:00Z" {
		t.Fatalf("event: %+v", ev)
	}
	var data Event
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.ID != first.IDHex() || data.MsgType != "Alert" || data.Test || data.Cancelled || data.Expired || data.Source != "meteoalarm-nl" ||
		data.Event != "Wind" || data.Severity != "Moderate" || data.Text != "Kans op windstoten tot 90 km/u" || data.Via != "hub" ||
		data.Bearers != `["mesh-alert","mesh-text","aprs"]` || data.Label != "Relay of KNMI via MeteoAlarm warning, issued 28 Oct 10:11 UTC via MeshSat, not an official NL-Alert" ||
		!data.Expires.Equal(first.Expires) {
		t.Fatalf("data: %+v", data)
	}
	// The raw JSON carries the row's fields flat, as GET /api/warnings does.
	if !strings.Contains(string(ev.Data), `"msg_type":"Alert"`) || !strings.Contains(string(ev.Data), `"label":"Relay of`) || strings.Contains(string(ev.Data), `"Warning"`) {
		t.Fatalf("data json: %s", ev.Data)
	}
	// A redelivery: no event.
	_ = r.Handle(context.Background(), wire, "iridium_0")
	if len(f.events) != 1 {
		t.Fatal("a seen digest made an event")
	}
	// A Cancel: cancelled, type Cancel.
	cancel := &Digest{MsgType: TypeCancel, Source: SourceMeteoAlarmNL, ID: idOf("c-1"), Ref: first.ID, Sent: first.Sent.Add(time.Hour), Event: 1}
	if err := r.Handle(context.Background(), encode(t, cancel, key), "hub"); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 2 || !strings.HasPrefix(f.events[1].Message, "CANCELLED: Wind") {
		t.Fatalf("cancel event: %+v", f.events)
	}
	data = Event{}
	_ = json.Unmarshal(f.events[1].Data, &data)
	if data.MsgType != "Cancel" || !data.Cancelled || data.Ref != first.IDHex() {
		t.Fatalf("cancel data: %+v", data)
	}
	// An expired arrival: kept, relayed nowhere, and the event says so.
	expired := alertDigest()
	expired.ID, expired.Expires = idOf("old"), time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC)
	_ = r.Handle(context.Background(), encode(t, expired, key), "hub")
	if len(f.events) != 3 {
		t.Fatalf("expired event: %+v", f.events)
	}
	data = Event{}
	_ = json.Unmarshal(f.events[2].Data, &data)
	if !data.Expired || data.Bearers != "[]" || data.ID != expired.IDHex() {
		t.Fatalf("expired data: %+v", data)
	}
	// A test digest is marked.
	test := alertDigest()
	test.Test, test.Source, test.ID = true, SourceTest, idOf("t-1")
	_ = r.Handle(context.Background(), encode(t, test, key), "hub")
	data = Event{}
	_ = json.Unmarshal(f.events[3].Data, &data)
	if !data.Test || data.Source != "test" || !strings.HasPrefix(f.events[3].Message, "TEST WARNING") || !strings.Contains(data.Label, "MeshSat test") {
		t.Fatalf("test data: %+v / %s", data, f.events[3].Message)
	}
}

// InForce is what the Post portal's banner shows: kept, current, not
// expired, newest sent first; a warning with no expiry for a day.
func TestInForceIsWhatTheBannerShows(t *testing.T) {
	r, _, _ := relayRig(t)
	key := testKey(t)
	now := r.now()
	first := alertDigest() // sent 10:11, expires tomorrow
	handle := func(d *Digest) {
		t.Helper()
		if err := r.Handle(context.Background(), encode(t, d, key), "hub"); err != nil && !errors.Is(err, ErrExpired) {
			t.Fatal(err)
		}
	}
	handle(first)
	test := alertDigest()
	test.Test, test.Source, test.ID, test.Sent = true, SourceTest, idOf("t-1"), now.Add(-time.Hour)
	handle(test)
	expired := alertDigest()
	expired.ID, expired.Expires = idOf("old"), now.Add(-time.Minute)
	handle(expired)
	open := alertDigest() // no expiry, sent two hours ago
	open.ID, open.Expires, open.Sent, open.Event = idOf("open"), time.Time{}, now.Add(-2*time.Hour), 9
	handle(open)
	stale := alertDigest() // no expiry, sent thirty hours ago
	stale.ID, stale.Expires, stale.Sent = idOf("stale"), time.Time{}, now.Add(-30*time.Hour)
	handle(stale)

	ids := func() []string {
		t.Helper()
		list, err := r.InForce()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, w := range list {
			out = append(out, w.ID)
		}
		return out
	}
	if got := ids(); strings.Join(got, ",") != strings.Join([]string{test.IDHex(), first.IDHex(), open.IDHex()}, ",") {
		t.Fatalf("in force: %v", got)
	}
	// An Update replaces the first; a Cancel of the update takes both out.
	upd := alertDigest()
	upd.MsgType, upd.ID, upd.Ref, upd.Sent = TypeUpdate, idOf("nl-2"), first.ID, now.Add(-30*time.Minute)
	handle(upd)
	if got := ids(); strings.Join(got, ",") != strings.Join([]string{upd.IDHex(), test.IDHex(), open.IDHex()}, ",") {
		t.Fatalf("after the update: %v", got)
	}
	cancel := &Digest{MsgType: TypeCancel, Source: SourceMeteoAlarmNL, ID: idOf("nl-3"), Ref: upd.ID, Sent: now, Event: 1}
	handle(cancel)
	if got := ids(); strings.Join(got, ",") != strings.Join([]string{test.IDHex(), open.IDHex()}, ",") {
		t.Fatalf("after the cancel: %v", got)
	}
	// Time passes: the open one ages out, the test one expires.
	r.now = func() time.Time { return now.Add(23 * time.Hour) }
	if got := ids(); len(got) != 0 {
		t.Fatalf("a day on: %v", got)
	}
	// The label a kept row renders is the mesh's.
	if LabelFor("dwd", first.Sent) != "Relay of DWD warning, issued 28 Oct 10:11 UTC via MeshSat, not an official NL-Alert" ||
		LabelFor("meteoalarm-nl", first.Sent) != Label(first) || !strings.HasPrefix(LabelFor("nobody", first.Sent), "Relay of unknown source warning") {
		t.Fatalf("labels: %q", LabelFor("dwd", first.Sent))
	}
}
