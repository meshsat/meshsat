package interop

// Measurement, not a gate [MESHSAT-1548]: what a stock LXMF client shows when
// its message crosses a slow paid link between two bridges.
//
//	Python A (sender) -TCP- bridge T1 =sat= bridge T2 -TCP- Python B (receiver)
//
// "sat" is a paid interface (never floodable) opened to A's and B's delivery
// destinations with a PaidPolicy, and every packet on it is held for
// MESHSAT_SLOWLINK_DELAY (default 30s) each way, like an Iridium hop through
// the Hub. Runs only with MESHSAT_SLOWLINK=1 because one run takes minutes:
//
//	MESHSAT_INTEROP=1 MESHSAT_SLOWLINK=1 MESHSAT_SLOWLINK_DELAY=30s \
//	  go test -run TestMeasureLXMFAcrossSlowPaidLink -v -timeout 15m ./internal/interop/

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"meshsat/internal/reticulum"
	"meshsat/internal/rns"
	"meshsat/internal/routing"
)

type slowTx struct {
	tcp   *routing.TCPInterface
	peer  func() *rns.Node
	delay time.Duration
	mu    sync.Mutex
	sat   int
	queue chan satFrame // FIFO, like one modem's MO queue: order is kept
	once  sync.Once
}

type satFrame struct {
	raw []byte
	due time.Time
}

func (x *slowTx) Transmit(ifaceID string, raw []byte) error {
	if ifaceID == "sat" {
		x.once.Do(func() {
			x.queue = make(chan satFrame, 1024)
			go func() {
				for f := range x.queue {
					time.Sleep(time.Until(f.due))
					x.peer().Inbound(f.raw, "sat")
				}
			}()
		})
		x.mu.Lock()
		x.sat++
		x.mu.Unlock()
		x.queue <- satFrame{raw: append([]byte(nil), raw...), due: time.Now().Add(x.delay)}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return x.tcp.Send(ctx, raw)
}
func (x *slowTx) Floodable() []string {
	if x.tcp.IsOnline() {
		return []string{"tcp_0"}
	}
	return nil
}
func (x *slowTx) HWMTU(string) int   { return 500 }
func (x *slowTx) Bitrate(string) int { return 0 }
func (x *slowTx) satCount() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.sat
}

func slowBridge(t *testing.T, ctx context.Context, delay time.Duration, peer func() *rns.Node) (*rns.Node, *slowTx, int) {
	t.Helper()
	id, _ := reticulum.GenerateIdentity()
	port := freePort(t)
	var node *rns.Node
	tcp := routing.NewTCPInterface(routing.TCPInterfaceConfig{Name: "tcp_0", ListenAddr: fmt.Sprintf("127.0.0.1:%d", port)},
		func(packet []byte) { node.Inbound(packet, "tcp_0") })
	tx := &slowTx{tcp: tcp, peer: peer, delay: delay}
	n, err := rns.New(rns.Config{Identity: id}, tx)
	if err != nil {
		t.Fatal(err)
	}
	node = n
	if err := tcp.Start(ctx); err != nil {
		t.Fatal(err)
	}
	n.Start(ctx)
	t.Cleanup(tcp.Stop)
	return n, tx, port
}

