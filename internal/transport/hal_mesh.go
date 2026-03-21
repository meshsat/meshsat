package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// HALMeshTransport implements MeshTransport by talking to HAL's REST/SSE API.
type HALMeshTransport struct {
	halURL string
	apiKey string
	client *http.Client
	cancel context.CancelFunc
}

// NewHALMeshTransport creates a new HAL-backed mesh transport.
func NewHALMeshTransport(halURL, apiKey string) *HALMeshTransport {
	return &HALMeshTransport{
		halURL: strings.TrimRight(halURL, "/"),
		apiKey: apiKey,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Subscribe opens an SSE connection to HAL's Meshtastic event stream.
// Returns a channel that emits MeshEvents. Auto-reconnects on disconnect.
func (t *HALMeshTransport) Subscribe(ctx context.Context) (<-chan MeshEvent, error) {
	ch := make(chan MeshEvent, 64)
	ctx, t.cancel = context.WithCancel(ctx)

	go t.sseLoop(ctx, ch)
	return ch, nil
}

func (t *HALMeshTransport) sseLoop(ctx context.Context, ch chan<- MeshEvent) {
	defer close(ch)
	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		start := time.Now()
		err := t.readSSE(ctx, ch)
		if ctx.Err() != nil {
			return
		}

		connDuration := time.Since(start)

		// If the connection lasted more than 10 seconds, it was a real session
		// that eventually dropped — reset backoff so we reconnect quickly.
		if connDuration > 10*time.Second {
			backoff = time.Second
		}

		log.Warn().Err(err).Dur("backoff", backoff).Dur("was_connected", connDuration).Msg("meshtastic SSE disconnected, reconnecting")

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		// Exponential backoff: 1s → 2s → 4s → 5s cap (local HAL, reconnect fast)
		backoff *= 2
		if backoff > 5*time.Second {
			backoff = 5 * time.Second
		}
	}
}

func (t *HALMeshTransport) readSSE(ctx context.Context, ch chan<- MeshEvent) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.halURL+"/meshtastic/events", nil)
	if err != nil {
		return fmt.Errorf("create SSE request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if t.apiKey != "" {
		req.Header.Set("X-HAL-Key", t.apiKey)
	}

	// No timeout for SSE — use context cancellation
	sseClient := &http.Client{}
	resp, err := sseClient.Do(req)
	if err != nil {
		return fmt.Errorf("SSE connect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("SSE status %d: %s", resp.StatusCode, body)
	}

	log.Info().Msg("connected to HAL meshtastic SSE stream")

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var event MeshEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			log.Warn().Err(err).Str("data", data).Msg("failed to parse SSE event")
			continue
		}

		select {
		case ch <- event:
		case <-ctx.Done():
			return ctx.Err()
		default:
			log.Warn().Str("type", event.Type).Msg("event channel full, dropping event")
		}
	}
	return scanner.Err()
}

// SendMessage sends a text message through the mesh.
func (t *HALMeshTransport) SendMessage(ctx context.Context, req SendRequest) error {
	return t.postJSON(ctx, "/meshtastic/messages/send", req)
}

// SendRaw sends a raw packet through the mesh.
func (t *HALMeshTransport) SendRaw(ctx context.Context, req RawRequest) error {
	return t.postJSON(ctx, "/meshtastic/messages/send_raw", req)
}

// SendEncryptedRelay re-injects an encrypted payload via HAL (AES-256-CTR passthrough).
func (t *HALMeshTransport) SendEncryptedRelay(ctx context.Context, encryptedPayload []byte, to uint32, channel uint32, hopLimit uint32) error {
	req := struct {
		Payload  []byte `json:"payload"`
		To       uint32 `json:"to"`
		Channel  uint32 `json:"channel"`
		HopLimit uint32 `json:"hop_limit"`
	}{encryptedPayload, to, channel, hopLimit}
	return t.postJSON(ctx, "/meshtastic/messages/send_encrypted_relay", req)
}

