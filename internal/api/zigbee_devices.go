package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
)

// zigbeeDeviceRow is the merged view served by the device manager: row from
// the in-memory transport cache (live LQI, last_seen) + persisted columns
// (alias, manufacturer, history). Falls back gracefully when either side is
// missing — a freshly-paired device has no DB row yet, and a device that
// hasn't reported since startup has no live row.
type zigbeeDeviceRow struct {
	ShortAddr      int      `json:"short_addr"`
	IEEEAddr       string   `json:"ieee_addr"`
	Alias          string   `json:"alias"`
	DisplayName    string   `json:"display_name"` // Alias if set, else "ZB <short>"
	Manufacturer   string   `json:"manufacturer,omitempty"`
	Model          string   `json:"model,omitempty"`
	DeviceType     string   `json:"device_type,omitempty"`
	Endpoint       int      `json:"endpoint"`
	LQI            int      `json:"lqi"`
	BatteryPct     int      `json:"battery_pct"`
	LastTemp       *float64 `json:"last_temp,omitempty"`
	LastHumidity   *float64 `json:"last_humidity,omitempty"`
	LastOnOff      int      `json:"last_onoff"`
	LastZoneStatus int      `json:"last_zone_status"` // -1 unknown, else IAS Zone bitmask [MESHSAT-509]
	FirstSeen      string   `json:"first_seen,omitempty"`
	LastSeen       string   `json:"last_seen"`
	MessageCount   int      `json:"message_count"`
	Online         bool     `json:"online"` // true if seen in the last 30 minutes
}

// handleGetZigBeeDevicesEnriched is the new device list endpoint that joins
// the in-memory transport state with persisted DB metadata. It supersedes
// the legacy /api/zigbee/devices for the device-manager UI; the legacy
// endpoint stays for back-compat with the dashboard widget.
func (s *Server) handleGetZigBeeDevicesEnriched(w http.ResponseWriter, r *http.Request) {
	rows := s.collectZigBeeDeviceRows()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"devices": rows,
	})
}

