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
	PortNumTextMessage  = 1
	PortNumPosition     = 3
	PortNumNodeInfo     = 4
	PortNumAdminApp     = 6
	PortNumWaypoint     = 8
	PortNumSerial       = 64
	PortNumStoreForward = 65
	PortNumRangeTest    = 66
	PortNumTelemetry    = 67
	PortNumTraceroute   = 70
	PortNumNeighborInfo = 71
	PortNumPrivate      = 256
)

// Admin message field numbers
const (
	AdminFieldGetChannelRequest    = 1
	AdminFieldGetConfigRequest     = 5
	AdminFieldGetModuleConfigReq   = 7
	AdminFieldGetCannedMsgReq      = 10
	AdminFieldGetCannedMsgResponse = 11
	AdminFieldSetChannel           = 33
	AdminFieldSetConfig            = 34
	AdminFieldSetModuleConfig      = 35
	AdminFieldSetCannedMessages    = 36
	AdminFieldSetFixedPosition     = 41
	AdminFieldRemoveFixedPosition  = 42
	AdminFieldFactoryReset         = 94
	AdminFieldRemoveByNodenum      = 38
	AdminFieldRebootSeconds        = 97
	AdminFieldSetOwner             = 32
	AdminFieldSetTimeUnixSec       = 99
)

// Config section enum values (for get_config_request)
const (
	ConfigTypeDevice    = 0
	ConfigTypePosition  = 1
	ConfigTypePower     = 2
	ConfigTypeNetwork   = 3
	ConfigTypeDisplay   = 4
	ConfigTypeLora      = 5
	ConfigTypeBluetooth = 6
	ConfigTypeSecurity  = 7
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
)

