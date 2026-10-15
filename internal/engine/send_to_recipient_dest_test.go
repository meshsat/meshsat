package engine

import (
	"context"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/transport"
)

// Every row a contact send queues carries that contact's address for the
// route, in the form its gateway reads; SMS goes to the cellular gateway
// (sms_0 is Reticulum's packet interface and has no delivery gateway) and a
// Reticulum contact is an LXMF message on lxmf_0. [MESHSAT-1581]
func TestSendToRecipient_EachRowCarriesTheContactAddress(t *testing.T) {
	d, store := testDispatcher(t, directory.KindSMS, directory.KindMeshtastic, directory.KindAPRS)
	ctx := context.Background()

	c := &directory.Contact{DisplayName: "Bob"}
	if err := store.CreateContact(ctx, c); err != nil {
		t.Fatal(err)
	}
	for _, a := range []directory.Address{
		{ContactID: c.ID, Kind: directory.KindSMS, Value: " +31611111111 ", PrimaryRank: 0},
		{ContactID: c.ID, Kind: directory.KindMeshtastic, Value: "!ABCD1234", PrimaryRank: 1},
		{ContactID: c.ID, Kind: directory.KindAPRS, Value: "pd0abc-7", PrimaryRank: 2},
		{ContactID: c.ID, Kind: directory.KindReticulum, Value: "0d2e456bc1a14db717c3f070765650fa", PrimaryRank: 3},
	} {
		a := a
		if err := store.AddAddress(ctx, &a); err != nil {
			t.Fatal(err)
		}
	}

	res, err := d.SendToRecipient(ctx, RecipientRef{ContactID: c.ID}, []byte("meet at the gate"),
		SendOptions{Strategy: directory.StrategyAllBearers})
	if err != nil {
		t.Fatalf("SendToRecipient: %v (errors %v)", err, res.Errors)
	}
	want := map[directory.Kind]struct{ channel, dest, class string }{
		directory.KindSMS:        {"cellular_0", "+31611111111", database.DeliveryClassMessage},
		directory.KindMeshtastic: {"mesh_0", "!abcd1234", database.DeliveryClassMessage},
		directory.KindAPRS:       {"aprs_0", "PD0ABC-7", database.DeliveryClassMessage},
		directory.KindReticulum:  {"lxmf_0", "0d2e456bc1a14db717c3f070765650fa", database.DeliveryClassLXMF},
	}
	for kind, w := range want {
		ids := res.DeliveryIDs[kind]
		if len(ids) != 1 {
			t.Errorf("%s: %d rows, want 1 (errors %v)", kind, len(ids), res.Errors)
			continue
		}
		row, err := d.db.GetDelivery(ids[0])
		if err != nil {
			t.Fatal(err)
		}
		if row.Channel != w.channel || row.Destination != w.dest || row.Class != w.class {
			t.Errorf("%s: channel %q dest %q class %q; want %q %q %q", kind, row.Channel, row.Destination, row.Class, w.channel, w.dest, w.class)
		}
	}
}

// An address that cannot be one for its route is refused and recorded,
// never queued to the gateway's default.
func TestSendToRecipient_RefusesAnAddressItCannotRead(t *testing.T) {
	d, store := testDispatcher(t, directory.KindMeshtastic, directory.KindSMS)
	ctx := context.Background()

	c := &directory.Contact{DisplayName: "Carol"}
	if err := store.CreateContact(ctx, c); err != nil {
		t.Fatal(err)
	}
	for _, a := range []directory.Address{
		{ContactID: c.ID, Kind: directory.KindMeshtastic, Value: "12345", PrimaryRank: 0},
		{ContactID: c.ID, Kind: directory.KindReticulum, Value: "not-a-hash", PrimaryRank: 1},
		{ContactID: c.ID, Kind: directory.KindSMS, Value: "+31622222222", PrimaryRank: 2},
	} {
		a := a
		if err := store.AddAddress(ctx, &a); err != nil {
			t.Fatal(err)
		}
	}
	res, err := d.SendToRecipient(ctx, RecipientRef{ContactID: c.ID}, []byte("hi"),
		SendOptions{Strategy: directory.StrategyAllBearers})
	if err != nil {
		t.Fatalf("SendToRecipient: %v", err)
	}
	for _, kind := range []directory.Kind{directory.KindMeshtastic, directory.KindReticulum} {
		if res.Errors[kind] == nil || len(res.DeliveryIDs[kind]) != 0 {
			t.Errorf("%s: error %v, rows %v; want refused, nothing queued", kind, res.Errors[kind], res.DeliveryIDs[kind])
		}
	}
	if len(res.DeliveryIDs[directory.KindSMS]) != 1 {
		t.Errorf("the readable SMS address should still be queued: %v", res.DeliveryIDs)
	}
}

// Through the delivery worker: the cellular gateway is handed the contact's
// number, not its configured default list, and the mesh sends to the
// contact's node instead of broadcasting.
func TestSendToRecipient_WorkerSendsToTheContact(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "cellular_0", "cellular", true)
	h.addInterface(t, "mesh_0", "mesh", true)
	h.setOnline("cellular_0")
	h.setOnline("mesh_0")
	gw := h.addGateway("cellular_0", "cellular")
	store := directory.NewSQLStore(h.db.DB)
	h.dispatch.SetRecipientResolver(store)
	ctx := context.Background()

	c := &directory.Contact{DisplayName: "Dave"}
	if err := store.CreateContact(ctx, c); err != nil {
		t.Fatal(err)
	}
	for _, a := range []directory.Address{
		{ContactID: c.ID, Kind: directory.KindSMS, Value: "+31633333333", PrimaryRank: 0},
		{ContactID: c.ID, Kind: directory.KindMeshtastic, Value: "!0badf00d", PrimaryRank: 1},
	} {
		a := a
		if err := store.AddAddress(ctx, &a); err != nil {
			t.Fatal(err)
		}
	}
	res, err := h.dispatch.SendToRecipient(ctx, RecipientRef{ContactID: c.ID}, []byte("on my way"),
		SendOptions{Strategy: directory.StrategyAllBearers})
	if err != nil {
		t.Fatalf("SendToRecipient: %v (errors %v)", err, res.Errors)
	}

	sms, _ := h.db.GetDelivery(res.DeliveryIDs[directory.KindSMS][0])
	newTestWorker(h, "cellular_0", "cellular").deliver(ctx, *sms)
	msgs := gw.messages()
	if len(msgs) != 1 || len(msgs[0].SMSDestinations) != 1 || msgs[0].SMSDestinations[0] != "+31633333333" {
		t.Fatalf("cellular gateway got %+v; want one message to +31633333333", msgs)
	}

	mesh, _ := h.db.GetDelivery(res.DeliveryIDs[directory.KindMeshtastic][0])
	newTestWorker(h, "mesh_0", "mesh").deliver(ctx, *mesh)
	h.meshTx.mu.Lock()
	sent := append([]transport.SendRequest{}, h.meshTx.sent...)
	h.meshTx.mu.Unlock()
	if len(sent) != 1 || sent[0].To != "!0badf00d" {
		t.Fatalf("mesh sends %+v; want one to !0badf00d", sent)
	}
}
