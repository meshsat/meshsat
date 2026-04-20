package api

// Bluetooth system-management handlers. Ported from
// cubeos/hal/internal/handlers/bluetooth.go for the field-kit standalone
// deployment (no HAL container). The flow — status -> scan -> devices
// list -> pair -> connect — mirrors the Zigbee permit-join/pair UX
// operators already know. [MESHSAT-623]

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// BluetoothDevice is a paired or discovered Bluetooth endpoint.
type BluetoothDevice struct {
	Address   string `json:"address"`
	Name      string `json:"name"`
	Paired    bool   `json:"paired"`
	Connected bool   `json:"connected"`
	Trusted   bool   `json:"trusted"`
	Class     string `json:"class,omitempty"`
	RSSI      int    `json:"rssi,omitempty"`
}

type BluetoothDevicesResponse struct {
	Paired    []BluetoothDevice `json:"paired"`
	Available []BluetoothDevice `json:"available,omitempty"`
}

type BluetoothStatus struct {
	Available     bool   `json:"available"`
	Powered       bool   `json:"powered"`
	Discoverable  bool   `json:"discoverable"`
	Pairable      bool   `json:"pairable"`
	RFKillBlocked bool   `json:"rfkill_blocked"`
	Name          string `json:"name"`
	Address       string `json:"address"`
	Alias         string `json:"alias,omitempty"`
}

type BluetoothConnectRequest struct {
	Address string `json:"address"`
}

// @Summary Get Bluetooth adapter status
// @Description Returns controller address, powered/discoverable/pairable state, and rfkill status. Auto-starts bluetooth.service if the HCI hardware is present but bluetoothd isn't running.
// @Tags system
// @Produce json
// @Success 200 {object} BluetoothStatus
// @Failure 404 {object} map[string]string
// @Router /api/system/bluetooth/status [get]
func (s *Server) handleBluetoothStatus(w http.ResponseWriter, r *http.Request) {
	status := BluetoothStatus{Available: false}

	output, err := execWithTimeout(r.Context(), "bluetoothctl", "show")
	if err != nil {
		if _, statErr := os.Stat("/sys/class/bluetooth/hci0"); statErr != nil {
			writeError(w, http.StatusNotFound, "Bluetooth not available")
			return
		}
		log.Warn().Msg("bluetooth: HCI hardware present but bluetoothd not running — starting bluetooth.service")
		if _, startErr := execWithTimeout(r.Context(),
			"nsenter", "-t", "1", "-m", "-u", "-i", "-n", "--",
			"systemctl", "start", "bluetooth"); startErr != nil {
			log.Error().Err(startErr).Msg("bluetooth: failed to start bluetooth.service")
			writeError(w, http.StatusNotFound, "Bluetooth not available")
			return
		}
		_, _ = execWithTimeout(r.Context(),
			"nsenter", "-t", "1", "-m", "-u", "-i", "-n", "--",
			"systemctl", "enable", "bluetooth")
		time.Sleep(2 * time.Second)
		output, err = execWithTimeout(r.Context(), "bluetoothctl", "show")
		if err != nil {
			writeError(w, http.StatusNotFound, "Bluetooth not available")
			return
		}
	}

	status.Available = true
	status.RFKillBlocked = isBluetoothRFKillBlocked(r.Context())

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Controller "):
			if parts := strings.Fields(line); len(parts) >= 2 {
				status.Address = parts[1]
			}
		case strings.HasPrefix(line, "Name:"):
			status.Name = strings.TrimPrefix(line, "Name: ")
		case strings.HasPrefix(line, "Alias:"):
			status.Alias = strings.TrimPrefix(line, "Alias: ")
		case strings.HasPrefix(line, "Powered:"):
			status.Powered = strings.Contains(line, "yes")
		case strings.HasPrefix(line, "Discoverable:"):
			status.Discoverable = strings.Contains(line, "yes")
		case strings.HasPrefix(line, "Pairable:"):
			status.Pairable = strings.Contains(line, "yes")
		}
	}
	writeJSON(w, http.StatusOK, status)
}

// @Summary Power on the Bluetooth adapter
// @Tags system
// @Success 200 {object} map[string]string
// @Router /api/system/bluetooth/power/on [post]
func (s *Server) handleBluetoothPowerOn(w http.ResponseWriter, r *http.Request) {
	if _, err := execWithTimeout(r.Context(), "bluetoothctl", "power", "on"); err != nil {
		log.Error().Err(err).Msg("bluetooth power on failed")
		writeError(w, http.StatusInternalServerError, sanitizeExecError("power on Bluetooth", err))
		return
	}
	writeSuccess(w, "Bluetooth powered on")
}