// StoreAndForward RequestResponse enum values
const (
	SFClientHistory = 65
	SFClientStats   = 66
	SFClientPing    = 67
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
	case PortNumStoreForward:
		return "STORE_FORWARD_APP"
	case PortNumRangeTest:
		return "RANGE_TEST_APP"
	case PortNumTraceroute:
		return "TRACEROUTE_APP"
	case PortNumNeighborInfo:
		return "NEIGHBORINFO_APP"
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
		11: "HELTEC_V1", 12: "TBEAM_S3_CORE", 13: "RAK11200",
		14: "NANO_G1", 15: "TLORA_V2_1_1P8", 16: "TLORA_T3_S3",
		17: "NANO_G1_EXPLORER", 18: "NANO_G2_ULTRA", 19: "LORA_TYPE",
		20: "WIPHONE", 21: "WIO_WM1110", 22: "RAK2560",
		23: "HELTEC_HRU_3601", 24: "HELTEC_WIRELESS_BRIDGE",
		25: "STATION_G1", 26: "RAK11310", 27: "SENSELORA_RP2040",
		28: "SENSELORA_S3", 29: "CANARYONE", 30: "RP2040_LORA",
		31: "STATION_G2", 32: "LORA_RELAY_V1", 33: "T_ECHO_PLUS",
		34: "PPR", 35: "GENIEBLOCKS", 36: "NRF52_UNKNOWN",
		37: "PORTDUINO", 38: "ANDROID_SIM", 39: "DIY_V1",
		40: "NRF52840_PCA10059", 41: "DR_DEV", 42: "M5STACK",
		43: "HELTEC_V3", 44: "HELTEC_WSL_V3", 45: "BETAFPV_2400_TX",
		46: "BETAFPV_900_NANO_TX", 47: "RPI_PICO",
		48: "HELTEC_WIRELESS_TRACKER", 49: "HELTEC_WIRELESS_PAPER",
		50: "T_DECK", 51: "T_WATCH_S3", 52: "PICOMPUTER_S3",
		53: "HELTEC_HT62", 54: "EBYTE_ESP32_S3", 55: "ESP32_S3_PICO",
		56: "CHATTER_2", 57: "HELTEC_WIRELESS_PAPER_V1_0",
		58: "HELTEC_WIRELESS_TRACKER_V1_0", 59: "UNPHONE",
		60: "TD_LORAC", 61: "CDEBYTE_EORA_S3", 62: "TWC_MESH_V4",
		63: "NRF52_PROMICRO_DIY", 64: "RADIOMASTER_900_BANDIT_NANO",
		65: "HELTEC_CAPSULE_SENSOR_V3", 66: "HELTEC_VISION_MASTER_T190",
		67: "HELTEC_VISION_MASTER_E213", 68: "HELTEC_VISION_MASTER_E290",
		69: "HELTEC_MESH_NODE_T114", 70: "SENSECAP_INDICATOR",
		71: "TRACKER_T1000_E", 72: "RAK3172", 73: "WIO_E5",
		74: "RADIOMASTER_900_BANDIT", 75: "ME25LS01_4Y10TD",
		76: "RP2040_FEATHER_RFM95", 77: "M5STACK_COREBASIC",
		78: "M5STACK_CORE2", 79: "RPI_PICO2", 80: "M5STACK_CORES3",
		81: "SEEED_XIAO_S3", 82: "MS24SF1", 83: "TLORA_C6",
		84: "WISMESH_TAP", 85: "ROUTASTIC", 86: "MESH_TAB",
		87: "MESHLINK", 88: "XIAO_NRF52_KIT", 89: "THINKNODE_M1",
		90: "THINKNODE_M2", 91: "T_ETH_ELITE", 92: "HELTEC_SENSOR_HUB",
		93: "MUZI_BASE", 94: "HELTEC_MESH_POCKET", 95: "SEEED_SOLAR_NODE",
		96: "NOMADSTAR_METEOR_PRO", 97: "CROWPANEL", 98: "LINK_32",
		99: "SEEED_WIO_TRACKER_L1", 100: "SEEED_WIO_TRACKER_L1_EINK",
		101: "MUZI_R1_NEO", 102: "T_DECK_PRO", 103: "T_LORA_PAGER",
		105: "WISMESH_TAG", 106: "RAK3312", 107: "THINKNODE_M5",
		108: "HELTEC_MESH_SOLAR", 109: "T_ECHO_LITE", 110: "HELTEC_V4",
		111: "M5STACK_C6L", 112: "M5STACK_CARDPUTER_ADV",
		113: "HELTEC_WIRELESS_TRACKER_V2", 114: "T_WATCH_ULTRA",
		115: "THINKNODE_M3", 116: "WISMESH_TAP_V2", 117: "RAK3401",
		118: "RAK6421", 119: "THINKNODE_M4", 120: "THINKNODE_M6",
		121: "MESHSTICK_1262", 122: "TBEAM_1_WATT",
		123: "T5_S3_EPAPER_PRO", 124: "TBEAM_BPF",
		125: "MINI_EPAPER_S3", 255: "PRIVATE_HW",
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
		case 3: // position (Position)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return info, nil
			}
			info.Position, _ = parsePosition(val)
			pos = newPos
		case 4: // snr (float = fixed32)
			if pos+4 > len(data) {
				return info, nil
			}
			info.SNR = math.Float32frombits(binary.LittleEndian.Uint32(data[pos : pos+4]))
			pos += 4
		case 5: // last_heard (fixed32)
			if pos+4 > len(data) {
				return info, nil
			}
			info.LastHeard = binary.LittleEndian.Uint32(data[pos : pos+4])
			pos += 4
		case 6: // device_metrics (DeviceMetrics)
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
		case 5: // hw_model (enum = varint)
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
		case 19: // sats_in_view (uint32)
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
		case 1: // Telemetry.time (fixed32) — skip
			if wireType == wireFixed32 {
				offset += 4
			} else {
				offset = skipField(data, offset, wireType)
				if offset < 0 {
					return dm, nil
				}
			}
		case 2: // Telemetry.device_metrics (length-delimited submessage)
			if wireType == wireLengthDelimited {
				val, newPos, err := readLengthDelimited(data, offset)
				if err != nil {
					return dm, nil
				}
				offset = newPos
				return parseDeviceMetricsProto(val)
			}
			offset = skipField(data, offset, wireType)
			if offset < 0 {
				return dm, nil
			}
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

