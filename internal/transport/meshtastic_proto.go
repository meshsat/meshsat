package transport

// Meshtastic protocol engine — protobuf parsers and builders.
// Uses official generated Go bindings from buf.build/gen/go/meshtastic/protobufs.
// Hand-rolled encoding replaced in MESHSAT-242 to eliminate field-number bugs.

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// ============================================================================
// Constants
// ============================================================================

// Meshtastic PortNum values
const (
	PortNumTextMessage           = 1
	PortNumPosition              = 3
	PortNumNodeInfo              = 4
	PortNumRouting               = 5
	PortNumAdminApp              = 6
	PortNumTextMessageCompressed = 7
	PortNumWaypoint              = 8
	PortNumDetectionSensor       = 10
	PortNumAlert                 = 11
	PortNumReply                 = 32
	PortNumSerial                = 64
	PortNumStoreForward          = 65
	PortNumRangeTest             = 66
	PortNumTelemetry             = 67
	PortNumTraceroute            = 70
	PortNumNeighborInfo          = 71
	PortNumMapReport             = 73
	PortNumPrivate               = 256
)

// Routing error reasons (from meshtastic/protobufs mesh.proto Routing.Error)
const (
	RoutingErrorNone          = 0
	RoutingErrorNoRoute       = 1
	RoutingErrorGotNAK        = 2
	RoutingErrorTimeout       = 3
	RoutingErrorNoInterface   = 4
	RoutingErrorMaxRetransmit = 5
	RoutingErrorNoChannel     = 6
	RoutingErrorTooLarge      = 7
	RoutingErrorNoResponse    = 8
	RoutingErrorDutyCycle     = 9
	RoutingErrorBadRequest    = 32
	RoutingErrorNotAuthorized = 33
)

// RoutingInfo holds parsed ROUTING_APP data (ACK/NAK/error).
type RoutingInfo struct {
	ErrorReason uint32 `json:"error_reason"`
	ErrorName   string `json:"error_name"`
}

// routingErrorName returns a human-readable name for a routing error.
func routingErrorName(reason uint32) string {
	name := pb.Routing_Error(reason).String()
	if name != "" && !strings.HasPrefix(name, "Routing_Error(") {
		return name
	}
	switch reason {
	case RoutingErrorNone:
		return "ACK"
	case RoutingErrorNoRoute:
		return "NO_ROUTE"
	case RoutingErrorGotNAK:
		return "GOT_NAK"
	case RoutingErrorTimeout:
		return "TIMEOUT"
	case RoutingErrorNoInterface:
		return "NO_INTERFACE"
	case RoutingErrorMaxRetransmit:
		return "MAX_RETRANSMIT"
	case RoutingErrorNoChannel:
		return "NO_CHANNEL"
	case RoutingErrorTooLarge:
		return "TOO_LARGE"
	case RoutingErrorNoResponse:
		return "NO_RESPONSE"
	case RoutingErrorDutyCycle:
		return "DUTY_CYCLE_LIMIT"
	case RoutingErrorBadRequest:
		return "BAD_REQUEST"
	case RoutingErrorNotAuthorized:
		return "NOT_AUTHORIZED"
	default:
		return fmt.Sprintf("UNKNOWN_%d", reason)
	}
}

// parseRouting parses a ROUTING_APP payload using official protobuf unmarshal.
func parseRouting(data []byte) *RoutingInfo {
	r := &pb.Routing{}
	if err := proto.Unmarshal(data, r); err != nil {
		return &RoutingInfo{ErrorName: "PARSE_ERROR"}
	}
	info := &RoutingInfo{}
	if er, ok := r.GetVariant().(*pb.Routing_ErrorReason); ok {
		info.ErrorReason = uint32(er.ErrorReason)
	}
	info.ErrorName = routingErrorName(info.ErrorReason)
	return info
}

// Config section enum values (for get_config_request)
const (
	ConfigTypeDevice     = 0
	ConfigTypePosition   = 1
	ConfigTypePower      = 2
	ConfigTypeNetwork    = 3
	ConfigTypeDisplay    = 4
	ConfigTypeLora       = 5
	ConfigTypeBluetooth  = 6
	ConfigTypeSecurity   = 7
	ConfigTypeSessionkey = 8
	ConfigTypeDeviceUI   = 9
)

// ModuleConfig section enum values (for get_module_config_request)
const (
	ModuleConfigMQTT                 = 0
	ModuleConfigSerial               = 1
	ModuleConfigExternalNotification = 2
	ModuleConfigStoreForward         = 3
	ModuleConfigRangeTest            = 4
	ModuleConfigTelemetry            = 5
	ModuleConfigCannedMessage        = 6
	ModuleConfigAudio                = 7
	ModuleConfigRemoteHardware       = 8
	ModuleConfigNeighborInfo         = 9
	ModuleConfigAmbientLighting      = 10
	ModuleConfigDetectionSensor      = 11
	ModuleConfigPaxcounter           = 12
	ModuleConfigStatusMessage        = 13
	ModuleConfigTrafficManagement    = 14
	ModuleConfigTAKConfig            = 15
)

// StoreAndForward RequestResponse enum values
const (
	SFClientHistory = 65
	SFClientStats   = 66
	SFClientPing    = 67
)

func portNumName(pn int) string {
	name := pb.PortNum(pn).String()
	if name != "" && !strings.HasPrefix(name, "PortNum(") && !isDigits(name) {
		return name
	}
	return fmt.Sprintf("PORTNUM_%d", pn)
}

// isDigits returns true if s is non-empty and contains only ASCII digits.
func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

func hwModelName(model int) string {
	name := pb.HardwareModel(model).String()
	if name != "" && !strings.HasPrefix(name, "HardwareModel(") && !isDigits(name) {
		return name
	}
	return fmt.Sprintf("HW_MODEL_%d", model)
}

// computeSignalQuality determines LoRa signal quality from RSSI and SNR.
func computeSignalQuality(rssi, snr float64) (quality string, notes string) {
	switch {
	case snr >= -7 && rssi >= -115:
		quality = "GOOD"
	case snr >= -15 && rssi >= -126:
		quality = "FAIR"
	default:
		quality = "BAD"
	}

	var parts []string
	if rssi < -126 {
		parts = append(parts, "very weak signal")
	} else if rssi < -115 {
		parts = append(parts, "marginal signal strength")
	}
	if snr < -15 {
		parts = append(parts, "high noise floor")
	} else if snr < -7 {
		parts = append(parts, "moderate noise present")
	}
	if len(parts) > 0 {
		notes = strings.Join(parts, "; ")
	}
	return quality, notes
}

// ============================================================================
// Proto Types — parsed protobuf structures (internal API, unchanged)
// ============================================================================

// ProtoFromRadio represents a parsed FromRadio message.
type ProtoFromRadio struct {
	ID               uint32
	Packet           *ProtoMeshPacket
	MyInfo           *ProtoMyNodeInfo
	NodeInfo         *ProtoNodeInfo
	ConfigRaw        []byte // Config message (raw protobuf — kept for decodeProtoToMap passthrough)
	ConfigCompleteID uint32
	ModuleConfigRaw  []byte // ModuleConfig message (raw protobuf)
	ChannelRaw       []byte // Channel message (raw protobuf)
}

