package gateway

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"

	pb "meshsat/internal/gateway/takproto"

	"google.golang.org/protobuf/proto"
)

// TAK Protocol v1 magic byte — precedes all protobuf messages.
const takMagic = 0xBF

// CotEventToProto converts an XML CotEvent to a TAK Protocol v1 TakMessage.
func CotEventToProto(ev CotEvent) (*pb.TakMessage, error) {
	sendTime, _ := time.Parse(cotTimeFormat, ev.Time)
	startTime, _ := time.Parse(cotTimeFormat, ev.Start)
	staleTime, _ := time.Parse(cotTimeFormat, ev.Stale)

	cotPb := &pb.CotEvent{
		Type:      ev.Type,
		Uid:       ev.UID,
		How:       ev.How,
		SendTime:  uint64(sendTime.UnixMilli()),
		StartTime: uint64(startTime.UnixMilli()),
		StaleTime: uint64(staleTime.UnixMilli()),
		Lat:       ev.Point.Lat,
		Lon:       ev.Point.Lon,
		Hae:       ev.Point.Hae,
		Ce:        ev.Point.Ce,
		Le:        ev.Point.Le,
	}

	if ev.Detail != nil {
		detail := &pb.Detail{}
		if ev.Detail.Contact != nil {
			detail.Contact = &pb.Contact{
				Callsign: ev.Detail.Contact.Callsign,
			}
		}
		if ev.Detail.Group != nil {
			detail.Group = &pb.Group{
				Name: ev.Detail.Group.Name,
				Role: ev.Detail.Group.Role,
			}
		}
		if ev.Detail.Precision != nil {
			detail.PrecisionLocation = &pb.PrecisionLocation{
				Geopointsrc: ev.Detail.Precision.GeoPointSrc,
				Altsrc:      ev.Detail.Precision.AltSrc,
			}
		}
		if ev.Detail.Track != nil {
			detail.Track = &pb.Track{
				Speed:  ev.Detail.Track.Speed,
				Course: ev.Detail.Track.Course,
			}
		}
		if ev.Detail.Status != nil && ev.Detail.Status.Battery != "" {
			var bat uint32
			fmt.Sscanf(ev.Detail.Status.Battery, "%d", &bat)
			detail.Status = &pb.Status{Battery: bat}
		}
		if ev.Detail.Takv != nil {
			detail.Takv = &pb.Takv{
				Device:   ev.Detail.Takv.Device,
				Platform: ev.Detail.Takv.Platform,
				Os:       ev.Detail.Takv.OS,
				Version:  ev.Detail.Takv.Version,
			}
		}

		// Carry remaining detail elements (emergency, remarks, etc.) as xmlDetail
		xmlExtra := ""
		if ev.Detail.Emergency != nil {
			xmlExtra += fmt.Sprintf(`<emergency type="%s">%s</emergency>`, ev.Detail.Emergency.Type, ev.Detail.Emergency.Text)
		}
		if ev.Detail.Remarks != nil {
			xmlExtra += fmt.Sprintf(`<remarks source="%s">%s</remarks>`, ev.Detail.Remarks.Source, ev.Detail.Remarks.Text)
		}
		if xmlExtra != "" {
			detail.XmlDetail = xmlExtra
		}

		cotPb.Detail = detail
	}

	return &pb.TakMessage{CotEvent: cotPb}, nil
}

// ProtoToCotEvent converts a TAK Protocol v1 TakMessage back to an XML CotEvent.
func ProtoToCotEvent(msg *pb.TakMessage) (*CotEvent, error) {
	c := msg.GetCotEvent()
	if c == nil {
		return nil, fmt.Errorf("tak proto: no CotEvent in TakMessage")
	}

	ev := &CotEvent{
		Version: "2.0",
		UID:     c.GetUid(),
		Type:    c.GetType(),
		How:     c.GetHow(),
		Time:    time.UnixMilli(int64(c.GetSendTime())).UTC().Format(cotTimeFormat),
		Start:   time.UnixMilli(int64(c.GetStartTime())).UTC().Format(cotTimeFormat),
		Stale:   time.UnixMilli(int64(c.GetStaleTime())).UTC().Format(cotTimeFormat),
		Point: CotPoint{
			Lat: c.GetLat(),
			Lon: c.GetLon(),
			Hae: c.GetHae(),
			Ce:  c.GetCe(),
			Le:  c.GetLe(),
		},
	}

	d := c.GetDetail()
	if d != nil {
		detail := &CotDetail{}
		if ct := d.GetContact(); ct != nil {
			detail.Contact = &CotContact{Callsign: ct.GetCallsign()}
		}
		if g := d.GetGroup(); g != nil {
			detail.Group = &CotGroup{Name: g.GetName(), Role: g.GetRole()}
		}
		if p := d.GetPrecisionLocation(); p != nil {
			detail.Precision = &CotPrecision{GeoPointSrc: p.GetGeopointsrc(), AltSrc: p.GetAltsrc()}
		}
		if t := d.GetTrack(); t != nil {
			detail.Track = &CotTrack{Speed: t.GetSpeed(), Course: t.GetCourse()}
		}
		if s := d.GetStatus(); s != nil && s.GetBattery() > 0 {
			detail.Status = &CotStatus{Battery: fmt.Sprintf("%d", s.GetBattery())}
		}
		if v := d.GetTakv(); v != nil {
			detail.Takv = &CotTakv{Device: v.GetDevice(), Platform: v.GetPlatform(), OS: v.GetOs(), Version: v.GetVersion()}
		}
		ev.Detail = detail
	}

	return ev, nil
}

// MarshalTakProto serializes a TakMessage with TAK Protocol v1 stream framing.
// Format: 0xBF <varint payload_length> <protobuf payload>
func MarshalTakProto(msg *pb.TakMessage) ([]byte, error) {
	payload, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("tak proto marshal: %w", err)
	}

	// Frame: magic + varint length + payload
	lenBuf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(lenBuf, uint64(len(payload)))

	frame := make([]byte, 0, 1+n+len(payload))
	frame = append(frame, takMagic)
	frame = append(frame, lenBuf[:n]...)
	frame = append(frame, payload...)
	return frame, nil
}

// ReadTakProtoMessage reads one TAK Protocol v1 framed message from a reader.
// Returns the parsed TakMessage or error.
func ReadTakProtoMessage(r io.Reader) (*pb.TakMessage, error) {
	// Read magic byte
	magic := make([]byte, 1)
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, err
	}
	if magic[0] != takMagic {
		return nil, fmt.Errorf("tak proto: invalid magic byte 0x%02x", magic[0])
	}

	// Read varint payload length (byte-by-byte)
	var payloadLen uint64
	var shift uint
	for i := 0; i < binary.MaxVarintLen64; i++ {
		b := make([]byte, 1)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		payloadLen |= uint64(b[0]&0x7F) << shift
		if b[0]&0x80 == 0 {
			break
		}
		shift += 7
	}

	if payloadLen > 256*1024 { // 256KB sanity limit
		return nil, fmt.Errorf("tak proto: payload too large (%d bytes)", payloadLen)
	}

	// Read payload
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}

	msg := &pb.TakMessage{}
	if err := proto.Unmarshal(payload, msg); err != nil {
		return nil, fmt.Errorf("tak proto unmarshal: %w", err)
	}
	return msg, nil
}
