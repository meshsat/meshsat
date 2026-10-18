package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"meshsat/internal/transport"
)

// With no broker connection Forward fails, so the delivery ledger keeps the
// message for a retry instead of marking it delivered. [MESHSAT-1581]
func TestMQTTForward_DisconnectedIsAnError(t *testing.T) {
	g := NewMQTTGateway(DefaultMQTTConfig())
	err := g.Forward(context.Background(), &transport.MeshMessage{DecodedText: "hello"})
	if !errors.Is(err, transport.ErrNotConnected) {
		t.Fatalf("Forward while disconnected: %v; want ErrNotConnected", err)
	}
	if n := g.msgsOut.Load(); n != 0 {
		t.Fatalf("msgsOut %d after a failed forward", n)
	}
}

// fakeMQTTClient is a connected paho client that records what is published.
type fakeMQTTClient struct {
	mqtt.Client
	published [][]byte
}

type doneToken struct{ mqtt.Token }

func (doneToken) Wait() bool                     { return true }
func (doneToken) WaitTimeout(time.Duration) bool { return true }
func (doneToken) Error() error                   { return nil }

func (c *fakeMQTTClient) Publish(_ string, _ byte, _ bool, payload interface{}) mqtt.Token {
	c.published = append(c.published, payload.([]byte))
	return doneToken{}
}
func (c *fakeMQTTClient) IsConnected() bool { return true }

type fakeMQTTMessage struct {
	mqtt.Message
	payload []byte
}

func (m fakeMQTTMessage) Payload() []byte { return m.payload }
func (m fakeMQTTMessage) Topic() string   { return "msh/cubeos/LongFast/00001234" }

// A direct send has packet id 0. Its own echo is dropped, and another
// node's message without a packet id is not: the echo filter used to key on
// the packet id, so for five minutes after a direct send every inbound
// message with id 0 was dropped as ours. [MESHSAT-1584]
func TestMQTTEchoFilterKeysOnTheBytesNotThePacketID(t *testing.T) {
	g := NewMQTTGateway(DefaultMQTTConfig())
	c := &fakeMQTTClient{}
	g.client = c
	g.connected.Store(true)
	if err := g.Forward(context.Background(), &transport.MeshMessage{DecodedText: "a direct send"}); err != nil {
		t.Fatal(err)
	}
	if len(c.published) != 1 {
		t.Fatalf("%d publishes", len(c.published))
	}
	g.onMessage(nil, fakeMQTTMessage{payload: c.published[0]})
	select {
	case m := <-g.inCh:
		t.Fatalf("our own echo came in: %q", m.Text)
	default:
	}
	other, _ := json.Marshal(map[string]interface{}{"from": 0x1234, "text": "from another node", "packet_id": 0})
	g.onMessage(nil, fakeMQTTMessage{payload: other})
	select {
	case m := <-g.inCh:
		if m.Text != "from another node" {
			t.Fatalf("got %q", m.Text)
		}
	default:
		t.Fatal("another node's message without a packet id was dropped as our echo")
	}
}