// ProtoEnvironmentMetrics represents environment sensor data (Telemetry field 2).
type ProtoEnvironmentMetrics struct {
	Temperature float32
	Humidity    float32
	Pressure    float32
}

// parseEnvironmentMetrics extracts environment metrics from a Telemetry message.
// Telemetry field 3 = environment_metrics submessage.
// Returns nil if no environment data is present.
func parseEnvironmentMetrics(data []byte) *ProtoEnvironmentMetrics {
	offset := 0
	for offset < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, offset)
		if err != nil {
			return nil
		}
		offset = newPos

		if fieldNum == 3 && wireType == wireLengthDelimited {
			val, newPos, err := readLengthDelimited(data, offset)
			if err != nil {
				return nil
			}
			_ = newPos
			return parseEnvironmentMetricsProto(val)
		}
		offset = skipField(data, offset, wireType)
		if offset < 0 {
			return nil
		}
	}
	return nil
}

func parseEnvironmentMetricsProto(data []byte) *ProtoEnvironmentMetrics {
	em := &ProtoEnvironmentMetrics{}
	offset := 0
	for offset < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, offset)
		if err != nil {
			return em
		}
		offset = newPos

		switch fieldNum {
		case 1: // temperature (float = fixed32)
			if offset+4 > len(data) {
				return em
			}
			em.Temperature = math.Float32frombits(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
		case 2: // relative_humidity (float = fixed32)
			if offset+4 > len(data) {
				return em
			}
			em.Humidity = math.Float32frombits(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
		case 3: // barometric_pressure (float = fixed32)
			if offset+4 > len(data) {
				return em
			}
			em.Pressure = math.Float32frombits(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
		default:
			offset = skipField(data, offset, wireType)
			if offset < 0 {
				return em
			}
		}
	}
	return em
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

	// field 7 (rx_time): current UTC timestamp so receiving nodes display correct time
	pkt = append(pkt, 0x3D) // field 7, wire type 5 (fixed32)
	pkt = appendFixed32(pkt, uint32(time.Now().Unix()))

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
	// field 7 (rx_time): current UTC timestamp
	pkt = append(pkt, 0x3D) // field 7, wire type 5 (fixed32)
	pkt = appendFixed32(pkt, uint32(time.Now().Unix()))
	pkt = append(pkt, 0x48) // field 9 (hop_limit), varint
	pkt = appendVarint(pkt, 3)
	if wantAck {
		pkt = append(pkt, 0x50, 0x01) // field 10, varint 1
	}
	return pkt
}

// buildEncryptedPacket builds a MeshPacket with the encrypted field (field 5)
// instead of decoded (field 4). Used for AES-256-CTR passthrough relay —
// re-injecting encrypted Meshtastic payloads into the mesh without decryption.
func buildEncryptedPacket(encryptedPayload []byte, to uint32, channel uint32, hopLimit uint32) []byte {
	pkt := make([]byte, 0, len(encryptedPayload)+32)

	if to == 0 {
		to = 0xFFFFFFFF // Broadcast
	}
	pkt = append(pkt, 0x15) // field 2 (to), wire type 5 (fixed32)
	pkt = appendFixed32(pkt, to)

	if channel > 0 {
		pkt = append(pkt, 0x18) // field 3 (channel), varint
		pkt = appendVarint(pkt, uint64(channel))
	}

	// field 5 (encrypted), length-delimited — NOT field 4 (decoded)
	pkt = append(pkt, 0x2A) // field 5, wire type 2 (length-delimited)
	pkt = appendVarint(pkt, uint64(len(encryptedPayload)))
	pkt = append(pkt, encryptedPayload...)

	if hopLimit == 0 {
		hopLimit = 3
	}
	pkt = append(pkt, 0x48) // field 9 (hop_limit), varint
	pkt = appendVarint(pkt, uint64(hopLimit))

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

// buildAdminSetTime builds a ToRadio with AdminMessage field 99 (set_time_unixsec).
// Sends the current UTC time as a fixed32 to the specified destination node.
func buildAdminSetTime(myNodeNum, destNode uint32, unixSec uint32) []byte {
	admin := make([]byte, 0, 16)
	// field 99, wire type 5 (fixed32) → (99 << 3) | 5 = 797
	admin = appendVarint(admin, uint64(AdminFieldSetTimeUnixSec)<<3|5)
	admin = appendFixed32(admin, unixSec)
	return buildAdminToRadio(myNodeNum, destNode, admin)
}

// buildAdminFactoryReset builds a ToRadio with AdminMessage field 94.
func buildAdminFactoryReset(myNodeNum, destNode uint32) []byte {
	admin := make([]byte, 0, 16)
	admin = appendVarint(admin, AdminFieldFactoryReset<<3|0)
	admin = appendVarint(admin, 1)
	return buildAdminToRadio(myNodeNum, destNode, admin)
}

// buildAdminRemoveNode builds a ToRadio with AdminMessage field 38 (remove_by_nodenum).
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
	// ChannelSettings (see meshtastic/channel.proto)
	settings := make([]byte, 0, 64)
	if len(psk) > 0 {
		settings = append(settings, 0x12) // field 2 (psk), length-delimited
		settings = appendVarint(settings, uint64(len(psk)))
		settings = append(settings, psk...)
	}
	if name != "" {
		settings = append(settings, 0x1A) // field 3 (name), length-delimited
		settings = appendVarint(settings, uint64(len(name)))
		settings = append(settings, []byte(name)...)
	}
	if uplinkEnabled {
		settings = append(settings, 0x28, 0x01) // field 5 (uplink_enabled)
	}
	if downlinkEnabled {
		settings = append(settings, 0x30, 0x01) // field 6 (downlink_enabled)
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

	// AdminMessage field 33: set_channel
	admin := make([]byte, 0, len(channel)+8)
	admin = appendVarint(admin, uint64(AdminFieldSetChannel)<<3|2) // field 33, length-delimited
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
		if len(pkt.Decoded.Payload) > 0 {
			msg.RawPayload = make([]byte, len(pkt.Decoded.Payload))
			copy(msg.RawPayload, pkt.Decoded.Payload)
		}
	} else if len(pkt.Encrypted) > 0 {
		// AES-256-CTR passthrough: carry raw encrypted payload for relay
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
	ni := &ProtoNeighborInfo{}
	pos := 0
	for pos < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, pos)
		if err != nil {
			return ni, nil
		}
		pos = newPos
		switch fieldNum {
		case 1: // node_id
			val, n := readVarint(data, pos)
			if n <= 0 {
				return ni, nil
			}
			ni.NodeID = uint32(val)
			pos += n
		case 2: // last_sent_by_id
			val, n := readVarint(data, pos)
			if n <= 0 {
				return ni, nil
			}
			ni.LastSentByID = uint32(val)
			pos += n
		case 3: // node_broadcast_interval_secs
			val, n := readVarint(data, pos)
			if n <= 0 {
				return ni, nil
			}
			ni.NodeBroadcastIntervalSec = uint32(val)
			pos += n
		case 4: // neighbors (repeated, length-delimited)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return ni, nil
			}
			pos = newPos
			neighbor := parseNeighbor(val)
			ni.Neighbors = append(ni.Neighbors, neighbor)
		default:
			pos = skipField(data, pos, wireType)
			if pos < 0 {
				return ni, nil
			}
		}
	}
	return ni, nil
}