func TestMeasureLXMFAcrossSlowPaidLink(t *testing.T) {
	if os.Getenv("MESHSAT_SLOWLINK") != "1" {
		t.Skip("measurement; set MESHSAT_SLOWLINK=1 (takes minutes)")
	}
	delay := 30 * time.Second
	if v := os.Getenv("MESHSAT_SLOWLINK_DELAY"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatal(err)
		}
		delay = d
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var t1, t2 *rns.Node
	t1, tx1, port1 := slowBridge(t, ctx, delay, func() *rns.Node { return t2 })
	t2, tx2, port2 := slowBridge(t, ctx, delay, func() *rns.Node { return t1 })

	loglevel := 0
	if v := os.Getenv("MESHSAT_SLOWLINK_LOGLEVEL"); v != "" {
		if _, err := fmt.Sscan(v, &loglevel); err != nil {
			t.Fatal(err)
		}
	}
	a := startPeer(t, peerOpts{name: "A", connect: fmt.Sprintf("127.0.0.1:%d", port1), loglevel: loglevel})
	b := startPeer(t, peerOpts{name: "B", connect: fmt.Sprintf("127.0.0.1:%d", port2), loglevel: loglevel})
	// MESHSAT_SLOWLINK_LOGDIR keeps both peers' rns.log for reading afterwards.
	if dir := os.Getenv("MESHSAT_SLOWLINK_LOGDIR"); dir != "" {
		t.Cleanup(func() {
			for name, p := range map[string]*peer{"A": a, "B": b} {
				if data, err := os.ReadFile(filepath.Join(p.cfgDir, "rns.log")); err == nil {
					_ = os.WriteFile(filepath.Join(dir, "rns-"+name+".log"), data, 0o644)
				}
			}
		})
	}
	extra := 0
	if v := os.Getenv("MESHSAT_SLOWLINK_EXTRA_HOPS"); v != "" {
		if _, err := fmt.Sscan(v, &extra); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []*rns.Node{t1, t2} {
		n.SetPaidPolicy("sat", &rns.PaidPolicy{Dests: [][16]byte{h16(a.Delivery), h16(b.Delivery)}, ExtraHops: extra})
	}
	time.Sleep(2 * time.Second) // both TCP clients attached

	b.send(event{"cmd": "announce"})
	a.send(event{"cmd": "announce"})
	start := time.Now()
	a.wait("announce", func(e event) bool { return e["dest"] == b.Delivery }, delay+30*time.Second)
	t.Logf("A learned B after %s (one-way delay %s); hops %v", time.Since(start).Round(time.Second), delay, t1.Paths().Hops(h16(b.Delivery))+1)
	b.wait("announce", func(e event) bool { return e["dest"] == a.Delivery }, delay+30*time.Second)
	time.Sleep(3 * time.Second)

	before := tx1.satCount()
	beforeBack := tx2.satCount()
	sendAt := time.Now()
	method := os.Getenv("MESHSAT_SLOWLINK_METHOD")
	if method == "" {
		method = "opportunistic"
	}
	a.send(event{"cmd": "send_lxm", "dest": b.Delivery, "content": "hello from the far mesh", "method": method})
	a.wait("lxm_sent", nil, 10*time.Second)

	var recvAfter, resultAfter time.Duration
	result := ""
	deadline := time.After(6*delay + 4*time.Minute)
	for result == "" || recvAfter == 0 {
		select {
		case ev, ok := <-b.events:
			if ok && ev["event"] == "lxm_received" && recvAfter == 0 {
				recvAfter = time.Since(sendAt)
			}
		case ev, ok := <-a.events:
			if ok && (ev["event"] == "lxm_delivered" || ev["event"] == "lxm_failed") && result == "" {
				result = ev["event"].(string)
				resultAfter = time.Since(sendAt)
			}
		case <-deadline:
			t.Logf("deadline: result=%q received=%v", result, recvAfter > 0)
			result += " (deadline)"
			recvAfter = -1
		}
	}
	time.Sleep(delay + 2*time.Second) // let late copies land before counting
	t.Logf("MEASURE method=%s extra_hops=%d delay=%s received_after=%s sender_result=%s after=%s data_packets_on_sat=%d proofs_back_on_sat=%d",
		method, extra, delay, recvAfter.Round(time.Second), result, resultAfter.Round(time.Second),
		tx1.satCount()-before, tx2.satCount()-beforeBack)

	// MESHSAT_SLOWLINK_REPLY=1: B answers with the method Sideband and
	// MeshChat would pick on their own ("auto": opportunistic when no link
	// exists and A announced a ratchet, else direct over the link A opened).
	if os.Getenv("MESHSAT_SLOWLINK_REPLY") != "1" || recvAfter <= 0 {
		return
	}
	fromB, fromA := tx2.satCount(), tx1.satCount()
	replyAt := time.Now()
	b.send(event{"cmd": "send_lxm", "dest": a.Delivery, "content": "reply from the grid", "method": "auto"})
	sent := b.wait("lxm_sent", nil, 10*time.Second)
	var replyRecv, replyResultAfter time.Duration
	replyResult := ""
	replyDeadline := time.After(6*delay + 4*time.Minute)
wait:
	for replyResult == "" || replyRecv == 0 {
		select {
		case ev, ok := <-a.events:
			if ok && ev["event"] == "lxm_received" && replyRecv == 0 {
				replyRecv = time.Since(replyAt)
			}
		case ev, ok := <-b.events:
			if ok && (ev["event"] == "lxm_delivered" || ev["event"] == "lxm_failed") && replyResult == "" {
				replyResult = ev["event"].(string)
				replyResultAfter = time.Since(replyAt)
			}
		case <-replyDeadline:
			replyResult += " (deadline)"
			break wait
		}
	}
	time.Sleep(delay + 2*time.Second)
	t.Logf("REPLY method_chosen=%v received_after=%s sender_result=%s after=%s packets_on_sat=%d proofs_back_on_sat=%d",
		sent["method"], replyRecv.Round(time.Second), replyResult, replyResultAfter.Round(time.Second),
		tx2.satCount()-fromB, tx1.satCount()-fromA)
}
