package hubreporter

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/rs/zerolog/log"
)

// An SOS under its own identity on the Bridge's Hub session (HeMB-NG stage 2, C10,
// MESHSAT-1584): the bridge publishes each start and cancel on sos/event and learns from
// sos/ack that the Hub has it; the Hub keeps on hub/caps, retained, whether it understands
// them at all. The contract is the Hub's docs/bridge-hub-protocol.md "SOS identity (C10)";
// the shared cases are internal/hembfixtures/testdata/v1/sos.json.

// CapSOSV2 is the hub/caps entry that lets a sender use the C10 frames and events.
const CapSOSV2 = "sos_v2"

// HubCaps is what the Hub publishes, retained, on {root}/bridge/<id>/hub/caps.
type HubCaps struct {
	V    int      `json:"v"`
	Caps []string `json:"caps"`
}

// Has reports whether the Hub listed c.
func (h HubCaps) Has(c string) bool { return slices.Contains(h.Caps, c) }

// ParseHubCaps reads a hub/caps payload. An empty payload (the retained message cleared)
// is no caps at all.
func ParseHubCaps(payload []byte) (HubCaps, error) {
	var h HubCaps
	if len(payload) == 0 {
		return h, nil
	}
	if err := json.Unmarshal(payload, &h); err != nil {
		return HubCaps{}, fmt.Errorf("hub/caps: %w", err)
	}
	if h.V != 1 {
		return HubCaps{}, fmt.Errorf("hub/caps: version %d", h.V)
	}
	return h, nil
}

// SOS event kinds.
const (
	SOSEventStart  = "start"
	SOSEventCancel = "cancel"
)

// SOSEvent is the v2 JSON of a start or a cancel on {root}/bridge/<id>/sos/event.
// run_start_ms is in every event; the Hub refuses one without it.
type SOSEvent struct {
	V          int      `json:"v"`
	Kind       string   `json:"kind"`
	SOSID      string   `json:"sos_id"`
	RunStartMs int64    `json:"run_start_ms"`
	DeviceID   string   `json:"device_id,omitempty"`
	Text       string   `json:"text,omitempty"`
	Lat        *float64 `json:"lat,omitempty"`
	Lon        *float64 `json:"lon,omitempty"`
	Reason     *int     `json:"reason,omitempty"` // a cancel's: 0 the operator, 1 test ended
	Timestamp  int64    `json:"timestamp,omitempty"`
}

// SOSAck is the Hub's answer on {root}/bridge/<id>/sos/ack: it has the start or the
// cancel, and State is the run's state after it (a start that came after its cancel is
// answered "cancelled").
type SOSAck struct {
	V     int    `json:"v"`
	SOSID string `json:"sos_id"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	Rev   int    `json:"rev"`
}

// ParseSOSAck reads an sos/ack payload.
func ParseSOSAck(payload []byte) (SOSAck, error) {
	var a SOSAck
	if err := json.Unmarshal(payload, &a); err != nil {
		return SOSAck{}, fmt.Errorf("sos/ack: %w", err)
	}
	if a.V != 2 || a.SOSID == "" || (a.Kind != SOSEventStart && a.Kind != SOSEventCancel) {
		return SOSAck{}, errors.New("sos/ack: not a v2 ack of a start or a cancel")
	}
	return a, nil
}

// SetSOSHandlers sets who takes hub/caps and sos/ack; the subscriptions are made on every
// connect. Set them before Start.
func (r *HubReporter) SetSOSHandlers(caps func(HubCaps), ack func(SOSAck)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hubCaps, r.sosAck = caps, ack
}

// subscribeSOS subscribes hub/caps and sos/ack when their handlers are set.
func (r *HubReporter) subscribeSOS() {
	r.mu.Lock()
	caps, ack := r.hubCaps, r.sosAck
	r.mu.Unlock()
	type sub struct {
		topic string
		fn    func([]byte)
	}
	var subs []sub
	if caps != nil {
		subs = append(subs, sub{r.topics.HubCaps(r.cfg.BridgeID), func(p []byte) {
			h, err := ParseHubCaps(p)
			if err != nil {
				log.Warn().Err(err).Msg("hubreporter: hub/caps does not read; taken as no caps")
			}
			caps(h)
		}})
	}
	if ack != nil {
		subs = append(subs, sub{r.topics.SOS(r.cfg.BridgeID, "ack"), func(p []byte) {
			a, err := ParseSOSAck(p)
			if err != nil {
				log.Warn().Err(err).Msg("hubreporter: sos/ack ignored")
				return
			}
			ack(a)
		}})
	}
	for _, s := range subs {
		fn := s.fn
		const maxRetries = 5
		for attempt := 1; attempt <= maxRetries; attempt++ {
			token := r.client.Subscribe(s.topic, 1, func(_ mqtt.Client, m mqtt.Message) { fn(m.Payload()) })
			if token.WaitTimeout(10*time.Second) && token.Error() == nil {
				log.Info().Str("topic", s.topic).Msg("hubreporter: subscribed to the SOS topics")
				break
			}
			log.Error().Err(token.Error()).Str("topic", s.topic).Int("attempt", attempt).Msg("hubreporter: SOS subscribe failed")
			if attempt < maxRetries {
				time.Sleep(time.Duration(attempt) * time.Second)
			}
		}
	}
}

// PublishSOSEvent publishes a start or cancel on this bridge's sos/event at QoS 1, not
// retained and never queued (the SOS repeats it until the Hub's sos/ack), waiting up to wait
// for the broker's PUBACK. ErrNotConnected means nothing left. A PUBACK is not the Hub having
// it: only the ack is.
func (r *HubReporter) PublishSOSEvent(ev SOSEvent, wait time.Duration) error {
	r.mu.Lock()
	up := r.connected && r.client != nil
	r.mu.Unlock()
	if !up {
		return ErrNotConnected
	}
	ev.V = 2
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("hubreporter: sos/event: %w", err)
	}
	token := r.client.Publish(r.topics.SOS(r.cfg.BridgeID, "event"), 1, false, payload)
	if !token.WaitTimeout(wait) {
		return ErrMsgNoPuback
	}
	if err := token.Error(); err != nil {
		if errors.Is(err, mqtt.ErrNotConnected) {
			return ErrNotConnected
		}
		return fmt.Errorf("hubreporter: sos/event: %w", err)
	}
	return nil
}
