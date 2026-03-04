package transport

// Meshtastic protocol engine — protobuf parsers and builders.
// Ported from HAL meshtastic_driver.go (hand-rolled protobuf, no external lib).

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ============================================================================
// Constants
// ============================================================================

// Meshtastic PortNum values
const (
	PortNumTextMessage = 1
	PortNumPosition    = 3
	PortNumNodeInfo    = 4
	PortNumAdminApp    = 6
	PortNumWaypoint    = 8
	PortNumSerial      = 64
	PortNumTelemetry   = 67
	PortNumTraceroute  = 70
	PortNumPrivate     = 256
)

// Admin message field numbers
const (
	AdminFieldSetConfig       = 34
	AdminFieldSetModuleConfig = 35
	AdminFieldFactoryReset    = 94
	AdminFieldRemoveByNodenum = 96
	AdminFieldRebootSeconds   = 97
)

func portNumName(pn int) string {
	switch pn {
	case PortNumTextMessage:
		return "TEXT_MESSAGE_APP"
	case PortNumPosition:
		return "POSITION_APP"
	case PortNumNodeInfo:
		return "NODEINFO_APP"
	case PortNumAdminApp:
		return "ADMIN_APP"
	case PortNumWaypoint:
		return "WAYPOINT_APP"
	case PortNumTelemetry:
		return "TELEMETRY_APP"
	case PortNumSerial:
		return "SERIAL_APP"
	case PortNumTraceroute:
		return "TRACEROUTE_APP"
	case PortNumPrivate:
		return "PRIVATE_APP"
	default:
		return fmt.Sprintf("PORTNUM_%d", pn)
	}
}

func hwModelName(model int) string {
	names := map[int]string{
		0: "UNSET", 1: "TLORA_V2", 2: "TLORA_V1", 3: "TLORA_V2_1_1P6",
		4: "TBEAM", 5: "HELTEC_V2_0", 6: "TBEAM_V0P7", 7: "T_ECHO",
		8: "TLORA_V1_1P3", 9: "RAK4631", 10: "HELTEC_V2_1",
		11: "HELTEC_V1", 25: "RAK11200", 39: "STATION_G1",
		40: "RAK11310", 41: "SENSELORA_RP2040", 42: "SENSELORA_S3",
		43: "CANARYONE", 44: "RP2040_LORA", 47: "HELTEC_V3",
		48: "HELTEC_WSL_V3", 58: "TBEAM_S3_CORE", 59: "RAK11300",
		60: "WIO_E5", 61: "RADIOMASTER_900_BANDIT", 62: "HELTEC_CAPSULE_SENSOR_V3",
		63: "HELTEC_VISION_MASTER_T190", 64: "HELTEC_VISION_MASTER_E213",
		65: "HELTEC_VISION_MASTER_E290", 66: "HELTEC_MESH_NODE_T114",
	}
	if name, ok := names[model]; ok {
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
// Proto Types — parsed protobuf structures
// ============================================================================

// ProtoFromRadio represents a parsed FromRadio message.
type ProtoFromRadio struct {
	ID               uint32
	Packet           *ProtoMeshPacket
	MyInfo           *ProtoMyNodeInfo
	NodeInfo         *ProtoNodeInfo
	ConfigRaw        []byte // Config message (raw protobuf)
	ConfigCompleteID uint32 // Field 7 (NOT 6 — field 6 is log_record)
	ModuleConfigRaw  []byte // ModuleConfig message (raw protobuf)
	ChannelRaw       []byte // Channel message (raw protobuf)
}

// ProtoMeshPacket represents a parsed MeshPacket.
type ProtoMeshPacket struct {
	From      uint32
	To        uint32
	Channel   uint32
	ID        uint32
	Decoded   *ProtoData
	Encrypted []byte
	RxTime    uint32
	RxSNR     float32
	RxRSSI    int32
	HopLimit  uint32
	HopStart  uint32
}

// ProtoData represents a decoded Data submessage.
type ProtoData struct {
	PortNum      uint32
	Payload      []byte
	WantResponse bool
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
}

// ProtoUser represents a User submessage.
type ProtoUser struct {
	ID        string
	LongName  string
	ShortName string
	HWModel   uint32
}

// ProtoPosition represents a Position submessage.
type ProtoPosition struct {
	LatitudeI  int32
	LongitudeI int32
	Altitude   int32
	SatsInView uint32
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
// Parsers — FromRadio envelope → nested messages
// ============================================================================

func parseFromRadio(data []byte) (*ProtoFromRadio, error) {
	fr := &ProtoFromRadio{}
	pos := 0

	for pos < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, pos)
		if err != nil {
			return fr, nil
		}
		pos = newPos

		switch fieldNum {
		case 1: // id (uint32)
			val, n := readVarint(data, pos)
			if n <= 0 {
				return fr, nil
			}
			fr.ID = uint32(val)
			pos += n
		case 2: // packet (MeshPacket)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return fr, nil
			}
			fr.Packet, _ = parseMeshPacket(val)
			pos = newPos
		case 3: // my_info (MyNodeInfo)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return fr, nil
			}
			fr.MyInfo, _ = parseMyNodeInfo(val)
			pos = newPos
		case 4: // node_info (NodeInfo)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return fr, nil
			}
			fr.NodeInfo, _ = parseNodeInfo(val)
			pos = newPos
		case 5: // config (Config)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return fr, nil
			}
			fr.ConfigRaw = val
			pos = newPos
		case 6: // log_record — skip
			pos = skipField(data, pos, wireType)
			if pos < 0 {
				return fr, nil
			}
		case 7: // config_complete_id (uint32)
			val, n := readVarint(data, pos)
			if n <= 0 {
				return fr, nil
			}
			fr.ConfigCompleteID = uint32(val)
			pos += n
		case 8: // rebooted — skip
			pos = skipField(data, pos, wireType)
			if pos < 0 {
				return fr, nil
			}
		case 9: // moduleConfig
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return fr, nil
			}
			fr.ModuleConfigRaw = val
			pos = newPos
		case 10: // channel
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return fr, nil
			}
			fr.ChannelRaw = val
			pos = newPos
		default:
			pos = skipField(data, pos, wireType)
			if pos < 0 {
				return fr, nil
			}
		}
	}

	return fr, nil
}

