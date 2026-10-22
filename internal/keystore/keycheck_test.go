package keystore

import (
	"bytes"
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