func parseNeighbor(data []byte) ProtoNeighbor {
	n := ProtoNeighbor{}
	pos := 0
	for pos < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, pos)
		if err != nil {
			return n
		}
		pos = newPos
		switch fieldNum {
		case 1: // node_id
			val, nn := readVarint(data, pos)
			if nn <= 0 {
				return n
			}
			n.NodeID = uint32(val)
			pos += nn
		case 2: // snr (float = fixed32)
			if pos+4 > len(data) {
				return n
			}
			n.SNR = math.Float32frombits(binary.LittleEndian.Uint32(data[pos : pos+4]))
			pos += 4
		case 3: // last_rx_time (fixed32)
			if pos+4 > len(data) {
				return n
			}
			n.LastRxTime = binary.LittleEndian.Uint32(data[pos : pos+4])
			pos += 4
		case 4: // node_broadcast_interval_secs
			val, nn := readVarint(data, pos)
			if nn <= 0 {
				return n
			}
			n.NodeBroadcastIntervalSec = uint32(val)
			pos += nn
		default:
			pos = skipField(data, pos, wireType)
			if pos < 0 {
				return n
			}
		}
	}
	return n
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
	sf := &ProtoStoreForward{}
	pos := 0
	for pos < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, pos)
		if err != nil {
			return sf
		}
		pos = newPos
		switch fieldNum {
		case 1: // rr (enum = varint)
			val, n := readVarint(data, pos)
			if n <= 0 {
				return sf
			}
			sf.RequestResponse = int(val)
			pos += n
		case 2: // stats (length-delimited)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return sf
			}
			pos = newPos
			sf.Stats = parseSFStats(val)
		case 3: // history (length-delimited)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return sf
			}
			pos = newPos
			sf.History = parseSFHistory(val)
		case 5: // text (bytes)
			val, newPos, err := readLengthDelimited(data, pos)
			if err != nil {
				return sf
			}
			pos = newPos
			sf.Text = val
		default:
			pos = skipField(data, pos, wireType)
			if pos < 0 {
				return sf
			}
		}
	}
	return sf
}