// collectZigBeeDeviceRows merges the live transport cache + DB persistence.
// Used by the list endpoint and (single-device variant) the detail endpoint.
func (s *Server) collectZigBeeDeviceRows() []zigbeeDeviceRow {
	zgw := s.gwManager.GetZigBeeGateway()
	var liveDevices []ieeeIndexedDevice
	if zgw != nil {
		if t := zgw.GetTransport(); t != nil {
			for _, d := range t.GetDevices() {
				liveDevices = append(liveDevices, ieeeIndexedDevice{
					ShortAddr:      int(d.ShortAddr),
					IEEEAddr:       d.IEEEAddr,
					Alias:          d.Alias,
					Manufacturer:   d.Manufacturer,
					Model:          d.Model,
					Endpoint:       int(d.Endpoint),
					LQI:            int(d.LQI),
					BatteryPct:     d.BatteryPct,
					LastTemp:       d.Temperature,
					LastHumidity:   d.Humidity,
					LastOnOff:      d.OnOff,
					LastZoneStatus: d.ZoneStatus,
					LastSeen:       d.LastSeen.UTC().Format("2006-01-02T15:04:05Z"),
					Online:         true, // by definition, in the live cache
				})
			}
		}
	}

	// Persisted devices — covers anything that hasn't shown up live yet
	// (e.g. coordinator restart with sleeping end-devices that haven't
	// rejoined). These rows are flagged "offline".
	var persisted []database.ZigBeeDevice
	if s.db != nil {
		if list, err := s.db.ListZigBeeDevices(); err == nil {
			persisted = list
		}
	}

	// Merge by IEEE address, preferring the live row's volatile fields
	// (LQI, last_seen) and the persisted row's stable fields (alias, history).
	byIEEE := map[string]*zigbeeDeviceRow{}
	add := func(in zigbeeDeviceRow) {
		key := strings.ToLower(in.IEEEAddr)
		if key == "" {
			key = fmt.Sprintf("short:%d", in.ShortAddr)
		}
		if cur, ok := byIEEE[key]; ok {
			// Live row overrides volatile fields, persisted overrides stable
			if in.Online {
				cur.Online = true
				cur.LQI = in.LQI
				if in.LastSeen != "" {
					cur.LastSeen = in.LastSeen
				}
				if in.LastTemp != nil {
					cur.LastTemp = in.LastTemp
				}
				if in.LastHumidity != nil {
					cur.LastHumidity = in.LastHumidity
				}
				if in.BatteryPct >= 0 {
					cur.BatteryPct = in.BatteryPct
				}
				if in.LastOnOff >= 0 {
					cur.LastOnOff = in.LastOnOff
				}
				if in.LastZoneStatus >= 0 {
					cur.LastZoneStatus = in.LastZoneStatus
				}
			} else {
				if cur.Alias == "" && in.Alias != "" {
					cur.Alias = in.Alias
				}
				if cur.Manufacturer == "" && in.Manufacturer != "" {
					cur.Manufacturer = in.Manufacturer
				}
				if cur.Model == "" && in.Model != "" {
					cur.Model = in.Model
				}
				if cur.FirstSeen == "" && in.FirstSeen != "" {
					cur.FirstSeen = in.FirstSeen
				}
				if cur.MessageCount == 0 && in.MessageCount > 0 {
					cur.MessageCount = in.MessageCount
				}
			}
			return
		}
		row := in
		byIEEE[key] = &row
	}
	for _, d := range liveDevices {
		add(zigbeeDeviceRow{
			ShortAddr:      d.ShortAddr,
			IEEEAddr:       d.IEEEAddr,
			Alias:          d.Alias,
			Manufacturer:   d.Manufacturer,
			Model:          d.Model,
			Endpoint:       d.Endpoint,
			LQI:            d.LQI,
			BatteryPct:     d.BatteryPct,
			LastTemp:       d.LastTemp,
			LastHumidity:   d.LastHumidity,
			LastOnOff:      d.LastOnOff,
			LastZoneStatus: d.LastZoneStatus,
			LastSeen:       d.LastSeen,
			Online:         true,
		})
	}
	for _, p := range persisted {
		add(zigbeeDeviceRow{
			ShortAddr:      p.ShortAddr,
			IEEEAddr:       p.IEEEAddr,
			Alias:          p.Alias,
			Manufacturer:   p.Manufacturer,
			Model:          p.Model,
			DeviceType:     p.DeviceType,
			Endpoint:       p.Endpoint,
			LQI:            p.LQI,
			BatteryPct:     p.BatteryPct,
			LastTemp:       p.LastTemp,
			LastHumidity:   p.LastHumidity,
			LastOnOff:      p.LastOnOff,
			LastZoneStatus: p.LastZoneStatus,
			FirstSeen:      p.FirstSeen,
			LastSeen:       p.LastSeen,
			MessageCount:   p.MessageCount,
			Online:         false,
		})
	}

	out := make([]zigbeeDeviceRow, 0, len(byIEEE))
	for _, v := range byIEEE {
		v.DisplayName = v.Alias
		if v.DisplayName == "" {
			v.DisplayName = fmt.Sprintf("ZB %d", v.ShortAddr)
		}
		out = append(out, *v)
	}
	return out
}

// ieeeIndexedDevice is an internal helper for the merge — exists only so
// we can build []zigbeeDeviceRow without circular conversions.
type ieeeIndexedDevice struct {
	ShortAddr      int
	IEEEAddr       string
	Alias          string
	Manufacturer   string
	Model          string
	Endpoint       int
	LQI            int
	BatteryPct     int
	LastTemp       *float64
	LastHumidity   *float64
	LastOnOff      int
	LastZoneStatus int
	LastSeen       string
	Online         bool
}

