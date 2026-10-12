package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"meshsat/internal/codec"
	"meshsat/internal/database"
	"meshsat/internal/rules"
)

// Access rules carry messages between LXMF and the satellite: a satellite
// text to a Sideband user, an LXMF message out over Iridium. [MESHSAT-1554]

const (
	lxmfPeerA = "0d2e456bc1a14db717c3f070765650fa"
	lxmfPeerB = "5db1c0718d4cb874cd8a48f43d412136"
)

func lxmfRuleDispatcher(t *testing.T, rule database.AccessRule) (*Dispatcher, *database.DB) {
	t.Helper()
	d, db := setupTestDispatcher(t)
	if _, err := db.InsertAccessRule(&rule); err != nil {
		t.Fatal(err)
	}
	ae := rules.NewAccessEvaluator(db)
	if err := ae.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}
	d.SetAccessEvaluator(ae)
	return d, db
}

func TestLXMFLinkSeeded(t *testing.T) {
	_, db := setupTestDispatcher(t)
	iface, err := db.GetInterface("lxmf_0")
	if err != nil {
		t.Fatalf("lxmf_0 not seeded: %v", err)
	}
	if iface.ChannelType != "lxmf" || !iface.Enabled {
		t.Fatalf("lxmf_0 = %+v, want channel type lxmf, enabled", iface)
	}
	if channelNeedsDevice("lxmf") {
		t.Fatal("lxmf_0 has no device: it must be online when on, like mqtt")
	}
}