// @Summary Power off the Bluetooth adapter
// @Tags system
// @Success 200 {object} map[string]string
// @Router /api/system/bluetooth/power/off [post]
func (s *Server) handleBluetoothPowerOff(w http.ResponseWriter, r *http.Request) {
	if _, err := execWithTimeout(r.Context(), "bluetoothctl", "power", "off"); err != nil {
		log.Error().Err(err).Msg("bluetooth power off failed")
		writeError(w, http.StatusInternalServerError, sanitizeExecError("power off Bluetooth", err))
		return
	}
	writeSuccess(w, "Bluetooth powered off")
}

// @Summary List Bluetooth devices (paired + available)
// @Tags system
// @Produce json
// @Success 200 {object} BluetoothDevicesResponse
// @Router /api/system/bluetooth/devices [get]
func (s *Server) handleBluetoothDevices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	paired := getPairedBluetoothDevices(ctx)
	pairedSet := make(map[string]bool, len(paired))
	for _, d := range paired {
		pairedSet[d.Address] = true
	}
	available := getDiscoveredBluetoothDevices(ctx, pairedSet)
	writeJSON(w, http.StatusOK, BluetoothDevicesResponse{Paired: paired, Available: available})
}

// @Summary Scan for Bluetooth devices
// @Description Runs bluetoothctl scan for `duration` seconds (1-30, default 10). Use GET /devices afterwards to retrieve the discovered endpoints.
// @Tags system
// @Param duration query int false "Scan duration in seconds (1-30)"
// @Success 200 {object} map[string]string
// @Router /api/system/bluetooth/scan [post]
func (s *Server) handleBluetoothScan(w http.ResponseWriter, r *http.Request) {
	duration := 10
	if d := r.URL.Query().Get("duration"); d != "" {
		if n, err := strconv.Atoi(d); err == nil {
			duration = n
		}
	}
	if duration < 1 || duration > 30 {
		writeError(w, http.StatusBadRequest, "scan duration must be 1-30 seconds")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(duration)*time.Second)
	defer cancel()
	_, err := execWithTimeout(ctx, "bluetoothctl", "--timeout", strconv.Itoa(duration), "scan", "on")
	if err != nil && ctx.Err() != context.DeadlineExceeded {
		log.Warn().Err(err).Msg("bluetooth scan error")
	}
	writeSuccess(w, fmt.Sprintf("Bluetooth scan completed (%ds)", duration))
}

// @Summary Pair with a Bluetooth device
// @Tags system
// @Accept json
// @Param body body BluetoothConnectRequest true "Target device address"
// @Success 200 {object} map[string]string
// @Router /api/system/bluetooth/pair [post]
func (s *Server) handleBluetoothPair(w http.ResponseWriter, r *http.Request) {
	r = limitBody(r, 1<<20)
	var req BluetoothConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateMACAddress(req.Address); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := execWithTimeout(r.Context(), "bluetoothctl", "pair", req.Address); err != nil {
		log.Error().Err(err).Str("addr", req.Address).Msg("bluetooth pair failed")
		writeError(w, http.StatusInternalServerError, sanitizeExecError("pairing", err))
		return
	}
	writeSuccess(w, fmt.Sprintf("pairing initiated with %s", req.Address))
}

// @Summary Connect to a paired Bluetooth device
// @Tags system
// @Param address path string true "Device MAC address"
// @Success 200 {object} map[string]string
// @Router /api/system/bluetooth/connect/{address} [post]
func (s *Server) handleBluetoothConnect(w http.ResponseWriter, r *http.Request) {
	address := chi.URLParam(r, "address")
	if err := validateMACAddress(address); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := execWithTimeout(r.Context(), "bluetoothctl", "connect", address); err != nil {
		log.Error().Err(err).Str("addr", address).Msg("bluetooth connect failed")
		writeError(w, http.StatusInternalServerError, sanitizeExecError("connect", err))
		return
	}
	writeSuccess(w, fmt.Sprintf("connected to %s", address))
}

// @Summary Disconnect from a connected Bluetooth device
// @Tags system
// @Param address path string true "Device MAC address"
// @Success 200 {object} map[string]string
// @Router /api/system/bluetooth/disconnect/{address} [post]
func (s *Server) handleBluetoothDisconnect(w http.ResponseWriter, r *http.Request) {
	address := chi.URLParam(r, "address")
	if err := validateMACAddress(address); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := execWithTimeout(r.Context(), "bluetoothctl", "disconnect", address); err != nil {
		log.Error().Err(err).Str("addr", address).Msg("bluetooth disconnect failed")
		writeError(w, http.StatusInternalServerError, sanitizeExecError("disconnect", err))
		return
	}
	writeSuccess(w, fmt.Sprintf("disconnected from %s", address))
}

