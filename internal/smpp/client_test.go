package smpp_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"meshsat/internal/smpp"
	"meshsat/internal/smpp/smpptest"
)

func startServer(t *testing.T) *smpptest.Server {
	t.Helper()
	srv, err := smpptest.New()
	if err != nil {
		t.Fatal(err)
	}
	srv.SystemID, srv.Password = "meshsat-bridge", "pw"
	t.Cleanup(srv.Close)
	return srv
}

type recorder struct {
	mu      sync.Mutex
	states  []smpp.State
	errs    []error
	delivs  []smpp.SM
	alerts  []smpp.Alert
	deliver uint32
}

func (r *recorder) callbacks() smpp.Callbacks {
	return smpp.Callbacks{
		OnDeliver: func(sm smpp.SM) uint32 {
			r.mu.Lock()
			r.delivs = append(r.delivs, sm)
			st := r.deliver
			r.mu.Unlock()
			return st
		},
		OnAlert: func(a smpp.Alert) {
			r.mu.Lock()
			r.alerts = append(r.alerts, a)
			r.mu.Unlock()
		},
		OnState: func(s smpp.State, err error) {
			r.mu.Lock()
			r.states = append(r.states, s)
			r.errs = append(r.errs, err)
			r.mu.Unlock()
		},
	}
}

func (r *recorder) lastErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.errs) - 1; i >= 0; i-- {
		if r.errs[i] != nil {
			return r.errs[i]
		}
	}
	return nil
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func fastConfig(addr string) smpp.Config {
	return smpp.Config{Addr: addr, SystemID: "meshsat-bridge", Password: "pw",
		EnquireEvery: 50 * time.Millisecond, EnquireTimeout: 100 * time.Millisecond,
		ReconnectMin: 20 * time.Millisecond, ReconnectMax: 200 * time.Millisecond, SubmitTimeout: time.Second}
}

func TestClientBindsSubmitsAndReceives(t *testing.T) {
	srv := startServer(t)
	srv.OnSubmit = func(sm smpp.SM) (uint32, time.Duration, string) {
		if sm.DestAddr == "2002" {
			return smpp.StatusInvalidDstAddr, 0, ""
		}
		return smpp.StatusOK, 30 * time.Millisecond, ""
	}
	rec := &recorder{}
	c := smpp.New(fastConfig(srv.Addr), rec.callbacks())
	ctx, cancel := context.WithCancel(context.Background())
	go c.Run(ctx)
	waitFor(t, "bound", 2*time.Second, c.Bound)

	// submit_sm: accepted after the server's "paging" delay; refused for 2002.
	_, status, err := c.Submit(ctx, smpp.SM{SourceAddr: "1000", DestAddr: "1001", ESMClass: smpp.ESMModeForward, ShortMessage: []byte("hi")})
	if err != nil || status != smpp.StatusOK {
		t.Fatalf("submit 1001: status %x err %v", status, err)
	}
	_, status, err = c.Submit(ctx, smpp.SM{SourceAddr: "1000", DestAddr: "2002", ShortMessage: []byte("hi")})
	if err != nil || status != smpp.StatusInvalidDstAddr {
		t.Fatalf("submit 2002: status %x err %v", status, err)
	}
	if got := srv.Submits(); len(got) != 2 || got[0].DestAddr != "1001" || got[1].DestAddr != "2002" {
		t.Fatalf("server saw %+v", got)
	}

	// deliver_sm: the handler's status goes back as deliver_sm_resp.
	st, err := srv.Deliver(smpp.SM{SourceTON: 3, SourceNPI: 1, SourceAddr: "2002", DestAddr: "1000", DataCoding: smpp.DCSGSM7, ShortMessage: []byte{0x59, 0x6F}}, 5*time.Second)
	if err != nil || st != smpp.StatusOK {
		t.Fatalf("deliver resp %x %v", st, err)
	}
	rec.mu.Lock()
	rec.deliver = smpp.StatusInvalidDstAddr
	rec.mu.Unlock()
	st, err = srv.Deliver(smpp.SM{SourceAddr: "2002", DestAddr: "4444", ShortMessage: []byte("x")}, 5*time.Second)
	if err != nil || st != smpp.StatusInvalidDstAddr {
		t.Fatalf("refused deliver resp %x %v", st, err)
	}
	rec.mu.Lock()
	n := len(rec.delivs)
	rec.mu.Unlock()
	if n != 2 {
		t.Fatalf("handler saw %d delivers", n)
	}

	// alert_notification reaches the callback.
	if err := srv.Alert("1001", 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "alert", 2*time.Second, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.alerts) == 1 && rec.alerts[0].SourceAddr == "1001" && rec.alerts[0].Availability() == 0
	})

	// enquire_link keeps running and measures an RTT.
	waitFor(t, "enquire rtt", 2*time.Second, func() bool { return c.Status().LastRxAt != nil && c.Status().Submits == 2 })

	// A clean stop unbinds: the server's route empties.
	cancel()
	c.Wait()
	waitFor(t, "unbound", 2*time.Second, func() bool { return srv.Current() == nil })
	if s := c.Status(); s.State != smpp.StateDisconnected || s.Delivers != 2 || s.Alerts != 1 {
		t.Fatalf("final status %+v", s)
	}
}

