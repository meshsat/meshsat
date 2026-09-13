package engine

import (
	"fmt"
	"strings"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// A relayed text sent over APRS that the peer never acknowledged moves to the
// next member of the rule's failover group at once. On 13 Sep 2026 8 of 120
// relayed texts were lost this way while the group [aprs_0, cellular_0] only
// ever fell back for a down or deaf member. [MESHSAT-1021]

var errNoAckFromPeer = fmt.Errorf("aprs: %w (id 7K2Q9, 3 attempts)", transport.ErrNoAck)

func noAckHarness(t *testing.T) (*e2eHarness, int64) {
	t.Helper()
	h := setupE2E(t)
	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "aprs_0", "aprs", true)
	h.addInterface(t, "cellular_0", "cellular", true)
	h.setOnline("aprs_0")
	h.setOnline("cellular_0")
	h.addGateway("aprs_0", "aprs")
	h.addGateway("cellular_0", "cellular")
	if err := h.db.InsertFailoverGroup(&database.FailoverGroup{ID: "peer_link", Label: "Peer kit", Mode: "priority"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []database.FailoverMember{
		{GroupID: "peer_link", InterfaceID: "aprs_0", Priority: 1},
		{GroupID: "peer_link", InterfaceID: "cellular_0", Priority: 2},
	} {
		if err := h.db.InsertFailoverMember(&m); err != nil {
			t.Fatal(err)
		}
	}
	ruleID, err := h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "ttc:aprs", Enabled: true, Priority: 10,
		Action: "forward", ForwardTo: "peer_link", Filters: "{}", QoSLevel: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, ruleID
}

func queueOnAPRS(t *testing.T, h *e2eHarness, ruleID int64, visited string) database.MessageDelivery {
	t.Helper()
	row := database.MessageDelivery{
		MsgRef: "booth-text", RuleID: &ruleID, Channel: "aprs_0", Status: "queued", Priority: 10,
		Payload: []byte("hello booth"), TextPreview: "hello booth", MaxRetries: 3, Visited: visited, QoSLevel: 1,
	}
	id, err := h.db.InsertDelivery(row)
	if err != nil {
		t.Fatal(err)
	}
	row.ID = id
	return row
}

func noAckWorker(h *e2eHarness) *DeliveryWorker {
	w := newTestWorker(h, "aprs_0", "aprs")
	w.failover = h.failover
	return w
}

func deliveriesOn(t *testing.T, h *e2eHarness, channel string) []database.MessageDelivery {
	t.Helper()
	rows, err := h.db.GetDeliveries(database.DeliveryFilter{Channel: channel})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestHandleFailure_NoAckMovesTheMessageToTheNextFailoverMember(t *testing.T) {
	h, ruleID := noAckHarness(t)
	row := queueOnAPRS(t, h, ruleID, `["mesh_0"]`)
	var events []transport.MeshEvent
	w := noAckWorker(h)
	w.emit = func(e transport.MeshEvent) { events = append(events, e) }

	w.handleFailure(row, errNoAckFromPeer)

	got, err := h.db.GetDelivery(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "dead" || !strings.Contains(got.LastError, "moved to cellular_0") {
		t.Fatalf("original: status %q last_error %q, want dead and moved to cellular_0", got.Status, got.LastError)
	}
	moved := deliveriesOn(t, h, "cellular_0")
	if len(moved) != 1 {
		t.Fatalf("%d deliveries on cellular_0, want 1", len(moved))
	}
	m := moved[0]
	if m.Status != "queued" || m.MsgRef != row.MsgRef || string(m.Payload) != "hello booth" ||
		m.RuleID == nil || *m.RuleID != ruleID || m.Retries != 0 || m.QoSLevel != 1 || m.Visited != row.Visited {
		t.Fatalf("moved delivery %+v", m)
	}
	if len(events) != 2 || events[0].Type != "delivery_dead" || events[1].Type != "delivery_queued" {
		t.Fatalf("events %+v, want delivery_dead then delivery_queued", events)
	}
}

func TestHandleFailure_NoAckWithNoOtherMemberUpRetries(t *testing.T) {
	h, ruleID := noAckHarness(t)
	h.setOffline("cellular_0")
	row := queueOnAPRS(t, h, ruleID, `["mesh_0"]`)

	noAckWorker(h).handleFailure(row, errNoAckFromPeer)

	got, err := h.db.GetDelivery(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "retry" || got.Retries != 1 {
		t.Fatalf("status %q retries %d, want an ordinary retry", got.Status, got.Retries)
	}
	if n := len(deliveriesOn(t, h, "cellular_0")); n != 0 {
		t.Fatalf("%d deliveries queued on an offline member", n)
	}
}

func TestHandleFailure_NoAckOnARuleWithoutAGroupRetries(t *testing.T) {
	h, _ := noAckHarness(t)
	direct, err := h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "straight to aprs", Enabled: true, Priority: 20,
		Action: "forward", ForwardTo: "aprs_0", Filters: "{}", QoSLevel: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	row := queueOnAPRS(t, h, direct, `["mesh_0"]`)

	noAckWorker(h).handleFailure(row, errNoAckFromPeer)

	if got, _ := h.db.GetDelivery(row.ID); got.Status != "retry" {
		t.Fatalf("status %q, want retry", got.Status)
	}
	if n := len(deliveriesOn(t, h, "cellular_0")); n != 0 {
		t.Fatalf("%d deliveries on cellular_0 for a rule with no group", n)
	}
}

func TestHandleFailure_NoAckNeverMovesToAVisitedInterface(t *testing.T) {
	h, ruleID := noAckHarness(t)
	row := queueOnAPRS(t, h, ruleID, `["mesh_0","cellular_0"]`)

	noAckWorker(h).handleFailure(row, errNoAckFromPeer)

	if got, _ := h.db.GetDelivery(row.ID); got.Status != "retry" {
		t.Fatalf("status %q, want retry", got.Status)
	}
	if n := len(deliveriesOn(t, h, "cellular_0")); n != 0 {
		t.Fatalf("message moved back to an interface it came through (%d rows)", n)
	}
}

func TestHandleFailure_OtherErrorsNeverFailOver(t *testing.T) {
	h, ruleID := noAckHarness(t)
	row := queueOnAPRS(t, h, ruleID, `["mesh_0"]`)

	noAckWorker(h).handleFailure(row, fmt.Errorf("aprs outbound queue full"))

	if got, _ := h.db.GetDelivery(row.ID); got.Status != "retry" {
		t.Fatalf("status %q, want retry", got.Status)
	}
	if n := len(deliveriesOn(t, h, "cellular_0")); n != 0 {
		t.Fatalf("%d deliveries moved for an error that is not a missing ack", n)
	}
}

func TestFailover_ResolveExcluding(t *testing.T) {
	h, _ := noAckHarness(t)
	if got := h.failover.ResolveExcluding("peer_link", "aprs_0"); got != "cellular_0" {
		t.Fatalf("excluding aprs_0: %q, want cellular_0", got)
	}
	if got := h.failover.ResolveExcluding("peer_link", "cellular_0"); got != "aprs_0" {
		t.Fatalf("excluding cellular_0: %q, want aprs_0", got)
	}
	h.setOffline("cellular_0")
	if got := h.failover.ResolveExcluding("peer_link", "aprs_0"); got != "" {
		t.Fatalf("offline member returned %q; a member that would only hold the message is no use", got)
	}
	if got := h.failover.ResolveExcluding("aprs_0", "cellular_0"); got != "" {
		t.Fatalf("a plain interface resolved to %q, want empty", got)
	}
}
