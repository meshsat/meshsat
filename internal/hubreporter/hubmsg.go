package hubreporter

import (
	"errors"
	"fmt"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/rs/zerolog/log"
)

// hub_msg on the Bridge's Hub session (HeMB-NG stage 2, MESHSAT-1584): the bridge publishes
// its submissions on msg/out and its answers on msg/ack, and takes deliveries from msg/in and
// evidence from msg/status. QoS 1, never retained, never queued in the outbox: the delivery
// ledger owns every retry.

var (
	// ErrMsgNotConnected: the session was down, nothing was handed to the client.
	ErrMsgNotConnected = errors.New("hubreporter: not connected to the Hub")
	// ErrMsgNoPuback: handed to the client, no PUBACK in time. The client does not cancel
	// it, so it may still reach the Hub: the outcome is unknown, never not sent.
	ErrMsgNoPuback = errors.New("hubreporter: no PUBACK from the Hub broker in time")
)

// Principal is this bridge's principal on the Hub ("bridge:<id>").
func (r *HubReporter) Principal() string { return "bridge:" + r.cfg.BridgeID }

// SetMsgHandlers sets who takes msg/in and msg/status payloads; the subscriptions are made on
// every connect. Set them before Start.
func (r *HubReporter) SetMsgHandlers(in, status func(payload []byte)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgIn, r.msgStatus = in, status
}

// subscribeMsg subscribes msg/in and msg/status when their handlers are set.
func (r *HubReporter) subscribeMsg() {
	r.mu.Lock()
	in, status := r.msgIn, r.msgStatus
	r.mu.Unlock()
	for _, sub := range []struct {
		kind string
		fn   func([]byte)
	}{{"in", in}, {"status", status}} {
		if sub.fn == nil {
			continue
		}
		fn := sub.fn
		topic := r.topics.Msg(r.cfg.BridgeID, sub.kind)
		const maxRetries = 5
		for attempt := 1; attempt <= maxRetries; attempt++ {
			token := r.client.Subscribe(topic, 1, func(_ mqtt.Client, m mqtt.Message) { fn(m.Payload()) })
			if token.WaitTimeout(10*time.Second) && token.Error() == nil {
				log.Info().Str("topic", topic).Msg("hubreporter: subscribed to hub_msg")
				break
			}
			log.Error().Err(token.Error()).Str("topic", topic).Int("attempt", attempt).Msg("hubreporter: hub_msg subscribe failed")
			if attempt < maxRetries {
				time.Sleep(time.Duration(attempt) * time.Second)
			}
		}
	}
}

// PublishMsg publishes payload on this bridge's msg/<kind> at QoS 1 and waits up to wait for
// the broker's PUBACK. ErrMsgNotConnected means nothing left; ErrMsgNoPuback, or any other
// error, means it may have.
func (r *HubReporter) PublishMsg(kind string, payload []byte, wait time.Duration) error {
	if kind != "out" && kind != "ack" {
		return fmt.Errorf("hubreporter: a bridge does not publish msg/%s", kind)
	}
	r.mu.Lock()
	up := r.connected && r.client != nil
	r.mu.Unlock()
	if !up {
		return ErrMsgNotConnected
	}
	token := r.client.Publish(r.topics.Msg(r.cfg.BridgeID, kind), 1, false, payload)
	if !token.WaitTimeout(wait) {
		return ErrMsgNoPuback
	}
	if err := token.Error(); err != nil {
		if errors.Is(err, mqtt.ErrNotConnected) {
			return ErrMsgNotConnected
		}
		return fmt.Errorf("hubreporter: msg/%s: %w", kind, err)
	}
	return nil
}
