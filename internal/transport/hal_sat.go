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

// HALSatTransport implements SatTransport by talking to HAL's Iridium REST/SSE API.
type HALSatTransport struct {
	halURL string
	apiKey string
	client *http.Client
	cancel context.CancelFunc
}

// NewHALSatTransport creates a new HAL-backed satellite transport.
func NewHALSatTransport(halURL, apiKey string) *HALSatTransport {
	return &HALSatTransport{
		halURL: strings.TrimRight(halURL, "/"),
		apiKey: apiKey,
		client: &http.Client{Timeout: 60 * time.Second}, // Iridium operations are slow
	}
}

// Subscribe opens an SSE connection to HAL's Iridium event stream.
// Cancels any existing subscription before creating a new one.
func (t *HALSatTransport) Subscribe(ctx context.Context) (<-chan SatEvent, error) {
	if t.cancel != nil {
		t.cancel() // clean up any existing subscription first
	}
	ch := make(chan SatEvent, 32)
	ctx, t.cancel = context.WithCancel(ctx)

	go t.sseLoop(ctx, ch)
	return ch, nil
}

func (t *HALSatTransport) sseLoop(ctx context.Context, ch chan<- SatEvent) {
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
		if connDuration > 10*time.Second {
			backoff = time.Second
		}

		log.Warn().Err(err).Dur("backoff", backoff).Dur("was_connected", connDuration).Msg("iridium SSE disconnected, reconnecting")

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > 5*time.Second {
			backoff = 5 * time.Second
		}
	}
}

func (t *HALSatTransport) readSSE(ctx context.Context, ch chan<- SatEvent) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.halURL+"/iridium/events", nil)
	if err != nil {
		return fmt.Errorf("create SSE request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if t.apiKey != "" {
		req.Header.Set("X-HAL-Key", t.apiKey)
	}

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

	log.Info().Msg("connected to HAL iridium SSE stream")

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var event SatEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			log.Warn().Err(err).Str("data", data).Msg("failed to parse iridium SSE event")
			continue
		}

		select {
		case ch <- event:
		case <-ctx.Done():
			return ctx.Err()
		default:
			log.Warn().Str("type", event.Type).Msg("sat event channel full, dropping event")
		}
	}
	return scanner.Err()
}

// Send transmits data via SBD MO.
func (t *HALSatTransport) Send(ctx context.Context, data []byte) (*SBDResult, error) {
	var result SBDResult
	if err := t.postJSONResp(ctx, "/iridium/send", map[string]interface{}{
		"data": data,
	}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SendText transmits a plain-text SBD message (AT+SBDWT, max 120 chars).
// The message appears as readable text on the RockBLOCK portal.
func (t *HALSatTransport) SendText(ctx context.Context, text string) (*SBDResult, error) {
	var result SBDResult
	if err := t.postJSONResp(ctx, "/iridium/send", map[string]interface{}{
		"text":   text,
		"format": "text",
	}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Receive retrieves a pending MT message.
func (t *HALSatTransport) Receive(ctx context.Context) ([]byte, error) {
	var resp struct {
		Data []byte `json:"data"`
	}
	if err := t.getJSON(ctx, "/iridium/receive", &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// MailboxCheck performs an SBDIX session to check for queued MT messages.
func (t *HALSatTransport) MailboxCheck(ctx context.Context) (*SBDResult, error) {
	var result SBDResult
	if err := t.postJSONResp(ctx, "/iridium/mailbox_check", nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetSignal returns the current satellite signal quality (blocking AT+CSQ, up to 60s).
func (t *HALSatTransport) GetSignal(ctx context.Context) (*SignalInfo, error) {
	var info SignalInfo
	if err := t.getJSON(ctx, "/iridium/signal", &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// GetSignalFast returns a cached signal quality reading (AT+CSQF, ~100ms).
func (t *HALSatTransport) GetSignalFast(ctx context.Context) (*SignalInfo, error) {
	var info SignalInfo
	if err := t.getJSON(ctx, "/iridium/signal/fast", &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// GetStatus returns the Iridium modem connection status.
func (t *HALSatTransport) GetStatus(ctx context.Context) (*SatStatus, error) {
	var status SatStatus
	if err := t.getJSON(ctx, "/iridium/status", &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// GetGeolocation returns the Iridium-derived geolocation estimate (AT-MSGEO).
func (t *HALSatTransport) GetGeolocation(ctx context.Context) (*GeolocationInfo, error) {
	var info GeolocationInfo
	if err := t.getJSON(ctx, "/iridium/geolocation", &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// Close stops the SSE subscription.
func (t *HALSatTransport) Close() error {
	if t.cancel != nil {
		t.cancel()
	}
	return nil
}

func (t *HALSatTransport) getJSON(ctx context.Context, path string, out interface{}) error {
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

func (t *HALSatTransport) postJSONResp(ctx context.Context, path string, body interface{}, out interface{}) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.halURL+path, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
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

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