// ProtoMeshPacket represents a parsed MeshPacket.
type ProtoMeshPacket struct {
	From         uint32
	To           uint32
	Channel      uint32
	ID           uint32
	Decoded      *ProtoData
	Encrypted    []byte
	RxTime       uint32
	RxSNR        float32
	RxRSSI       int32
	HopLimit     uint32
	WantAck      bool
	HopStart     uint32
	ViaMqtt      bool   // field 14 — set when packet was relayed via MQTT
	PublicKey    []byte // field 16 — sender's X25519 public key (PKI encryption, Meshtastic 2.5+)
	PKIEncrypted bool   // field 17 — true if payload uses PKI (not channel) encryption
}

// ProtoData represents a decoded Data submessage.
type ProtoData struct {
	PortNum      uint32
	Payload      []byte
	WantResponse bool
	Bitfield     uint32 // field 4 — contains ok_to_mqtt flag (bit 0)
	RequestID    uint32 // field 6 — correlates ACK/NAK to original request
	ReplyID      uint32 // field 7 — correlates response to request
}

// OkToMQTT returns whether the sender has opted in to MQTT relay.
func (d *ProtoData) OkToMQTT() bool {
	return d.Bitfield&1 != 0
}

// ProtoMyNodeInfo represents my_info from config download.
type ProtoMyNodeInfo struct {
	MyNodeNum uint32
}

// ProtoNodeInfo represents a NodeInfo from config download or mesh.
type ProtoNodeInfo struct {
	Num           uint32
	User          *ProtoUser
	Position      *ProtoPosition
	DeviceMetrics *ProtoDeviceMetrics
	SNR           float32
	LastHeard     uint32
	Channel       uint32 // field 7 — channel index
	HopsAway      uint32 // field 8
	ViaMqtt       bool   // field 9
	IsFavorite    bool   // field 10
	IsIgnored     bool   // field 11
}

// ProtoUser represents a User submessage.
type ProtoUser struct {
	ID             string
	LongName       string
	ShortName      string
	Macaddr        []byte // field 4 — MAC address
	HWModel        uint32 // field 5
	Role           uint32 // field 6 — device role enum
	PublicKey      []byte // field 7 — X25519 public key
	IsLicensed     bool   // field 8 — licensed HAM operator
	IsUnmessagable bool   // field 9 — node does not accept messages
}

// ProtoPosition represents a Position submessage.
type ProtoPosition struct {
	LatitudeI     int32
	LongitudeI    int32
	Altitude      int32
	Time          uint32 // field 4 — GPS fix time (fixed32)
	GroundSpeed   uint32 // field 9 — m/s
	GroundTrack   uint32 // field 10 — heading degrees * 1e5
	FixQuality    uint32 // field 11 — 0=invalid, 1=GPS, 2=DGPS, ...
	FixType       uint32 // field 12 — 0=none, 2=2D, 3=3D
	PDOP          uint32 // field 16 — *100
	HDOP          uint32 // field 17 — *100
	VDOP          uint32 // field 18 — *100
	SatsInView    uint32 // field 19
	PrecisionBits uint32 // field 20 — position precision (32=full, lower=truncated)
	Heading       uint32 // field 22 — compass heading degrees * 1e5 (independent of movement)
	Speed         uint32 // field 23 — speed m/s (alias for devices that use this instead of ground_speed)
}

// ProtoDeviceMetrics represents a DeviceMetrics submessage.
type ProtoDeviceMetrics struct {
	BatteryLevel  uint32
	Voltage       float32
	ChannelUtil   float32
	AirUtilTx     float32
	UptimeSeconds uint32
}

// ============================================================================
// Parsers — using official proto.Unmarshal
// ============================================================================

func parseFromRadio(data []byte) (*ProtoFromRadio, error) {
	fr := &ProtoFromRadio{}
	msg := &pb.FromRadio{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return fr, nil // graceful degradation like original
	}

	fr.ID = msg.GetId()

	switch v := msg.GetPayloadVariant().(type) {
	case *pb.FromRadio_Packet:
		if v.Packet != nil {
			fr.Packet = convertMeshPacket(v.Packet)
		}
	case *pb.FromRadio_MyInfo:
		if v.MyInfo != nil {
			fr.MyInfo = &ProtoMyNodeInfo{MyNodeNum: v.MyInfo.GetMyNodeNum()}
		}
	case *pb.FromRadio_NodeInfo:
		if v.NodeInfo != nil {
			fr.NodeInfo = convertNodeInfo(v.NodeInfo)
		}
	case *pb.FromRadio_Config:
		if v.Config != nil {
			// Keep raw bytes for decodeProtoToMap passthrough
			fr.ConfigRaw, _ = proto.Marshal(v.Config)
		}
	case *pb.FromRadio_ConfigCompleteId:
		fr.ConfigCompleteID = v.ConfigCompleteId
	case *pb.FromRadio_ModuleConfig:
		if v.ModuleConfig != nil {
			fr.ModuleConfigRaw, _ = proto.Marshal(v.ModuleConfig)
		}
	case *pb.FromRadio_Channel:
		if v.Channel != nil {
			fr.ChannelRaw, _ = proto.Marshal(v.Channel)
		}
	}

	return fr, nil
}

func convertMeshPacket(p *pb.MeshPacket) *ProtoMeshPacket {
	pkt := &ProtoMeshPacket{
		From:         p.GetFrom(),
		To:           p.GetTo(),
		Channel:      p.GetChannel(),
		ID:           p.GetId(),
		RxTime:       p.GetRxTime(),
		RxSNR:        p.GetRxSnr(),
		RxRSSI:       p.GetRxRssi(),
		HopLimit:     p.GetHopLimit(),
		WantAck:      p.GetWantAck(),
		HopStart:     p.GetHopStart(),
		ViaMqtt:      p.GetViaMqtt(),
		PublicKey:    p.GetPublicKey(),
		PKIEncrypted: p.GetPkiEncrypted(),
	}

	switch v := p.GetPayloadVariant().(type) {
	case *pb.MeshPacket_Decoded:
		if v.Decoded != nil {
			pkt.Decoded = &ProtoData{
				PortNum:      uint32(v.Decoded.GetPortnum()),
				Payload:      v.Decoded.GetPayload(),
				WantResponse: v.Decoded.GetWantResponse(),
				Bitfield:     v.Decoded.GetBitfield(),
				RequestID:    v.Decoded.GetRequestId(),
				ReplyID:      v.Decoded.GetReplyId(),
			}
		}
	case *pb.MeshPacket_Encrypted:
		pkt.Encrypted = v.Encrypted
	}

	return pkt
}

func convertNodeInfo(ni *pb.NodeInfo) *ProtoNodeInfo {
	info := &ProtoNodeInfo{
		Num:        ni.GetNum(),
		SNR:        ni.GetSnr(),
		LastHeard:  ni.GetLastHeard(),
		Channel:    uint32(ni.GetChannel()),
		HopsAway:   ni.GetHopsAway(),
		ViaMqtt:    ni.GetViaMqtt(),
		IsFavorite: ni.GetIsFavorite(),
		IsIgnored:  ni.GetIsIgnored(),
	}

	if u := ni.GetUser(); u != nil {
		info.User = &ProtoUser{
			ID:             u.GetId(),
			LongName:       u.GetLongName(),
			ShortName:      u.GetShortName(),
			Macaddr:        u.GetMacaddr(),
			HWModel:        uint32(u.GetHwModel()),
			Role:           uint32(u.GetRole()),
			PublicKey:      u.GetPublicKey(),
			IsLicensed:     u.GetIsLicensed(),
			IsUnmessagable: u.GetIsUnmessagable(),
		}
	}

	if p := ni.GetPosition(); p != nil {
		info.Position = convertPosition(p)
	}

	if dm := ni.GetDeviceMetrics(); dm != nil {
		info.DeviceMetrics = convertDeviceMetrics(dm)
	}

	return info
}

