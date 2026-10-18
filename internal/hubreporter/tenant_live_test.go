package hubreporter

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// The tenant topic root against the real Hub (MESHSAT-1467, HeMB-NG C13 and
// C4.1). Off by default. HUB_TENANT_LIVE_BUNDLE names a provisioning bundle (the
// JSON the Hub's POST /api/bridges/{id}/provision returns) for a bridge in a
// test tenant; HUB_TENANT_LIVE_PREFIX, when set, replaces the bundle's
// mqtt_topic_prefix (an empty value puts the bridge on the platform root, which
// is the defect this fixed). The test connects with the real reporter, publishes
// the birth and a health ping, and also publishes on its own msg/in and
// msg/status, which the broker must refuse. What the Hub and the broker made of
// it is read on their side; the client gets a PUBACK either way. HUB_TENANT_LIVE_HOLD
// (a Go duration) keeps the link up that long at the end.
func TestHubTenantLive(t *testing.T) {
	path := os.Getenv("HUB_TENANT_LIVE_BUNDLE")
	if path == "" {
		t.Skip("HUB_TENANT_LIVE_BUNDLE not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		BridgeID string `json:"bid"`
		MQTT     string `json:"mqtt"`
		Prefix   string `json:"mqtt_topic_prefix"`
		User     string `json:"user"`
		Pass     string `json:"pass"`
		Cert     string `json:"cert"`
		Key      string `json:"key"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	prefix := b.Prefix
	if p, ok := os.LookupEnv("HUB_TENANT_LIVE_PREFIX"); ok {
		prefix = p
	}
	r := NewHubReporter(ReporterConfig{
		HubURL: b.MQTT, BridgeID: b.BridgeID, Username: b.User, Password: b.Pass,
		TLSCertPEM: []byte(b.Cert), TLSKeyPEM: []byte(b.Key), TopicPrefix: prefix,
	}, func() BridgeBirth { return BridgeBirth{BridgeID: b.BridgeID, Version: "c13-live-probe"} },
		func() BridgeHealth { return BridgeHealth{BridgeID: b.BridgeID} })
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()
	t.Logf("connected as %s on root %q", b.BridgeID, r.topics.Root())
	elapsed, err := r.Ping()
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Logf("health ping acknowledged in %s", elapsed)
	for _, suffix := range []string{"msg/in", "msg/status"} {
		topic := r.topics.Root() + "/bridge/" + b.BridgeID + "/" + suffix
		err := r.publish(topic, 1, false, map[string]string{"probe": "c4.1"})
		t.Logf("published %s: client error %v", topic, err)
	}
	hold := 5 * time.Second
	if s, err := time.ParseDuration(os.Getenv("HUB_TENANT_LIVE_HOLD")); err == nil {
		hold = s // time for the caller to read the Hub's view while the link is up
	}
	time.Sleep(hold)
}