// GetNodes returns all known mesh nodes.
func (t *HALMeshTransport) GetNodes(ctx context.Context) ([]MeshNode, error) {
	var resp struct {
		Count int        `json:"count"`
		Nodes []MeshNode `json:"nodes"`
	}
	if err := t.getJSON(ctx, "/meshtastic/nodes", &resp); err != nil {
		return nil, err
	}
	return resp.Nodes, nil
}

// GetStatus returns the Meshtastic connection status.
func (t *HALMeshTransport) GetStatus(ctx context.Context) (*MeshStatus, error) {
	var status MeshStatus
	if err := t.getJSON(ctx, "/meshtastic/status", &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// GetMessages returns recent messages from HAL's in-memory buffer.
func (t *HALMeshTransport) GetMessages(ctx context.Context, limit int) ([]MeshMessage, error) {
	var resp struct {
		Count    int           `json:"count"`
		Messages []MeshMessage `json:"messages"`
	}
	path := fmt.Sprintf("/meshtastic/messages?limit=%d", limit)
	if err := t.getJSON(ctx, path, &resp); err != nil {
		return nil, err
	}
	return resp.Messages, nil
}

// GetConfig retrieves the full Meshtastic device configuration from HAL.
func (t *HALMeshTransport) GetConfig(ctx context.Context) (map[string]interface{}, error) {
	var config map[string]interface{}
	if err := t.getJSON(ctx, "/meshtastic/config", &config); err != nil {
		return nil, err
	}
	return config, nil
}

// AdminReboot sends a reboot command to a remote node (Phase 2 HAL endpoint).
func (t *HALMeshTransport) AdminReboot(ctx context.Context, nodeNum uint32, delay int) error {
	return t.postJSON(ctx, "/meshtastic/admin/reboot", map[string]interface{}{
		"node_id":    nodeNum,
		"delay_secs": delay,
	})
}

// AdminFactoryReset sends a factory reset command (Phase 2 HAL endpoint).
func (t *HALMeshTransport) AdminFactoryReset(ctx context.Context, nodeNum uint32) error {
	return t.postJSON(ctx, "/meshtastic/admin/factory_reset", map[string]interface{}{
		"node_id": nodeNum,
	})
}

// Traceroute initiates a traceroute to a remote node (Phase 2 HAL endpoint).
func (t *HALMeshTransport) Traceroute(ctx context.Context, nodeNum uint32) error {
	return t.postJSON(ctx, "/meshtastic/admin/traceroute", map[string]interface{}{
		"node_id": nodeNum,
	})
}

// SetRadioConfig sets radio configuration (Phase 2 HAL endpoint).
func (t *HALMeshTransport) SetRadioConfig(ctx context.Context, section string, data json.RawMessage) error {
	return t.postJSON(ctx, "/meshtastic/config/radio", map[string]interface{}{
		"section":     section,
		"config_data": data,
	})
}

// SetModuleConfig sets module configuration (Phase 2 HAL endpoint).
func (t *HALMeshTransport) SetModuleConfig(ctx context.Context, section string, data json.RawMessage) error {
	return t.postJSON(ctx, "/meshtastic/config/module", map[string]interface{}{
		"section":     section,
		"config_data": data,
	})
}

// SetChannel configures a radio channel.
func (t *HALMeshTransport) SetChannel(ctx context.Context, req ChannelRequest) error {
	return t.postJSON(ctx, "/meshtastic/channel", req)
}

// SendWaypoint sends a waypoint to the mesh (Phase 2 HAL endpoint).
func (t *HALMeshTransport) SendWaypoint(ctx context.Context, wp Waypoint) error {
	return t.postJSON(ctx, "/meshtastic/waypoints", wp)
}

// RemoveNode sends a remove_by_nodenum admin command to forget a node from the NodeDB.
func (t *HALMeshTransport) RemoveNode(ctx context.Context, nodeNum uint32) error {
	return t.postJSON(ctx, "/meshtastic/admin/remove_node", map[string]interface{}{
		"node_num": nodeNum,
	})
}

// GetConfigSection requests a specific config section from the device via HAL.
func (t *HALMeshTransport) GetConfigSection(ctx context.Context, section string) error {
	return t.postJSON(ctx, "/meshtastic/config/get", map[string]string{"section": section})
}

// GetModuleConfigSection requests a specific module config section via HAL.
func (t *HALMeshTransport) GetModuleConfigSection(ctx context.Context, section string) error {
	return t.postJSON(ctx, "/meshtastic/config/module/get", map[string]string{"section": section})
}

// SendPosition broadcasts MeshSat's own position to the mesh via HAL.
func (t *HALMeshTransport) SendPosition(ctx context.Context, lat, lon float64, alt int32) error {
	return t.postJSON(ctx, "/meshtastic/position/send", map[string]interface{}{
		"latitude": lat, "longitude": lon, "altitude": alt,
	})
}

// SetFixedPosition sets a fixed GPS position on the device via HAL.
func (t *HALMeshTransport) SetFixedPosition(ctx context.Context, lat, lon float64, alt int32) error {
	return t.postJSON(ctx, "/meshtastic/admin/set_fixed_position", map[string]interface{}{
		"latitude": lat, "longitude": lon, "altitude": alt,
	})
}

// RemoveFixedPosition removes the fixed position from the device via HAL.
func (t *HALMeshTransport) RemoveFixedPosition(ctx context.Context) error {
	return t.postJSON(ctx, "/meshtastic/admin/remove_fixed_position", nil)
}

// SetOwner sets the device owner name — not supported via HAL.
func (t *HALMeshTransport) SetOwner(_ context.Context, _, _ string) error {
	return fmt.Errorf("set_owner not supported via HAL transport")
}

// RequestNodeInfo requests NodeInfo from a remote node via HAL.
func (t *HALMeshTransport) RequestNodeInfo(ctx context.Context, nodeNum uint32) error {
	return t.postJSON(ctx, "/meshtastic/admin/request_nodeinfo", map[string]interface{}{
		"node_num": nodeNum,
	})
}

// RequestStoreForward requests message history from a store & forward server via HAL.
func (t *HALMeshTransport) RequestStoreForward(ctx context.Context, nodeNum uint32, window uint32) error {
	return t.postJSON(ctx, "/meshtastic/store_forward/request", map[string]interface{}{
		"node_id": nodeNum, "window": window,
	})
}

// SendRangeTest sends a range test packet via HAL.
func (t *HALMeshTransport) SendRangeTest(ctx context.Context, text string, to uint32) error {
	return t.postJSON(ctx, "/meshtastic/range_test/send", map[string]interface{}{
		"text": text, "to": to,
	})
}

// SetCannedMessages sets the canned messages on the device via HAL.
func (t *HALMeshTransport) SetCannedMessages(ctx context.Context, messages string) error {
	return t.postJSON(ctx, "/meshtastic/admin/set_canned_messages", map[string]string{
		"messages": messages,
	})
}

// GetCannedMessages requests canned messages from the device via HAL.
func (t *HALMeshTransport) GetCannedMessages(ctx context.Context) error {
	return t.postJSON(ctx, "/meshtastic/admin/get_canned_messages", nil)
}

// GetNeighborInfo returns cached neighbor info from HAL.
func (t *HALMeshTransport) GetNeighborInfo(ctx context.Context) ([]NeighborInfo, error) {
	var resp struct {
		Neighbors []NeighborInfo `json:"neighbors"`
	}
	if err := t.getJSON(ctx, "/meshtastic/neighbors", &resp); err != nil {
		return nil, err
	}
	return resp.Neighbors, nil
}

// Close stops the SSE subscription.
func (t *HALMeshTransport) Close() error {
	if t.cancel != nil {
		t.cancel()
	}
	return nil
}

func (t *HALMeshTransport) getJSON(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.halURL+path, nil)
	if err != nil {
		return err
	}
	if t.apiKey != "" {
		req.Header.Set("X-HAL-Key", t.apiKey)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET %s: status %d: %s", path, resp.StatusCode, body)
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func (t *HALMeshTransport) postJSON(ctx context.Context, path string, body interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.halURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if t.apiKey != "" {
		req.Header.Set("X-HAL-Key", t.apiKey)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("POST %s: status %d: %s", path, resp.StatusCode, respBody)
	}
	return nil
}