func convertPosition(p *pb.Position) *ProtoPosition {
	return &ProtoPosition{
		LatitudeI:     p.GetLatitudeI(),
		LongitudeI:    p.GetLongitudeI(),
		Altitude:      p.GetAltitude(),
		Time:          p.GetTime(),
		GroundSpeed:   p.GetGroundSpeed(),
		GroundTrack:   p.GetGroundTrack(),
		FixQuality:    p.GetFixQuality(),
		FixType:       p.GetFixType(),
		PDOP:          p.GetPDOP(),
		HDOP:          p.GetHDOP(),
		VDOP:          p.GetVDOP(),
		SatsInView:    p.GetSatsInView(),
		PrecisionBits: p.GetPrecisionBits(),
	}
}

func convertDeviceMetrics(dm *pb.DeviceMetrics) *ProtoDeviceMetrics {
	return &ProtoDeviceMetrics{
		BatteryLevel:  dm.GetBatteryLevel(),
		Voltage:       dm.GetVoltage(),
		ChannelUtil:   dm.GetChannelUtilization(),
		AirUtilTx:     dm.GetAirUtilTx(),
		UptimeSeconds: dm.GetUptimeSeconds(),
	}
}

func parseUser(data []byte) (*ProtoUser, error) {
	u := &pb.User{}
	if err := proto.Unmarshal(data, u); err != nil {
		return &ProtoUser{}, nil
	}
	return &ProtoUser{
		ID:             u.GetId(),
		LongName:       u.GetLongName(),
		ShortName:      u.GetShortName(),
		Macaddr:        u.GetMacaddr(),
		HWModel:        uint32(u.GetHwModel()),
		Role:           uint32(u.GetRole()),
		PublicKey:      u.GetPublicKey(),
		IsLicensed:     u.GetIsLicensed(),
		IsUnmessagable: u.GetIsUnmessagable(),
	}, nil
}

// ParsePositionPayload parses a POSITION_APP protobuf payload into a ProtoPosition.
// Exported for use by other packages (e.g., TAK gateway).
func ParsePositionPayload(data []byte) (*ProtoPosition, error) {
	return parsePosition(data)
}

func parsePosition(data []byte) (*ProtoPosition, error) {
	p := &pb.Position{}
	if err := proto.Unmarshal(data, p); err != nil {
		return &ProtoPosition{}, nil
	}
	return convertPosition(p), nil
}

func parseMyNodeInfo(data []byte) (*ProtoMyNodeInfo, error) {
	m := &pb.MyNodeInfo{}
	if err := proto.Unmarshal(data, m); err != nil {
		return &ProtoMyNodeInfo{}, nil
	}
	return &ProtoMyNodeInfo{MyNodeNum: m.GetMyNodeNum()}, nil
}

func parseNodeInfo(data []byte) (*ProtoNodeInfo, error) {
	ni := &pb.NodeInfo{}
	if err := proto.Unmarshal(data, ni); err != nil {
		return &ProtoNodeInfo{}, nil
	}
	return convertNodeInfo(ni), nil
}

// parseDeviceMetrics handles both Telemetry wrapper and raw DeviceMetrics.
func parseDeviceMetrics(data []byte) (*ProtoDeviceMetrics, error) {
	// Try as Telemetry first (the common case from the wire)
	t := &pb.Telemetry{}
	if err := proto.Unmarshal(data, t); err == nil {
		if dm, ok := t.GetVariant().(*pb.Telemetry_DeviceMetrics); ok && dm.DeviceMetrics != nil {
			return convertDeviceMetrics(dm.DeviceMetrics), nil
		}
	}
	// Fallback: try as raw DeviceMetrics
	dm := &pb.DeviceMetrics{}
	if err := proto.Unmarshal(data, dm); err != nil {
		return &ProtoDeviceMetrics{}, nil
	}
	return convertDeviceMetrics(dm), nil
}

func parseDeviceMetricsProto(data []byte) (*ProtoDeviceMetrics, error) {
	dm := &pb.DeviceMetrics{}
	if err := proto.Unmarshal(data, dm); err != nil {
		return &ProtoDeviceMetrics{}, nil
	}
	return convertDeviceMetrics(dm), nil
}

// ProtoEnvironmentMetrics represents environment sensor data (Telemetry field 3).
type ProtoEnvironmentMetrics struct {
	Temperature     float32 // field 1
	Humidity        float32 // field 2
	Pressure        float32 // field 3
	GasResistance   float32 // field 4
	Voltage         float32 // field 5 — sensor supply voltage
	Current         float32 // field 6 — sensor current draw
	IAQ             uint32  // field 7 — Indoor Air Quality index
	Distance        float32 // field 8 — distance sensor (mm)
	Lux             float32 // field 9 — ambient light (lux)
	WhiteLight      float32 // field 10 — white lux value
	IR              float32 // field 11 — infrared lux
	UV              float32 // field 12 — UV index
	WindDirection   uint32  // field 13 — degrees
	WindSpeed       float32 // field 14 — m/s
	WindGust        float32 // field 15 — m/s
	WindLull        float32 // field 16 — m/s
	Weight          float32 // field 17 — kg (e.g. beehive)
	SoilTemperature float32 // field 18
	SoilMoisture    float32 // field 19
	Radiation       float32 // field 20 — μSv/h
}

// parseEnvironmentMetrics extracts environment metrics from a Telemetry message.
func parseEnvironmentMetrics(data []byte) *ProtoEnvironmentMetrics {
	t := &pb.Telemetry{}
	if err := proto.Unmarshal(data, t); err != nil {
		return nil
	}
	em, ok := t.GetVariant().(*pb.Telemetry_EnvironmentMetrics)
	if !ok || em.EnvironmentMetrics == nil {
		return nil
	}
	e := em.EnvironmentMetrics
	return &ProtoEnvironmentMetrics{
		Temperature:     e.GetTemperature(),
		Humidity:        e.GetRelativeHumidity(),
		Pressure:        e.GetBarometricPressure(),
		GasResistance:   e.GetGasResistance(),
		Voltage:         e.GetVoltage(),
		Current:         e.GetCurrent(),
		IAQ:             uint32(e.GetIaq()),
		Distance:        e.GetDistance(),
		Lux:             e.GetLux(),
		WhiteLight:      e.GetWhiteLux(),
		IR:              e.GetIrLux(),
		UV:              e.GetUvLux(),
		WindDirection:   uint32(e.GetWindDirection()),
		WindSpeed:       e.GetWindSpeed(),
		WindGust:        e.GetWindGust(),
		WindLull:        e.GetWindLull(),
		Weight:          e.GetWeight(),
		SoilTemperature: e.GetSoilTemperature(),
		SoilMoisture:    float32(e.GetSoilMoisture()),
		Radiation:       e.GetRadiation(),
	}
}

// ============================================================================
// Builders — construct ToRadio protobuf messages using official types
// ============================================================================

// buildWantConfigID builds a ToRadio protobuf with want_config_id set.
func buildWantConfigID(configID uint32) []byte {
	msg := &pb.ToRadio{
		PayloadVariant: &pb.ToRadio_WantConfigId{WantConfigId: configID},
	}
	data, err := proto.Marshal(msg)
	if err != nil {
		return nil
	}
	return data
}

