package gateway

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/transport"
)

// routeSensorEvent fans a decoded sensor reading out to TAK / hub / log
// based on the per-device routing config. Called from receiveWorker for
// every "temperature", "humidity", "battery", "onoff" event.
//
// Defaults (for devices with no row in zigbee_device_routing yet): TAK on,
// hub on, log on, mesh off — matches the typical MeshSat "field sensor →
// situational awareness" use case.
func (g *ZigBeeGateway) routeSensorEvent(evt transport.ZigBeeEvent) {
	r := g.sensorRouter
	if evt.Device.IEEEAddr == "" {
		// No IEEE binding yet — we can't look up routing by IEEE. Skip
		// silently; the next announce frame will populate the binding.
		return
	}

	// Per-device rate limit — sensors can chatter every few seconds; the
	// default 0 means "no rate limit, forward every reading".
	cfg, _ := g.lookupRouting(evt.Device.IEEEAddr)
	minInt := r.minIntervalDefault
	if cfg != nil && cfg.MinInterval > 0 {
		minInt = cfg.MinInterval
	}
	if minInt > 0 && !sensorRateAllow(evt.Device.IEEEAddr, evt.Type, time.Duration(minInt)*time.Second) {
		return
	}

	toTAK, toHub, toLog := true, true, true
	cotType := ""
	if cfg != nil {
		toTAK = cfg.ToTAK
		toHub = cfg.ToHub
		toLog = cfg.ToLog
		cotType = cfg.CoTType
	}

	displayName := evt.Device.Alias
	if displayName == "" {
		displayName = fmt.Sprintf("ZB-%d", evt.Device.ShortAddr)
	}
	value, unit := sensorValueUnit(evt)

	if toLog {
		log.Info().
			Str("ieee", evt.Device.IEEEAddr).
			Str("name", displayName).
			Str("kind", evt.Type).
			Float64("value", value).
			Str("unit", unit).
			Uint8("lqi", evt.Device.LQI).
			Int("battery", evt.Device.BatteryPct).
			Msg("zigbee: sensor reading")
	}

	if toTAK && r.takSend != nil {
		ev := buildSensorCoT(evt, displayName, r, cotType)
		if err := r.takSend(ev); err != nil {
			log.Warn().Err(err).Str("ieee", evt.Device.IEEEAddr).Msg("zigbee: TAK forward failed")
		}
	}

	if toHub && r.hubPublish != nil {
		if err := r.hubPublish(evt.Device.IEEEAddr, evt.Type, value, unit); err != nil {
			log.Warn().Err(err).Str("ieee", evt.Device.IEEEAddr).Msg("zigbee: hub forward failed")
		}
	}
}

// lookupRouting fetches the per-device routing config, returning nil if
// the lookup fails or no router/db is wired (caller falls back to defaults).
func (g *ZigBeeGateway) lookupRouting(ieeeAddr string) (*databaseRouting, error) {
	r := g.sensorRouter
	if r.db == nil {
		return nil, nil
	}
	return r.db.GetZigBeeRouting(ieeeAddr)
}

// sensorValueUnit picks the headline value + unit out of a typed event.
// Returns (0, "") for an unrecognized event so the caller can short-circuit.
func sensorValueUnit(evt transport.ZigBeeEvent) (float64, string) {
	switch {
	case evt.Temperature != nil:
		return *evt.Temperature, "°C"
	case evt.Humidity != nil:
		return *evt.Humidity, "%"
	case evt.BatteryPct != nil:
		return float64(*evt.BatteryPct), "%"
	case evt.OnOff != nil:
		if *evt.OnOff {
			return 1, "on"
		}
		return 0, "off"
	case evt.ZoneStatus != nil:
		return float64(evt.ZoneStatus.Raw), "flags"
	}
	return 0, ""
}