// handleGetZigBeeDevice returns one device with full metadata + recent
// reading. The {addr} URL parameter accepts either the IEEE 16-hex address
// or the decimal short address — the device-manager UI links by IEEE,
// the dashboard widget links by short.
func (s *Server) handleGetZigBeeDevice(w http.ResponseWriter, r *http.Request) {
	addr := chi.URLParam(r, "addr")
	row := s.findZigBeeDeviceRow(addr)
	if row == nil {
		writeError(w, http.StatusNotFound, "no zigbee device with that address")
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) findZigBeeDeviceRow(addr string) *zigbeeDeviceRow {
	addr = strings.ToLower(strings.TrimSpace(addr))
	for _, d := range s.collectZigBeeDeviceRows() {
		if strings.ToLower(d.IEEEAddr) == addr {
			return &d
		}
		if shortStr := strconv.Itoa(d.ShortAddr); shortStr == addr {
			return &d
		}
	}
	return nil
}

// handlePatchZigBeeDevice updates the user-given alias for a device.
// Other mutable fields can land here later (notes, position override, etc.)
func (s *Server) handlePatchZigBeeDevice(w http.ResponseWriter, r *http.Request) {
	addr := chi.URLParam(r, "addr")
	row := s.findZigBeeDeviceRow(addr)
	if row == nil {
		writeError(w, http.StatusNotFound, "no zigbee device with that address")
		return
	}
	var req struct {
		Alias *string `json:"alias"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Alias != nil {
		alias := strings.TrimSpace(*req.Alias)
		// Persist first so a transport-cache miss still records the alias.
		if s.db != nil {
			if err := s.db.SetZigBeeDeviceAlias(row.IEEEAddr, alias); err != nil && !errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("save alias: %v", err))
				return
			}
		}
		// Then update the live cache so subsequent list responses reflect it
		// without needing a DB round-trip.
		if zgw := s.gwManager.GetZigBeeGateway(); zgw != nil {
			if t := zgw.GetTransport(); t != nil {
				t.SetDeviceAlias(row.IEEEAddr, alias)
			}
		}
		row.Alias = alias
	}
	row.DisplayName = row.Alias
	if row.DisplayName == "" {
		row.DisplayName = fmt.Sprintf("ZB %d", row.ShortAddr)
	}
	writeJSON(w, http.StatusOK, row)
}

// handleDeleteZigBeeDevice forgets a paired device — clears the DB row,
// the in-memory cache, and the routing config.
func (s *Server) handleDeleteZigBeeDevice(w http.ResponseWriter, r *http.Request) {
	addr := chi.URLParam(r, "addr")
	row := s.findZigBeeDeviceRow(addr)
	if row == nil {
		writeError(w, http.StatusNotFound, "no zigbee device with that address")
		return
	}
	if s.db != nil {
		if err := s.db.DeleteZigBeeDevice(row.IEEEAddr); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("delete: %v", err))
			return
		}
	}
	// Drop from live cache too. (We can't unpair from the coordinator
	// without ZDO_MGMT_LEAVE_REQ — left for a follow-up; the device will
	// rediscover and re-pair on its next announce.)
	if zgw := s.gwManager.GetZigBeeGateway(); zgw != nil {
		if t := zgw.GetTransport(); t != nil {
			t.ForgetDevice(uint16(row.ShortAddr))
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": true, "ieee_addr": row.IEEEAddr})
}

// handleGetZigBeeDeviceHistory returns the time-series sensor history.
// Query params: hours (default 24, max 720 = 30d).
func (s *Server) handleGetZigBeeDeviceHistory(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	addr := chi.URLParam(r, "addr")
	row := s.findZigBeeDeviceRow(addr)
	if row == nil {
		writeError(w, http.StatusNotFound, "no zigbee device with that address")
		return
	}
	hours := 24
	if h := r.URL.Query().Get("hours"); h != "" {
		if v, err := strconv.Atoi(h); err == nil && v > 0 && v <= 720 {
			hours = v
		}
	}
	readings, err := s.db.GetZigBeeSensorHistory(row.IEEEAddr, hours)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("history: %v", err))
		return
	}
	if readings == nil {
		// JSON-marshal nil slice as empty array for cleaner client code
		// (Vue + Playwright assertions expect Array.isArray to be true).
		readings = []database.ZigBeeSensorReading{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ieee_addr": row.IEEEAddr,
		"alias":     row.Alias,
		"hours":     hours,
		"readings":  readings,
	})
}

// handleGetZigBeeDeviceRouting returns the per-device routing config.
func (s *Server) handleGetZigBeeDeviceRouting(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	addr := chi.URLParam(r, "addr")
	row := s.findZigBeeDeviceRow(addr)
	if row == nil {
		writeError(w, http.StatusNotFound, "no zigbee device with that address")
		return
	}
	rt, err := s.db.GetZigBeeRouting(row.IEEEAddr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("routing: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, rt)
}

// handlePutZigBeeDeviceRouting upserts the per-device routing config.
func (s *Server) handlePutZigBeeDeviceRouting(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	addr := chi.URLParam(r, "addr")
	row := s.findZigBeeDeviceRow(addr)
	if row == nil {
		writeError(w, http.StatusNotFound, "no zigbee device with that address")
		return
	}
	var req database.ZigBeeDeviceRouting
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.IEEEAddr = row.IEEEAddr
	if err := s.db.SetZigBeeRouting(&req); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("save routing: %v", err))
		return
	}
	rt, _ := s.db.GetZigBeeRouting(row.IEEEAddr)
	writeJSON(w, http.StatusOK, rt)
}

// handlePostZigBeeDeviceRefresh sends ZCL Read Attributes for the
// temperature, humidity, and battery clusters on this device. Used by the
// "Refresh now" button in the detail view so users don't have to wait
// 30 min for sleepy Tuya sensors to report on their own cycle. The
// response arrives async via AF_INCOMING_MSG. [MESHSAT-509]
func (s *Server) handlePostZigBeeDeviceRefresh(w http.ResponseWriter, r *http.Request) {
	zgw := s.gwManager.GetZigBeeGateway()
	if zgw == nil {
		writeError(w, http.StatusServiceUnavailable, "zigbee gateway not running")
		return
	}
	t := zgw.GetTransport()
	if t == nil {
		writeError(w, http.StatusServiceUnavailable, "zigbee transport not available")
		return
	}
	addr := chi.URLParam(r, "addr")
	row := s.findZigBeeDeviceRow(addr)
	if row == nil {
		writeError(w, http.StatusNotFound, "no zigbee device with that address")
		return
	}
	go t.RefreshDeviceSensors(uint16(row.ShortAddr))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"refreshing": true,
		"short_addr": row.ShortAddr,
	})
}

// handlePostZigBeeDeviceCommand sends a command to a device. Currently
// supports OnOff (cluster 0x0006) commands "on" / "off" / "toggle" via
// AF_DATA_REQUEST. Future: level (0x0008), color (0x0300).
func (s *Server) handlePostZigBeeDeviceCommand(w http.ResponseWriter, r *http.Request) {
	zgw := s.gwManager.GetZigBeeGateway()
	if zgw == nil {
		writeError(w, http.StatusServiceUnavailable, "zigbee gateway not running")
		return
	}
	t := zgw.GetTransport()
	if t == nil {
		writeError(w, http.StatusServiceUnavailable, "zigbee transport not available")
		return
	}
	addr := chi.URLParam(r, "addr")
	row := s.findZigBeeDeviceRow(addr)
	if row == nil {
		writeError(w, http.StatusNotFound, "no zigbee device with that address")
		return
	}
	var req struct {
		Command string   `json:"command"`           // on/off/toggle/level/color/color_temp
		Level   *int     `json:"level,omitempty"`   // 0-254 for "level"
		ColorX  *float64 `json:"color_x,omitempty"` // 0.0-1.0 CIE x for "color"
		ColorY  *float64 `json:"color_y,omitempty"` // 0.0-1.0 CIE y for "color"
		Mireds  *int     `json:"mireds,omitempty"`  // 153-500 for "color_temp"
		Kelvin  *int     `json:"kelvin,omitempty"`  // alt to mireds (auto-converted)
		// Transition time in deciseconds (0 = instant). Default 5 (= 0.5s).
		TransitionDS *int `json:"transition_ds,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	cmd := strings.ToLower(strings.TrimSpace(req.Command))
	ep := byte(row.Endpoint)
	if ep == 0 {
		ep = 1 // default endpoint for HA on/off
	}
	transition := uint16(5)
	if req.TransitionDS != nil && *req.TransitionDS >= 0 && *req.TransitionDS <= 0xFFFE {
		transition = uint16(*req.TransitionDS)
	}

	switch cmd {
	case "on", "off", "toggle":
		if err := t.SendOnOffCommand(uint16(row.ShortAddr), ep, cmd); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("send %s: %v", cmd, err))
			return
		}
	case "level":
		if req.Level == nil {
			writeError(w, http.StatusBadRequest, `"level" command requires "level" field (0-254)`)
			return
		}
		lvl := *req.Level
		if lvl < 0 {
			lvl = 0
		}
		if lvl > 254 {
			lvl = 254
		}
		if err := t.SendLevelCommand(uint16(row.ShortAddr), ep, byte(lvl), transition); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("send level: %v", err))
			return
		}
	case "color":
		if req.ColorX == nil || req.ColorY == nil {
			writeError(w, http.StatusBadRequest, `"color" requires "color_x" and "color_y" (0.0..1.0)`)
			return
		}
		// CIE xy → wire form: scale 0..1 → 0..65279 (65535 is reserved as "invalid").
		x := uint16(*req.ColorX * 65279)
		y := uint16(*req.ColorY * 65279)
		if err := t.SendColorCommand(uint16(row.ShortAddr), ep, x, y, transition); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("send color: %v", err))
			return
		}
	case "color_temp":
		var mireds uint16
		switch {
		case req.Mireds != nil:
			mireds = uint16(*req.Mireds)
		case req.Kelvin != nil && *req.Kelvin > 0:
			mireds = uint16(1_000_000 / *req.Kelvin)
		default:
			writeError(w, http.StatusBadRequest, `"color_temp" requires "mireds" or "kelvin"`)
			return
		}
		if err := t.SendColorTempCommand(uint16(row.ShortAddr), ep, mireds, transition); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("send color_temp: %v", err))
			return
		}
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown command %q (want on/off/toggle/level/color/color_temp)", cmd))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sent":       cmd,
		"short_addr": row.ShortAddr,
		"endpoint":   ep,
	})
}
