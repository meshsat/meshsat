package gateway

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"

	"meshsat/internal/transport"
)

// CotEvent is the CoT XML event envelope.
type CotEvent struct {
	XMLName xml.Name   `xml:"event"`
	Version string     `xml:"version,attr"`
	UID     string     `xml:"uid,attr"`
	Type    string     `xml:"type,attr"`
	How     string     `xml:"how,attr"`
	Time    string     `xml:"time,attr"`
	Start   string     `xml:"start,attr"`
	Stale   string     `xml:"stale,attr"`
	Point   CotPoint   `xml:"point"`
	Detail  *CotDetail `xml:"detail,omitempty"`
}

// CotPoint is the CoT point element with WGS84 coordinates.
type CotPoint struct {
	Lat float64 `xml:"lat,attr"`
	Lon float64 `xml:"lon,attr"`
	Hae float64 `xml:"hae,attr"`
	Ce  float64 `xml:"ce,attr"`
	Le  float64 `xml:"le,attr"`
}

// CotDetail holds optional detail sub-elements.
type CotDetail struct {
	Contact   *CotContact   `xml:"contact,omitempty"`
	Group     *CotGroup     `xml:"__group,omitempty"`
	Precision *CotPrecision `xml:"precisionlocation,omitempty"`
	Track     *CotTrack     `xml:"track,omitempty"`
	Status    *CotStatus    `xml:"status,omitempty"`
	Takv      *CotTakv      `xml:"takv,omitempty"`
	Emergency *CotEmergency `xml:"emergency,omitempty"`
	Remarks   *CotRemarks   `xml:"remarks,omitempty"`
}

// CotTakv identifies the TAK client software.
type CotTakv struct {
	Device   string `xml:"device,attr"`
	Platform string `xml:"platform,attr"`
	OS       string `xml:"os,attr"`
	Version  string `xml:"version,attr"`
}

// CotContact identifies the event source callsign.
type CotContact struct {
	Callsign string `xml:"callsign,attr"`
}

// CotGroup specifies team affiliation.
type CotGroup struct {
	Name string `xml:"name,attr"`
	Role string `xml:"role,attr"`
}

// CotPrecision specifies the source of location data.
type CotPrecision struct {
	AltSrc      string `xml:"altsrc,attr"`
	GeoPointSrc string `xml:"geopointsrc,attr"`
}

// CotTrack specifies course and speed.
type CotTrack struct {
	Course float64 `xml:"course,attr"`
	Speed  float64 `xml:"speed,attr"`
}

// CotStatus reports device status.
type CotStatus struct {
	Battery string `xml:"battery,attr,omitempty"`
}

// CotEmergency signals an emergency condition.
type CotEmergency struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

// CotRemarks holds freetext detail.
type CotRemarks struct {
	Source string `xml:"source,attr,omitempty"`
	Text   string `xml:",chardata"`
}

const cotTimeFormat = "2006-01-02T15:04:05Z"

// CotEventTypePosition is the standard CoT type for a friendly ground unit.
const CotEventTypePosition = "a-f-G-U-C"

// CotEventTypeSensor is the CoT type for sensor/telemetry data.
const CotEventTypeSensor = "t-x-d-d"

// CotEventTypeAlarm is the CoT type for alarm events (dead man's switch).
const CotEventTypeAlarm = "b-a"

// CotEventTypeChat is the CoT type for GeoChat/freetext messages.
const CotEventTypeChat = "b-t-f"

// BuildPositionEvent creates a CoT PLI event from a MeshSat position.
func BuildPositionEvent(uid, callsign string, lat, lon, alt float64, staleSec int) CotEvent {
	now := time.Now().UTC()
	return CotEvent{
		Version: "2.0",
		UID:     uid,
		Type:    CotEventTypePosition,
		How:     "m-g",
		Time:    now.Format(cotTimeFormat),
		Start:   now.Format(cotTimeFormat),
		Stale:   now.Add(time.Duration(staleSec) * time.Second).Format(cotTimeFormat),
		Point: CotPoint{
			Lat: lat,
			Lon: lon,
			Hae: alt,
			Ce:  10.0,
			Le:  10.0,
		},
		Detail: &CotDetail{
			Contact: &CotContact{Callsign: callsign},
			Group:   &CotGroup{Name: "Cyan", Role: "Team Member"},
			Precision: &CotPrecision{
				AltSrc:      "GPS",
				GeoPointSrc: "GPS",
			},
			Track: &CotTrack{Course: 0, Speed: 0},
		},
	}
}

