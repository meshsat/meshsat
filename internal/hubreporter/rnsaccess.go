package hubreporter

import (
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/rs/zerolog/log"
)

// The workspace's Private Reticulum network (MESHSAT-1715; the Hub's
// docs/bridge-hub-protocol.md "Reticulum access"): the bridge asks on
// rns/join, the Hub answers on rns/access with the network name and passphrase,
// or "none", and pushes there when the workspace changes them. QoS 0 from the
// Hub and never retained, so a bridge that was away asks again.

// SetRNSAccessHandler sets who takes rns/access payloads and who is told once
// the subscription is up on a connection (to ask). Set it before Start.
func (r *HubReporter) SetRNSAccessHandler(access func(payload []byte), up func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rnsAccess, r.rnsUp = access, up
}

// subscribeRNSAccess subscribes rns/access when its handler is set, then
// tells the handler's owner, which asks.
func (r *HubReporter) subscribeRNSAccess() {
	r.mu.Lock()
	fn, up := r.rnsAccess, r.rnsUp
	r.mu.Unlock()
	if fn == nil {
		return
	}
	topic := r.topics.RNS(r.cfg.BridgeID, "access")
	const maxRetries = 5
	for attempt := 1; attempt <= maxRetries; attempt++ {
		token := r.client.Subscribe(topic, 1, func(_ mqtt.Client, m mqtt.Message) { fn(m.Payload()) })
		if token.WaitTimeout(10*time.Second) && token.Error() == nil {
			log.Info().Str("topic", topic).Msg("hubreporter: subscribed to the workspace's Reticulum access")
			if up != nil {
				go up()
			}
			return
		}
		log.Error().Err(token.Error()).Str("topic", topic).Int("attempt", attempt).Msg("hubreporter: rns/access subscribe failed")
		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
}

// AskRNSAccess publishes rns/join; false when the session is down or the
// broker did not take it in time.
func (r *HubReporter) AskRNSAccess() bool {
	if !r.IsConnected() {
		return false
	}
	token := r.client.Publish(r.topics.RNS(r.cfg.BridgeID, "join"), 1, false, []byte(`{"v":1}`))
	if !token.WaitTimeout(10*time.Second) || token.Error() != nil {
		log.Warn().Err(token.Error()).Msg("hubreporter: rns/join not taken by the broker")
		return false
	}
	return true
}
