package database

import (
	"strings"
	"testing"
)

// At start, a satellite delivery a stop left 'sending' is given up, "may
// have been sent", never sent again by itself: every session is billed and
// its outcome is unknown. A link counts as satellite by its channel type
// (whatever its name) or its name; every other link's rows are recovered for
// a retry, as before, and RecoverStaleDeliveries never re-queues a
// satellite row even when called alone. [MESHSAT-1391]
func TestEndStaleSatelliteSends(t *testing.T) {
	db := testDB(t)
	if err := db.InsertInterface(&Interface{ID: "sat_backup", ChannelType: "iridium", Label: "Satellite", Enabled: true, Config: "{}",
		IngressTransforms: "[]", EgressTransforms: "[]"}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, ch := range []string{"iridium_0", "iridium_imt_0", "sat_backup", "mqtt_0", "cellular_0"} {
		id, err := db.InsertDelivery(MessageDelivery{MsgRef: "stale-" + ch, Channel: ch, Status: "sending", Priority: 1,
			TextPreview: "under way", MaxRetries: 3, Visited: "[]", QoSLevel: 1})
		if err != nil {
			t.Fatal(err)
		}
		ids[ch] = id
	}

	// RecoverStaleDeliveries alone never re-queues a satellite row.
	n, err := db.RecoverStaleDeliveries()
	if err != nil || n != 2 {
		t.Fatalf("recovered %d, %v; want the 2 rows of other links", n, err)
	}
	ended, err := db.EndStaleSatelliteSends()
	if err != nil || ended != 3 {
		t.Fatalf("ended %d, %v; want the 3 satellite rows", ended, err)
	}
	for ch, id := range ids {
		d, err := db.GetDelivery(id)
		if err != nil {
			t.Fatal(err)
		}
		satellite := ch == "iridium_0" || ch == "iridium_imt_0" || ch == "sat_backup"
		switch {
		case satellite && (d.Status != "dead" || !strings.HasPrefix(d.LastError, UnconfirmedPrefix)):
			t.Errorf("%s: %q, %q; want dead, may have been sent", ch, d.Status, d.LastError)
		case !satellite && d.Status != "retry":
			t.Errorf("%s: %q; want retry", ch, d.Status)
		}
	}
	// A person's Retry sends it again.
	if err := db.RetryDelivery(ids["iridium_0"]); err != nil {
		t.Fatalf("a person's retry: %v", err)
	}
}

// What EndStaleSatelliteSends counts as satellite: the interface row's
// channel type decides whatever the link's name (an MQTT link a person
// named iridium_relay is recovered for a retry, as any MQTT row), and the
// name only for a row with no interface. The SOS's frame to the Hub
// (priority 0, hub_uplink), which no person's Retry sends again
// (ErrSOSFrameNotRetried), is re-queued as before on every kit, SBD or IMT:
// a duplicate SOS frame is the lesser harm. [MESHSAT-1391]
func TestEndStaleSatelliteSends_Scope(t *testing.T) {
	db := testDB(t)
	if err := db.InsertInterface(&Interface{ID: "iridium_relay", ChannelType: "mqtt", Label: "MQTT relay", Enabled: true, Config: "{}",
		IngressTransforms: "[]", EgressTransforms: "[]"}); err != nil {
		t.Fatal(err)
	}
	insert := func(ref, channel string, priority int, class string) int64 {
		t.Helper()
		id, err := db.InsertDelivery(MessageDelivery{MsgRef: ref, Channel: channel, Status: "sending", Priority: priority,
			TextPreview: "under way", MaxRetries: 3, Visited: "[]", QoSLevel: 1, Class: class})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	mqttNamedIridium := insert("mq", "iridium_relay", 1, "")
	sosSBD := insert("sos-sbd", "iridium_0", 0, DeliveryClassHubUplink)
	sosIMT := insert("sos-imt", "iridium_imt_0", 0, DeliveryClassHubUplink)
	uplinkFrame := insert("frame", "iridium_0", 1, DeliveryClassHubUplink) // a position frame: not the SOS
	noRow := insert("no-row", "iridium_9", 1, "")

	if _, err := db.EndStaleSatelliteSends(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecoverStaleDeliveries(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		id   int64
		dead bool
	}{
		{"an MQTT link named iridium_relay", mqttNamedIridium, false},
		{"the SOS frame by SBD", sosSBD, false},
		{"the SOS frame by IMT", sosIMT, false},
		{"another Hub frame by SBD", uplinkFrame, true},
		{"a satellite link with no interface row", noRow, true},
	} {
		d, err := db.GetDelivery(c.id)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case c.dead && (d.Status != "dead" || !strings.HasPrefix(d.LastError, UnconfirmedPrefix)):
			t.Errorf("%s: %q, %q; want dead, may have been sent", c.name, d.Status, d.LastError)
		case !c.dead && d.Status != "retry":
			t.Errorf("%s: %q, %q; want re-queued for a retry", c.name, d.Status, d.LastError)
		}
	}
}