// buildToRadioPacket wraps a MeshPacket into a ToRadio message (field 1).
func buildToRadioPacket(meshPacketBytes []byte) []byte {
	pkt := &pb.MeshPacket{}
	if err := proto.Unmarshal(meshPacketBytes, pkt); err != nil {
		// Fallback: wrap raw bytes using hand-rolled encoding
		buf := make([]byte, 0, len(meshPacketBytes)+8)
		buf = append(buf, 0x0A) // field 1, length-delimited
		buf = appendVarint(buf, uint64(len(meshPacketBytes)))
		buf = append(buf, meshPacketBytes...)
		return buf
	}
	msg := &pb.ToRadio{
		PayloadVariant: &pb.ToRadio_Packet{Packet: pkt},
	}
	data, err := proto.Marshal(msg)
	if err != nil {
		return nil
	}
	return data
}

// buildTextMessage builds a MeshPacket with TEXT_MESSAGE_APP portnum.
func buildTextMessage(text string, to uint32, channel uint32) []byte {
	return buildMeshPacketBytes([]byte(text), int(pb.PortNum_TEXT_MESSAGE_APP), to, channel, true, false)
}

// buildMeshPacket builds the outer MeshPacket wrapper.
func buildMeshPacket(decodedPayload []byte, to uint32, channel uint32) []byte {
	// This function expects pre-encoded Data bytes — parse and re-encode
	return buildMeshPacketFromData(decodedPayload, to, channel, false)
}

// buildMeshPacketOpts builds a MeshPacket with optional via_mqtt flag.
func buildMeshPacketOpts(decodedPayload []byte, to uint32, channel uint32, viaMqtt bool) []byte {
	return buildMeshPacketFromData(decodedPayload, to, channel, viaMqtt)
}

// buildMeshPacketFromData builds a MeshPacket from pre-encoded Data protobuf bytes.
func buildMeshPacketFromData(dataBytes []byte, to uint32, channel uint32, viaMqtt bool) []byte {
	if to == 0 {
		to = 0xFFFFFFFF // Broadcast
	}
	d := &pb.Data{}
	if err := proto.Unmarshal(dataBytes, d); err != nil {
		return nil
	}
	pkt := &pb.MeshPacket{
		To:             to,
		Channel:        channel,
		RxTime:         uint32(time.Now().Unix()),
		HopLimit:       3,
		WantAck:        true,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: d},
	}
	if viaMqtt {
		pkt.ViaMqtt = true
	}
	data, err := proto.Marshal(pkt)
	if err != nil {
		return nil
	}
	return data
}

// buildRawPacket builds a MeshPacket with arbitrary portnum and payload.
func buildRawPacket(payload []byte, portnum int, to uint32, channel uint32, wantAck bool) []byte {
	return buildMeshPacketBytes(payload, portnum, to, channel, wantAck, false)
}

// buildMeshPacketBytes builds a complete MeshPacket with explicit portnum and payload.
func buildMeshPacketBytes(payload []byte, portnum int, to uint32, channel uint32, wantAck bool, viaMqtt bool) []byte {
	if to == 0 {
		to = 0xFFFFFFFF
	}
	pkt := &pb.MeshPacket{
		To:       to,
		Channel:  channel,
		RxTime:   uint32(time.Now().Unix()),
		HopLimit: 3,
		WantAck:  wantAck,
		PayloadVariant: &pb.MeshPacket_Decoded{
			Decoded: &pb.Data{
				Portnum: pb.PortNum(portnum),
				Payload: payload,
			},
		},
	}
	if viaMqtt {
		pkt.ViaMqtt = true
	}
	data, err := proto.Marshal(pkt)
	if err != nil {
		return nil
	}
	return data
}

// buildEncryptedPacket builds a MeshPacket with the encrypted field (field 5)
// instead of decoded (field 4). Used for AES-256-CTR passthrough relay.
func buildEncryptedPacket(encryptedPayload []byte, to uint32, channel uint32, hopLimit uint32) []byte {
	if to == 0 {
		to = 0xFFFFFFFF
	}
	if hopLimit == 0 {
		hopLimit = 3
	}
	pkt := &pb.MeshPacket{
		To:             to,
		Channel:        channel,
		HopLimit:       hopLimit,
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: encryptedPayload},
	}
	data, err := proto.Marshal(pkt)
	if err != nil {
		return nil
	}
	return data
}

// buildAdminToRadio wraps an admin message in Data → MeshPacket → ToRadio.
func buildAdminToRadio(myNodeNum, destNode uint32, adminPayload []byte) []byte {
	admin := &pb.AdminMessage{}
	if err := proto.Unmarshal(adminPayload, admin); err != nil {
		return nil
	}
	return buildAdminToRadioMsg(myNodeNum, destNode, admin)
}

// buildAdminToRadioMsg wraps a typed AdminMessage in Data → MeshPacket → ToRadio.
func buildAdminToRadioMsg(myNodeNum, destNode uint32, admin *pb.AdminMessage) []byte {
	adminBytes, err := proto.Marshal(admin)
	if err != nil {
		return nil
	}
	pkt := &pb.MeshPacket{
		From:     myNodeNum,
		To:       destNode,
		HopLimit: 3,
		WantAck:  true,
		PayloadVariant: &pb.MeshPacket_Decoded{
			Decoded: &pb.Data{
				Portnum:      pb.PortNum_ADMIN_APP,
				Payload:      adminBytes,
				WantResponse: true,
			},
		},
	}
	pktBytes, err := proto.Marshal(pkt)
	if err != nil {
		return nil
	}
	toRadio := &pb.ToRadio{
		PayloadVariant: &pb.ToRadio_Packet{Packet: pkt},
	}
	// Use the struct directly instead of re-marshaling pktBytes
	_ = pktBytes
	data, err := proto.Marshal(toRadio)
	if err != nil {
		return nil
	}
	return data
}

// buildAdminReboot builds a ToRadio with AdminMessage reboot_seconds.
func buildAdminReboot(myNodeNum, destNode uint32, delaySecs int) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_RebootSeconds{RebootSeconds: int32(delaySecs)},
	}
	return buildAdminToRadioMsg(myNodeNum, destNode, admin)
}

// buildAdminSetTime builds a ToRadio with AdminMessage set_time_only.
func buildAdminSetTime(myNodeNum, destNode uint32, unixSec uint32) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_SetTimeOnly{SetTimeOnly: unixSec},
	}
	return buildAdminToRadioMsg(myNodeNum, destNode, admin)
}

// buildAdminFactoryReset builds a ToRadio with AdminMessage factory_reset_device.
func buildAdminFactoryReset(myNodeNum, destNode uint32) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_FactoryResetDevice{FactoryResetDevice: 1},
	}
	return buildAdminToRadioMsg(myNodeNum, destNode, admin)
}

