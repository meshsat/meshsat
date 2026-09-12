package hubreporter

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// A minimal MQTT 3.1.1 server: just enough to complete a session so the client's
// OnConnect handler runs. There is no broker anywhere in this repo and no test
// has ever exercised HubReporter.Start against one, which is why a first connect
// that could never succeed shipped. [MESHSAT-1027]
type fakeBroker struct {
	ln        net.Listener
	connects  atomic.Int32
	closeOnce chan struct{}
}

func newFakeBroker(t *testing.T, addr string) *fakeBroker {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	b := &fakeBroker{ln: ln, closeOnce: make(chan struct{})}
	go b.serve()
	t.Cleanup(func() { b.Close() })
	return b
}

func (b *fakeBroker) addr() string { return b.ln.Addr().String() }

func (b *fakeBroker) Close() {
	select {
	case <-b.closeOnce:
	default:
		close(b.closeOnce)
		b.ln.Close()
	}
}

func (b *fakeBroker) serve() {
	for {
		c, err := b.ln.Accept()
		if err != nil {
			return
		}
		go b.session(c)
	}
}

// session answers the handful of packet types a connecting client sends. Any
// packet it does not understand is read and discarded, which is enough: the test
// only needs the session to stay up long enough for OnConnect to fire.
func (b *fakeBroker) session(c net.Conn) {
	defer c.Close()
	br := make([]byte, 1)
	for {
		if _, err := io.ReadFull(c, br); err != nil {
			return
		}
		pktType := br[0] >> 4
		n, err := readRemainingLength(c)
		if err != nil {
			return
		}
		body := make([]byte, n)
		if n > 0 {
			if _, err := io.ReadFull(c, body); err != nil {
				return
			}
		}

		switch pktType {
		case 1: // CONNECT
			b.connects.Add(1)
			// CONNACK: session present 0, return code 0 (accepted)
			if _, err := c.Write([]byte{0x20, 0x02, 0x00, 0x00}); err != nil {
				return
			}
		case 3: // PUBLISH — acknowledge QoS 1 so the client does not stall
			if (br[0]>>1)&0x03 == 1 && len(body) >= 2 {
				topicLen := int(binary.BigEndian.Uint16(body[:2]))
				if len(body) >= 2+topicLen+2 {
					id := body[2+topicLen : 2+topicLen+2]
					c.Write([]byte{0x40, 0x02, id[0], id[1]})
				}
			}
		case 8: // SUBSCRIBE — SUBACK granting QoS 0
			if len(body) >= 2 {
				c.Write([]byte{0x90, 0x03, body[0], body[1], 0x00})
			}
		case 12: // PINGREQ
			c.Write([]byte{0xD0, 0x00})
		case 14: // DISCONNECT
			return
		}
	}
}

func readRemainingLength(r io.Reader) (int, error) {
	var value, multiplier int
	buf := make([]byte, 1)
	for i := 0; i < 4; i++ {
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, err
		}
		value += int(buf[0]&0x7F) << multiplier
		if buf[0]&0x80 == 0 {
			return value, nil
		}
		multiplier += 7
	}
	return 0, fmt.Errorf("malformed remaining length")
}

// The whole point of MESHSAT-1027: a first connect that fails must not be the
// end of it. A kit powered on before its WiFi associates used to stay off the
// Hub for the life of the process, and — because the satellite fallback only
// disarms from the MQTT connect handler — kept paying for Iridium or SMS
// uplinks long after the Hub was reachable again.
func TestStart_RetriesAfterARefusedFirstConnect(t *testing.T) {
	// Claim a port, then let it go, so the first connect is refused against an
	// address nothing is listening on. The broker takes the same port after.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()

	// Keep the test quick: a short first wait and a short retry gap.
	origWait, origRetry := initialConnectWait, connectRetryInterval
	initialConnectWait = 300 * time.Millisecond
	connectRetryInterval = 200 * time.Millisecond
	defer func() { initialConnectWait, connectRetryInterval = origWait, origRetry }()

	var fallbackArmed, fallbackCleared atomic.Bool

	r := NewHubReporter(ReporterConfig{
		HubURL:   "tcp://" + addr,
		BridgeID: "test-bridge",
	}, func() BridgeBirth { return BridgeBirth{BridgeID: "test-bridge"} },
		func() BridgeHealth { return BridgeHealth{BridgeID: "test-bridge"} })

	r.SetConnectionHooks(
		func() { fallbackCleared.Store(true) }, // onConnect
		func() { fallbackArmed.Store(true) },   // onLost
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = r.Start(ctx)
	if err == nil {
		t.Fatal("Start returned nil against a refused endpoint; the caller can no longer tell it must arm a fallback")
	}
	if !errors.Is(err, ErrConnectPending) {
		t.Fatalf("err = %v, want ErrConnectPending so the caller knows to keep waiting", err)
	}
	if r.IsConnected() {
		t.Fatal("reporter claims to be connected with nothing listening")
	}

	// The Hub appears.
	b := newFakeBroker(t, addr)

	deadline := time.Now().Add(10 * time.Second)
	for !r.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !r.IsConnected() {
		t.Fatalf("reporter never connected after the broker appeared (%d connect attempts reached it)", b.connects.Load())
	}
	if !fallbackCleared.Load() {
		t.Error("the connect hook never ran, so a satellite fallback armed at boot would never disarm")
	}
	r.Stop()
}

// Unusable TLS material is a configuration fault. Retrying it forever would
// never succeed and would surface as broker authentication failures rather than
// as the bad PEM it is.
func TestStart_BadClientCertificateFailsLoudly(t *testing.T) {
	r := NewHubReporter(ReporterConfig{
		HubURL:      "ssl://127.0.0.1:8883",
		BridgeID:    "test-bridge",
		TLSCertPEM:  []byte("-----BEGIN CERTIFICATE-----\nnot a certificate\n-----END CERTIFICATE-----\n"),
		TLSKeyPEM:   []byte("-----BEGIN PRIVATE KEY-----\nnot a key\n-----END PRIVATE KEY-----\n"),
		TLSInsecure: true,
	}, func() BridgeBirth { return BridgeBirth{} }, func() BridgeHealth { return BridgeHealth{} })

	err := r.Start(context.Background())
	if err == nil {
		t.Fatal("Start accepted an unparseable client certificate")
	}
	if errors.Is(err, ErrConnectPending) {
		t.Errorf("err = %v, want a configuration error rather than a pending connect", err)
	}
}
