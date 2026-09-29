package hubreporter

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// The Hub link as the apps show it (MeshSat Android's HubReporter states,
// SettingsScreen.kt:1635-1643): "connecting" while an attempt is in flight,
// "connected", "error" after a failed attempt until the next one starts (for
// good when the TLS material is unusable), "disconnected" after a session was
// lost. The reason of the last failure is kept until a connect succeeds.
const (
	LinkConnecting   = "connecting"
	LinkConnected    = "connected"
	LinkError        = "error"
	LinkDisconnected = "disconnected"
)

// ErrNotConnected is Ping's answer when there is no session to test.
var ErrNotConnected = errors.New("hubreporter: not connected")

// pingWait bounds how long Ping waits for the broker's PUBACK.
var pingWait = 10 * time.Second

// LinkState returns the Hub link's state and the reason of the last failure.
func (r *HubReporter) LinkState() (state, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.linkState == "" {
		return LinkDisconnected, r.linkError
	}
	return r.linkState, r.linkError
}

// BridgeID is the id this reporter runs as on the Hub.
func (r *HubReporter) BridgeID() string {
	return r.cfg.BridgeID
}

func (r *HubReporter) setLink(state, reason string) {
	r.mu.Lock()
	r.linkState = state
	if state == LinkConnected {
		r.linkError = ""
	} else if reason != "" {
		r.linkError = reason
	}
	r.mu.Unlock()
}

// onConnectionNotification follows paho's connect attempts, which it reports
// in order from the goroutine that makes them. A first connect and its retries
// go through "connecting" and "error"; the automatic reconnect after a lost
// session stays "disconnected" until it succeeds, as Android's Paho does, and
// only records why an attempt failed. "connected" and "disconnected" are set by
// the connect and lost handlers, together with IsConnected.
func (r *HubReporter) onConnectionNotification(_ mqtt.Client, note mqtt.ConnectionNotification) {
	switch n := note.(type) {
	case mqtt.ConnectionNotificationConnecting:
		if !n.IsReconnect {
			r.setLink(LinkConnecting, "")
		}
	case mqtt.ConnectionNotificationFailed:
		r.mu.Lock()
		state := LinkError
		if r.linkState == LinkDisconnected {
			state = LinkDisconnected
		}
		r.mu.Unlock()
		r.setLink(state, FailureText(n.Reason))
	}
}

// FailureText is a failed connect in words, by Android's rule
// (HubReporter.connectFailureText): the outermost message, and the innermost
// cause after a colon when it adds something. A message left blank is the
// error's type name. At most eight links of the chain are walked.
func FailureText(err error) string {
	if err == nil {
		return ""
	}
	var chain []error
	for e := err; e != nil && len(chain) < 8; e = cause(e) {
		chain = append(chain, e)
	}
	message := func(e error) string {
		if m := strings.TrimSpace(e.Error()); m != "" {
			return m
		}
		t := reflect.TypeOf(e)
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Name() != "" {
			return t.Name()
		}
		return fmt.Sprintf("%T", e)
	}
	outer := message(chain[0])
	inner := message(chain[len(chain)-1])
	if len(chain) > 1 && inner != outer && !strings.Contains(outer, inner) {
		return outer + ": " + inner
	}
	return outer
}

// cause is the next error down the chain; of an error that joins several, the
// last one, which is where paho puts the network's own error.
func cause(err error) error {
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		if errs := e.Unwrap(); len(errs) > 0 {
			return errs[len(errs)-1]
		}
		return nil
	default:
		return errors.Unwrap(err)
	}
}

// Ping tests the Hub link as Android's "Test the connection" does: a QoS 1
// {"ping":true} on the health topic, answered by the broker's PUBACK. The Hub
// drops the message (it carries no protocol version), so nothing is recorded.
func (r *HubReporter) Ping() (time.Duration, error) {
	if r.client == nil || !r.IsConnected() {
		return 0, ErrNotConnected
	}
	start := time.Now()
	token := r.client.Publish(TopicBridgeHealth(r.cfg.BridgeID), 1, false, []byte(`{"ping":true}`))
	if !token.WaitTimeout(pingWait) {
		return 0, fmt.Errorf("no answer within %s", pingWait)
	}
	if err := token.Error(); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}
