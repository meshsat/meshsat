package hubreporter

import (
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/rs/zerolog/log"
)

// Public warnings (MESHSAT-1734): the Hub hands this bridge a signed digest
// of an official warning for its area on the bridge's own warn/in; only the
// Hub may publish there. The handler (warnings.Relay.Handle) verifies it.

// SetWarningHandler sets the callback for a digest delivered on warn/in.
// Call it before Start; the subscription is made on every connect.
func (r *HubReporter) SetWarningHandler(fn func([]byte)) {
	r.mu.Lock()
	r.warnIn = fn
	r.mu.Unlock()
}

// subscribeWarnIn subscribes warn/in when its handler is set, with retries.
func (r *HubReporter) subscribeWarnIn() {
	r.mu.Lock()
	fn := r.warnIn
	r.mu.Unlock()
	if fn == nil {
		return
	}
	topic := r.topics.WarnInFor(r.cfg.BridgeID)
	const maxRetries = 5
	for attempt := 1; attempt <= maxRetries; attempt++ {
		token := r.client.Subscribe(topic, 1, func(_ mqtt.Client, m mqtt.Message) { r.handleWarnIn(m.Payload()) })
		if token.WaitTimeout(10*time.Second) && token.Error() == nil {
			log.Info().Str("topic", topic).Msg("hubreporter: subscribed to public warnings for this bridge")
			return
		}
		log.Error().Err(token.Error()).Str("topic", topic).Int("attempt", attempt).Msg("hubreporter: warn/in subscribe failed")
		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
}

// handleWarnIn hands one delivered digest to the handler.
func (r *HubReporter) handleWarnIn(payload []byte) {
	r.mu.Lock()
	fn := r.warnIn
	r.mu.Unlock()
	if fn == nil {
		return
	}
	fn(append([]byte(nil), payload...))
}