func parseSFStats(data []byte) *ProtoSFStats {
	s := &ProtoSFStats{}
	pos := 0
	for pos < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, pos)
		if err != nil {
			return s
		}
		pos = newPos
		switch fieldNum {
		case 1:
			val, n := readVarint(data, pos)
			if n <= 0 {
				return s
			}
			s.MessagesTotal = uint32(val)
			pos += n
		case 2:
			val, n := readVarint(data, pos)
			if n <= 0 {
				return s
			}
			s.MessagesSaved = uint32(val)
			pos += n
		case 3:
			val, n := readVarint(data, pos)
			if n <= 0 {
				return s
			}
			s.MessagesMax = uint32(val)
			pos += n
		case 4:
			val, n := readVarint(data, pos)
			if n <= 0 {
				return s
			}
			s.UpTime = uint32(val)
			pos += n
		case 5:
			val, n := readVarint(data, pos)
			if n <= 0 {
				return s
			}
			s.Requests = uint32(val)
			pos += n
		case 7:
			val, n := readVarint(data, pos)
			if n <= 0 {
				return s
			}
			s.Heartbeat = val != 0
			pos += n
		case 8:
			val, n := readVarint(data, pos)
			if n <= 0 {
				return s
			}
			s.ReturnMax = uint32(val)
			pos += n
		case 9:
			val, n := readVarint(data, pos)
			if n <= 0 {
				return s
			}
			s.ReturnWindow = uint32(val)
			pos += n
		default:
			pos = skipField(data, pos, wireType)
			if pos < 0 {
				return s
			}
		}
	}
	return s
}

func parseSFHistory(data []byte) *ProtoSFHistory {
	h := &ProtoSFHistory{}
	pos := 0
	for pos < len(data) {
		fieldNum, wireType, newPos, err := readTag(data, pos)
		if err != nil {
			return h
		}
		pos = newPos
		switch fieldNum {
		case 1:
			val, n := readVarint(data, pos)
			if n <= 0 {
				return h
			}
			h.HistoryMessages = uint32(val)
			pos += n
		case 2:
			val, n := readVarint(data, pos)
			if n <= 0 {
				return h
			}
			h.Window = uint32(val)
			pos += n
		case 3:
			val, n := readVarint(data, pos)
			if n <= 0 {
				return h
			}
			h.LastRequest = uint32(val)
			pos += n
		default:
			pos = skipField(data, pos, wireType)
			if pos < 0 {
				return h
			}
		}
	}
	return h
}

// ============================================================================
// New builders
// ============================================================================

