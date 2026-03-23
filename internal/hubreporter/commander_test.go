package hubreporter

import (
	"encoding/json"
	"testing"
	"time"
)

func TestHandleCommandPing(t *testing.T) {
	healthCalled := false
	healthFn := func() BridgeHealth {
		healthCalled = true
		return BridgeHealth{
			UptimeSec: 3600,
			CPUPct:    12.5,
			MemPct:    45.0,
		}
	}

	// Create handler without a connected reporter (responses won't publish, but handler logic runs).
	ch := NewCommandHandler(nil, "test-bridge", healthFn)

	cmd := Command{
		Protocol:  ProtocolVersion,
		Cmd:       "ping",
		RequestID: "req-001",
		Timestamp: time.Now().UTC(),
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}

	// HandleCommand should not panic even without a connected reporter.
	ch.HandleCommand(payload)

	if !healthCalled {
		t.Error("expected health function to be called for ping command")
	}
}

func TestHandleCommandUnknown(t *testing.T) {
	healthFn := func() BridgeHealth { return BridgeHealth{} }
	ch := NewCommandHandler(nil, "test-bridge", healthFn)

	cmd := Command{
		Protocol:  ProtocolVersion,
		Cmd:       "nonexistent_command",
		RequestID: "req-002",
		Timestamp: time.Now().UTC(),
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}

	// Should not panic — logs warning and attempts to send error response.
	ch.HandleCommand(payload)
}

func TestHandleCommandInvalidJSON(t *testing.T) {
	healthFn := func() BridgeHealth { return BridgeHealth{} }
	ch := NewCommandHandler(nil, "test-bridge", healthFn)

	// Invalid JSON should not panic.
	ch.HandleCommand([]byte(`{invalid json`))
}

func TestHandleCommandWrongProtocol(t *testing.T) {
	healthFn := func() BridgeHealth { return BridgeHealth{} }
	ch := NewCommandHandler(nil, "test-bridge", healthFn)

	cmd := Command{
		Protocol:  "unknown-protocol/v99",
		Cmd:       "ping",
		RequestID: "req-003",
		Timestamp: time.Now().UTC(),
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}

	// Should not panic — logs warning about unknown protocol.
	ch.HandleCommand(payload)
}

func TestHandleCommandReboot(t *testing.T) {
	healthFn := func() BridgeHealth { return BridgeHealth{} }
	ch := NewCommandHandler(nil, "test-bridge", healthFn)

	cmd := Command{
		Protocol:  ProtocolVersion,
		Cmd:       "reboot",
		RequestID: "req-004",
		Timestamp: time.Now().UTC(),
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}

	// Reboot should not panic and should NOT actually reboot.
	ch.HandleCommand(payload)
}

func TestHandleCommandFlushBurst(t *testing.T) {
	healthFn := func() BridgeHealth { return BridgeHealth{} }
	ch := NewCommandHandler(nil, "test-bridge", healthFn)

	cmd := Command{
		Protocol:  ProtocolVersion,
		Cmd:       "flush_burst",
		RequestID: "req-005",
		Timestamp: time.Now().UTC(),
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}

	ch.HandleCommand(payload)
}

func TestHandleCommandSendMT(t *testing.T) {
	healthFn := func() BridgeHealth { return BridgeHealth{} }
	ch := NewCommandHandler(nil, "test-bridge", healthFn)

	cmd := Command{
		Protocol:     ProtocolVersion,
		Cmd:          "send_mt",
		RequestID:    "req-006",
		TargetDevice: "300234567890123",
		Payload:      json.RawMessage(`{"text":"Hello from Hub"}`),
		Timestamp:    time.Now().UTC(),
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}

	ch.HandleCommand(payload)
}