func parseMeshPacket(data []byte) (*ProtoMeshPacket, error) {
	pkt := &ProtoMeshPacket{}
	pos := 0

	for pos < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, pos)
		if err != nil {
			return pkt, nil
		}
		pos = newPos

		switch fieldNum {
		case 1: // from (fixed32)
			if pos+4 > len(data) {
				return pkt, nil
			}
			pkt.From = binary.LittleEndian.Uint32(data[pos : pos+4])
			pos += 4
		case 2: // to (fixed32)
			if pos+4 > len(data) {
				return pkt, nil
			}
			pkt.To = binary.LittleEndian.Uint32(data[pos : pos+4])
			pos += 4
		case 3: // channel (varint)
			val, n := readVarint(data, pos)
			if n <= 0 {
				return pkt, nil
			}
			pkt.Channel = uint32(val)
			pos += n
		case 4: // decoded (Data)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return pkt, nil
			}
			pkt.Decoded, _ = parseData(val)
			pos = newPos
		case 5: // encrypted (bytes)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return pkt, nil
			}
			pkt.Encrypted = val
			pos = newPos
		case 6: // id (fixed32)
			if pos+4 > len(data) {
				return pkt, nil
			}
			pkt.ID = binary.LittleEndian.Uint32(data[pos : pos+4])
			pos += 4
		case 7: // rx_time (fixed32)
			if pos+4 > len(data) {
				return pkt, nil
			}
			pkt.RxTime = binary.LittleEndian.Uint32(data[pos : pos+4])
			pos += 4
		case 8: // rx_snr (float = fixed32)
			if pos+4 > len(data) {
				return pkt, nil
			}
			pkt.RxSNR = math.Float32frombits(binary.LittleEndian.Uint32(data[pos : pos+4]))
			pos += 4
		case 9: // hop_limit (varint)
			val, n := readVarint(data, pos)
			if n <= 0 {
				return pkt, nil
			}
			pkt.HopLimit = uint32(val)
			pos += n
		case 12: // rx_rssi (int32 varint)
			val, n := readVarint(data, pos)
			if n <= 0 {
				return pkt, nil
			}
			pkt.RxRSSI = int32(val)
			pos += n
		case 15: // hop_start (varint)
			val, n := readVarint(data, pos)
			if n <= 0 {
				return pkt, nil
			}
			pkt.HopStart = uint32(val)
			pos += n
		default:
			pos = skipField(data, pos, wireType)
			if pos < 0 {
				return pkt, nil
			}
		}
	}
	return pkt, nil
}

