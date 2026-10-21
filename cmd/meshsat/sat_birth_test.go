package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/hubreporter"
)

// chainFake answers GetInterface with a chain or an error.
type chainFake struct {
	chain string
	err   error
}

func (f chainFake) GetInterface(id string) (*database.Interface, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &database.Interface{ID: id, EgressTransforms: f.chain}, nil
}

// R28-R2: the format comes from a chain that was read. A read that failed
// declares nothing, never plain; an empty chain that was read declares plain.
func TestSatFormatFromTheChainRead(t *testing.T) {
	for _, c := range []struct {
		name string
		db   chainFake
		want string
	}{
		{"encrypting chain", chainFake{chain: `[{"type":"smaz2g","params":{"dict":"meshtastic"}},{"type":"encrypt","params":{"key_ref":"iridium:shared"}}]`}, "sealed"},
		{"today's kit chain", chainFake{chain: `[{"type":"smaz2"},{"type":"encrypt","params":{"key_ref":"iridium:shared"}},{"type":"base64"}]`}, "sealed"},
		{"chain without encryption", chainFake{chain: `[{"type":"smaz2","params":{"dict":"meshtastic"}}]`}, "plain"},
		{"empty chain, read", chainFake{chain: ``}, "plain"},
		{"empty list, read", chainFake{chain: `[]`}, "plain"},
		{"unreadable chain JSON", chainFake{chain: `not a chain`}, "sealed"},
		{"read failed", chainFake{err: errors.New("database is locked")}, ""},
		{"interface row missing", chainFake{err: sql.ErrNoRows}, ""},
	} {
		if got := satFormat(c.db, "iridium_imt_0"); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The birth entry carries the modem and declares the format; with no format
// (a failed read) it declares nothing at all, so the Hub keeps the last one.
func TestSatBirthEntryDeclaresTheFormat(t *testing.T) {
	base := hubreporter.InterfaceInfo{Name: "iridium_imt", Type: "iridium_imt", Status: "online"}
	for _, c := range []struct {
		format string
		json   []string
		absent []string
	}{
		{"sealed", []string{`"egress_format":"sealed"`, `"egress_sealed":true`}, nil},
		{"plain", []string{`"egress_format":"plain"`}, []string{`"egress_sealed"`}},
		{"", nil, []string{`"egress_format"`, `"egress_sealed"`}},
	} {
		got := satBirthEntry(base, "300434060000001", c.format)
		if got.IMEI != "300434060000001" || got.Name != "iridium_imt" || got.Status != "online" {
			t.Fatalf("format %q: %+v", c.format, got)
		}
		b, _ := json.Marshal(got)
		for _, want := range c.json {
			if !strings.Contains(string(b), want) {
				t.Fatalf("format %q: %s lacks %s", c.format, b, want)
			}
		}
		for _, not := range c.absent {
			if strings.Contains(string(b), not) {
				t.Fatalf("format %q: %s has %s", c.format, b, not)
			}
		}
	}
}

// A read that recovers changes the watcher's key, so the held-back
// declaration is published without anyone republishing.
func TestSatModemKeyChangesWhenTheReadRecovers(t *testing.T) {
	failed := satModemKey("iridium_imt", "300434060000001", satFormat(chainFake{err: errors.New("database is locked")}, "iridium_imt_0"))
	read := satModemKey("iridium_imt", "300434060000001", satFormat(chainFake{chain: `[{"type":"encrypt","params":{"key_ref":"iridium:shared"}}]`}, "iridium_imt_0"))
	if failed == read {
		t.Fatalf("the key does not change when the chain is read again: %q", failed)
	}
}

// The birth is published again whenever the satellite modems change, the
// IMEI arriving after the connect included, and not on every tick.
func TestWatchSatModemsRepublishesOnChange(t *testing.T) {
	seq := []string{"", "", "iridium_imt=", "iridium_imt=300434060000001", "iridium_imt=300434060000001", "", "iridium_imt=300434060000001"}
	var mu sync.Mutex
	i, published := 0, 0
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		watchSatModems(ctx, time.Millisecond, func() string {
			mu.Lock()
			defer mu.Unlock()
			if i == len(seq) {
				cancel()
				return seq[len(seq)-1]
			}
			i++
			return seq[i-1]
		}, func() { mu.Lock(); published++; mu.Unlock() })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not stop")
	}
	// "" -> "iridium_imt=" -> the IMEI -> "" -> the IMEI: four changes.
	if published != 4 {
		t.Fatalf("published %d times, want 4", published)
	}
}

// The stored birth timestamp survives a restart (a new store on the same
// database). No row is no stamp and no error; a value that is not a timestamp
// and a database that cannot be read are errors, never the zero time; a save
// the database refuses is an error. [MESHSAT-1584]
func TestBirthStampsSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.db")
	db, err := database.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := (birthStamps{db}).LastBirthStamp(); err != nil || !got.IsZero() {
		t.Fatalf("nothing stored: %v %v", got, err)
	}
	at := time.Date(2026, 10, 21, 18, 0, 0, 123456789, time.UTC)
	if err := (birthStamps{db}).SaveBirthStamp(at); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = database.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := (birthStamps{db}).LastBirthStamp(); err != nil || !got.Equal(at) {
		t.Fatalf("after a restart: %v %v, want %v", got, err, at)
	}
	if err := db.SetSystemConfig(birthStampKey, "not a time"); err != nil {
		t.Fatal(err)
	}
	if got, err := (birthStamps{db}).LastBirthStamp(); err == nil || !got.IsZero() {
		t.Fatalf("unreadable value: %v %v, want an error", got, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// A database that cannot be read: an error, not "no stamp yet".
	if got, err := (birthStamps{db}).LastBirthStamp(); err == nil || errors.Is(err, sql.ErrNoRows) || !got.IsZero() {
		t.Fatalf("read failure: %v %v, want an error", got, err)
	}
	if err := (birthStamps{db}).SaveBirthStamp(at); err == nil {
		t.Fatal("a save the database refused reported success")
	}
}