// buildAdminRemoveNode builds a ToRadio with AdminMessage remove_by_nodenum.
func buildAdminRemoveNode(myNodeNum, nodeNum uint32) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_RemoveByNodenum{RemoveByNodenum: nodeNum},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// buildAdminSetConfig builds a ToRadio with AdminMessage set_config.
func buildAdminSetConfig(myNodeNum uint32, configData []byte) []byte {
	cfg := &pb.Config{}
	if err := proto.Unmarshal(configData, cfg); err != nil {
		return nil
	}
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_SetConfig{SetConfig: cfg},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// buildAdminSetModuleConfig builds a ToRadio with AdminMessage set_module_config.
func buildAdminSetModuleConfig(myNodeNum uint32, configData []byte) []byte {
	cfg := &pb.ModuleConfig{}
	if err := proto.Unmarshal(configData, cfg); err != nil {
		return nil
	}
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_SetModuleConfig{SetModuleConfig: cfg},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// buildSetChannel builds a ToRadio for channel configuration.
func buildSetChannel(myNodeNum uint32, index uint32, name string, psk []byte, role int, uplinkEnabled, downlinkEnabled bool) []byte {
	ch := &pb.Channel{
		Index: int32(index),
		Settings: &pb.ChannelSettings{
			Psk:             psk,
			Name:            name,
			UplinkEnabled:   uplinkEnabled,
			DownlinkEnabled: downlinkEnabled,
		},
		Role: pb.Channel_Role(role),
	}
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_SetChannel{SetChannel: ch},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// ParseWaypointPayload parses a WAYPOINT_APP protobuf payload into a Waypoint.
// Exported for use by other packages (e.g., TAK gateway).
func ParseWaypointPayload(data []byte) (*Waypoint, error) {
	w := &pb.Waypoint{}
	if err := proto.Unmarshal(data, w); err != nil {
		return nil, err
	}
	return &Waypoint{
		ID:          w.GetId(),
		Name:        w.GetName(),
		Description: w.GetDescription(),
		Latitude:    float64(w.GetLatitudeI()) / 1e7,
		Longitude:   float64(w.GetLongitudeI()) / 1e7,
		Icon:        int(w.GetIcon()),
		Expire:      int64(w.GetExpire()),
	}, nil
}

// buildWaypointPacket builds a waypoint MeshPacket.
func buildWaypointPacket(wp Waypoint, to uint32, channel uint32) []byte {
	latI := int32(wp.Latitude * 1e7)
	lonI := int32(wp.Longitude * 1e7)
	w := &pb.Waypoint{
		Id:          wp.ID,
		LatitudeI:   &latI,
		LongitudeI:  &lonI,
		Expire:      uint32(wp.Expire),
		Name:        wp.Name,
		Description: wp.Description,
		Icon:        uint32(wp.Icon),
	}
	payload, err := proto.Marshal(w)
	if err != nil {
		return nil
	}
	return buildRawPacket(payload, PortNumWaypoint, to, channel, true)
}

// buildTraceroutePacket builds a traceroute MeshPacket.
func buildTraceroutePacket(destNode uint32) []byte {
	return buildRawPacket([]byte{}, PortNumTraceroute, destNode, 0, true)
}

// buildRequestNodeInfo builds a MeshPacket requesting NodeInfo from a remote node.
func buildRequestNodeInfo(myNodeNum, destNode uint32) []byte {
	pkt := &pb.MeshPacket{
		From:     myNodeNum,
		To:       destNode,
		HopLimit: 3,
		WantAck:  true,
		PayloadVariant: &pb.MeshPacket_Decoded{
			Decoded: &pb.Data{
				Portnum:      pb.PortNum_NODEINFO_APP,
				WantResponse: true,
			},
		},
	}
	pktBytes, err := proto.Marshal(pkt)
	if err != nil {
		return nil
	}
	toRadio := &pb.ToRadio{
		PayloadVariant: &pb.ToRadio_Packet{Packet: pkt},
	}
	_ = pktBytes
	data, err := proto.Marshal(toRadio)
	if err != nil {
		return nil
	}
	return data
}

// ============================================================================
// Generic protobuf-to-map decoder (kept for config sections passthrough)
// ============================================================================

// decodeProtoToMap decodes arbitrary protobuf bytes into a map.
// Keys are field numbers as strings. Kept as fallback for raw config inspection.
func decodeProtoToMap(data []byte) map[string]interface{} {
	result := make(map[string]interface{})
	pos := 0

	for pos < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, pos)
		if err != nil {
			break
		}
		pos = newPos
		key := fmt.Sprintf("%d", fieldNum)

		var value interface{}
		switch wireType {
		case wireVarint:
			val, n := readVarint(data, pos)
			if n <= 0 {
				return result
			}
			pos += n
			value = val
		case wireFixed64:
			if pos+8 > len(data) {
				return result
			}
			value = uint64(data[pos]) | uint64(data[pos+1])<<8 | uint64(data[pos+2])<<16 | uint64(data[pos+3])<<24 |
				uint64(data[pos+4])<<32 | uint64(data[pos+5])<<40 | uint64(data[pos+6])<<48 | uint64(data[pos+7])<<56
			pos += 8
		case wireLengthDelimited:
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return result
			}
			pos = newPos
			if len(val) == 0 {
				value = ""
			} else if utf8.Valid(val) {
				value = string(val)
			} else {
				nested := decodeProtoToMap(val)
				if len(nested) > 0 {
					value = nested
				} else {
					value = base64.StdEncoding.EncodeToString(val)
				}
			}
		case wireFixed32:
			if pos+4 > len(data) {
				return result
			}
			value = uint32(data[pos]) | uint32(data[pos+1])<<8 | uint32(data[pos+2])<<16 | uint32(data[pos+3])<<24
			pos += 4
		default:
			pos = skipField(data, pos, wireType)
			if pos < 0 {
				return result
			}
			continue
		}

		if existing, ok := result[key]; ok {
			switch v := existing.(type) {
			case []interface{}:
				result[key] = append(v, value)
			default:
				result[key] = []interface{}{v, value}
			}
		} else {
			result[key] = value
		}
	}
	return result
}

// ============================================================================
// Compatibility parsers — parse individual proto messages from raw bytes
// These are used by tests and by direct_mesh.go for sub-message parsing.
// ============================================================================

func parseMeshPacket(data []byte) (*ProtoMeshPacket, error) {
	p := &pb.MeshPacket{}
	if err := proto.Unmarshal(data, p); err != nil {
		return &ProtoMeshPacket{}, nil
	}
	return convertMeshPacket(p), nil
}

func parseData(data []byte) (*ProtoData, error) {
	d := &pb.Data{}
	if err := proto.Unmarshal(data, d); err != nil {
		return &ProtoData{}, nil
	}
	return &ProtoData{
		PortNum:      uint32(d.GetPortnum()),
		Payload:      d.GetPayload(),
		WantResponse: d.GetWantResponse(),
		Bitfield:     d.GetBitfield(),
		RequestID:    d.GetRequestId(),
		ReplyID:      d.GetReplyId(),
	}, nil
}

// ============================================================================
// Conversion helpers — proto types → transport types
// ============================================================================

// protoNodeInfoToMeshNode converts a ProtoNodeInfo into a MeshNode for the transport layer.
func protoNodeInfoToMeshNode(ni *ProtoNodeInfo) MeshNode {
	node := MeshNode{
		Num:       ni.Num,
		UserID:    fmt.Sprintf("!%08x", ni.Num),
		LastHeard: int64(ni.LastHeard),
		SNR:       ni.SNR,
	}
	if ni.LastHeard > 0 {
		node.LastHeardStr = time.Unix(int64(ni.LastHeard), 0).UTC().Format(time.RFC3339)
	}
	if ni.User != nil {
		node.UserID = ni.User.ID
		node.LongName = ni.User.LongName
		node.ShortName = ni.User.ShortName
		node.HWModel = int(ni.User.HWModel)
		node.HWModelName = hwModelName(int(ni.User.HWModel))
		node.Role = ni.User.Role
		node.IsLicensed = ni.User.IsLicensed
	}
	node.HopsAway = ni.HopsAway
	node.IsFavorite = ni.IsFavorite
	node.IsIgnored = ni.IsIgnored
	if ni.Position != nil {
		node.Latitude = float64(ni.Position.LatitudeI) / 1e7
		node.Longitude = float64(ni.Position.LongitudeI) / 1e7
		node.Altitude = ni.Position.Altitude
		node.Sats = int(ni.Position.SatsInView)
	}
	if ni.DeviceMetrics != nil {
		node.BatteryLevel = int(ni.DeviceMetrics.BatteryLevel)
		node.Voltage = ni.DeviceMetrics.Voltage
		node.ChannelUtil = ni.DeviceMetrics.ChannelUtil
		node.AirUtilTx = ni.DeviceMetrics.AirUtilTx
		node.UptimeSeconds = int(ni.DeviceMetrics.UptimeSeconds)
	}
	if node.SNR != 0 || node.RSSI != 0 {
		q, n := computeSignalQuality(float64(node.RSSI), float64(node.SNR))
		node.SignalQuality = q
		node.DiagnosticNotes = n
	}
	return node
}

