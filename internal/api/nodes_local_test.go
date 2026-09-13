package api

import (
	"encoding/json"
	"strings"
	"testing"

	"meshsat/internal/transport"
)

// The firmware version rides on the local radio's own entry, matched by
// node number: parallax's radio kept its old user id after it renumbered.
// [MESHSAT-850, MESHSAT-1102]
func TestMarkLocalRadio(t *testing.T) {
	views := annotateNodes([]transport.MeshNode{
		{Num: 0x402d9e7b, UserID: "!6bcc53b2"},
		{Num: 0x698690dd, UserID: "!698690dd"},
	}, nil)
	markLocalRadio(views, &transport.MeshStatus{NodeID: "!402d9e7b", FirmwareVersion: "2.6.10.9ce4455"})
	if views[0].FirmwareVersion != "2.6.10.9ce4455" {
		t.Fatalf("local radio firmware %q", views[0].FirmwareVersion)
	}
	if views[1].FirmwareVersion != "" {
		t.Fatalf("remote node got firmware %q", views[1].FirmwareVersion)
	}
	b, err := json.Marshal(views[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "firmware_version") {
		t.Fatalf("remote node JSON carries firmware_version: %s", b)
	}
	markLocalRadio(views, nil)
	markLocalRadio(views, &transport.MeshStatus{NodeID: "!402d9e7b"})
}