func parseData(data []byte) (*ProtoData, error) {
	d := &ProtoData{}
	pos := 0

	for pos < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, pos)
		if err != nil {
			return d, nil
		}
		pos = newPos

		switch fieldNum {
		case 1: // portnum (varint)
			val, n := readVarint(data, pos)
			if n <= 0 {
				return d, nil
			}
			d.PortNum = uint32(val)
			pos += n
		case 2: // payload (bytes)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return d, nil
			}
			d.Payload = val
			pos = newPos
		case 3: // want_response (bool)
			val, n := readVarint(data, pos)
			if n <= 0 {
				return d, nil
			}
			d.WantResponse = val != 0
			pos += n
		default:
			pos = skipField(data, pos, wireType)
			if pos < 0 {
				return d, nil
			}
		}
	}
	return d, nil
}

func parseMyNodeInfo(data []byte) (*ProtoMyNodeInfo, error) {
	info := &ProtoMyNodeInfo{}
	pos := 0

	for pos < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, pos)
		if err != nil {
			return info, nil
		}
		pos = newPos

		switch fieldNum {
		case 1: // my_node_num (uint32)
			val, n := readVarint(data, pos)
			if n <= 0 {
				return info, nil
			}
			info.MyNodeNum = uint32(val)
			pos += n
		default:
			pos = skipField(data, pos, wireType)
			if pos < 0 {
				return info, nil
			}
		}
	}
	return info, nil
}

func parseNodeInfo(data []byte) (*ProtoNodeInfo, error) {
	info := &ProtoNodeInfo{}
	pos := 0

	for pos < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, pos)
		if err != nil {
			return info, nil
		}
		pos = newPos

		switch fieldNum {
		case 1: // num (uint32)
			val, n := readVarint(data, pos)
			if n <= 0 {
				return info, nil
			}
			info.Num = uint32(val)
			pos += n
		case 2: // user (User)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return info, nil
			}
			info.User, _ = parseUser(val)
			pos = newPos
		case 4: // position (Position)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return info, nil
			}
			info.Position, _ = parsePosition(val)
			pos = newPos
		case 6: // snr (float = fixed32)
			if pos+4 > len(data) {
				return info, nil
			}
			info.SNR = math.Float32frombits(binary.LittleEndian.Uint32(data[pos : pos+4]))
			pos += 4
		case 7: // last_heard (fixed32)
			if pos+4 > len(data) {
				return info, nil
			}
			info.LastHeard = binary.LittleEndian.Uint32(data[pos : pos+4])
			pos += 4
		case 8: // device_metrics (DeviceMetrics)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return info, nil
			}
			info.DeviceMetrics, _ = parseDeviceMetricsProto(val)
			pos = newPos
		default:
			pos = skipField(data, pos, wireType)
			if pos < 0 {
				return info, nil
			}
		}
	}
	return info, nil
}

func parseUser(data []byte) (*ProtoUser, error) {
	user := &ProtoUser{}
	pos := 0

	for pos < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, pos)
		if err != nil {
			return user, nil
		}
		pos = newPos

		switch fieldNum {
		case 1: // id (string)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return user, nil
			}
			user.ID = string(val)
			pos = newPos
		case 2: // long_name (string)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return user, nil
			}
			user.LongName = string(val)
			pos = newPos
		case 3: // short_name (string)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return user, nil
			}
			user.ShortName = string(val)
			pos = newPos
		case 6: // hw_model (enum = varint)
			val, n := readVarint(data, pos)
			if n <= 0 {
				return user, nil
			}
			user.HWModel = uint32(val)
			pos += n
		default:
			pos = skipField(data, pos, wireType)
			if pos < 0 {
				return user, nil
			}
		}
	}
	return user, nil
}