// BuildSOSEvent creates a CoT emergency event.
func BuildSOSEvent(uid, callsign string, lat, lon, alt float64, staleSec int, reason string) CotEvent {
	ev := BuildPositionEvent(uid, callsign, lat, lon, alt, staleSec)
	ev.Detail.Emergency = &CotEmergency{
		Type: "911 Alert",
		Text: reason,
	}
	ev.Detail.Remarks = &CotRemarks{
		Source: "MeshSat",
		Text:   "Emergency: " + reason,
	}
	return ev
}

// BuildDeadmanEvent creates a CoT alarm event for a dead man's switch timeout.
func BuildDeadmanEvent(uid, callsign string, lat, lon float64, staleSec int, timeoutSec int) CotEvent {
	now := time.Now().UTC()
	return CotEvent{
		Version: "2.0",
		UID:     uid + "-DEADMAN",
		Type:    CotEventTypeAlarm,
		How:     "h-e",
		Time:    now.Format(cotTimeFormat),
		Start:   now.Format(cotTimeFormat),
		Stale:   now.Add(time.Duration(staleSec) * time.Second).Format(cotTimeFormat),
		Point: CotPoint{
			Lat: lat,
			Lon: lon,
			Hae: 0,
			Ce:  100.0,
			Le:  100.0,
		},
		Detail: &CotDetail{
			Contact: &CotContact{Callsign: callsign},
			Remarks: &CotRemarks{
				Source: "MeshSat",
				Text:   fmt.Sprintf("Dead man's switch timeout — no check-in for %ds", timeoutSec),
			},
		},
	}
}

// BuildTelemetryEvent creates a CoT data event for sensor telemetry.
func BuildTelemetryEvent(uid, callsign string, lat, lon float64, staleSec int, data string) CotEvent {
	now := time.Now().UTC()
	return CotEvent{
		Version: "2.0",
		UID:     uid + "-SENSOR",
		Type:    CotEventTypeSensor,
		How:     "m-g",
		Time:    now.Format(cotTimeFormat),
		Start:   now.Format(cotTimeFormat),
		Stale:   now.Add(time.Duration(staleSec) * time.Second).Format(cotTimeFormat),
		Point: CotPoint{
			Lat: lat,
			Lon: lon,
			Hae: 0,
			Ce:  50.0,
			Le:  50.0,
		},
		Detail: &CotDetail{
			Contact: &CotContact{Callsign: callsign + "-SENSOR"},
			Remarks: &CotRemarks{
				Source: "MeshSat",
				Text:   data,
			},
		},
	}
}

// BuildChatEvent creates a CoT GeoChat event for text messages.
func BuildChatEvent(uid, callsign string, text string, staleSec int) CotEvent {
	now := time.Now().UTC()
	return CotEvent{
		Version: "2.0",
		UID:     uid + "-CHAT-" + strconv.FormatInt(now.UnixMilli(), 36),
		Type:    CotEventTypeChat,
		How:     "h-g-i-g-o",
		Time:    now.Format(cotTimeFormat),
		Start:   now.Format(cotTimeFormat),
		Stale:   now.Add(time.Duration(staleSec) * time.Second).Format(cotTimeFormat),
		Point: CotPoint{
			Lat: 0, Lon: 0, Hae: 0, Ce: 9999999, Le: 9999999,
		},
		Detail: &CotDetail{
			Contact: &CotContact{Callsign: callsign},
			Remarks: &CotRemarks{
				Source: callsign,
				Text:   text,
			},
		},
	}
}

// PositionEnrichment carries GPS quality and velocity data for CoT enrichment.
type PositionEnrichment struct {
	Lat, Lon, Alt       float64
	Speed, Course       float64 // m/s and degrees
	PDOP, HDOP          float64 // dilution of precision (*100 from Meshtastic)
	Battery             int     // 0-100 percentage, -1 = unknown
	SatsInView          int
	FixQuality, FixType int
}