// protoPacketToMeshMessage converts a ProtoMeshPacket into a MeshMessage.
func protoPacketToMeshMessage(pkt *ProtoMeshPacket) MeshMessage {
	msg := MeshMessage{
		From:         pkt.From,
		To:           pkt.To,
		Channel:      pkt.Channel,
		ID:           pkt.ID,
		RxSNR:        pkt.RxSNR,
		HopLimit:     int(pkt.HopLimit),
		HopStart:     int(pkt.HopStart),
		ViaMqtt:      pkt.ViaMqtt,
		PKIEncrypted: pkt.PKIEncrypted,
	}
	if pkt.RxTime > 0 {
		msg.RxTime = int64(pkt.RxTime)
		msg.Timestamp = time.Unix(int64(pkt.RxTime), 0).UTC().Format(time.RFC3339)
	} else {
		msg.RxTime = time.Now().Unix()
		msg.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if pkt.Decoded != nil {
		msg.PortNum = int(pkt.Decoded.PortNum)
		msg.PortNumName = portNumName(int(pkt.Decoded.PortNum))
		msg.RequestID = pkt.Decoded.RequestID
		msg.ReplyID = pkt.Decoded.ReplyID
		msg.OkToMQTT = pkt.Decoded.OkToMQTT()

		if pkt.Decoded.PortNum == PortNumTextMessage {
			msg.DecodedText = string(pkt.Decoded.Payload)
		}
		if pkt.Decoded.PortNum == PortNumRouting && len(pkt.Decoded.Payload) > 0 {
			msg.Routing = parseRouting(pkt.Decoded.Payload)
		}
		if len(pkt.Decoded.Payload) > 0 {
			msg.RawPayload = make([]byte, len(pkt.Decoded.Payload))
			copy(msg.RawPayload, pkt.Decoded.Payload)
		}
	} else if len(pkt.Encrypted) > 0 {
		msg.PortNumName = "ENCRYPTED_RELAY"
		msg.EncryptedPayload = make([]byte, len(pkt.Encrypted))
		copy(msg.EncryptedPayload, pkt.Encrypted)
	}
	return msg
}

// ============================================================================
// Neighbor info types and parsers
// ============================================================================

// ProtoNeighbor represents a single neighbor edge.
type ProtoNeighbor struct {
	NodeID                   uint32  `json:"node_id"`
	SNR                      float32 `json:"snr"`
	LastRxTime               uint32  `json:"last_rx_time"`
	NodeBroadcastIntervalSec uint32  `json:"node_broadcast_interval_secs"`
}

// ProtoNeighborInfo represents a NeighborInfo message (portnum 71).
type ProtoNeighborInfo struct {
	NodeID                   uint32          `json:"node_id"`
	LastSentByID             uint32          `json:"last_sent_by_id"`
	NodeBroadcastIntervalSec uint32          `json:"node_broadcast_interval_secs"`
	Neighbors                []ProtoNeighbor `json:"neighbors"`
}

func parseNeighborInfo(data []byte) (*ProtoNeighborInfo, error) {
	ni := &pb.NeighborInfo{}
	if err := proto.Unmarshal(data, ni); err != nil {
		return &ProtoNeighborInfo{}, nil
	}
	info := &ProtoNeighborInfo{
		NodeID:                   ni.GetNodeId(),
		LastSentByID:             ni.GetLastSentById(),
		NodeBroadcastIntervalSec: ni.GetNodeBroadcastIntervalSecs(),
	}
	for _, n := range ni.GetNeighbors() {
		info.Neighbors = append(info.Neighbors, ProtoNeighbor{
			NodeID:                   n.GetNodeId(),
			SNR:                      n.GetSnr(),
			LastRxTime:               n.GetLastRxTime(),
			NodeBroadcastIntervalSec: n.GetNodeBroadcastIntervalSecs(),
		})
	}
	return info, nil
}

// ============================================================================
// Store & Forward types
// ============================================================================

// ProtoStoreForward represents a parsed StoreAndForward message (portnum 65).
type ProtoStoreForward struct {
	RequestResponse int             `json:"rr"`
	Text            []byte          `json:"text,omitempty"`
	Stats           *ProtoSFStats   `json:"stats,omitempty"`
	History         *ProtoSFHistory `json:"history,omitempty"`
}

// ProtoSFStats represents StoreAndForward.Statistics.
type ProtoSFStats struct {
	MessagesTotal uint32 `json:"messages_total"`
	MessagesSaved uint32 `json:"messages_saved"`
	MessagesMax   uint32 `json:"messages_max"`
	UpTime        uint32 `json:"up_time"`
	Requests      uint32 `json:"requests"`
	Heartbeat     bool   `json:"heartbeat"`
	ReturnMax     uint32 `json:"return_max"`
	ReturnWindow  uint32 `json:"return_window"`
}

// ProtoSFHistory represents StoreAndForward.History.
type ProtoSFHistory struct {
	HistoryMessages uint32 `json:"history_messages"`
	Window          uint32 `json:"window"`
	LastRequest     uint32 `json:"last_request"`
}

func parseStoreForward(data []byte) *ProtoStoreForward {
	sf := &pb.StoreAndForward{}
	if err := proto.Unmarshal(data, sf); err != nil {
		return &ProtoStoreForward{}
	}
	result := &ProtoStoreForward{
		RequestResponse: int(sf.GetRr()),
	}
	if s, ok := sf.GetVariant().(*pb.StoreAndForward_Stats); ok && s.Stats != nil {
		result.Stats = &ProtoSFStats{
			MessagesTotal: s.Stats.GetMessagesTotal(),
			MessagesSaved: s.Stats.GetMessagesSaved(),
			MessagesMax:   s.Stats.GetMessagesMax(),
			UpTime:        s.Stats.GetUpTime(),
			Requests:      s.Stats.GetRequests(),
			Heartbeat:     s.Stats.GetHeartbeat(),
			ReturnMax:     s.Stats.GetReturnMax(),
			ReturnWindow:  s.Stats.GetReturnWindow(),
		}
	}
	if h, ok := sf.GetVariant().(*pb.StoreAndForward_History_); ok && h.History != nil {
		result.History = &ProtoSFHistory{
			HistoryMessages: h.History.GetHistoryMessages(),
			Window:          h.History.GetWindow(),
			LastRequest:     h.History.GetLastRequest(),
		}
	}
	if t, ok := sf.GetVariant().(*pb.StoreAndForward_Text); ok {
		result.Text = t.Text
	}
	return result
}

// ============================================================================
// Additional builders
// ============================================================================

// buildPositionPacket builds a Position protobuf and wraps it as a POSITION_APP MeshPacket.
func buildPositionPacket(lat, lon float64, alt int32, timestamp uint32) []byte {
	latI := int32(lat * 1e7)
	lonI := int32(lon * 1e7)
	p := &pb.Position{
		LatitudeI:  &latI,
		LongitudeI: &lonI,
		Altitude:   &alt,
		Time:       timestamp,
	}
	payload, err := proto.Marshal(p)
	if err != nil {
		return nil
	}
	return buildRawPacket(payload, PortNumPosition, 0, 0, true)
}

// buildAdminGetConfig builds a get_config_request admin message.
func buildAdminGetConfig(myNodeNum uint32, configType int) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_GetConfigRequest{
			GetConfigRequest: pb.AdminMessage_ConfigType(configType),
		},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// buildAdminGetModuleConfig builds a get_module_config_request admin message.
func buildAdminGetModuleConfig(myNodeNum uint32, moduleType int) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_GetModuleConfigRequest{
			GetModuleConfigRequest: pb.AdminMessage_ModuleConfigType(moduleType),
		},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// buildAdminGetChannel builds a get_channel_request admin message.
func buildAdminGetChannel(myNodeNum uint32, channelIndex int) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_GetChannelRequest{
			GetChannelRequest: uint32(channelIndex),
		},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// buildAdminSetCannedMessages builds an AdminMessage to set canned messages.
func buildAdminSetCannedMessages(myNodeNum uint32, messages string) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_SetCannedMessageModuleMessages{
			SetCannedMessageModuleMessages: messages,
		},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// buildAdminGetCannedMessages builds a get_canned_message_module_messages_request.
func buildAdminGetCannedMessages(myNodeNum uint32) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_GetCannedMessageModuleMessagesRequest{
			GetCannedMessageModuleMessagesRequest: true,
		},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// buildStoreForwardRequest builds a StoreAndForward CLIENT_HISTORY request.
func buildStoreForwardRequest(destNode uint32, window uint32) []byte {
	sf := &pb.StoreAndForward{
		Rr: pb.StoreAndForward_CLIENT_HISTORY,
	}
	if window > 0 {
		sf.Variant = &pb.StoreAndForward_History_{
			History: &pb.StoreAndForward_History{
				Window: window,
			},
		}
	}
	payload, err := proto.Marshal(sf)
	if err != nil {
		return nil
	}
	return buildRawPacket(payload, PortNumStoreForward, destNode, 0, true)
}

// buildRangeTestPacket builds a range test packet (portnum 66).
func buildRangeTestPacket(text string, to uint32) []byte {
	return buildRawPacket([]byte(text), PortNumRangeTest, to, 0, true)
}

// buildAdminSetFixedPosition builds an AdminMessage to set a fixed GPS position.
func buildAdminSetFixedPosition(myNodeNum uint32, lat, lon float64, alt int32) []byte {
	latI := int32(lat * 1e7)
	lonI := int32(lon * 1e7)
	p := &pb.Position{
		LatitudeI:  &latI,
		LongitudeI: &lonI,
		Altitude:   &alt,
		Time:       uint32(time.Now().Unix()),
	}
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_SetFixedPosition{SetFixedPosition: p},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// buildAdminSetOwner builds an AdminMessage to set the device owner name.
func buildAdminSetOwner(myNodeNum uint32, longName, shortName string) []byte {
	user := &pb.User{
		LongName:  longName,
		ShortName: shortName,
	}
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_SetOwner{SetOwner: user},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// buildAdminRemoveFixedPosition builds an AdminMessage to remove fixed position.
func buildAdminRemoveFixedPosition(myNodeNum uint32) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_RemoveFixedPosition{RemoveFixedPosition: true},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// configSectionToEnum maps section name strings to Config enum values.
func configSectionToEnum(section string) (int, bool) {
	m := map[string]int{
		"device": ConfigTypeDevice, "position": ConfigTypePosition, "power": ConfigTypePower,
		"network": ConfigTypeNetwork, "display": ConfigTypeDisplay, "lora": ConfigTypeLora,
		"bluetooth": ConfigTypeBluetooth, "security": ConfigTypeSecurity,
		"sessionkey": ConfigTypeSessionkey, "device_ui": ConfigTypeDeviceUI,
	}
	v, ok := m[section]
	return v, ok
}

// moduleConfigSectionToEnum maps section name strings to ModuleConfig enum values.
func moduleConfigSectionToEnum(section string) (int, bool) {
	m := map[string]int{
		"mqtt": ModuleConfigMQTT, "serial": ModuleConfigSerial,
		"external_notification": ModuleConfigExternalNotification,
		"store_forward":         ModuleConfigStoreForward, "range_test": ModuleConfigRangeTest,
		"telemetry": ModuleConfigTelemetry, "canned_message": ModuleConfigCannedMessage,
		"audio": ModuleConfigAudio, "remote_hardware": ModuleConfigRemoteHardware,
		"neighbor_info":      ModuleConfigNeighborInfo,
		"ambient_lighting":   ModuleConfigAmbientLighting,
		"detection_sensor":   ModuleConfigDetectionSensor,
		"paxcounter":         ModuleConfigPaxcounter,
		"status_message":     ModuleConfigStatusMessage,
		"traffic_management": ModuleConfigTrafficManagement,
		"tak_config":         ModuleConfigTAKConfig,
	}
	v, ok := m[section]
	return v, ok
}

// ============================================================================
// Power metrics and air quality telemetry
// ============================================================================

// ProtoPowerMetrics represents power sensor data (Telemetry field 4).
type ProtoPowerMetrics struct {
	CH1Voltage float32 // field 1
	CH1Current float32 // field 2
	CH2Voltage float32 // field 3
	CH2Current float32 // field 4
	CH3Voltage float32 // field 5
	CH3Current float32 // field 6
}

// parsePowerMetrics extracts power metrics from a Telemetry message (field 4).
func parsePowerMetrics(data []byte) *ProtoPowerMetrics {
	t := &pb.Telemetry{}
	if err := proto.Unmarshal(data, t); err != nil {
		return nil
	}
	pm, ok := t.GetVariant().(*pb.Telemetry_PowerMetrics)
	if !ok || pm.PowerMetrics == nil {
		return nil
	}
	p := pm.PowerMetrics
	return &ProtoPowerMetrics{
		CH1Voltage: p.GetCh1Voltage(),
		CH1Current: p.GetCh1Current(),
		CH2Voltage: p.GetCh2Voltage(),
		CH2Current: p.GetCh2Current(),
		CH3Voltage: p.GetCh3Voltage(),
		CH3Current: p.GetCh3Current(),
	}
}

// ProtoAirQualityMetrics represents air quality sensor data (Telemetry field 5).
type ProtoAirQualityMetrics struct {
	PM10Standard  uint32 // field 1 — PM1.0 standard (µg/m³)
	PM25Standard  uint32 // field 2 — PM2.5 standard
	PM100Standard uint32 // field 3 — PM10.0 standard
	PM10Env       uint32 // field 4 — PM1.0 environmental
	PM25Env       uint32 // field 5 — PM2.5 environmental
	PM100Env      uint32 // field 6 — PM10.0 environmental
	Count03um     uint32 // field 7 — particles > 0.3µm / 0.1L air
	Count05um     uint32 // field 8
	Count10um     uint32 // field 9
	Count25um     uint32 // field 10
	Count50um     uint32 // field 11
	Count100um    uint32 // field 12
}

// parseAirQualityMetrics extracts air quality from a Telemetry message (field 5).
func parseAirQualityMetrics(data []byte) *ProtoAirQualityMetrics {
	t := &pb.Telemetry{}
	if err := proto.Unmarshal(data, t); err != nil {
		return nil
	}
	aq, ok := t.GetVariant().(*pb.Telemetry_AirQualityMetrics)
	if !ok || aq.AirQualityMetrics == nil {
		return nil
	}
	a := aq.AirQualityMetrics
	return &ProtoAirQualityMetrics{
		PM10Standard:  a.GetPm10Standard(),
		PM25Standard:  a.GetPm25Standard(),
		PM100Standard: a.GetPm100Standard(),
		PM10Env:       a.GetPm10Environmental(),
		PM25Env:       a.GetPm25Environmental(),
		PM100Env:      a.GetPm100Environmental(),
		Count03um:     a.GetParticles_03Um(),
		Count05um:     a.GetParticles_05Um(),
		Count10um:     a.GetParticles_10Um(),
		Count25um:     a.GetParticles_25Um(),
		Count50um:     a.GetParticles_50Um(),
		Count100um:    a.GetParticles_100Um(),
	}
}

// parsePowerMetricsProto parses raw PowerMetrics bytes (not wrapped in Telemetry).
func parsePowerMetricsProto(data []byte) *ProtoPowerMetrics {
	pm := &pb.PowerMetrics{}
	if err := proto.Unmarshal(data, pm); err != nil {
		return &ProtoPowerMetrics{}
	}
	return &ProtoPowerMetrics{
		CH1Voltage: pm.GetCh1Voltage(),
		CH1Current: pm.GetCh1Current(),
		CH2Voltage: pm.GetCh2Voltage(),
		CH2Current: pm.GetCh2Current(),
		CH3Voltage: pm.GetCh3Voltage(),
		CH3Current: pm.GetCh3Current(),
	}
}

// parseAirQualityMetricsProto parses raw AirQualityMetrics bytes (not wrapped in Telemetry).
func parseAirQualityMetricsProto(data []byte) *ProtoAirQualityMetrics {
	aq := &pb.AirQualityMetrics{}
	if err := proto.Unmarshal(data, aq); err != nil {
		return &ProtoAirQualityMetrics{}
	}
	return &ProtoAirQualityMetrics{
		PM10Standard:  aq.GetPm10Standard(),
		PM25Standard:  aq.GetPm25Standard(),
		PM100Standard: aq.GetPm100Standard(),
		PM10Env:       aq.GetPm10Environmental(),
		PM25Env:       aq.GetPm25Environmental(),
		PM100Env:      aq.GetPm100Environmental(),
		Count03um:     aq.GetParticles_03Um(),
		Count05um:     aq.GetParticles_05Um(),
		Count10um:     aq.GetParticles_10Um(),
		Count25um:     aq.GetParticles_25Um(),
		Count50um:     aq.GetParticles_50Um(),
		Count100um:    aq.GetParticles_100Um(),
	}
}

// ProtoDeviceMetadata represents a DeviceMetadata response (admin field 13).
type ProtoDeviceMetadata struct {
	FirmwareVersion string // field 1
	DeviceStateVer  uint32 // field 2
	CanShutdown     bool   // field 3
	HasWifi         bool   // field 4
	HasBluetooth    bool   // field 5
	HasEthernet     bool   // field 6
	Role            uint32 // field 7
	PositionFlags   uint32 // field 8
	HWModel         uint32 // field 9
	HasRemoteHW     bool   // field 10
}

func parseDeviceMetadata(data []byte) *ProtoDeviceMetadata {
	dm := &pb.DeviceMetadata{}
	if err := proto.Unmarshal(data, dm); err != nil {
		return &ProtoDeviceMetadata{}
	}
	return &ProtoDeviceMetadata{
		FirmwareVersion: dm.GetFirmwareVersion(),
		DeviceStateVer:  dm.GetDeviceStateVersion(),
		CanShutdown:     dm.GetCanShutdown(),
		HasWifi:         dm.GetHasWifi(),
		HasBluetooth:    dm.GetHasBluetooth(),
		HasEthernet:     dm.GetHasEthernet(),
		Role:            uint32(dm.GetRole()),
		PositionFlags:   dm.GetPositionFlags(),
		HWModel:         uint32(dm.GetHwModel()),
		HasRemoteHW:     dm.GetHasRemoteHardware(),
	}
}

// ============================================================================
// New admin builders
// ============================================================================

// buildAdminBeginEditSettings builds AdminMessage begin_edit_settings.
func buildAdminBeginEditSettings(myNodeNum uint32) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_BeginEditSettings{BeginEditSettings: true},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// buildAdminCommitEditSettings builds AdminMessage commit_edit_settings.
func buildAdminCommitEditSettings(myNodeNum uint32) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_CommitEditSettings{CommitEditSettings: true},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// buildAdminGetDeviceMetadata builds a get_device_metadata_request (admin field 12).
func buildAdminGetDeviceMetadata(myNodeNum, destNode uint32) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_GetDeviceMetadataRequest{GetDeviceMetadataRequest: true},
	}
	return buildAdminToRadioMsg(myNodeNum, destNode, admin)
}

// buildAdminShutdown builds AdminMessage shutdown_seconds.
func buildAdminShutdown(myNodeNum, destNode uint32, delaySecs int) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_ShutdownSeconds{ShutdownSeconds: int32(delaySecs)},
	}
	return buildAdminToRadioMsg(myNodeNum, destNode, admin)
}