func parsePosition(data []byte) (*ProtoPosition, error) {
	p := &ProtoPosition{}
	offset := 0

	for offset < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, offset)
		if err != nil {
			return p, nil
		}
		offset = newPos

		switch fieldNum {
		case 1: // latitude_i (sfixed32)
			if offset+4 > len(data) {
				return p, nil
			}
			p.LatitudeI = int32(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
		case 2: // longitude_i (sfixed32)
			if offset+4 > len(data) {
				return p, nil
			}
			p.LongitudeI = int32(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
		case 3: // altitude (int32 varint)
			val, n := readVarint(data, offset)
			if n <= 0 {
				return p, nil
			}
			p.Altitude = int32(val)
			offset += n
		case 9: // sats_in_view (uint32)
			val, n := readVarint(data, offset)
			if n <= 0 {
				return p, nil
			}
			p.SatsInView = uint32(val)
			offset += n
		default:
			offset = skipField(data, offset, wireType)
			if offset < 0 {
				return p, nil
			}
		}
	}
	return p, nil
}

// parseDeviceMetrics handles both Telemetry wrapper and raw DeviceMetrics.
func parseDeviceMetrics(data []byte) (*ProtoDeviceMetrics, error) {
	dm := &ProtoDeviceMetrics{}
	offset := 0

	for offset < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, offset)
		if err != nil {
			return dm, nil
		}
		offset = newPos

		switch fieldNum {
		case 1:
			if wireType == wireLengthDelimited {
				// Telemetry wrapper — field 1 = device_metrics submessage
				val, newPos, err := readLengthDelimited(data, offset)
				if err != nil {
					return dm, nil
				}
				offset = newPos
				return parseDeviceMetricsProto(val)
			}
			// Varint: raw device_metrics, field 1 = battery_level
			val, n := readVarint(data, offset)
			if n <= 0 {
				return dm, nil
			}
			dm.BatteryLevel = uint32(val)
			offset += n
		case 2: // voltage (float = fixed32)
			if offset+4 > len(data) {
				return dm, nil
			}
			dm.Voltage = math.Float32frombits(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
		default:
			offset = skipField(data, offset, wireType)
			if offset < 0 {
				return dm, nil
			}
		}
	}
	return dm, nil
}

