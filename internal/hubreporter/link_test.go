package hubreporter

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// wrapped is an error whose message does not repeat its cause, as Java's
// exceptions are built (Android's HubConnectFailureTest).
type wrapped struct {
	msg   string
	cause error
}

func (w wrapped) Error() string { return w.msg }
func (w wrapped) Unwrap() error { return w.cause }

// IllegalStateException has no message, so its type name stands in.
type IllegalStateException struct{}

func (IllegalStateException) Error() string { return "" }

// Android's HubConnectFailureTest, case for case. [MESHSAT-1417]
func TestFailureText_AndroidCases(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{wrapped{"Unable to connect to server", errors.New("Chain validation failed")}, "Unable to connect to server: Chain validation failed"},
		{wrapped{"connect failed: timeout", errors.New("timeout")}, "connect failed: timeout"},
		{IllegalStateException{}, "IllegalStateException"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := FailureText(c.err); got != c.want {
			t.Errorf("FailureText(%#v) = %q, want %q", c.err, got, c.want)
		}
	}
}

// paho joins its own words and the network's error; the network's comes last
// and is the cause followed.
func TestFailureText_PahoJoinedError(t *testing.T) {
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	err := fmt.Errorf("%w : %w", errors.New("network Error"), dial)
	got := FailureText(err)
	if !strings.HasPrefix(got, "network Error : dial tcp") || !strings.HasSuffix(got, "connection refused") {
		t.Fatalf("FailureText = %q", got)
	}
}

// A Hub that cannot be reached is "error" with the reason between attempts;
// once it answers the link is "connected" and the reason goes; a lost session
// is "disconnected". [MESHSAT-1417]
func TestLinkState_FollowsTheConnects(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()

	origWait, origRetry := initialConnectWait, connectRetryInterval
	initialConnectWait = 300 * time.Millisecond
	connectRetryInterval = 400 * time.Millisecond
	defer func() { initialConnectWait, connectRetryInterval = origWait, origRetry }()

	r := NewHubReporter(ReporterConfig{HubURL: "tcp://" + addr, BridgeID: "test-bridge"},
		func() BridgeBirth { return BridgeBirth{BridgeID: "test-bridge"} },
		func() BridgeHealth { return BridgeHealth{BridgeID: "test-bridge"} })
	if state, _ := r.LinkState(); state != LinkDisconnected {
		t.Fatalf("before Start: %q", state)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); !errors.Is(err, ErrConnectPending) {
		t.Fatalf("Start = %v", err)
	}
	state, reason := r.LinkState()
	if state != LinkError || !strings.Contains(reason, "refused") {
		t.Fatalf("with nothing listening: %q %q", state, reason)
	}
	if _, err := r.Ping(); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Ping while not connected = %v", err)
	}

	b := newFakeBroker(t, addr)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if state, _ = r.LinkState(); state == LinkConnected {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	state, reason = r.LinkState()
	if state != LinkConnected || reason != "" {
		t.Fatalf("after the broker appeared: %q %q", state, reason)
	}
	elapsed, err := r.Ping()
	if err != nil || elapsed < 0 || elapsed > pingWait {
		t.Fatalf("Ping = %v, %v", elapsed, err)
	}

	b.Close()
	b.dropSessions()
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if state, _ = r.LinkState(); state == LinkDisconnected {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if state != LinkDisconnected {
		t.Fatalf("after the broker went: %q", state)
	}
	r.Stop()
	if state, _ = r.LinkState(); state != LinkDisconnected {
		t.Fatalf("after Stop: %q", state)
	}
}

// Unusable TLS material is "error" for good, with the reason. [MESHSAT-1417]
func TestLinkState_BadCertificateIsAnError(t *testing.T) {
	r := NewHubReporter(ReporterConfig{
		HubURL:      "ssl://127.0.0.1:8883",
		BridgeID:    "test-bridge",
		TLSCertPEM:  []byte("-----BEGIN CERTIFICATE-----\nnot a certificate\n-----END CERTIFICATE-----\n"),
		TLSKeyPEM:   []byte("-----BEGIN PRIVATE KEY-----\nnot a key\n-----END PRIVATE KEY-----\n"),
		TLSInsecure: true,
	}, func() BridgeBirth { return BridgeBirth{} }, func() BridgeHealth { return BridgeHealth{} })
	_ = r.Start(context.Background())
	state, reason := r.LinkState()
	if state != LinkError || !strings.Contains(reason, "certificate") {
		t.Fatalf("bad certificate: %q %q", state, reason)
	}
}
