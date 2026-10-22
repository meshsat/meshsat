package keystore

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The check value a key list gives is the one MeshSat Android shows for its
// satellite key (SatelliteChain.keyCheck): the vectors are shared with
// SatelliteChainTest. [MESHSAT-1623]
func TestKeyCheck_SharedWithTheApps(t *testing.T) {
	seq := make([]byte, 32)
	for i := range seq {
		seq[i] = byte(i)
	}
	if got := KeyCheck(seq); got != "630dcd29" {
		t.Fatalf("00..1f: %s", got)
	}
	if got := KeyCheck(bytes.Repeat([]byte{0x11}, 32)); got != "02d449a3" {
		t.Fatalf("11 x 32: %s", got)
	}
}

// ListKeys gives each key's check, and the check follows the key.
func TestListKeys_KeyCheck(t *testing.T) {
	ks, _ := newChatKeyStore(t)
	if _, err := ks.StoreKeyLabelled("iridium", "shared", bytes.Repeat([]byte{0x11}, aesKeyLen), ""); err != nil {
		t.Fatal(err)
	}
	keys, err := ks.ListKeys()
	if err != nil || len(keys) != 1 {
		t.Fatalf("list: %v %d", err, len(keys))
	}
	if k := keys[0]; k.ChannelType != "iridium" || k.Address != "shared" || k.KeyCheck != "02d449a3" {
		t.Fatalf("listed: %s:%s check %q", k.ChannelType, k.Address, k.KeyCheck)
	}
}

// The key list carries no run of the key's own bytes: key_preview, its first 4 bytes, is gone, and
// no 3-byte window of any stored key appears in the JSON, in hex either case or base64. [MESHSAT-1639]
func TestListKeys_NoRawKeyBytes(t *testing.T) {
	ks, _ := newChatKeyStore(t)
	keys := [][]byte{make([]byte, aesKeyLen), bytes.Repeat([]byte{0xA5}, aesKeyLen)}
	for i := range keys[0] {
		keys[0][i] = byte(0x40 + i)
	}
	for i, k := range keys {
		if _, err := ks.StoreKeyLabelled("iridium", fmt.Sprintf("k%d", i), k, ""); err != nil {
			t.Fatal(err)
		}
	}
	metas, err := ks.ListKeys()
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(metas)
	if bytes.Contains(blob, []byte("key_preview")) {
		t.Fatalf("key_preview is still in the list: %s", blob)
	}
	low := strings.ToLower(string(blob))
	for _, k := range keys {
		for i := 0; i+3 <= len(k); i++ {
			w := k[i : i+3]
			for _, enc := range []string{hex.EncodeToString(w), base64.StdEncoding.EncodeToString(w)} {
				if strings.Contains(low, strings.ToLower(enc)) || strings.Contains(string(blob), enc) {
					t.Fatalf("bytes %d..%d of a key (%s) are in the list: %s", i, i+3, enc, blob)
				}
			}
		}
	}
	for _, m := range metas {
		if len(m.KeyCheck) != 8 {
			t.Fatalf("%s: check %q", m.Address, m.KeyCheck)
		}
	}
}
