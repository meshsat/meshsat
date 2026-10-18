package hubreporter

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A bundle's topic root is "meshsat" or "meshsat/{tenant}"; anything that would
// move the bridge onto other topics is refused. [MESHSAT-1467]
func TestNewTopicsAcceptsOnlyATenantRoot(t *testing.T) {
	for prefix, want := range map[string]string{
		"":               "meshsat",
		"meshsat":        "meshsat",
		" meshsat/acme ": "meshsat/acme",
		"meshsat/t-1_x":  "meshsat/t-1_x",
	} {
		got, err := NewTopics(prefix)
		if err != nil || got.Root() != want {
			t.Errorf("NewTopics(%q) = %q, %v; want %q", prefix, got.Root(), err, want)
		}
	}
	for _, bad := range []string{
		"acme", "meshsat/", "meshsat/acme/x", "meshsat/+", "meshsat/#", "meshsat/a.b", "meshsat/a b",
		"meshsat/bridge", "meshsat/broadcast", "meshsat/hub", "meshsat/relay", "meshsat/reticulum",
		"other/acme", "meshsat/" + strings.Repeat("a", 65),
	} {
		if _, err := NewTopics(bad); err == nil {
			t.Errorf("NewTopics(%q) accepted", bad)
		}
	}
	if err := (ReporterConfig{HubURL: "tcp://x", BridgeID: "kit", TopicPrefix: "meshsat/+"}).Validate(); err == nil {
		t.Error("Validate accepted a wildcard topic root")
	}
}

// The platform tenant's topics are what they always were.
func TestPlatformTopicsUnchanged(t *testing.T) {
	p := platformTopics
	for got, want := range map[string]string{
		p.BridgeBirth("kit"):                 "meshsat/bridge/kit/birth",
		p.BridgeCmdResp("kit"):               "meshsat/bridge/kit/cmd/response",
		p.DeviceBirth("kit", "dev"):          "meshsat/bridge/kit/device/dev/birth",
		p.DevicePosition("dev"):              "meshsat/dev/position",
		p.TAKCoTOut("kit+1"):                 "meshsat/bridge/kit%2B1/tak/cot/out",
		TopicBridgeHealth("kit"):             "meshsat/bridge/kit/health",
		TopicDeviceMessage("300234"):         "meshsat/300234/mo/decoded",
		strings.Join(TAKCoTInFilters(), " "): "meshsat/broadcast/tak/cot/in meshsat/broadcast/tak/cot/in/+",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// A bridge in a customer tenant publishes, subscribes and leaves its will under
// that tenant's root and nowhere else: the broker confines its credential
// there, so one topic on the platform root would be refused. [MESHSAT-1467]
func TestATenantReporterUsesTheTenantRootEverywhere(t *testing.T) {
	b := newFakeBroker(t, "127.0.0.1:0")
	r := NewHubReporter(ReporterConfig{
		HubURL:      "tcp://" + b.addr(),
		BridgeID:    "kit-a",
		TopicPrefix: "meshsat/acme",
	}, func() BridgeBirth { return BridgeBirth{BridgeID: "kit-a"} },
		func() BridgeHealth { return BridgeHealth{BridgeID: "kit-a"} })
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()
	// Start returns at the CONNACK; paho runs the connect handler, which marks
	// the reporter connected, on its own goroutine just after.
	for deadline := time.Now().Add(10 * time.Second); !r.IsConnected(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("never connected")
		}
	}

	if err := r.PublishDevicePosition("dev-1", DevicePosition{}); err != nil {
		t.Fatalf("position: %v", err)
	}
	if _, err := r.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	ch := NewCommandHandler(r, "kit-a", func() BridgeHealth { return BridgeHealth{} })
	ch.publishResponse(CommandResponse{RequestID: "r1", Cmd: "ping", Status: "ok"})

	want := map[string]bool{
		"pub meshsat/acme/bridge/kit-a/birth":        false,
		"pub meshsat/acme/dev-1/position":            false,
		"pub meshsat/acme/bridge/kit-a/health":       false,
		"pub meshsat/acme/bridge/kit-a/cmd/response": false,
		"sub meshsat/acme/bridge/kit-a/cmd":          false,
		"sub meshsat/acme/broadcast/tak/cot/in":      false,
		"sub meshsat/acme/broadcast/tak/cot/in/+":    false,
		"will meshsat/acme/bridge/kit-a/death":       false,
	}
	var seen []string
	deadline := time.After(10 * time.Second)
	for missing := len(want); missing > 0; {
		var got string
		select {
		case p := <-b.pubs:
			got = "pub " + p.topic
		case s := <-b.subs:
			got = "sub " + s
		case w := <-b.wills:
			got = "will " + w
		case <-deadline:
			t.Fatalf("still missing after 10 s; saw %v, want %v", seen, want)
		}
		seen = append(seen, got)
		if !strings.Contains(got, " meshsat/acme/") {
			t.Errorf("%s: not under the tenant's root", got)
		}
		if done, ok := want[got]; ok && !done {
			want[got] = true
			missing--
		}
	}

	// The tenant's own export comes back on the tenant's delivery topic and is
	// dropped there, as on the platform root.
	if s := r.topics.TAKCoTSender("meshsat/acme/broadcast/tak/cot/in/kit-a"); s != "kit-a" {
		t.Errorf("sender of the tenant's own echo = %q", s)
	}
}