// buildPositionPacket builds a Position protobuf and wraps it as a POSITION_APP MeshPacket.
func buildPositionPacket(lat, lon float64, alt int32, timestamp uint32) []byte {
	latI := int32(lat * 1e7)
	lonI := int32(lon * 1e7)

	pos := make([]byte, 0, 32)
	// field 1: latitude_i (sfixed32)
	pos = append(pos, 0x0D)
	pos = appendFixed32(pos, uint32(latI))
	// field 2: longitude_i (sfixed32)
	pos = append(pos, 0x15)
	pos = appendFixed32(pos, uint32(lonI))
	// field 3: altitude (int32 varint)
	if alt != 0 {
		pos = append(pos, 0x18)
		pos = appendVarint(pos, uint64(alt))
	}
	// field 4: time (fixed32)
	if timestamp > 0 {
		pos = append(pos, 0x25)
		pos = appendFixed32(pos, timestamp)
	}

	return buildRawPacket(pos, PortNumPosition, 0, 0, true)
}

// buildAdminGetConfig builds a get_config_request admin message.
// configType is the ConfigType enum value (0=device, 1=position, ..., 7=security).
func buildAdminGetConfig(myNodeNum uint32, configType int) []byte {
	admin := make([]byte, 0, 8)
	admin = appendVarint(admin, uint64(AdminFieldGetConfigRequest)<<3|0) // field 5, varint
	admin = appendVarint(admin, uint64(configType))
	return buildAdminToRadio(myNodeNum, myNodeNum, admin)
}

// buildAdminGetModuleConfig builds a get_module_config_request admin message.
// moduleType is the ModuleConfigType enum value.
func buildAdminGetModuleConfig(myNodeNum uint32, moduleType int) []byte {
	admin := make([]byte, 0, 8)
	admin = appendVarint(admin, uint64(AdminFieldGetModuleConfigReq)<<3|0) // field 7, varint
	admin = appendVarint(admin, uint64(moduleType))
	return buildAdminToRadio(myNodeNum, myNodeNum, admin)
}

// buildAdminGetChannel builds a get_channel_request admin message.
func buildAdminGetChannel(myNodeNum uint32, channelIndex int) []byte {
	admin := make([]byte, 0, 8)
	admin = appendVarint(admin, uint64(AdminFieldGetChannelRequest)<<3|0) // field 1, varint
	admin = appendVarint(admin, uint64(channelIndex))
	return buildAdminToRadio(myNodeNum, myNodeNum, admin)
}

// buildAdminSetCannedMessages builds an AdminMessage to set canned messages.
// Admin field 36 (set_canned_message_module_messages) = string.
func buildAdminSetCannedMessages(myNodeNum uint32, messages string) []byte {
	admin := make([]byte, 0, len(messages)+8)
	admin = appendVarint(admin, uint64(AdminFieldSetCannedMessages)<<3|2) // field 36, length-delimited
	admin = appendVarint(admin, uint64(len(messages)))
	admin = append(admin, []byte(messages)...)
	return buildAdminToRadio(myNodeNum, myNodeNum, admin)
}

// buildAdminGetCannedMessages builds a get_canned_message_module_messages_request.
// Admin field 10 = bool (true to request).
func buildAdminGetCannedMessages(myNodeNum uint32) []byte {
	admin := make([]byte, 0, 8)
	admin = appendVarint(admin, uint64(AdminFieldGetCannedMsgReq)<<3|0) // field 10, varint
	admin = appendVarint(admin, 1)                                      // true
	return buildAdminToRadio(myNodeNum, myNodeNum, admin)
}

// buildStoreForwardRequest builds a StoreAndForward CLIENT_HISTORY request.
// Sends to a specific S&F server node. window = seconds of history to request.
func buildStoreForwardRequest(destNode uint32, window uint32) []byte {
	// StoreAndForward: field 1 = rr (varint, CLIENT_HISTORY=65), field 3 = history
	sf := make([]byte, 0, 16)
	sf = append(sf, 0x08) // field 1, varint
	sf = appendVarint(sf, uint64(SFClientHistory))
	// History submessage: field 2 = window
	if window > 0 {
		hist := make([]byte, 0, 8)
		hist = append(hist, 0x10) // field 2, varint
		hist = appendVarint(hist, uint64(window))
		sf = append(sf, 0x1A) // field 3, length-delimited
		sf = appendVarint(sf, uint64(len(hist)))
		sf = append(sf, hist...)
	}

	return buildRawPacket(sf, PortNumStoreForward, destNode, 0, true)
}