// buildSensorCoT synthesizes a CoT marker for a sensor reading. The marker
// uses the device alias as callsign, the bridge's GPS position (if any)
// as the point, and a structured remarks string with all known sensor
// values for the device. Type defaults to "b-m-p-s-p-i" (sensor info point)
// unless the routing config specifies an override. IAS Zone alarm events
// promote the type to "b-a-o-tbl" (alarm) so ATAK clients render them
// with the urgent red icon set rather than the gray sensor pin.
func buildSensorCoT(evt transport.ZigBeeEvent, displayName string, r zigbeeSensorRouter, cotTypeOverride string) CotEvent {
	now := time.Now().UTC()
	cotType := cotTypeOverride
	if cotType == "" {
		cotType = "b-m-p-s-p-i" // sensor point of interest (TAK standard)
		if evt.ZoneStatus != nil && evt.ZoneStatus.Triggered {
			cotType = "b-a-o-tbl" // alarm — overridden by routing.cot_type if set
		}
	}
	stale := now.Add(time.Duration(r.staleSec) * time.Second)
	uid := fmt.Sprintf("meshsat-zb-%s", strings.ToLower(evt.Device.IEEEAddr))
	callsign := fmt.Sprintf("%s-%s", r.callsignPrefix, displayName)

	lat, lon := 0.0, 0.0
	if r.gps != nil {
		if la, lo, ok := r.gps(); ok {
			lat, lon = la, lo
		}
	}

	// Build a compact, human-readable remarks string with every value we
	// know about the device — TAK clients (ATAK/iTAK/WinTAK) render this
	// in the marker callout. Includes the latest sensor reading + cached
	// values for the other clusters so the operator gets the full picture
	// in one tap.
	remarks := strings.Builder{}
	remarks.WriteString(displayName)
	remarks.WriteString(" — ")
	remarks.WriteString(strings.Title(evt.Type))
	remarks.WriteString(": ")
	if v, u := sensorValueUnit(evt); u != "" {
		fmt.Fprintf(&remarks, "%.2f%s", v, u)
	}
	if evt.Device.Temperature != nil {
		fmt.Fprintf(&remarks, " | T=%.1f°C", *evt.Device.Temperature)
	}
	if evt.Device.Humidity != nil {
		fmt.Fprintf(&remarks, " | RH=%.0f%%", *evt.Device.Humidity)
	}
	if evt.Device.BatteryPct >= 0 {
		fmt.Fprintf(&remarks, " | Bat=%d%%", evt.Device.BatteryPct)
	}
	if evt.ZoneStatus != nil {
		zs := evt.ZoneStatus
		var flags []string
		if zs.Alarm1 {
			flags = append(flags, "ALARM1")
		}
		if zs.Alarm2 {
			flags = append(flags, "ALARM2")
		}
		if zs.Tamper {
			flags = append(flags, "TAMPER")
		}
		if zs.BatteryLow {
			flags = append(flags, "LOW_BAT")
		}
		if zs.Trouble {
			flags = append(flags, "TROUBLE")
		}
		if len(flags) > 0 {
			fmt.Fprintf(&remarks, " | %s", strings.Join(flags, " "))
		} else {
			remarks.WriteString(" | CLEAR")
		}
	}
	fmt.Fprintf(&remarks, " | LQI=%d", evt.Device.LQI)

	return CotEvent{
		Version: "2.0",
		UID:     uid,
		Type:    cotType,
		How:     "m-g",
		Time:    now.Format(cotTimeFormat),
		Start:   now.Format(cotTimeFormat),
		Stale:   stale.Format(cotTimeFormat),
		Point: CotPoint{
			Lat: lat,
			Lon: lon,
			Hae: 0,
			Ce:  100.0, // sensor position is the bridge's, not the sensor's
			Le:  100.0,
		},
		Detail: &CotDetail{
			Contact: &CotContact{Callsign: callsign},
			Remarks: &CotRemarks{
				Source: "MeshSat-Zigbee",
				Text:   remarks.String(),
			},
		},
	}
}

// sensorRateAllow rate-limits per (device, kind) using a process-local
// last-sent map. Coarse but effective for the field-kit case.
var (
	sensorRateMu   sync.Mutex
	sensorRateLast = map[string]time.Time{}
)

func sensorRateAllow(ieeeAddr, kind string, minInterval time.Duration) bool {
	key := ieeeAddr + "|" + kind
	sensorRateMu.Lock()
	defer sensorRateMu.Unlock()
	if last, ok := sensorRateLast[key]; ok && time.Since(last) < minInterval {
		return false
	}
	sensorRateLast[key] = time.Now()
	return true
}
