package hubreporter

// Hub → bridge system-management command surface.  Exposes the local
// /api/system/bluetooth/* and /api/system/wifi/* handlers (MESHSAT-623
// + MESHSAT-624) as Hub MQTT commands so a fleet admin can drive BT
// pairing or WiFi join on any bridge without SSH.
//
// Bridge side only — the Hub UI that invokes these commands lives in
// meshsat-hub (project 35) and is tracked separately.  [MESHSAT-632]

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// localAPIBase is the loopback URL the bridge serves its own REST API
// on.  Overridable via env so tests / alternate ports stay working.
func localAPIBase() string {
	if v := os.Getenv("MESHSAT_LOCAL_API_BASE"); v != "" {
		return strings.TrimRight(v, "/")
	}
	port := os.Getenv("MESHSAT_PORT")
	if port == "" {
		port = "6050"
	}
	return "http://127.0.0.1:" + port
}

var sysmgmtHTTPClient = &http.Client{Timeout: 60 * time.Second}

// callLocalAPI is the shared Hub-command → local-REST shim.  It hands
// the same JSON body a UI would POST, so the validation / exec /
// error-shape stays identical to what the WebUI sees.
func callLocalAPI(method, path string, body interface{}) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, localAPIBase()+path, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := sysmgmtHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("local api: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		// Try to surface the handler's own error field if present.
		var errObj struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(data, &errObj); err == nil && errObj.Error != "" {
			return nil, fmt.Errorf("%s", errObj.Error)
		}
		return nil, fmt.Errorf("local api %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.RawMessage(data), nil
}

// registerSysMgmtHandlers wires Hub → bridge commands for BT + WiFi.
// Called from NewCommandHandler.  [MESHSAT-632]
func (ch *CommandHandler) registerSysMgmtHandlers() {
	ch.handlers["bt_status"] = func(cmd Command) (json.RawMessage, error) {
		return callLocalAPI(http.MethodGet, "/api/system/bluetooth/status", nil)
	}
	ch.handlers["bt_devices"] = func(cmd Command) (json.RawMessage, error) {
		return callLocalAPI(http.MethodGet, "/api/system/bluetooth/devices", nil)
	}
	ch.handlers["bt_scan"] = func(cmd Command) (json.RawMessage, error) {
		var p struct {
			Duration int `json:"duration"`
		}
		_ = json.Unmarshal(cmd.Payload, &p)
		if p.Duration == 0 {
			p.Duration = 10
		}
		return callLocalAPI(http.MethodPost, fmt.Sprintf("/api/system/bluetooth/scan?duration=%d", p.Duration), nil)
	}
	ch.handlers["bt_pair"] = func(cmd Command) (json.RawMessage, error) {
		var p struct {
			Address string `json:"address"`
		}
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return nil, fmt.Errorf("payload: %w", err)
		}
		return callLocalAPI(http.MethodPost, "/api/system/bluetooth/pair", p)
	}
	ch.handlers["bt_connect"] = func(cmd Command) (json.RawMessage, error) {
		var p struct {
			Address string `json:"address"`
		}
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return nil, fmt.Errorf("payload: %w", err)
		}
		return callLocalAPI(http.MethodPost, "/api/system/bluetooth/connect/"+url.PathEscape(p.Address), nil)
	}
	ch.handlers["bt_disconnect"] = func(cmd Command) (json.RawMessage, error) {
		var p struct {
			Address string `json:"address"`
		}
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return nil, fmt.Errorf("payload: %w", err)
		}
		return callLocalAPI(http.MethodPost, "/api/system/bluetooth/disconnect/"+url.PathEscape(p.Address), nil)
	}
	ch.handlers["bt_remove"] = func(cmd Command) (json.RawMessage, error) {
		var p struct {
			Address string `json:"address"`
		}
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return nil, fmt.Errorf("payload: %w", err)
		}
		return callLocalAPI(http.MethodDelete, "/api/system/bluetooth/remove/"+url.PathEscape(p.Address), nil)
	}
	ch.handlers["bt_power"] = func(cmd Command) (json.RawMessage, error) {
		var p struct {
			On bool `json:"on"`
		}
		_ = json.Unmarshal(cmd.Payload, &p)
		suffix := "off"
		if p.On {
			suffix = "on"
		}
		return callLocalAPI(http.MethodPost, "/api/system/bluetooth/power/"+suffix, nil)
	}

	ch.handlers["wifi_status"] = func(cmd Command) (json.RawMessage, error) {
		var p struct {
			Interface string `json:"interface"`
		}
		_ = json.Unmarshal(cmd.Payload, &p)
		if p.Interface != "" {
			return callLocalAPI(http.MethodGet, "/api/system/wifi/status/"+url.PathEscape(p.Interface), nil)
		}
		return callLocalAPI(http.MethodGet, "/api/system/wifi/status", nil)
	}
	ch.handlers["wifi_scan"] = func(cmd Command) (json.RawMessage, error) {
		var p struct {
			Interface string `json:"interface"`
		}
		_ = json.Unmarshal(cmd.Payload, &p)
		if p.Interface != "" {
			return callLocalAPI(http.MethodGet, "/api/system/wifi/scan/"+url.PathEscape(p.Interface), nil)
		}
		return callLocalAPI(http.MethodGet, "/api/system/wifi/scan", nil)
	}
	ch.handlers["wifi_saved"] = func(cmd Command) (json.RawMessage, error) {
		var p struct {
			Interface string `json:"interface"`
		}
		_ = json.Unmarshal(cmd.Payload, &p)
		if p.Interface != "" {
			return callLocalAPI(http.MethodGet, "/api/system/wifi/saved/"+url.PathEscape(p.Interface), nil)
		}
		return callLocalAPI(http.MethodGet, "/api/system/wifi/saved", nil)
	}
	ch.handlers["wifi_connect"] = func(cmd Command) (json.RawMessage, error) {
		// Forward the Payload straight through — its shape matches the
		// REST endpoint's request DTO (ssid, password, interface).
		var p struct {
			SSID      string `json:"ssid"`
			Password  string `json:"password"`
			Interface string `json:"interface"`
		}
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return nil, fmt.Errorf("payload: %w", err)
		}
		log.Info().Str("ssid", p.SSID).Str("iface", p.Interface).Str("request_id", cmd.RequestID).
			Msg("hub-cmd: wifi_connect (password redacted)")
		return callLocalAPI(http.MethodPost, "/api/system/wifi/connect", p)
	}
	ch.handlers["wifi_disconnect"] = func(cmd Command) (json.RawMessage, error) {
		var p struct {
			Interface string `json:"interface"`
		}
		_ = json.Unmarshal(cmd.Payload, &p)
		if p.Interface != "" {
			return callLocalAPI(http.MethodPost, "/api/system/wifi/disconnect/"+url.PathEscape(p.Interface), nil)
		}
		return callLocalAPI(http.MethodPost, "/api/system/wifi/disconnect", nil)
	}
}