func TestClientBindRefusedBacksOff(t *testing.T) {
	srv := startServer(t)
	rec := &recorder{}
	cfg := fastConfig(srv.Addr)
	cfg.Password = "wrong"
	c := smpp.New(cfg, rec.callbacks())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	waitFor(t, "refusal", 2*time.Second, func() bool { return c.Status().BindRefusals >= 1 })
	if err := rec.lastErr(); !errors.Is(err, smpp.ErrBindRefused) {
		t.Fatalf("want ErrBindRefused, got %v", err)
	}
	if _, _, err := c.Submit(ctx, smpp.SM{DestAddr: "1001", ShortMessage: []byte("x")}); !errors.Is(err, smpp.ErrNotBound) {
		t.Fatalf("submit while unbound: %v", err)
	}
	// With the long backoff a wrong password does not hammer the server.
	time.Sleep(150 * time.Millisecond)
	if n := c.Status().BindRefusals; n > 2 {
		t.Fatalf("%d bind attempts in 150 ms with ReconnectMax 200 ms", n)
	}
	cancel()
	c.Wait()
}

func TestClientReconnectsAfterServerUnbindAndDrop(t *testing.T) {
	srv := startServer(t)
	rec := &recorder{}
	c := smpp.New(fastConfig(srv.Addr), rec.callbacks())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	waitFor(t, "bound", 2*time.Second, c.Bound)

	// osmo-msc restart: unbind then close.
	if err := srv.Unbind(); err != nil {
		t.Fatalf("unbind: %v", err)
	}
	waitFor(t, "rebound after unbind", 3*time.Second, func() bool { return srv.Binds() == 2 && c.Bound() })

	// A dropped socket, no word.
	srv.Drop()
	waitFor(t, "rebound after drop", 3*time.Second, func() bool { return srv.Binds() == 3 && c.Bound() })

	// A silent server: enquire_link gets no answer, the session is dead.
	srv.AnswerEnquire.Store(false)
	waitFor(t, "enquire timeout noticed", 3*time.Second, func() bool {
		err := rec.lastErr()
		return err != nil && errors.Is(err, smpp.ErrTimeout)
	})
	srv.AnswerEnquire.Store(true)
	waitFor(t, "rebound after enquire timeout", 3*time.Second, func() bool { return srv.Binds() >= 4 && c.Bound() })
	if c.Status().Reconnects < 3 {
		t.Fatalf("reconnects %d", c.Status().Reconnects)
	}
}

// Two clients with the same system id: the route follows the newest bind,
// the first still believes it is bound (nothing tells it), and a deliver
// goes to the second only. When the second closes the route is empty.
func TestDisplacedBind(t *testing.T) {
	srv := startServer(t)
	recA, recB := &recorder{}, &recorder{}
	a := smpp.New(fastConfig(srv.Addr), recA.callbacks())
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	go a.Run(ctxA)
	waitFor(t, "a bound", 2*time.Second, a.Bound)

	b := smpp.New(fastConfig(srv.Addr), recB.callbacks())
	ctxB, cancelB := context.WithCancel(context.Background())
	go b.Run(ctxB)
	waitFor(t, "b bound", 2*time.Second, func() bool { return b.Bound() && srv.Binds() == 2 })

	if _, err := srv.Deliver(smpp.SM{SourceAddr: "1001", DestAddr: "1000", ShortMessage: []byte("x")}, time.Second); err != nil {
		t.Fatal(err)
	}
	recA.mu.Lock()
	na := len(recA.delivs)
	recA.mu.Unlock()
	recB.mu.Lock()
	nb := len(recB.delivs)
	recB.mu.Unlock()
	if na != 0 || nb != 1 {
		t.Fatalf("deliver went to a=%d b=%d, want the newest bind only", na, nb)
	}
	if !a.Bound() {
		t.Fatal("the displaced client cannot tell: it still answers enquire_link and stays bound")
	}
	cancelB()
	b.Wait()
	waitFor(t, "route empty", 2*time.Second, func() bool { return srv.Current() == nil })
	if _, err := srv.Deliver(smpp.SM{SourceAddr: "1001", DestAddr: "1000", ShortMessage: []byte("x")}, time.Second); err == nil {
		t.Fatal("with the route empty a phone's text fails (RP cause 38); the first client is not found again")
	}
}

func TestSubmitTimeout(t *testing.T) {
	srv := startServer(t)
	srv.OnSubmit = func(sm smpp.SM) (uint32, time.Duration, string) { return smpp.StatusOK, 2 * time.Second, "" }
	cfg := fastConfig(srv.Addr)
	cfg.SubmitTimeout = 100 * time.Millisecond
	c := smpp.New(cfg, smpp.Callbacks{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	waitFor(t, "bound", 2*time.Second, c.Bound)
	if _, _, err := c.Submit(ctx, smpp.SM{DestAddr: "1001", ShortMessage: []byte("x")}); !errors.Is(err, smpp.ErrTimeout) {
		t.Fatalf("want timeout, got %v", err)
	}
	if !c.Bound() {
		t.Fatal("a slow submit_sm_resp does not end the session")
	}
}