// CeFromHDOP converts horizontal dilution of precision to CoT Circular Error (meters).
// CE ≈ HDOP * 5 (typical GPS receiver, 1-sigma). PDOP fallback: PDOP * 3.
func CeFromHDOP(hdop, pdop float64) float64 {
	if hdop > 0 {
		return hdop * 5.0
	}
	if pdop > 0 {
		return pdop * 3.0
	}
	return 10.0 // default
}

// LeFromVDOP converts vertical dilution of precision to CoT Linear Error (meters).
func LeFromVDOP(vdop, pdop float64) float64 {
	if vdop > 0 {
		return vdop * 5.0
	}
	if pdop > 0 {
		return pdop * 4.0
	}
	return 10.0 // default
}

// BuildEnrichedPositionEvent creates a CoT PLI with real GPS quality and velocity data.
func BuildEnrichedPositionEvent(uid, callsign string, e PositionEnrichment, staleSec int) CotEvent {
	ev := BuildPositionEvent(uid, callsign, e.Lat, e.Lon, e.Alt, staleSec)

	// GPS accuracy
	ev.Point.Ce = CeFromHDOP(e.HDOP, e.PDOP)
	ev.Point.Le = LeFromVDOP(0, e.PDOP)

	// Precision source
	src := "GPS"
	if e.FixQuality >= 2 {
		src = "DGPS"
	}
	if e.FixType == 0 || e.FixQuality == 0 {
		src = "???"
	}
	ev.Detail.Precision = &CotPrecision{AltSrc: src, GeoPointSrc: src}

	// Velocity
	ev.Detail.Track = &CotTrack{Speed: e.Speed, Course: e.Course}

	// Battery
	if e.Battery >= 0 {
		ev.Detail.Status = &CotStatus{Battery: strconv.Itoa(e.Battery)}
	}

	// TAK client identification
	ev.Detail.Takv = &CotTakv{
		Device:   "MeshSat Bridge",
		Platform: "MeshSat",
		OS:       "Linux",
		Version:  "0.20.0",
	}

	return ev
}

// MarshalCotEvent serializes a CoT event to XML bytes.
func MarshalCotEvent(ev CotEvent) ([]byte, error) {
	return xml.Marshal(ev)
}

// ParseCotEvent deserializes CoT XML into a CotEvent.
func ParseCotEvent(data []byte) (*CotEvent, error) {
	var ev CotEvent
	if err := xml.Unmarshal(data, &ev); err != nil {
		return nil, fmt.Errorf("parse cot event: %w", err)
	}
	return &ev, nil
}

// CotEventToInboundMessage converts a parsed CoT event to a MeshSat InboundMessage.
func CotEventToInboundMessage(ev *CotEvent) InboundMessage {
	text := ""
	if ev.Detail != nil && ev.Detail.Remarks != nil {
		text = ev.Detail.Remarks.Text
	}

	callsign := ""
	if ev.Detail != nil && ev.Detail.Contact != nil {
		callsign = ev.Detail.Contact.Callsign
	}

	// For position events, format as position text
	if strings.HasPrefix(ev.Type, "a-") && ev.Point.Lat != 0 && ev.Point.Lon != 0 {
		if text == "" {
			text = fmt.Sprintf("[TAK:%s] %.6f,%.6f", callsign, ev.Point.Lat, ev.Point.Lon)
		} else {
			text = fmt.Sprintf("[TAK:%s] %s (%.6f,%.6f)", callsign, text, ev.Point.Lat, ev.Point.Lon)
		}
	} else if text == "" {
		text = fmt.Sprintf("[TAK:%s] %s event", callsign, ev.Type)
	}

	return InboundMessage{
		Text:   text,
		Source: "tak",
	}
}

// MeshMessageToCotType determines the appropriate CoT event type for a MeshSat message.
func MeshMessageToCotType(msg *transport.MeshMessage) string {
	switch {
	case msg.PortNum == 67: // TELEMETRY_APP
		return CotEventTypeSensor
	case msg.PortNum == 3: // POSITION_APP
		return CotEventTypePosition
	case msg.PortNum == 1: // TEXT_MESSAGE_APP
		return CotEventTypeChat
	default:
		return CotEventTypePosition
	}
}
