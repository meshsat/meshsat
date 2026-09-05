package gateway

import (
	"strings"
	"testing"
)

// StartGatewayInstance parks a nil sentinel in the running map while an
// instance is being created; a stop that lands in that window must return
// an error instead of dereferencing the sentinel. [MESHSAT-786]
func TestStopGatewayInstance_StartingSentinel(t *testing.T) {
	m := &Manager{
		running:        map[string]Gateway{"zigbee_0": nil},
		runningByIface: map[string]Gateway{},
	}
	err := m.StopGatewayInstance("zigbee_0")
	if err == nil || !strings.Contains(err.Error(), "is starting") {
		t.Fatalf("want an 'is starting' error, got %v", err)
	}
	if _, still := m.running["zigbee_0"]; !still {
		t.Fatal("the starting sentinel must be left in place")
	}
	if err := m.StopGatewayInstance("absent_0"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("want a 'not running' error, got %v", err)
	}
}