// buildAdminSetHamMode builds AdminMessage set_ham_mode.
func buildAdminSetHamMode(myNodeNum uint32, callSign string, txPower int32, frequency float32, shortName string) []byte {
	ham := &pb.HamParameters{
		CallSign:  callSign,
		TxPower:   txPower,
		Frequency: frequency,
		ShortName: shortName,
	}
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_SetHamMode{SetHamMode: ham},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// buildAdminSetFavorite builds AdminMessage set_favorite_node.
func buildAdminSetFavorite(myNodeNum, nodeNum uint32) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_SetFavoriteNode{SetFavoriteNode: nodeNum},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// buildAdminRemoveFavorite builds AdminMessage remove_favorite_node.
func buildAdminRemoveFavorite(myNodeNum, nodeNum uint32) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_RemoveFavoriteNode{RemoveFavoriteNode: nodeNum},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// buildAdminSetIgnored builds AdminMessage set_ignored_node.
func buildAdminSetIgnored(myNodeNum, nodeNum uint32) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_SetIgnoredNode{SetIgnoredNode: nodeNum},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// buildAdminRemoveIgnored builds AdminMessage remove_ignored_node.
func buildAdminRemoveIgnored(myNodeNum, nodeNum uint32) []byte {
	admin := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_RemoveIgnoredNode{RemoveIgnoredNode: nodeNum},
	}
	return buildAdminToRadioMsg(myNodeNum, myNodeNum, admin)
}

// Iridium signal descriptions (shared with DirectSatTransport)
var signalDescriptions = map[int]string{
	0: "No signal",
	1: "Poor (~-110 dBm)",
	2: "Fair (~-108 dBm)",
	3: "Good (~-106 dBm)",
	4: "Very good (~-104 dBm)",
	5: "Excellent (~-102 dBm)",
}

func signalAssessment(bars int) string {
	switch {
	case bars == 0:
		return "none"
	case bars <= 1:
		return "poor"
	case bars <= 2:
		return "fair"
	case bars <= 3:
		return "good"
	case bars <= 4:
		return "very good"
	default:
		return "excellent"
	}
}