func TestDispatchAccess_SatelliteTextToLXMF(t *testing.T) {
	opts, _ := json.Marshal(map[string]any{
		"lxmf_destinations": []string{lxmfPeerA, strings.ToUpper(lxmfPeerA), " " + lxmfPeerB + " ", "not-a-hash", "abcd"},
		"lxmf_title":        "via Iridium",
	})
	d, db := lxmfRuleDispatcher(t, database.AccessRule{
		InterfaceID: "iridium_imt_0", Direction: "ingress", Name: "Satellite to Sideband", Enabled: true,
		Priority: 1, Action: "forward", ForwardTo: "lxmf_0", Filters: "{}", ForwardOptions: string(opts), QoSLevel: 1,
	})

	msg := rules.RouteMessage{Text: "Hello from the park", From: "cloudloop"}
	if n := d.DispatchAccess("iridium_imt_0", msg, []byte(msg.Text)); n != 2 {
		t.Fatalf("want one delivery per distinct valid destination (2), got %d", n)
	}
	dl, err := db.GetPendingDeliveries("lxmf_0", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(dl) != 2 {
		t.Fatalf("want 2 queued lxmf rows, got %d", len(dl))
	}
	got := map[string]bool{}
	for _, del := range dl {
		if del.Class != database.DeliveryClassLXMF {
			t.Errorf("row %d: class %q, want %q", del.ID, del.Class, database.DeliveryClassLXMF)
		}
		var body struct{ Content, Title string }
		if err := json.Unmarshal(del.Payload, &body); err != nil {
			t.Fatalf("row %d: payload is not the LXMF body: %v", del.ID, err)
		}
		if body.Content != "Hello from the park" || body.Title != "via Iridium" {
			t.Errorf("row %d: body %+v", del.ID, body)
		}
		if del.TextPreview != "Hello from the park" {
			t.Errorf("row %d: preview %q", del.ID, del.TextPreview)
		}
		got[del.Destination] = true
	}
	if !got[lxmfPeerA] || !got[lxmfPeerB] {
		t.Fatalf("destinations %v, want %s and %s in lower case", got, lxmfPeerA, lxmfPeerB)
	}
}

func TestDispatchAccess_LXMFNeedsADestination(t *testing.T) {
	d, db := lxmfRuleDispatcher(t, database.AccessRule{
		InterfaceID: "iridium_imt_0", Direction: "ingress", Name: "no destination", Enabled: true,
		Priority: 1, Action: "forward", ForwardTo: "lxmf_0", Filters: "{}", ForwardOptions: "{}",
	})
	if n := d.DispatchAccess("iridium_imt_0", rules.RouteMessage{Text: "hi", From: "cloudloop"}, []byte("hi")); n != 0 {
		t.Fatalf("a rule without destinations queued %d rows", n)
	}
	if dl, _ := db.GetPendingDeliveries("lxmf_0", 10); len(dl) != 0 {
		t.Fatalf("want nothing queued, got %d", len(dl))
	}
}

func TestDispatchAccess_BrevityCodeReachesLXMFAsWords(t *testing.T) {
	opts, _ := json.Marshal(map[string]any{"lxmf_destinations": []string{lxmfPeerA}})
	d, db := lxmfRuleDispatcher(t, database.AccessRule{
		InterfaceID: "iridium_imt_0", Direction: "ingress", Name: "codes", Enabled: true,
		Priority: 1, Action: "forward", ForwardTo: "lxmf_0", Filters: "{}", ForwardOptions: string(opts),
	})
	code := string(codec.EncodeCanned(1))
	if n := d.DispatchAccess("iridium_imt_0", rules.RouteMessage{Text: code, From: "cloudloop"}, []byte(code)); n != 1 {
		t.Fatalf("want 1 row, got %d", n)
	}
	dl, _ := db.GetPendingDeliveries("lxmf_0", 10)
	var body struct{ Content string }
	if len(dl) != 1 || json.Unmarshal(dl[0].Payload, &body) != nil || body.Content != "Copy." {
		t.Fatalf("want the words of code 1, got %q", payloadOf(dl))
	}
}

func TestDispatchAccess_LXMFToSatelliteOnlyFromNamedSenders(t *testing.T) {
	filters, _ := json.Marshal(map[string]string{"nodes": `["lxmf:` + lxmfPeerA + `"]`})
	d, db := lxmfRuleDispatcher(t, database.AccessRule{
		InterfaceID: "lxmf_0", Direction: "ingress", Name: "Sideband to satellite", Enabled: true,
		Priority: 1, Action: "forward", ForwardTo: "iridium_imt_0", Filters: string(filters),
	})

	from := func(hash, text string) int {
		return d.DispatchAccess("lxmf_0", rules.RouteMessage{Text: text, From: "lxmf:" + hash, PortNum: 1, Plain: true}, []byte(text))
	}
	if n := from(lxmfPeerA, "Reply from the trail"); n != 1 {
		t.Fatalf("the named sender: want 1 satellite row, got %d", n)
	}
	if n := from(lxmfPeerB, "a stranger"); n != 0 {
		t.Fatalf("another sender reached the satellite: %d rows", n)
	}
	dl, _ := db.GetPendingDeliveries("iridium_imt_0", 10)
	if len(dl) != 1 || string(dl[0].Payload) != "Reply from the trail" {
		t.Fatalf("want the text as the satellite payload, got %d rows %q", len(dl), payloadOf(dl))
	}
	if !strings.Contains(dl[0].Visited, "lxmf_0") {
		t.Fatalf("visited %q must carry lxmf_0 so it cannot come back", dl[0].Visited)
	}
}

func TestDispatchAccess_LXMFLoopGuard(t *testing.T) {
	opts, _ := json.Marshal(map[string]any{"lxmf_destinations": []string{lxmfPeerA}})
	d, db := lxmfRuleDispatcher(t, database.AccessRule{
		InterfaceID: "iridium_imt_0", Direction: "ingress", Name: "back", Enabled: true,
		Priority: 1, Action: "forward", ForwardTo: "lxmf_0", Filters: "{}", ForwardOptions: string(opts),
	})
	msg := rules.RouteMessage{Text: "echo", From: "cloudloop", Visited: []string{"lxmf_0"}}
	if n := d.DispatchAccess("iridium_imt_0", msg, []byte("echo")); n != 0 {
		t.Fatalf("a message that came from lxmf_0 went back to it: %d rows", n)
	}
	if dl, _ := db.GetPendingDeliveries("lxmf_0", 10); len(dl) != 0 {
		t.Fatalf("want nothing queued, got %d", len(dl))
	}
}
