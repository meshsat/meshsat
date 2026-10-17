package engine

import (
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/gateway"
)

// The ZigBee route (owner ruling 17 Oct 2026: code now, live with a
// dongle) through the engine: a ZigBee contact plans a free route on
// zigbee_0 in adapter mode zigbee; written to the coordinator closes it
// accepted, not confirmed. [MESHSAT-1584]

func TestIntentZigBee_TakenByTheCoordinator(t *testing.T) {
	h := newIntentHarness(t)
	if err := h.db.InsertInterface(&database.Interface{ID: "zigbee_0", ChannelType: "zigbee", Label: "zigbee_0", Enabled: true,
		Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"}); err != nil {
		t.Fatal(err)
	}
	coord := &fakeAPRS{answer: func(gateway.IntentSend) gateway.IntentOutcome {
		return gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "coordinator"}
	}}
	prov := ifaceProv{"zigbee_0": coord}
	h.d.gwProv = prov
	h.workers["zigbee_0"] = &DeliveryWorker{db: h.db, channelID: "zigbee_0", gwProv: prov, intents: h.esc,
		desc: channel.ChannelDescriptor{ID: "zigbee_0", CanSend: true}}
	sub, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: h.contact(directory.Address{Kind: directory.KindZigBee, Value: "00124b0029a1b2c3"}),
		Body: []byte("by zigbee"), Strategy: directory.StrategyPrimaryOnly})
	if err != nil {
		t.Fatal(err)
	}
	p := sub.Intents[0].RoutePlan[0]
	if !p.Admitted || p.Paid || p.AdapterMode != modeZB || p.InterfaceID != "zigbee_0" || p.CompletionRule != "" {
		t.Fatalf("plan %+v", p)
	}
	h.work("zigbee_0")
	h.drain()
	h.clock.Advance(3 * time.Minute)
	h.esc.Heartbeat()
	h.drain()
	if s := h.snap(sub.Intents[0].ID); coord.count() != 1 || s.Intent.Outcome != database.OutcomeAcceptedUnconfirmed {
		t.Fatalf("%d sends, %s", coord.count(), s.Intent.Outcome)
	}
}