func parseDeviceMetricsProto(data []byte) (*ProtoDeviceMetrics, error) {
	dm := &ProtoDeviceMetrics{}
	offset := 0

	for offset < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, offset)
		if err != nil {
			return dm, nil
		}
		offset = newPos

		switch fieldNum {
		case 1: // battery_level
			val, n := readVarint(data, offset)
			if n <= 0 {
				return dm, nil
			}
			dm.BatteryLevel = uint32(val)
			offset += n
		case 2: // voltage (float = fixed32)
			if offset+4 > len(data) {
				return dm, nil
			}
			dm.Voltage = math.Float32frombits(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
		case 3: // channel_utilization (float = fixed32)
			if offset+4 > len(data) {
				return dm, nil
			}
			dm.ChannelUtil = math.Float32frombits(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
		case 4: // air_util_tx (float = fixed32)
			if offset+4 > len(data) {
				return dm, nil
			}
			dm.AirUtilTx = math.Float32frombits(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
		case 5: // uptime_seconds
			val, n := readVarint(data, offset)
			if n <= 0 {
				return dm, nil
			}
			dm.UptimeSeconds = uint32(val)
			offset += n
		default:
			offset = skipField(data, offset, wireType)
			if offset < 0 {
				return dm, nil
			}
		}
	}
	return dm, nil
}

// ============================================================================
// Builders — construct ToRadio protobuf messages
// ============================================================================

// buildWantConfigID builds a ToRadio protobuf with want_config_id set.
// ToRadio field 3 (uint32) = want_config_id.
func buildWantConfigID(configID uint32) []byte {
	buf := make([]byte, 0, 8)
	buf = append(buf, 0x18) // field 3, varint
	buf = appendVarint(buf, uint64(configID))
	return buf
}

// buildToRadioPacket wraps a MeshPacket into a ToRadio message (field 1).
func buildToRadioPacket(meshPacket []byte) []byte {
	buf := make([]byte, 0, len(meshPacket)+8)
	buf = append(buf, 0x0A) // field 1, length-delimited
	buf = appendVarint(buf, uint64(len(meshPacket)))
	buf = append(buf, meshPacket...)
	return buf
}

// buildTextMessage builds a MeshPacket with TEXT_MESSAGE_APP portnum.
func buildTextMessage(text string, to uint32, channel uint32) []byte {
	payload := []byte(text)
	data := make([]byte, 0, len(payload)+8)
	data = append(data, 0x08) // Data field 1: portnum (varint)
	data = appendVarint(data, uint64(PortNumTextMessage))
	data = append(data, 0x12) // Data field 2: payload (bytes)
	data = appendVarint(data, uint64(len(payload)))
	data = append(data, payload...)

	return buildMeshPacket(data, to, channel)
}

// buildMeshPacket builds the outer MeshPacket wrapper.
func buildMeshPacket(decodedData []byte, to uint32, channel uint32) []byte {
	pkt := make([]byte, 0, len(decodedData)+32)

	if to == 0 {
		to = 0xFFFFFFFF // Broadcast
	}
	pkt = append(pkt, 0x15) // field 2 (to), wire type 5 (fixed32)
	pkt = appendFixed32(pkt, to)

	if channel > 0 {
		pkt = append(pkt, 0x18) // field 3 (channel), varint
		pkt = appendVarint(pkt, uint64(channel))
	}

	pkt = append(pkt, 0x22) // field 4 (decoded), length-delimited
	pkt = appendVarint(pkt, uint64(len(decodedData)))
	pkt = append(pkt, decodedData...)

	pkt = append(pkt, 0x48) // field 9 (hop_limit), varint
	pkt = appendVarint(pkt, 3)

	pkt = append(pkt, 0x50, 0x01) // field 10 (want_ack), varint 1

	return pkt
}

// buildRawPacket builds a MeshPacket with arbitrary portnum and payload.
func buildRawPacket(payload []byte, portnum int, to uint32, channel uint32, wantAck bool) []byte {
	data := make([]byte, 0, len(payload)+16)
	data = append(data, 0x08) // Data field 1: portnum
	data = appendVarint(data, uint64(portnum))
	data = append(data, 0x12) // Data field 2: payload
	data = appendVarint(data, uint64(len(payload)))
	data = append(data, payload...)

	pkt := make([]byte, 0, len(data)+32)
	if to == 0 {
		to = 0xFFFFFFFF
	}
	pkt = append(pkt, 0x15) // field 2 (to), fixed32
	pkt = appendFixed32(pkt, to)
	if channel > 0 {
		pkt = append(pkt, 0x18) // field 3 (channel), varint
		pkt = appendVarint(pkt, uint64(channel))
	}
	pkt = append(pkt, 0x22) // field 4 (decoded), length-delimited
	pkt = appendVarint(pkt, uint64(len(data)))
	pkt = append(pkt, data...)
	pkt = append(pkt, 0x48) // field 9 (hop_limit), varint
	pkt = appendVarint(pkt, 3)
	if wantAck {
		pkt = append(pkt, 0x50, 0x01) // field 10, varint 1
	}
	return pkt
}

// buildAdminToRadio wraps an admin message in Data → MeshPacket → ToRadio.
func buildAdminToRadio(myNodeNum, destNode uint32, adminPayload []byte) []byte {
	// Data: portnum=ADMIN_APP, payload=adminPayload, want_response=true
	data := make([]byte, 0, len(adminPayload)+16)
	data = append(data, 0x08) // Data field 1: portnum
	data = appendVarint(data, uint64(PortNumAdminApp))
	data = append(data, 0x12) // Data field 2: payload
	data = appendVarint(data, uint64(len(adminPayload)))
	data = append(data, adminPayload...)
	data = append(data, 0x18, 0x01) // Data field 3: want_response

	// MeshPacket
	pkt := make([]byte, 0, len(data)+32)
	pkt = append(pkt, 0x0D) // field 1 (from), fixed32
	pkt = appendFixed32(pkt, myNodeNum)
	pkt = append(pkt, 0x15) // field 2 (to), fixed32
	pkt = appendFixed32(pkt, destNode)
	pkt = append(pkt, 0x22) // field 4 (decoded), length-delimited
	pkt = appendVarint(pkt, uint64(len(data)))
	pkt = append(pkt, data...)
	pkt = append(pkt, 0x48) // field 9 (hop_limit), varint
	pkt = appendVarint(pkt, 3)
	pkt = append(pkt, 0x50, 0x01) // field 10 (want_ack), varint 1

	return buildToRadioPacket(pkt)
}

// buildAdminReboot builds a ToRadio with AdminMessage field 97 (reboot_seconds).
func buildAdminReboot(myNodeNum, destNode uint32, delaySecs int) []byte {
	admin := make([]byte, 0, 16)
	admin = appendVarint(admin, AdminFieldRebootSeconds<<3|0)
	admin = appendVarint(admin, uint64(delaySecs))
	return buildAdminToRadio(myNodeNum, destNode, admin)
}

// buildAdminFactoryReset builds a ToRadio with AdminMessage field 94.
func buildAdminFactoryReset(myNodeNum, destNode uint32) []byte {
	admin := make([]byte, 0, 16)
	admin = appendVarint(admin, AdminFieldFactoryReset<<3|0)
	admin = appendVarint(admin, 1)
	return buildAdminToRadio(myNodeNum, destNode, admin)
}

// buildAdminRemoveNode builds a ToRadio with AdminMessage field 96 (remove_by_nodenum).
func buildAdminRemoveNode(myNodeNum, nodeNum uint32) []byte {
	admin := make([]byte, 0, 16)
	admin = appendVarint(admin, AdminFieldRemoveByNodenum<<3|0)
	admin = appendVarint(admin, uint64(nodeNum))
	return buildAdminToRadio(myNodeNum, myNodeNum, admin)
}

// buildAdminSetConfig builds a ToRadio with AdminMessage field 34 (set_config).
func buildAdminSetConfig(myNodeNum uint32, configData []byte) []byte {
	admin := make([]byte, 0, len(configData)+8)
	admin = appendVarint(admin, AdminFieldSetConfig<<3|2) // field 34, length-delimited
	admin = appendVarint(admin, uint64(len(configData)))
	admin = append(admin, configData...)
	return buildAdminToRadio(myNodeNum, myNodeNum, admin)
}

// buildAdminSetModuleConfig builds a ToRadio with AdminMessage field 35 (set_module_config).
func buildAdminSetModuleConfig(myNodeNum uint32, configData []byte) []byte {
	admin := make([]byte, 0, len(configData)+8)
	admin = appendVarint(admin, AdminFieldSetModuleConfig<<3|2) // field 35, length-delimited
	admin = appendVarint(admin, uint64(len(configData)))
	admin = append(admin, configData...)
	return buildAdminToRadio(myNodeNum, myNodeNum, admin)
}

// buildSetChannel builds a ToRadio for channel configuration.
func buildSetChannel(myNodeNum uint32, index uint32, name string, psk []byte, role int, uplinkEnabled, downlinkEnabled bool) []byte {
	// ChannelSettings
	settings := make([]byte, 0, 64)
	if len(psk) > 0 {
		settings = append(settings, 0x1A) // field 3, length-delimited
		settings = appendVarint(settings, uint64(len(psk)))
		settings = append(settings, psk...)
	}
	if name != "" {
		settings = append(settings, 0x22) // field 4, length-delimited
		settings = appendVarint(settings, uint64(len(name)))
		settings = append(settings, []byte(name)...)
	}
	if uplinkEnabled {
		settings = append(settings, 0x30, 0x01) // field 6
	}
	if downlinkEnabled {
		settings = append(settings, 0x38, 0x01) // field 7
	}

	// Channel message
	channel := make([]byte, 0, len(settings)+16)
	channel = append(channel, 0x08) // field 1 (index), varint
	channel = appendVarint(channel, uint64(index))
	if len(settings) > 0 {
		channel = append(channel, 0x12) // field 2 (settings), length-delimited
		channel = appendVarint(channel, uint64(len(settings)))
		channel = append(channel, settings...)
	}
	if role > 0 {
		channel = append(channel, 0x18) // field 3 (role), varint
		channel = appendVarint(channel, uint64(role))
	}

	// AdminMessage field 2: set_channel
	admin := make([]byte, 0, len(channel)+8)
	admin = append(admin, 0x12) // field 2, length-delimited
	admin = appendVarint(admin, uint64(len(channel)))
	admin = append(admin, channel...)

	return buildAdminToRadio(myNodeNum, myNodeNum, admin)
}

// buildWaypointPacket builds a waypoint MeshPacket.
func buildWaypointPacket(wp Waypoint, to uint32, channel uint32) []byte {
	buf := make([]byte, 0, 128)

	if wp.ID > 0 {
		buf = append(buf, 0x08)
		buf = appendVarint(buf, uint64(wp.ID))
	}
	// latitude_i (sfixed32 — field 2, wire type 5)
	latI := int32(wp.Latitude * 1e7)
	buf = append(buf, 0x15)
	buf = appendFixed32(buf, uint32(latI))
	// longitude_i (sfixed32 — field 3, wire type 5)
	lonI := int32(wp.Longitude * 1e7)
	buf = append(buf, 0x1D)
	buf = appendFixed32(buf, uint32(lonI))
	if wp.Expire > 0 {
		buf = append(buf, 0x20)
		buf = appendVarint(buf, uint64(wp.Expire))
	}
	if wp.Name != "" {
		buf = append(buf, 0x32)
		buf = appendVarint(buf, uint64(len(wp.Name)))
		buf = append(buf, []byte(wp.Name)...)
	}
	if wp.Description != "" {
		buf = append(buf, 0x3A)
		buf = appendVarint(buf, uint64(len(wp.Description)))
		buf = append(buf, []byte(wp.Description)...)
	}
	if wp.Icon > 0 {
		buf = append(buf, 0x45)
		buf = appendFixed32(buf, uint32(wp.Icon))
	}

	return buildRawPacket(buf, PortNumWaypoint, to, channel, true)
}

// buildTraceroutePacket builds a traceroute MeshPacket.
func buildTraceroutePacket(destNode uint32) []byte {
	// RouteDiscovery message — empty payload, firmware populates route
	return buildRawPacket([]byte{}, PortNumTraceroute, destNode, 0, true)
}

// ============================================================================
// Generic protobuf-to-map decoder (for config sections)
// ============================================================================

// decodeProtoToMap decodes arbitrary protobuf bytes into a map.
// Keys are field numbers as strings.
func decodeProtoToMap(data []byte) map[string]interface{} {
	result := make(map[string]interface{})
	pos := 0

	for pos < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, pos)
		if err != nil {
			break
		}
		pos = newPos
		key := strconv.FormatUint(uint64(fieldNum), 10)

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
			value = binary.LittleEndian.Uint64(data[pos : pos+8])
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
			value = binary.LittleEndian.Uint32(data[pos : pos+4])
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
// Conversion helpers — proto types → transport types
// ============================================================================

// protoNodeInfoToMeshNode converts a ProtoNodeInfo into a MeshNode for the transport layer.
func protoNodeInfoToMeshNode(ni *ProtoNodeInfo) MeshNode {
	node := MeshNode{
		Num:       ni.Num,
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
	}
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
		From:     pkt.From,
		To:       pkt.To,
		Channel:  pkt.Channel,
		ID:       pkt.ID,
		RxSNR:    pkt.RxSNR,
		HopLimit: int(pkt.HopLimit),
		HopStart: int(pkt.HopStart),
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
		if pkt.Decoded.PortNum == PortNumTextMessage {
			msg.DecodedText = string(pkt.Decoded.Payload)
		}
	}
	return msg
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
