package engine

// Upgrade from schema v62 (stage 1a as first shipped, 2a91688) with real
// data: testdata/v62-intents.sqlite.gz was written by that code (the
// generator is TestGenerateV62UpgradeFixture there; v62-intents.json names
// the intents and holds the synthetic test identity of the known router).
// Each test opens a copy with database.New, so the real migrations run.
// [MESHSAT-1584]

import (
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/lxmf"
	"meshsat/internal/reticulum"
	"meshsat/internal/rns"
	"meshsat/internal/rnsstack"
)

type v62Meta struct {
	Queued, Closed, Known        string
	RemoteEncPriv, RemoteSigPriv string
	RemoteDest                   string
}

func loadV62Meta(t *testing.T) v62Meta {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "v62-intents.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return v62Meta{Queued: m["queued"], Closed: m["closed"], Known: m["known"],
		RemoteEncPriv: m["remote_enc_priv"], RemoteSigPriv: m["remote_sig_priv"], RemoteDest: m["remote_dest"]}
}

// v62Snapshot is the fixture as a plain SQLite file: the one named by
// MESHSAT_REVIEW_V62_INPUT, else testdata's, decompressed.
func v62Snapshot(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("MESHSAT_REVIEW_V62_INPUT"); p != "" {
		return p
	}
	f, err := os.Open(filepath.Join("testdata", "v62-intents.sqlite.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "v62.sqlite")
	w, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(w, zr); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	return out
}

// reviewV62Upgrade is the closure review's helper: the fixture opened
// (migrated) by the current code, wired into a harness, restarted with
// MESHSAT_DELIVERY_INTENTS off.
func reviewV62Upgrade(t *testing.T) *intentHarness {
	t.Helper()
	data, err := os.ReadFile(v62Snapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "upgraded.db")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := database.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var version int
	if err := db.Get(&version, `SELECT MAX(version) FROM schema_version`); err != nil || version < 63 {
		t.Fatalf("upgrade did not reach v63: version=%d err=%v", version, err)
	}
	h := newIntentHarness(t)
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	h.db, h.d.db = db, db
	h.store = directory.NewSQLStore(db.DB)
	h.resolver = &countingResolver{RecipientResolver: h.store}
	h.d.SetRecipientResolver(h.resolver)
	h.start(false)
	return h
}

func knownRemote(t *testing.T, m v62Meta) *reticulum.Identity {
	t.Helper()
	enc, _ := hex.DecodeString(m.RemoteEncPriv)
	sig, _ := hex.DecodeString(m.RemoteSigPriv)
	id, err := reticulum.LoadIdentity(enc, sig)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// The v62 row to the known router, upgraded: over a free path it is
// delivered, under its v62 wire timestamp (its identity survives the
// correction), with no packet on the paid link.
func TestUpgradeV62QueuedLXMF_FreePathDeliversUnderItsOldTimestamp(t *testing.T) {
	m := loadV62Meta(t)
	h := reviewV62Upgrade(t)
	stored := rowsByRound(h.snap(m.Known))[1]
	var body rnsstack.LXMFBody
	if err := json.Unmarshal(stored.Payload, &body); err != nil || body.FreeOnly || body.Timestamp == 0 {
		t.Fatalf("fixture row: %s (free_only must be absent, ts set)", stored.Payload)
	}
	p := newLXMFPairWith(t, knownRemote(t, m), []string{"lora_rns"}, []string{"lora_rns"}, "lora_rns", "iridium_0")
	if p.remoteRouter.HashHex() != m.RemoteDest {
		t.Fatalf("setup: router %s, fixture %s", p.remoteRouter.HashHex(), m.RemoteDest)
	}
	var got *lxmf.Message
	p.onMessage = func(msg *lxmf.Message) { got = msg }
	h.useLXMF(p)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h.ctx = ctx
	h.work("lxmf_0")
	h.drain()
	if s := h.snap(m.Known); s.Intent.Outcome != database.OutcomeAcceptedDestinationNode {
		t.Fatalf("upgraded intent: %s/%s %+v", s.Intent.State, s.Intent.Outcome, s.Transmissions)
	}
	deadline := time.Now().Add(5 * time.Second)
	for p.received.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got == nil || got.Timestamp != body.Timestamp || string(got.Content) != body.Content {
		t.Fatalf("delivered %+v; want the v62 content under ts %v", got, body.Timestamp)
	}
	if n := p.localTX.count("iridium_0"); n != 0 {
		t.Fatalf("%d packets on the paid link", n)
	}
}

// The same row with its path known on a paid link (learned after the
// upgrade; no paid policy here, so the worker's own check cannot see it):
// nothing leaves on the paid link, the attempt waits for a free path.
func TestUpgradeV62QueuedLXMF_KnownPaidPathSendsNothing(t *testing.T) {
	m := loadV62Meta(t)
	h := reviewV62Upgrade(t)
	p := newLXMFPairWith(t, knownRemote(t, m), []string{"lora_rns"}, nil, "iridium_0")
	dest := p.remoteRouter.Hash()
	p.remote.SetPaidPolicy("iridium_0", &rns.PaidPolicy{Dests: [][rns.HashLen]byte{dest}})
	if err := p.remoteRouter.Announce(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for p.local.NextHopInterface(dest) != "iridium_0" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if p.local.NextHopInterface(dest) != "iridium_0" {
		t.Fatal("setup: no path on iridium_0")
	}
	h.useLXMF(p)
	before := p.localTX.count("iridium_0")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h.ctx = ctx
	h.work("lxmf_0")
	s := h.snap(m.Known)
	if n := p.localTX.count("iridium_0") - before; n != 0 {
		t.Fatalf("the upgraded v62 attempt sent %d packets on the paid link", n)
	}
	if len(s.Transmissions) != 1 || s.Transmissions[0].OutcomeClass != database.ClassNotSent || rowsByRound(s)[1].Status != "retry" {
		t.Fatalf("transmissions %+v, row %s", s.Transmissions, rowsByRound(s)[1].Status)
	}
}

// U-2 repeated: startup and heartbeats after the upgrade apply the stored
// ACK once (T14), stay stable, clear the review work, send nothing.
func TestUpgradeV62ClosedEvidenceAppliedOnceAndStable(t *testing.T) {
	m := loadV62Meta(t)
	h := reviewV62Upgrade(t)
	attempts := h.mesh.attempts
	for i := 0; i < 3; i++ {
		h.esc.Heartbeat()
		h.restart()
	}
	s := h.snap(m.Closed)
	var dirty, n int
	_ = h.db.Get(&dirty, `SELECT dirty FROM delivery_intents WHERE id = ?`, m.Closed)
	_ = h.db.Get(&n, `SELECT COUNT(*) FROM message_deliveries WHERE intent_id = ?`, m.Closed)
	if s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeDeliveredDevice || s.Intent.LastTransition != "T14" {
		t.Fatalf("%s/%s by %s", s.Intent.State, s.Intent.Outcome, s.Intent.LastTransition)
	}
	if dirty != 0 || n != 1 || h.mesh.attempts != attempts {
		t.Fatalf("dirty %d, %d rows, %d new attempts", dirty, n, h.mesh.attempts-attempts)
	}
	if v := s.Intent.Version; v != 3 {
		t.Fatalf("version %d; want 3 (v1, T11, one T14)", v)
	}
}

// A database that already ran v63 (every intent clean, the stored ACK not
// applied) is repaired by v64 when it is opened again.
func TestUpgradeV63DatabaseIsRepaired(t *testing.T) {
	m := loadV62Meta(t)
	path := filepath.Join(t.TempDir(), "v63.db")
	data, err := os.ReadFile(v62Snapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := database.New(path) // v62 -> current
	if err != nil {
		t.Fatal(err)
	}
	// Put it back where a v63 database stands: v63 applied, nothing dirty.
	if _, err := db.Exec(`UPDATE delivery_intents SET dirty = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE schema_version SET version = 63`); err != nil {
		t.Fatal(err)
	}
	// A v63 database has none of the later schema (v65 B5, v66 A-13): undo
	// it, or reopening would add it twice. [MESHSAT-1584]
	for _, q := range []string{
		`ALTER TABLE directory_dispatch_policy DROP COLUMN seeded`,
		`DROP INDEX idx_delivery_tx_proof`,
		`ALTER TABLE delivery_transmissions DROP COLUMN proof_hash`,
		`ALTER TABLE delivery_transmissions DROP COLUMN proof_key`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	db, err = database.New(path) // v63 -> current
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var dirty int
	if err := db.Get(&dirty, `SELECT dirty FROM delivery_intents WHERE id = ?`, m.Closed); err != nil || dirty != 1 {
		t.Fatalf("closed intent dirty=%d (%v) after reopening a v63 database; want 1", dirty, err)
	}
	ids, _ := db.IntentsToReview()
	found := false
	for _, id := range ids {
		found = found || id == m.Closed
	}
	if !found {
		t.Fatal("the closed intent is not up for review")
	}
}
