package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/hubreporter"
	"meshsat/internal/transport"
)

const sqliteTime = "2006-01-02 15:04:05"

// stopSOS ends the SOS burst a test started.
func stopSOS(s *Server) func() {
	return func() {
		s.sos.mu.Lock()
		if s.sos.cancelFn != nil {
			s.sos.cancelFn()
		}
		s.sos.mu.Unlock()
	}
}

func setRow(t *testing.T, s *Server, id int64, sets string, args ...interface{}) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE message_deliveries SET `+sets+` WHERE id = ?`, append(args, id)...); err != nil {
		t.Fatal(err)
	}
}

func rowStatus(t *testing.T, s *Server, id int64) (status, lastError string) {
	t.Helper()
	del, err := s.db.GetDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	return del.Status, del.LastError
}

func pendingOn(t *testing.T, s *Server, iface string, id int64) bool {
	t.Helper()
	rows, err := s.db.GetPendingDeliveries(iface, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == id {
			return true
		}
	}
	return false
}

// While the alarm test's satellite leg is open on its link (queued, waiting
// for a retry, held, or being sent), another call queues nothing and
// answers with it: its delivery id, msg_ref, its own status and the bridge
// id in its frame, with existing true, whatever position or bridge id the
// new call would have used. A client's retry after a lost answer costs no
// second credit, nor does a second press of the button. Once it has gone
// out, or its 30 minutes are over (reaped or not), the next call queues a
// new test. [MESHSAT-1430]
func TestSOSTest_ASecondCallAnswersWithTheOpenTest(t *testing.T) {
	s := sosSatServer(t, &apiSat{kind: "sbd", connected: true}, nil)
	first, del := postSatTest(t, s, sosTestSatBody)
	if first.Existing {
		t.Fatalf("the first test answered existing: %+v", first)
	}

	s.SetHubBridgeID("nllei01tesseract01")
	for _, status := range []string{"queued", "retry", "held", "sending"} {
		setRow(t, s, del.ID, `status = ?`, status)
		w := post(t, s, "/api/sos/test", `{"satellite":true,"latitude":48.8566,"longitude":2.3522}`)
		var resp sosSatAnswer
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", status, w.Code, w.Body.String())
		}
		if !resp.Existing || resp.DeliveryID != del.ID || resp.MsgRef != del.MsgRef || resp.Status != status ||
			resp.Interface != "iridium_0" || resp.BridgeID != "msa-flaneur" || resp.Message != sosTestSatellitePreview {
			t.Fatalf("%s: answered %+v, want the open test %d (%s)", status, resp, del.ID, del.MsgRef)
		}
		if n := deliveryCount(t, s); n != 1 {
			t.Fatalf("%s: %d deliveries, want the one test", status, n)
		}
	}

	// It went out: the next call is a new test, with the new bridge id.
	if err := s.db.SetDeliveryStatus(del.ID, "sent", "", ""); err != nil {
		t.Fatal(err)
	}
	second, del2 := postSatTest(t, s, sosTestSatBody)
	if second.Existing || second.DeliveryID == del.ID || second.BridgeID != "nllei01tesseract01" {
		t.Fatalf("after the first went out: %+v", second)
	}

	// Its 30 minutes are over and the reaper has not run yet: not open.
	setRow(t, s, del2.ID, `expires_at = ?`, time.Now().UTC().Add(-time.Minute).Format(sqliteTime))
	third, _ := postSatTest(t, s, sosTestSatBody)
	if third.Existing || third.DeliveryID == del2.ID {
		t.Fatalf("after the second expired: %+v", third)
	}
	if n := deliveryCount(t, s); n != 3 {
		t.Fatalf("%d deliveries, want 3", n)
	}
}

// A test open on another link does not stand in: with the 9603 running, a
// test waiting on the 9704 from before does not answer for it.
// [MESHSAT-1430]
func TestSOSTest_AnOpenTestCountsOnItsOwnLinkOnly(t *testing.T) {
	s := sosSatServer(t, &apiSat{kind: "sbd", connected: true}, &apiSat{kind: "imt", connected: true})
	frame := hubreporter.EncodeSatPosition("msa-flaneur", 52.1601, 4.4970, 3.9, 1, time.Unix(1790000000, 0))
	imt, _, err := s.dispatcher.QueueDirectSendTo("iridium_imt_0", sosTestSatellitePreview,
		engine.DirectSendOptions{Precedence: "Routine", Class: database.DeliveryClassHubUplink, Payload: frame, TTLSeconds: 1800})
	if err != nil {
		t.Fatal(err)
	}
	resp, del := postSatTest(t, s, sosTestSatBody)
	if resp.Existing || resp.DeliveryID == imt || del.Channel != "iridium_0" {
		t.Fatalf("answered %+v on %s, want a new test on iridium_0", resp, del.Channel)
	}
}

// The satellite leg waits 30 minutes at most, as MeshSat Android's test:
// its row's deadline is 30 minutes on; past it the delivery worker never
// gets it and the reaper expires it. A test held while its link is down
// keeps its deadline: the modem back three days later, the test expires
// instead of spending a credit and putting a three-day-old position on the
// Hub. [MESHSAT-1430]
func TestSOSTest_SatelliteLivesThirtyMinutes(t *testing.T) {
	s := sosSatServer(t, &apiSat{kind: "sbd", connected: true}, nil)
	_, del := postSatTest(t, s, sosTestSatBody)
	if del.TTLSeconds != 1800 || del.ExpiresAt == nil {
		t.Fatalf("ttl_seconds %d, expires_at %v", del.TTLSeconds, del.ExpiresAt)
	}
	exp, err := time.Parse(sqliteTime, *del.ExpiresAt)
	if d := exp.Sub(time.Now().UTC()); err != nil || d < 29*time.Minute || d > 30*time.Minute+time.Second {
		t.Fatalf("expires_at %s (%v): %v from now, want 30 minutes", *del.ExpiresAt, err, d)
	}
	if !pendingOn(t, s, "iridium_0", del.ID) {
		t.Fatal("the test is not handed to the delivery worker")
	}

	// 31 minutes on.
	setRow(t, s, del.ID, `expires_at = ?`, time.Now().UTC().Add(-time.Minute).Format(sqliteTime))
	if pendingOn(t, s, "iridium_0", del.ID) {
		t.Fatal("a test past its 30 minutes is handed to the delivery worker")
	}
	if n, err := s.db.ExpireDeliveries(); err != nil || n != 1 {
		t.Fatalf("the reaper expired %d (%v)", n, err)
	}
	if st, _ := rowStatus(t, s, del.ID); st != "expired" {
		t.Fatalf("the test is %s, want expired", st)
	}

	// Held: the modem went a minute after the test and came back three
	// days later.
	_, held := postSatTest(t, s, sosTestSatBody)
	if n, err := s.db.HoldDeliveriesForChannel("iridium_0"); err != nil || n != 1 {
		t.Fatalf("held %d (%v)", n, err)
	}
	threeDaysAgo := time.Now().UTC().Add(-72 * time.Hour)
	deadline := threeDaysAgo.Add(30 * time.Minute).Format(sqliteTime)
	setRow(t, s, held.ID, `held_at = ?, expires_at = ?`, threeDaysAgo.Add(time.Minute).Format(sqliteTime), deadline)
	if n, err := s.db.UnholdDeliveriesForChannel("iridium_0"); err != nil || n != 1 {
		t.Fatalf("unheld %d (%v)", n, err)
	}
	if back, _ := s.db.GetDelivery(held.ID); back.ExpiresAt == nil || *back.ExpiresAt != deadline {
		t.Fatalf("after the hold the deadline is %v, want %s", back.ExpiresAt, deadline)
	}
	if pendingOn(t, s, "iridium_0", held.ID) {
		t.Fatal("a test held past its deadline is handed to the delivery worker when the modem is back")
	}
	if n, err := s.db.ExpireDeliveries(); err != nil || n != 1 {
		t.Fatalf("the reaper expired %d (%v)", n, err)
	}
}

// While an SOS is active the satellite leg of a test is refused (409) and
// nothing is queued, as MeshSat Android refuses to start a test during an
// SOS: the SOS replaces its test. Once the SOS is over, a test goes.
// [MESHSAT-1430]
func TestSOSTest_SatelliteRefusedWhileAnSOSIsActive(t *testing.T) {
	s := sosSatServer(t, &apiSat{kind: "sbd", connected: true}, nil)
	if !s.TriggerSOS("hold") {
		t.Fatal("the SOS was refused")
	}
	t.Cleanup(stopSOS(s))

	w := post(t, s, "/api/sos/test", sosTestSatBody)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "an SOS is active") {
		t.Fatalf("during an SOS: %d %s, want 409", w.Code, w.Body.String())
	}
	if n := deliveryCount(t, s); n != 0 {
		t.Fatalf("%d deliveries queued during an SOS", n)
	}

	if w := post(t, s, "/api/sos/cancel", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "cancelled") {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	if resp, _ := postSatTest(t, s, sosTestSatBody); resp.Status != "queued" || resp.Existing {
		t.Fatalf("after the SOS: %+v", resp)
	}
}

// Starting an SOS cancels the alarm test's satellite leg wherever it has not
// gone out, on every link: queued, waiting for a retry, or held while its
// modem is away, as MeshSat Android's SOS replaces a running test. The rows
// end cancelled by the queue's own cancel (dead, "cancelled"), which the
// delivery worker skips. A test being sent cannot be stopped, one that went
// out is history, and the satellite fallback's frames and a message that
// happens to carry the same words are no tests. [MESHSAT-1430]
func TestSOSTest_AnSOSCancelsTheWaitingTest(t *testing.T) {
	s := sosSatServer(t, &apiSat{kind: "sbd", connected: true}, nil)
	_, queued := postSatTest(t, s, sosTestSatBody)

	frame := hubreporter.EncodeSatPosition("msa-flaneur", 52.1601, 4.4970, 3.9, 1, time.Unix(1790000000, 0))
	queue := func(iface, preview string, opts engine.DirectSendOptions, status string) int64 {
		t.Helper()
		id, _, err := s.dispatcher.QueueDirectSendTo(iface, preview, opts)
		if err != nil {
			t.Fatal(err)
		}
		if status != "queued" {
			setRow(t, s, id, `status = ?`, status)
		}
		return id
	}
	testOpts := engine.DirectSendOptions{Precedence: "Routine", Class: database.DeliveryClassHubUplink, Payload: frame, TTLSeconds: 1800}
	retry := queue("iridium_imt_0", sosTestSatellitePreview, testOpts, "retry")
	held := queue("iridium_imt_0", sosTestSatellitePreview, testOpts, "held")
	sending := queue("iridium_imt_0", sosTestSatellitePreview, testOpts, "sending")
	sent := queue("iridium_imt_0", sosTestSatellitePreview, testOpts, "sent")
	fallback := queue("iridium_0", "hub uplink frame, 31 B",
		engine.DirectSendOptions{Precedence: "Priority", Class: database.DeliveryClassHubUplink, Payload: frame}, "queued")
	words := queue("iridium_0", sosTestSatellitePreview, engine.DirectSendOptions{}, "queued")

	if !s.TriggerSOS("hold") {
		t.Fatal("the SOS was refused")
	}
	t.Cleanup(stopSOS(s))

	for _, tc := range []struct {
		name, status, lastError string
		id                      int64
	}{
		{"the queued test", "dead", "cancelled", queued.ID},
		{"the test waiting for a retry", "dead", "cancelled", retry},
		{"the held test", "dead", "cancelled", held},
		{"the test being sent", "sending", "", sending},
		{"the test that went out", "sent", "", sent},
		{"the fallback's frame", "queued", "", fallback},
		{"a message with the same words", "queued", "", words},
	} {
		if st, le := rowStatus(t, s, tc.id); st != tc.status || le != tc.lastError {
			t.Errorf("%s: %s %q, want %s %q", tc.name, st, le, tc.status, tc.lastError)
		}
	}
	if pendingOn(t, s, "iridium_0", queued.ID) {
		t.Fatal("the cancelled test is still handed to the delivery worker")
	}
	// The test being sent keeps going, with a deadline of now: if that send
	// fails it is not tried again (TestSOSTest_ATestOnTheModemWhenAnSOSStartsIsNotTriedAgain).
	onModem, err := s.db.GetDelivery(sending)
	if err != nil {
		t.Fatal(err)
	}
	if exp, err := time.Parse(sqliteTime, *onModem.ExpiresAt); err != nil || exp.After(time.Now().UTC()) {
		t.Fatalf("the test being sent expires %s (%v), want now", *onModem.ExpiresAt, err)
	}
	if other, _ := s.db.GetDelivery(fallback); other.ExpiresAt != nil {
		t.Fatalf("the fallback's frame got a deadline: %s", *other.ExpiresAt)
	}
}

// okSat is a 9603 whose sessions all succeed; it records the texts sent.
type okSat struct {
	*apiSat
	mu    sync.Mutex
	texts []string
}

func (o *okSat) Send(_ context.Context, _ []byte) (*transport.SatResult, error) {
	return &transport.SatResult{MOStatus: 0}, nil
}
func (o *okSat) SendText(_ context.Context, text string) (*transport.SatResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.texts = append(o.texts, text)
	return &transport.SatResult{MOStatus: 0}, nil
}
func (o *okSat) sentTexts() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.texts...)
}

// With the day's credit budget used up, the SOS burst's direct satellite
// send still goes (MeshMessage.Critical); it failed with "sbd: budget
// exceeded" before. An ordinary send on the same gateway is still refused.
// The modem is a fake: no satellite session opens. [MESHSAT-1431]
func TestSOS_TheSatelliteLegPassesAnExhaustedBudget(t *testing.T) {
	modem := &okSat{apiSat: &apiSat{kind: "sbd", connected: true}}
	s := newTestServerWithDB(t)
	mgr := gateway.NewManager(s.db, modem)
	ctx, cancel := context.WithCancel(context.Background())
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		mgr.Stop()
		cancel()
	})
	if err := mgr.ConfigureInstance(ctx, "iridium", "iridium_0", true, `{"mailbox_mode":"off","auto_receive":false,"daily_budget":1}`); err != nil {
		t.Fatal(err)
	}
	s.gwManager = mgr
	if err := s.db.InsertCreditUsage(nil, 1, nil); err != nil {
		t.Fatal(err)
	}

	gw := mgr.GatewayByInterfaceID("iridium_0")
	if gw == nil {
		t.Fatal("no SBD gateway on iridium_0")
	}
	if err := gw.Forward(context.Background(), &transport.MeshMessage{PortNum: 1, DecodedText: "hello from the kit"}); err == nil ||
		!strings.Contains(err.Error(), "budget exceeded") {
		t.Fatalf("an ordinary send over the used-up budget: %v", err)
	}

	if !s.TriggerSOS("hold") {
		t.Fatal("the SOS was refused")
	}
	t.Cleanup(stopSOS(s))
	deadline := time.Now().Add(5 * time.Second)
	for {
		texts := modem.sentTexts()
		if len(texts) == 1 && texts[0] == sosDefaultText {
			break
		}
		if len(texts) > 1 || time.Now().After(deadline) {
			t.Fatalf("the modem sent %q, want the SOS text alone", texts)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
