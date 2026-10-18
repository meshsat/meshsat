package hembfixtures

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The copy is the release the lock names: SHA256SUMS hashes to the lock's
// digest, every file matches SHA256SUMS, and nothing else is in the folder.
// Taking a new release means changing the lock. [MESHSAT-1584]
func TestTheFixtureCopyIsTheLockedRelease(t *testing.T) {
	lock, err := os.ReadFile("fixtures.lock")
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(lock)), "\n") {
		k, v, _ := strings.Cut(line, " ")
		fields[k] = v
	}
	dir := filepath.Join("testdata", "v1")
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	if got := sha256.Sum256(sums); hex.EncodeToString(got[:]) != fields["sha256"] {
		t.Fatalf("SHA256SUMS is %x; fixtures.lock names %s", got, fields["sha256"])
	}
	listed := map[string]bool{"SHA256SUMS": true}
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		want, name, ok := strings.Cut(sc.Text(), "  ")
		if !ok || strings.ContainsAny(name, "/\\") {
			t.Fatalf("bad SHA256SUMS line %q", sc.Text())
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := sha256.Sum256(data); hex.EncodeToString(got[:]) != want {
			t.Errorf("%s differs from the release", name)
		}
		listed[name] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !listed[e.Name()] {
			t.Errorf("%s is not part of the release", e.Name())
		}
	}
	if r, _ := os.ReadFile(filepath.Join(dir, "RELEASE")); strings.TrimSpace(string(r)) != fields["tag"] {
		t.Errorf("RELEASE is %q; fixtures.lock names %q", r, fields["tag"])
	}
}
