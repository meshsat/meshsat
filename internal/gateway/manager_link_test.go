package gateway

import (
	"context"
	"fmt"
	"testing"
	"time"

	"meshsat/internal/database"
)

func mustLink(t *testing.T, db *database.DB, id string) *database.Interface {
	t.Helper()
	l, err := db.GetInterface(id)
	if err != nil {
		t.Fatalf("link %s: %v", id, err)
	}
	return l
}

func noLink(t *testing.T, db *database.DB, id string) {
	t.Helper()
	if _, err := db.GetInterface(id); err == nil {
		t.Fatalf("link %s exists", id)
	}
}

// A gateway saved with no link row gets its <type>_0 link, labelled from the
// channel registry and switched as the gateway was saved; what exists is
// left alone. [MESHSAT-1421]
func TestConfigureCreatesMissingLink(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	m := NewManager(db, nil)
	t.Cleanup(m.Stop)

	// A fresh database holds only what its migrations seed, like the phone's.
	noLink(t, db, "aprs_0")
	noLink(t, db, "tak_0")

	// A refused config creates nothing.
	if err := m.Configure(ctx, "aprs", false, `{"callsign":"N0CALL","mode":"ax25"}`); err == nil {
		t.Fatal("unknown mode accepted")
	}
	noLink(t, db, "aprs_0")

	// APRS saved switched off: aprs_0 appears, switched off like the gateway.
	if err := m.Configure(ctx, "aprs", false, `{"callsign":"N0CALL","ssid":10,"external_direwolf":true}`); err != nil {
		t.Fatalf("configure aprs: %v", err)
	}
	l := mustLink(t, db, "aprs_0")
	if l.ChannelType != "aprs" || l.Label != "APRS (Direwolf)" || l.Enabled || l.Config != "{}" ||
		l.IngressTransforms != "[]" || l.EgressTransforms != "[]" {
		t.Fatalf("aprs_0: %+v", l)
	}

	// TAK, through the link creator main.go installs.
	var created []string
	m.SetLinkCreator(func(link database.Interface) error {
		created = append(created, link.ID)
		return db.InsertInterface(&link)
	})
	if err := m.Configure(ctx, "tak", false, `{"tak_host":"127.0.0.1"}`); err != nil {
		t.Fatalf("configure tak: %v", err)
	}
	if l := mustLink(t, db, "tak_0"); l.ChannelType != "tak" || l.Label != "TAK/CoT" || l.Enabled {
		t.Fatalf("tak_0: %+v", l)
	}
	if len(created) != 1 || created[0] != "tak_0" {
		t.Fatalf("link creator calls %v, want [tak_0]", created)
	}

	// A link that exists is left as it is.
	l.Label, l.Enabled = "APRS on the roof", true
	if err := db.UpdateInterface(l); err != nil {
		t.Fatal(err)
	}
	if err := m.Configure(ctx, "aprs", false, `{"callsign":"N0CALL","ssid":10,"external_direwolf":true}`); err != nil {
		t.Fatalf("configure aprs again: %v", err)
	}
	if l := mustLink(t, db, "aprs_0"); l.Label != "APRS on the roof" || !l.Enabled {
		t.Fatalf("aprs_0 changed: %+v", l)
	}

	// A type with a link under another id gets no second one.
	other := database.Interface{ID: "webhook_rx", ChannelType: "webhook", Label: "Webhook in", Enabled: true, Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"}
	if err := db.InsertInterface(&other); err != nil {
		t.Fatal(err)
	}
	if err := m.Configure(ctx, "webhook", false, `{"inbound_enabled":true}`); err != nil {
		t.Fatalf("configure webhook: %v", err)
	}
	noLink(t, db, "webhook_0")
	if len(created) != 1 {
		t.Fatalf("link creator calls %v", created)
	}

	// A gateway type the channel registry does not know gets no link.
	if _, known := channelLabel("hf"); known {
		t.Fatal("hf has a channel label")
	}

	// Saved switched on: the link is on and bound to the running gateway at
	// once. Mode is starts without a radio, against the loopback fake.
	setAPRSISKnobs(t, 200*time.Millisecond)
	f := newFakeAPRSIS(t, logrespVerified)
	db2 := newTestDB(t)
	m2 := NewManager(db2, nil)
	t.Cleanup(m2.Stop)
	cfg := fmt.Sprintf(`{"mode":"is","callsign":"N0CALL","ssid":10,"aprs_is_server":%q}`, f.addr())
	if err := m2.Configure(ctx, "aprs", true, cfg); err != nil {
		t.Fatalf("configure aprs on: %v", err)
	}
	if l := mustLink(t, db2, "aprs_0"); !l.Enabled || l.Label != "APRS (Direwolf)" {
		t.Fatalf("aprs_0 saved on: %+v", l)
	}
	if gw := m2.GatewayByInterfaceID("aprs_0"); gw == nil || gw.Type() != "aprs" {
		t.Fatalf("aprs_0 is not bound to the running gateway: %v", gw)
	}
}
