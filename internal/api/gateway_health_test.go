package api

import (
	"testing"

	"meshsat/internal/gateway"
)

// A modem that keeps its serial link but has stopped answering must not read
// as healthy: gateway rows carry the device health state beside the
// unchanged connected flag. [MESHSAT-1064]
func TestApplyDeviceHealth_AddsStateBesideConnected(t *testing.T) {
	dh := gateway.NewDeviceHealth(gateway.DeviceHealthConfig{}, gateway.DeviceHealthActions{})
	dh.RegisterExternal("cellular", []string{"cellular_0", "sms_0"}, func() (string, string) {
		return gateway.HealthStateHealing, "step 1, level 1, serial reconnect"
	})
	gws := []gateway.GatewayStatusResponse{
		{Type: "cellular", InstanceID: "cellular_0", Connected: true},
		{Type: "zigbee", InstanceID: "zigbee_0", Connected: true},
	}

	applyDeviceHealth(dh, gws)
	if gws[0].HealthState != gateway.HealthStateHealing || gws[0].HealthDetail == "" || !gws[0].Connected {
		t.Fatalf("cellular row: %+v", gws[0])
	}
	if gws[1].HealthState != "" || gws[1].HealthDetail != "" {
		t.Fatalf("uncovered gateway got a health state: %+v", gws[1])
	}

	applyDeviceHealth(nil, gws) // no watchdog: rows untouched, no panic
}
