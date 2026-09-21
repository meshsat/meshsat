package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"meshsat/internal/directory"
	"meshsat/internal/gateway"
	"meshsat/internal/transport"
)

// A modem that must never be written to directly by the SMS endpoint.
type noDirectSend struct{ transport.CellTransport }

func (noDirectSend) SendSMS(context.Context, string, string) error {
	return errors.New("the SMS endpoint wrote to the modem directly")
}
func (noDirectSend) ExecAT(context.Context, string, time.Duration) (string, error) { return "", nil }

// The dashboard's SMS goes through the delivery ledger like every other send:
// a queued delivery on cellular_0 addressed to the number, never a direct
// modem write, so the Hub's number gets the plaintext rule, and the send is
// retried and visible in the queue. [MESHSAT-756]
func TestSendSMS_QueuesThroughTheLedger(t *testing.T) {
	s, _ := testServerWithDispatcher(t, directory.KindCellular)
	s.SetCellTransport(noDirectSend{})

	body, _ := json.Marshal(map[string]string{"to": "+3197010258258", "text": "hello hub"})
	rec := httptest.NewRecorder()
	s.handleSendSMS(rec, httptest.NewRequest(http.MethodPost, "/api/cellular/sms/send", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status     string `json:"status"`
		DeliveryID int64  `json:"delivery_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Status != "queued" || resp.DeliveryID == 0 {
		t.Fatalf("response %s", rec.Body.String())
	}
	del, err := s.db.GetDelivery(resp.DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if del.Channel != "cellular_0" || del.Destination != "+3197010258258" || del.TextPreview != "hello hub" || del.Status != "queued" {
		t.Fatalf("delivery %+v", del)
	}

	rec = httptest.NewRecorder()
	s.handleSendSMS(rec, httptest.NewRequest(http.MethodPost, "/api/cellular/sms/send", strings.NewReader(`{"to":"","text":"x"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty number: status %d", rec.Code)
	}
}

// The on-air size check follows the worker's choice: clear text for a
// plaintext peer, the egress chain plus the version byte for everyone else.
func TestOnAirSMSLength(t *testing.T) {
	cfg := gateway.CellularConfig{MaxSMSSegments: 1, PlaintextPeers: []string{"+3197010258258"}}
	grow := func(b []byte) ([]byte, error) { return append(bytes.Repeat([]byte{'x'}, 40), b...), nil } // ciphertext overhead
	text := strings.Repeat("a", 130)

	if n, limit, _ := onAirSMSLength(cfg, grow, "+3197010258258", text); n != 130 || limit != 160 {
		t.Fatalf("plaintext peer: %d of %d, want 130 of 160", n, limit)
	}
	if n, _, _ := onAirSMSLength(cfg, grow, "+31600000001", text); n != 171 {
		t.Fatalf("encrypted: %d on air, want 171 (130 + 40 overhead + version byte)", n)
	}
	if _, limit, _ := onAirSMSLength(gateway.CellularConfig{}, grow, "+31600000001", text); limit != 0 {
		t.Fatal("an unknown segment limit must not refuse anything")
	}
}
