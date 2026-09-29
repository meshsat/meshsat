package hubreporter

import (
	"encoding/json"
	"strings"
	"testing"
)

type recordedKey struct {
	channelType, address, label string
	labelled                    bool
}

// plainImporter stores keys the old way; labelledImporter keeps a label too.
type plainImporter struct{ got []recordedKey }

func (p *plainImporter) StoreKey(channelType, address string, rawKey []byte) (int, error) {
	p.got = append(p.got, recordedKey{channelType: channelType, address: address})
	return len(p.got), nil
}

type labelledImporter struct{ plainImporter }

func (l *labelledImporter) StoreKeyLabelled(channelType, address string, rawKey []byte, label string) (int, error) {
	l.got = append(l.got, recordedKey{channelType: channelType, address: address, label: label, labelled: true})
	return len(l.got), nil
}

// A Hub rotation is labelled as MeshSat Android labels it, hub-rotated-v<n>
// (version 1 when the Hub gives none), when the store keeps labels.
func TestKeyRotate_LabelsTheHubsVersion(t *testing.T) {
	healthFn := func() BridgeHealth { return BridgeHealth{} }
	cmd := func(version int) Command {
		payload, _ := json.Marshal(map[string]interface{}{
			"channel_type": "sms", "address": "+31612345678", "key_hex": strings.Repeat("ab", 32), "version": version,
		})
		return Command{Cmd: "key_rotate", Payload: payload}
	}

	labelled := &labelledImporter{}
	ch := NewCommandHandler(nil, "test-bridge", healthFn)
	ch.SetKeyStore(labelled)
	for _, v := range []int{7, 0} {
		if _, err := ch.handleKeyRotate(cmd(v)); err != nil {
			t.Fatal(err)
		}
	}
	if len(labelled.got) != 2 || !labelled.got[0].labelled || labelled.got[0].label != "hub-rotated-v7" ||
		labelled.got[1].label != "hub-rotated-v1" || labelled.got[0].address != "+31612345678" {
		t.Fatalf("stored %+v", labelled.got)
	}

	plain := &plainImporter{}
	ch = NewCommandHandler(nil, "test-bridge", healthFn)
	ch.SetKeyStore(plain)
	if _, err := ch.handleKeyRotate(cmd(3)); err != nil {
		t.Fatal(err)
	}
	if len(plain.got) != 1 || plain.got[0].labelled {
		t.Fatalf("plain store: %+v", plain.got)
	}
}