// buildRangeTestPacket builds a range test packet (portnum 66).
func buildRangeTestPacket(text string, to uint32) []byte {
	return buildRawPacket([]byte(text), PortNumRangeTest, to, 0, true)
}

// buildAdminSetFixedPosition builds an AdminMessage to set a fixed GPS position.
// Admin field 41 = Position message (length-delimited).
func buildAdminSetFixedPosition(myNodeNum uint32, lat, lon float64, alt int32) []byte {
	latI := int32(lat * 1e7)
	lonI := int32(lon * 1e7)

	pos := make([]byte, 0, 24)
	pos = append(pos, 0x0D) // field 1: latitude_i (sfixed32)
	pos = appendFixed32(pos, uint32(latI))
	pos = append(pos, 0x15) // field 2: longitude_i (sfixed32)
	pos = appendFixed32(pos, uint32(lonI))
	if alt != 0 {
		pos = append(pos, 0x18) // field 3: altitude (varint)
		pos = appendVarint(pos, uint64(alt))
	}
	pos = append(pos, 0x25) // field 4: time (fixed32)
	pos = appendFixed32(pos, uint32(time.Now().Unix()))

	admin := make([]byte, 0, len(pos)+8)
	admin = appendVarint(admin, uint64(AdminFieldSetFixedPosition)<<3|2) // field 41, length-delimited
	admin = appendVarint(admin, uint64(len(pos)))
	admin = append(admin, pos...)
	return buildAdminToRadio(myNodeNum, myNodeNum, admin)
}

// buildAdminSetOwner builds an AdminMessage to set the device owner name.
// Admin field 32 = User message (length-delimited).
func buildAdminSetOwner(myNodeNum uint32, longName, shortName string) []byte {
	user := make([]byte, 0, 64)
	if longName != "" {
		user = append(user, 0x12) // field 2: long_name (length-delimited)
		user = appendVarint(user, uint64(len(longName)))
		user = append(user, []byte(longName)...)
	}
	if shortName != "" {
		user = append(user, 0x1A) // field 3: short_name (length-delimited)
		user = appendVarint(user, uint64(len(shortName)))
		user = append(user, []byte(shortName)...)
	}

	admin := make([]byte, 0, len(user)+8)
	admin = appendVarint(admin, uint64(AdminFieldSetOwner)<<3|2) // field 32, length-delimited
	admin = appendVarint(admin, uint64(len(user)))
	admin = append(admin, user...)
	return buildAdminToRadio(myNodeNum, myNodeNum, admin)
}

// buildAdminRemoveFixedPosition builds an AdminMessage to remove fixed position.
// Admin field 42 = bool (true).
func buildAdminRemoveFixedPosition(myNodeNum uint32) []byte {
	admin := make([]byte, 0, 8)
	admin = appendVarint(admin, uint64(AdminFieldRemoveFixedPosition)<<3|0) // field 42, varint
	admin = appendVarint(admin, 1)
	return buildAdminToRadio(myNodeNum, myNodeNum, admin)
}

// configSectionToEnum maps section name strings to Config enum values.
func configSectionToEnum(section string) (int, bool) {
	m := map[string]int{
		"device": ConfigTypeDevice, "position": ConfigTypePosition, "power": ConfigTypePower,
		"network": ConfigTypeNetwork, "display": ConfigTypeDisplay, "lora": ConfigTypeLora,
		"bluetooth": ConfigTypeBluetooth, "security": ConfigTypeSecurity,
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
		"neighbor_info": ModuleConfigNeighborInfo,
	}
	v, ok := m[section]
	return v, ok
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
