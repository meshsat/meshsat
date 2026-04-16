package gateway

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// AX25Address represents an AX.25 callsign + SSID.
type AX25Address struct {
	Call string
	SSID int
}

// AX25Frame represents a decoded AX.25 UI frame.
type AX25Frame struct {
	Dst  AX25Address
	Src  AX25Address
	Path []AX25Address
	Info []byte // information field (APRS payload)
}

// APRSPacket represents a decoded APRS packet.
type APRSPacket struct {
	Source   string  // source callsign-SSID
	Dest     string  // destination callsign
	Path     string  // digipeater path
	DataType byte    // APRS data type indicator ('!', '/', '=', '@', ':', etc.)
	Lat      float64 // decoded latitude
	Lon      float64 // decoded longitude
	Symbol   string  // 2-char APRS symbol (table + code)
	Comment  string  // freetext comment
	Message  string  // APRS message text (for ':' type)
	MsgTo    string  // message addressee
	MsgID    string  // message sequence number (for ack)
	Raw      string  // raw info field as string
}

// EncodeAX25Frame encodes an AX.25 UI frame for transmission.
func EncodeAX25Frame(dst, src AX25Address, path []AX25Address, info []byte) []byte {
	var buf []byte

	// Destination address (7 bytes)
	buf = append(buf, encodeAX25Addr(dst, false)...)

	// Source address (7 bytes) — set "last" flag if no path
	buf = append(buf, encodeAX25Addr(src, len(path) == 0)...)

	// Path addresses
	for i, p := range path {
		buf = append(buf, encodeAX25Addr(p, i == len(path)-1)...)
	}

	// Control field: UI frame
	buf = append(buf, 0x03)
	// Protocol ID: no layer 3
	buf = append(buf, 0xF0)
	// Information field
	buf = append(buf, info...)

	return buf
}

// DecodeAX25Frame decodes an AX.25 UI frame from raw bytes.
func DecodeAX25Frame(data []byte) (*AX25Frame, error) {
	if len(data) < 16 { // minimum: dst(7) + src(7) + ctrl(1) + pid(1)
		return nil, fmt.Errorf("ax25: frame too short (%d bytes)", len(data))
	}

	frame := &AX25Frame{}

	// Destination (bytes 0-6)
	frame.Dst = decodeAX25Addr(data[0:7])
	isLast := data[6]&0x01 == 1

	// Source (bytes 7-13)
	frame.Src = decodeAX25Addr(data[7:14])
	if !isLast {
		isLast = data[13]&0x01 == 1
	}

	offset := 14

	// Path (optional digipeaters)
	if !isLast && data[13]&0x01 == 0 {
		for offset+7 <= len(data) {
			addr := decodeAX25Addr(data[offset : offset+7])
			frame.Path = append(frame.Path, addr)
			last := data[offset+6]&0x01 == 1
			offset += 7
			if last {
				break
			}
		}
	}

	// Control + PID
	if offset+2 > len(data) {
		return nil, fmt.Errorf("ax25: no control/pid fields")
	}
	ctrl := data[offset]
	pid := data[offset+1]
	if ctrl != 0x03 {
		return nil, fmt.Errorf("ax25: not a UI frame (ctrl=0x%02x)", ctrl)
	}
	if pid != 0xF0 {
		return nil, fmt.Errorf("ax25: unexpected PID (0x%02x)", pid)
	}
	offset += 2

	// Information field
	if offset < len(data) {
		frame.Info = data[offset:]
	}

	return frame, nil
}

func encodeAX25Addr(addr AX25Address, last bool) []byte {
	call := strings.ToUpper(addr.Call)
	// Pad to 6 characters
	for len(call) < 6 {
		call += " "
	}
	if len(call) > 6 {
		call = call[:6]
	}

	buf := make([]byte, 7)
	for i := 0; i < 6; i++ {
		buf[i] = call[i] << 1
	}

	// SSID byte: bits 5-1 = SSID, bit 0 = last address flag
	ssid := byte(addr.SSID & 0x0F)
	buf[6] = (ssid << 1) | 0x60 // set reserved bits
	if last {
		buf[6] |= 0x01
	}

	return buf
}

func decodeAX25Addr(data []byte) AX25Address {
	var call strings.Builder
	for i := 0; i < 6; i++ {
		c := data[i] >> 1
		if c != ' ' {
			call.WriteByte(c)
		}
	}
	ssid := int((data[6] >> 1) & 0x0F)
	return AX25Address{
		Call: call.String(),
		SSID: ssid,
	}
}

// FormatCallsign formats a callsign with SSID (omitting -0).
func FormatCallsign(addr AX25Address) string {
	if addr.SSID == 0 {
		return addr.Call
	}
	return fmt.Sprintf("%s-%d", addr.Call, addr.SSID)
}