// @Summary Remove (unpair) a Bluetooth device
// @Tags system
// @Param address path string true "Device MAC address"
// @Success 200 {object} map[string]string
// @Router /api/system/bluetooth/remove/{address} [delete]
func (s *Server) handleBluetoothRemove(w http.ResponseWriter, r *http.Request) {
	address := chi.URLParam(r, "address")
	if err := validateMACAddress(address); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := execWithTimeout(r.Context(), "bluetoothctl", "remove", address); err != nil {
		log.Error().Err(err).Str("addr", address).Msg("bluetooth remove failed")
		writeError(w, http.StatusInternalServerError, sanitizeExecError("remove", err))
		return
	}
	writeSuccess(w, fmt.Sprintf("removed %s", address))
}

// @Summary Block/unblock Bluetooth via rfkill
// @Description Useful when the onboard adapter conflicts with WiFi AP mode sharing the 2.4 GHz radio.
// @Tags system
// @Accept json
// @Param body body object{block=bool} true "block flag"
// @Success 200 {object} map[string]string
// @Router /api/system/bluetooth/rfkill [post]
func (s *Server) handleBluetoothRFKill(w http.ResponseWriter, r *http.Request) {
	r = limitBody(r, 1<<20)
	var req struct {
		Block bool `json:"block"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	action := "unblock"
	if req.Block {
		action = "block"
	}
	if _, err := execWithTimeout(r.Context(),
		"nsenter", "-t", "1", "-m", "--",
		"rfkill", action, "bluetooth"); err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeExecError("rfkill "+action+" bluetooth", err))
		return
	}
	if req.Block {
		writeSuccess(w, "Bluetooth blocked")
	} else {
		writeSuccess(w, "Bluetooth unblocked")
	}
}

// ── helpers ──────────────────────────────────────────────────────────

func isBluetoothRFKillBlocked(ctx context.Context) bool {
	output, err := execWithTimeout(ctx, "rfkill", "list", "bluetooth")
	if err != nil {
		return false
	}
	return strings.Contains(output, "Soft blocked: yes")
}

func getPairedBluetoothDevices(ctx context.Context) []BluetoothDevice {
	var devices []BluetoothDevice
	output, err := execWithTimeout(ctx, "bluetoothctl", "devices", "Paired")
	if err != nil {
		return devices
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Device ") {
			continue
		}
		parts := strings.SplitN(line[7:], " ", 2)
		if len(parts) < 1 {
			continue
		}
		d := BluetoothDevice{Address: parts[0], Paired: true}
		if len(parts) >= 2 {
			d.Name = parts[1]
		}
		if info, err := execWithTimeout(ctx, "bluetoothctl", "info", d.Address); err == nil {
			d.Connected = strings.Contains(info, "Connected: yes")
			d.Trusted = strings.Contains(info, "Trusted: yes")
		}
		devices = append(devices, d)
	}
	return devices
}

// getDiscoveredBluetoothDevices returns devices known to bluetoothctl
// that aren't already in the paired list.
func getDiscoveredBluetoothDevices(ctx context.Context, paired map[string]bool) []BluetoothDevice {
	var devices []BluetoothDevice
	output, err := execWithTimeout(ctx, "bluetoothctl", "devices")
	if err != nil {
		return devices
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Device ") {
			continue
		}
		parts := strings.SplitN(line[7:], " ", 2)
		if len(parts) < 1 {
			continue
		}
		addr := parts[0]
		if paired[addr] {
			continue
		}
		d := BluetoothDevice{Address: addr}
		if len(parts) >= 2 {
			d.Name = parts[1]
		}
		if info, err := execWithTimeout(ctx, "bluetoothctl", "info", addr); err == nil {
			for _, il := range strings.Split(info, "\n") {
				il = strings.TrimSpace(il)
				if strings.HasPrefix(il, "RSSI:") {
					if idx := strings.Index(il, "("); idx >= 0 {
						if end := strings.Index(il[idx:], ")"); end >= 0 {
							if rssi, err := strconv.Atoi(il[idx+1 : idx+end]); err == nil {
								d.RSSI = rssi
							}
						}
					}
				}
				if strings.HasPrefix(il, "Icon:") {
					d.Class = strings.TrimPrefix(il, "Icon: ")
				}
			}
		}
		devices = append(devices, d)
	}
	return devices
}
