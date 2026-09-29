package api

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"meshsat/internal/engine"
	"meshsat/internal/gateway"
)

// apiTAKHub stands in for the Hub reporter behind MQTT Export.
type apiTAKHub struct {
	mu   sync.Mutex
	docs []*gateway.CotEvent
}

func (h *apiTAKHub) IsConnected() bool { return true }

func (h *apiTAKHub) PublishTAKCoT(xml []byte) error {
	ev, err := gateway.ParseCotEvent(xml)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.docs = append(h.docs, ev)
	return nil
}

func (h *apiTAKHub) waitFor(t *testing.T, what string, match func(*gateway.CotEvent) bool) *gateway.CotEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		for _, ev := range h.docs {
			if match(ev) {
				h.mu.Unlock()
				return ev
			}
		}
		h.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the Hub never got %s", what)
	return nil
}

// A text the app sends to the mesh and an SOS reach TAK from the Bridge itself,
// through the running TAK gateway, as on MeshSat Android. [MESHSAT-1421]
func TestTAKOwnEvents_FromTheAPI(t *testing.T) {
	s := newTestServerWithDB(t)
	s.mesh = &sendRecorder{}
	s.processor = engine.NewProcessor(nil, nil)

	mgr := gateway.NewManager(s.db, nil)
	ctx, cancel := context.WithCancel(context.Background())
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		mgr.Stop()
		cancel()
	})
	hub := &apiTAKHub{}
	mgr.SetTAKHooks(gateway.TAKHooks{
		Hub:          hub,
		NodeID:       func() string { return "aabbccdd" },
		SelfPosition: func() (float64, float64, float64, bool) { return 52.3676, 4.9041, 0, true },
	})
	if err := mgr.ConfigureInstance(ctx, "tak", "tak_0", true, `{"hub_export":true,"coalesce_seconds":3600}`); err != nil {
		t.Fatalf("configure tak: %v", err)
	}
	s.gwManager = mgr

	if w := post(t, s, "/api/messages/send", `{"text":"hello from the app"}`); w.Code != http.StatusOK {
		t.Fatalf("send: %d %s", w.Code, w.Body.String())
	}
	chat := hub.waitFor(t, "the chat", func(ev *gateway.CotEvent) bool {
		return ev.Type == gateway.CotEventTypeChat && ev.Detail != nil && ev.Detail.Remarks != nil && ev.Detail.Remarks.Text == "hello from the app"
	})
	if chat.Detail.Contact == nil || chat.Detail.Contact.Callsign != "MESHSAT-CCDD" {
		t.Errorf("chat callsign: %+v", chat.Detail.Contact)
	}

	s.mesh = nil // the SOS burst's mesh leg is not what this test is about
	if !s.TriggerSOSWithText("test", "SOS: help at the booth") {
		t.Fatal("SOS refused")
	}
	t.Cleanup(func() {
		s.sos.mu.Lock()
		if s.sos.cancelFn != nil {
			s.sos.cancelFn()
		}
		s.sos.mu.Unlock()
	})
	sos := hub.waitFor(t, "the SOS", func(ev *gateway.CotEvent) bool {
		return ev.Detail != nil && ev.Detail.Emergency != nil
	})
	if sos.UID != "MESHSAT-aabbccdd" || sos.Detail.Emergency.Text != "SOS: help at the booth" || sos.Point.Lat != 52.3676 {
		t.Errorf("SOS: uid %q text %q point %+v", sos.UID, sos.Detail.Emergency.Text, sos.Point)
	}
}

// Without a TAK gateway the handlers go on as before.
func TestTAKOwnEvents_NoGateway(t *testing.T) {
	(&Server{}).takOwnChat("nobody")
	(&Server{}).takOwnSOS("nobody")
}