// ParseAPRSPacket decodes an APRS packet from the info field of an AX.25 frame.
// Returns error for non-APRS frames (Reticulum, binary protocols) so the
// gateway doesn't enqueue garbage as "messages" in the dashboard. [MESHSAT-403]
func ParseAPRSPacket(frame *AX25Frame) (*APRSPacket, error) {
	// Reject non-APRS frames by destination callsign.
	// APRS uses "APxxxx" tocalls. Reticulum uses "RTICUL".
	dstCall := strings.TrimRight(frame.Dst.Call, " ")
	if dstCall == "RTICUL" || dstCall == "RNS" {
		return nil, fmt.Errorf("not APRS: destination %s is Reticulum", dstCall)
	}

	// Reject frames with mostly non-printable info (binary protocols).
	if len(frame.Info) > 0 {
		nonPrintable := 0
		for _, b := range frame.Info {
			if b < 0x20 && b != '\r' && b != '\n' && b != '\t' {
				nonPrintable++
			}
		}
		if len(frame.Info) > 4 && nonPrintable > len(frame.Info)/4 {
			return nil, fmt.Errorf("not APRS: info field is %d%% non-printable", nonPrintable*100/len(frame.Info))
		}
	}

	pkt := &APRSPacket{
		Source: FormatCallsign(frame.Src),
		Dest:   FormatCallsign(frame.Dst),
		Raw:    string(frame.Info),
	}

	if len(frame.Path) > 0 {
		parts := make([]string, len(frame.Path))
		for i, p := range frame.Path {
			parts[i] = FormatCallsign(p)
		}
		pkt.Path = strings.Join(parts, ",")
	}

	if len(frame.Info) == 0 {
		return pkt, nil
	}

	pkt.DataType = frame.Info[0]

	switch pkt.DataType {
	case '!', '=': // Position without timestamp
		parseAPRSPosition(pkt, string(frame.Info[1:]))
	case '/', '@': // Position with timestamp
		if len(frame.Info) > 8 {
			parseAPRSPosition(pkt, string(frame.Info[8:])) // skip timestamp
		}
	case ':': // Message
		parseAPRSMessage(pkt, string(frame.Info[1:]))
	}

	return pkt, nil
}

// parseAPRSPosition extracts lat/lon from an uncompressed APRS position string.
// Format: DDMM.MMN/DDDMM.MMW$... where $ is symbol code
func parseAPRSPosition(pkt *APRSPacket, s string) {
	if len(s) < 19 {
		pkt.Comment = s
		return
	}

	// Try uncompressed format: 4903.50N/07201.75W-
	lat, err := parseAPRSLat(s[0:8])
	if err != nil {
		pkt.Comment = s
		return
	}
	lon, err := parseAPRSLon(s[9:18])
	if err != nil {
		pkt.Comment = s
		return
	}

	pkt.Lat = lat
	pkt.Lon = lon
	pkt.Symbol = string([]byte{s[8], s[18]})
	if len(s) > 19 {
		pkt.Comment = s[19:]
	}
}

// parseAPRSLat parses "DDMM.MMN" format latitude.
func parseAPRSLat(s string) (float64, error) {
	if len(s) != 8 {
		return 0, fmt.Errorf("invalid lat length")
	}
	deg, err := strconv.ParseFloat(s[0:2], 64)
	if err != nil {
		return 0, err
	}
	min, err := strconv.ParseFloat(s[2:7], 64)
	if err != nil {
		return 0, err
	}
	lat := deg + min/60.0
	if s[7] == 'S' {
		lat = -lat
	}
	return lat, nil
}

// parseAPRSLon parses "DDDMM.MMW" format longitude.
func parseAPRSLon(s string) (float64, error) {
	if len(s) != 9 {
		return 0, fmt.Errorf("invalid lon length")
	}
	deg, err := strconv.ParseFloat(s[0:3], 64)
	if err != nil {
		return 0, err
	}
	min, err := strconv.ParseFloat(s[3:8], 64)
	if err != nil {
		return 0, err
	}
	lon := deg + min/60.0
	if s[8] == 'W' {
		lon = -lon
	}
	return lon, nil
}

// parseAPRSMessage parses an APRS message packet.
// Format: :ADDRESSEE:message text{seq
func parseAPRSMessage(pkt *APRSPacket, s string) {
	if len(s) < 11 {
		return
	}
	// Addressee is 9 chars padded with spaces, followed by ':'
	pkt.MsgTo = strings.TrimSpace(s[0:9])
	if len(s) > 10 && s[9] == ':' {
		msg := s[10:]
		// Check for message ID after '{'
		if idx := strings.LastIndex(msg, "{"); idx >= 0 {
			pkt.MsgID = msg[idx+1:]
			msg = msg[:idx]
		}
		pkt.Message = msg
	}
}

// EncodeAPRSPosition creates an APRS uncompressed position string.
// Returns: !DDMM.MMN/DDDMM.MMW-comment
func EncodeAPRSPosition(lat, lon float64, symbolTable, symbolCode byte, comment string) []byte {
	latDir := byte('N')
	if lat < 0 {
		latDir = 'S'
		lat = -lat
	}
	latDeg := int(lat)
	latMin := (lat - float64(latDeg)) * 60.0

	lonDir := byte('E')
	if lon < 0 {
		lonDir = 'W'
		lon = -lon
	}
	lonDeg := int(lon)
	lonMin := (lon - float64(lonDeg)) * 60.0

	s := fmt.Sprintf("!%02d%05.2f%c%c%03d%05.2f%c%c%s",
		latDeg, latMin, latDir,
		symbolTable,
		lonDeg, lonMin, lonDir,
		symbolCode,
		comment,
	)
	return []byte(s)
}

// EncodeAPRSMessage creates an APRS message packet info field.
// Format: :ADDRESSEE:message text{seq
func EncodeAPRSMessage(to, text, msgID string) []byte {
	// Pad addressee to 9 characters
	padded := to
	for len(padded) < 9 {
		padded += " "
	}
	if msgID != "" {
		return []byte(fmt.Sprintf(":%s:%s{%s", padded, text, msgID))
	}
	return []byte(fmt.Sprintf(":%s:%s", padded, text))
}

// DistanceKm returns the distance in km between two lat/lon points.
func DistanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0 // Earth radius km
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return R * c
}
