package transport

// Z-Stack ZNP (ZigBee Network Processor) protocol codec for TI CC2652P.
// Reference: Z-Stack Monitor and Test API (SWRA371)

import (
	"encoding/binary"
	"fmt"
)

// ZNP frame structure: SOF | LEN | CMD0 | CMD1 | DATA[0..250] | FCS
const (
	znpSOF         byte = 0xFE
	znpMaxDataLen       = 250
	znpHeaderLen        = 4 // SOF + LEN + CMD0 + CMD1
	znpFCSLen           = 1
	znpMinFrameLen      = znpHeaderLen + znpFCSLen // 5 bytes minimum
)

// ZNP command types (CMD0 bits 7-5)
const (
	ZNPTypePOLL byte = 0x00 // 000
	ZNPTypeSREQ byte = 0x20 // 001 — synchronous request
	ZNPTypeAREQ byte = 0x40 // 010 — asynchronous request/notification
	ZNPTypeSRSP byte = 0x60 // 011 — synchronous response
)

// ZNP subsystems (CMD0 bits 4-0)
const (
	ZNPSubSys     byte = 0x01 // SYS
	ZNPSubAF      byte = 0x04 // AF (Application Framework)
	ZNPSubZDO     byte = 0x05 // ZDO (ZigBee Device Object)
	ZNPSubSAPI    byte = 0x06 // Simple API
	ZNPSubUtil    byte = 0x07 // UTIL
	ZNPSubDebug   byte = 0x08 // DEBUG
	ZNPSubApp     byte = 0x09 // APP
	ZNPSubAPPCfg  byte = 0x0F // APP_CNF
	ZNPSubGreenPw byte = 0x15 // GP (Green Power)
)

// Common ZNP commands (CMD0 | CMD1 pairs)
var (
	// SYS commands
	CmdSysResetReq   = [2]byte{ZNPTypeSREQ | ZNPSubSys, 0x00}
	CmdSysResetInd   = [2]byte{ZNPTypeAREQ | ZNPSubSys, 0x80}
	CmdSysPing       = [2]byte{ZNPTypeSREQ | ZNPSubSys, 0x01}
	CmdSysPingRsp    = [2]byte{ZNPTypeSRSP | ZNPSubSys, 0x01}
	CmdSysVersion    = [2]byte{ZNPTypeSREQ | ZNPSubSys, 0x02}
	CmdSysVersionRsp = [2]byte{ZNPTypeSRSP | ZNPSubSys, 0x02}

	// AF commands
	CmdAFRegister    = [2]byte{ZNPTypeSREQ | ZNPSubAF, 0x00}
	CmdAFRegisterRsp = [2]byte{ZNPTypeSRSP | ZNPSubAF, 0x00}
	CmdAFDataReq     = [2]byte{ZNPTypeSREQ | ZNPSubAF, 0x01}
	CmdAFDataReqRsp  = [2]byte{ZNPTypeSRSP | ZNPSubAF, 0x01}
	CmdAFIncomingMsg = [2]byte{ZNPTypeAREQ | ZNPSubAF, 0x81}

	// ZDO commands
	CmdZDOStartupFromApp    = [2]byte{ZNPTypeSREQ | ZNPSubZDO, 0x40}
	CmdZDOStartupFromAppRsp = [2]byte{ZNPTypeSRSP | ZNPSubZDO, 0x40}
	CmdZDOStateChangeInd    = [2]byte{ZNPTypeAREQ | ZNPSubZDO, 0xC0}
	CmdZDOEndDeviceAnnceInd = [2]byte{ZNPTypeAREQ | ZNPSubZDO, 0xC1}
	CmdZDONodeDescReq       = [2]byte{ZNPTypeSREQ | ZNPSubZDO, 0x02}
	CmdZDOActiveEPReq       = [2]byte{ZNPTypeSREQ | ZNPSubZDO, 0x05}

	// ZDO permit join
	CmdZDOMgmtPermitJoinReq = [2]byte{ZNPTypeSREQ | ZNPSubZDO, 0x36}
	CmdZDOMgmtPermitJoinRsp = [2]byte{ZNPTypeSRSP | ZNPSubZDO, 0x36}
	CmdZDOPermitJoinInd     = [2]byte{ZNPTypeAREQ | ZNPSubZDO, 0xCB}

	// ZDO leave (used to evict a device from the network) [MESHSAT-509]
	CmdZDOMgmtLeaveReq = [2]byte{ZNPTypeSREQ | ZNPSubZDO, 0x34}
	CmdZDOMgmtLeaveRsp = [2]byte{ZNPTypeSRSP | ZNPSubZDO, 0x34}
	CmdZDOMgmtLeaveCnf = [2]byte{ZNPTypeAREQ | ZNPSubZDO, 0xC9}
	CmdZDOLeaveInd     = [2]byte{ZNPTypeAREQ | ZNPSubZDO, 0xC9}

	// UTIL commands
	CmdUtilGetDeviceInfo    = [2]byte{ZNPTypeSREQ | ZNPSubUtil, 0x00}
	CmdUtilGetDeviceInfoRsp = [2]byte{ZNPTypeSRSP | ZNPSubUtil, 0x00}
)

// ZNP device states (from ZDO_STATE_CHANGE_IND)
const (
	ZNPDevStateHold          byte = 0x00
	ZNPDevStateInit          byte = 0x01
	ZNPDevStateNwkDisc       byte = 0x02
	ZNPDevStateNwkJoining    byte = 0x03
	ZNPDevStateNwkRejoin     byte = 0x04
	ZNPDevStateEndDevUnauth  byte = 0x05
	ZNPDevStateEndDev        byte = 0x06
	ZNPDevStateRouter        byte = 0x07
	ZNPDevStateCoordStarting byte = 0x08
	ZNPDevStateCoord         byte = 0x09 // coordinator started — network formed
)

// ZNPDevStateName returns a human-readable name for a ZNP device state.
func ZNPDevStateName(s byte) string {
	switch s {
	case ZNPDevStateHold:
		return "hold"
	case ZNPDevStateInit:
		return "init"
	case ZNPDevStateNwkDisc:
		return "nwk-discovery"
	case ZNPDevStateNwkJoining:
		return "nwk-joining"
	case ZNPDevStateNwkRejoin:
		return "nwk-rejoin"
	case ZNPDevStateEndDevUnauth:
		return "end-device-unauth"
	case ZNPDevStateEndDev:
		return "end-device"
	case ZNPDevStateRouter:
		return "router"
	case ZNPDevStateCoordStarting:
		return "coord-starting"
	case ZNPDevStateCoord:
		return "coord-ready"
	default:
		return fmt.Sprintf("0x%02x", s)
	}
}

// Z-Stack reset types (SYS_RESET_REQ argument).
const (
	ZNPResetTypeHard byte = 0x00 // full MCU reboot
	ZNPResetTypeSoft byte = 0x01 // stack re-init, keep NV
)

// Z-Stack reset reasons (SYS_RESET_IND data[0]).
const (
	ZNPResetReasonPowerUp  byte = 0x00
	ZNPResetReasonExternal byte = 0x01
	ZNPResetReasonWatchdog byte = 0x02
	ZNPResetReasonSoft     byte = 0x03
	ZNPResetReasonHardFlt  byte = 0x04
)

// ZNPResetReasonName returns a human-readable reset reason.
func ZNPResetReasonName(r byte) string {
	switch r {
	case ZNPResetReasonPowerUp:
		return "power-up"
	case ZNPResetReasonExternal:
		return "external-reset"
	case ZNPResetReasonWatchdog:
		return "watchdog"
	case ZNPResetReasonSoft:
		return "soft-reset"
	case ZNPResetReasonHardFlt:
		return "hard-fault"
	default:
		return fmt.Sprintf("0x%02x", r)
	}
}

// Common Z-Stack status codes returned in ZNP responses.
// Reference: TI Z-Stack ZComDef.h
const (
	ZStatusSuccess          byte = 0x00
	ZStatusFailure          byte = 0x01
	ZStatusInvalidParameter byte = 0x02

	ZStatusApsFail           byte = 0xB1
	ZStatusApsDuplicateEntry byte = 0xB8
	ZStatusNwkInvalidParam   byte = 0xC1
	ZStatusNwkInvalidRequest byte = 0xC2 // operation not valid for current network state
	ZStatusNwkNotPermitted   byte = 0xC3
	ZStatusNwkStartupFailure byte = 0xC4
	ZStatusNwkTableFull      byte = 0xC7
	ZStatusNwkNoNetworks     byte = 0xCA
	ZStatusNwkLeaveUnconfirm byte = 0xCB
	ZStatusMacNoResource     byte = 0x1A
	ZStatusMacNoBeacon       byte = 0xEA
	ZStatusMacTransactionExp byte = 0xF0
)

// ZNPStatusString returns a human-readable description of a ZNP status byte.
// Useful for translating the numeric codes returned by the coordinator into
// error messages the operator can act on.
func ZNPStatusString(s byte) string {
	switch s {
	case ZStatusSuccess:
		return "success"
	case ZStatusFailure:
		return "failure"
	case ZStatusInvalidParameter:
		return "invalid parameter"
	case ZStatusApsFail:
		return "APS fail"
	case ZStatusApsDuplicateEntry:
		return "APS duplicate entry"
	case ZStatusNwkInvalidParam:
		return "network invalid parameter"
	case ZStatusNwkInvalidRequest:
		return "network not ready (coordinator not in operational state)"
	case ZStatusNwkNotPermitted:
		return "network operation not permitted"
	case ZStatusNwkStartupFailure:
		return "network startup failure"
	case ZStatusNwkTableFull:
		return "network table full"
	case ZStatusNwkNoNetworks:
		return "no networks available"
	case ZStatusNwkLeaveUnconfirm:
		return "leave unconfirmed"
	case ZStatusMacNoResource:
		return "MAC no resource"
	case ZStatusMacNoBeacon:
		return "MAC no beacon"
	case ZStatusMacTransactionExp:
		return "MAC transaction expired"
	default:
		return fmt.Sprintf("status 0x%02x", s)
	}
}

// SysResetInd represents a parsed SYS_RESET_IND AREQ.
type SysResetInd struct {
	Reason       byte
	TransportRev byte
	Product      byte
	MajorRel     byte
	MinorRel     byte
	HwRev        byte
}

// ParseSysResetInd parses a SYS_RESET_IND data payload (6 bytes).
func ParseSysResetInd(data []byte) (*SysResetInd, error) {
	if len(data) < 6 {
		return nil, fmt.Errorf("SYS_RESET_IND too short: %d", len(data))
	}
	return &SysResetInd{
		Reason:       data[0],
		TransportRev: data[1],
		Product:      data[2],
		MajorRel:     data[3],
		MinorRel:     data[4],
		HwRev:        data[5],
	}, nil
}

// ZNPFrame represents a parsed ZNP serial frame.
type ZNPFrame struct {
	Cmd  [2]byte // CMD0, CMD1
	Data []byte  // 0..250 bytes
}

// Type returns the frame type (SREQ, AREQ, SRSP, POLL).
func (f ZNPFrame) Type() byte {
	return f.Cmd[0] & 0xE0
}

// Subsystem returns the subsystem ID.
func (f ZNPFrame) Subsystem() byte {
	return f.Cmd[0] & 0x1F
}

// IsCmd checks if this frame matches a specific command.
func (f ZNPFrame) IsCmd(cmd [2]byte) bool {
	return f.Cmd[0] == cmd[0] && f.Cmd[1] == cmd[1]
}

// String returns a debug representation.
func (f ZNPFrame) String() string {
	typeName := "UNKNOWN"
	switch f.Type() {
	case ZNPTypePOLL:
		typeName = "POLL"
	case ZNPTypeSREQ:
		typeName = "SREQ"
	case ZNPTypeAREQ:
		typeName = "AREQ"
	case ZNPTypeSRSP:
		typeName = "SRSP"
	}
	return fmt.Sprintf("ZNP[%s sub=%d cmd=0x%02x len=%d]", typeName, f.Subsystem(), f.Cmd[1], len(f.Data))
}

// EncodeZNP encodes a ZNP frame to wire format.
func EncodeZNP(f ZNPFrame) ([]byte, error) {
	if len(f.Data) > znpMaxDataLen {
		return nil, fmt.Errorf("ZNP data too long: %d > %d", len(f.Data), znpMaxDataLen)
	}

	buf := make([]byte, znpHeaderLen+len(f.Data)+znpFCSLen)
	buf[0] = znpSOF
	buf[1] = byte(len(f.Data))
	buf[2] = f.Cmd[0]
	buf[3] = f.Cmd[1]
	copy(buf[4:], f.Data)

	// FCS = XOR of LEN, CMD0, CMD1, DATA
	fcs := buf[1]
	for i := 2; i < len(buf)-1; i++ {
		fcs ^= buf[i]
	}
	buf[len(buf)-1] = fcs

	return buf, nil
}

// DecodeZNP parses a ZNP frame from a byte buffer.
// Returns the frame and number of bytes consumed, or error.
func DecodeZNP(buf []byte) (ZNPFrame, int, error) {
	if len(buf) < znpMinFrameLen {
		return ZNPFrame{}, 0, fmt.Errorf("buffer too short: %d < %d", len(buf), znpMinFrameLen)
	}

	// Find SOF
	sofIdx := -1
	for i, b := range buf {
		if b == znpSOF {
			sofIdx = i
			break
		}
	}
	if sofIdx == -1 {
		return ZNPFrame{}, len(buf), fmt.Errorf("no SOF found")
	}

	remaining := buf[sofIdx:]
	if len(remaining) < znpMinFrameLen {
		return ZNPFrame{}, sofIdx, fmt.Errorf("incomplete frame after SOF")
	}

	dataLen := int(remaining[1])
	frameLen := znpHeaderLen + dataLen + znpFCSLen
	if len(remaining) < frameLen {
		return ZNPFrame{}, sofIdx, fmt.Errorf("incomplete frame: need %d, have %d", frameLen, len(remaining))
	}

	// Verify FCS
	fcs := remaining[1]
	for i := 2; i < frameLen-1; i++ {
		fcs ^= remaining[i]
	}
	if fcs != remaining[frameLen-1] {
		return ZNPFrame{}, sofIdx + frameLen, fmt.Errorf("FCS mismatch: got 0x%02x, want 0x%02x", remaining[frameLen-1], fcs)
	}

	f := ZNPFrame{
		Cmd:  [2]byte{remaining[2], remaining[3]},
		Data: make([]byte, dataLen),
	}
	copy(f.Data, remaining[4:4+dataLen])

	return f, sofIdx + frameLen, nil
}

// ---- ZNP message builders ----

// BuildSysPing creates a SYS_PING request.
func BuildSysPing() ZNPFrame {
	return ZNPFrame{Cmd: CmdSysPing}
}

// BuildSysVersion creates a SYS_VERSION request.
func BuildSysVersion() ZNPFrame {
	return ZNPFrame{Cmd: CmdSysVersion}
}

// BuildAFRegister creates an AF_REGISTER request for endpoint 1.
func BuildAFRegister(endpoint byte, profileID, deviceID uint16, inClusters, outClusters []uint16) ZNPFrame {
	data := make([]byte, 0, 9+2*len(inClusters)+2*len(outClusters))
	data = append(data, endpoint)
	data = append(data, byte(profileID), byte(profileID>>8))
	data = append(data, byte(deviceID), byte(deviceID>>8))
	data = append(data, 0) // device version
	data = append(data, 0) // latency req (0=none)
	data = append(data, byte(len(inClusters)))
	for _, c := range inClusters {
		data = append(data, byte(c), byte(c>>8))
	}
	data = append(data, byte(len(outClusters)))
	for _, c := range outClusters {
		data = append(data, byte(c), byte(c>>8))
	}
	return ZNPFrame{Cmd: CmdAFRegister, Data: data}
}

// BuildZDOStartup creates a ZDO_STARTUP_FROM_APP request.
//
// The startDelay field is a uint16 little-endian giving the milliseconds the
// coordinator should wait before starting. zigbee-herdsman uses 100 — small
// but non-zero values give the stack time to settle, especially after a soft
// reset. The original Z-Stack docs accept any value 0..65535.
func BuildZDOStartup() ZNPFrame {
	return ZNPFrame{Cmd: CmdZDOStartupFromApp, Data: []byte{0x64, 0x00}} // startDelay=100ms LE
}

// BuildSysResetReq creates a SYS_RESET_REQ.
// resetType: 0 = hard reset (MCU reboot), 1 = soft reset (stack re-init only).
// Note: Z-Stack replies with an unsolicited SYS_RESET_IND AREQ after the reset
// completes — there is no SRSP for SYS_RESET_REQ.
func BuildSysResetReq(resetType byte) ZNPFrame {
	return ZNPFrame{Cmd: CmdSysResetReq, Data: []byte{resetType}}
}

// BuildUtilGetDeviceInfo creates a UTIL_GET_DEVICE_INFO request.
// The response carries the coordinator's current device state — used by
// initCoordinator to decide whether startup is needed.
func BuildUtilGetDeviceInfo() ZNPFrame {
	return ZNPFrame{Cmd: CmdUtilGetDeviceInfo}
}

// APP_CNF (BDB) commands — Z-Stack 3.0.x base device behaviour layer.
var (
	CmdAppCnfBdbStartCommissioning    = [2]byte{ZNPTypeSREQ | ZNPSubAPPCfg, 0x05}
	CmdAppCnfBdbStartCommissioningRsp = [2]byte{ZNPTypeSRSP | ZNPSubAPPCfg, 0x05}
	CmdAppCnfBdbCommissioningNotif    = [2]byte{ZNPTypeAREQ | ZNPSubAPPCfg, 0x80}
)

// BDB commissioning modes (bit field).
const (
	BDBModeTouchlink        byte = 0x01
	BDBModeNetworkSteering  byte = 0x02
	BDBModeNetworkFormation byte = 0x04
	BDBModeFindingBinding   byte = 0x08
)

// BuildBdbStartCommissioning creates an APP_CNF_BDB_START_COMMISSIONING
// request. On Z-Stack 3.0.x this is the standard way to kick the
// coordinator into forming (mode=0x04) or opening (mode=0x02) a network.
// zigbee-herdsman uses mode=0x04 to form a new network after clearing NV.
func BuildBdbStartCommissioning(mode byte) ZNPFrame {
	return ZNPFrame{Cmd: CmdAppCnfBdbStartCommissioning, Data: []byte{mode}}
}

// BdbCommissioningStatus names the status byte returned in an
// APP_CNF_BDB_COMMISSIONING_NOTIFICATION AREQ.
func BdbCommissioningStatus(s byte) string {
	switch s {
	case 0x00:
		return "success"
	case 0x01:
		return "in-progress"
	case 0x02:
		return "no-network"
	case 0x03:
		return "tclk-ex-failure"
	case 0x04:
		return "network-left"
	case 0x05:
		return "parent-lost"
	case 0x06:
		return "enrollment-successful"
	case 0x07:
		return "enrollment-failure"
	case 0x08:
		return "network-restored"
	case 0x09:
		return "formation-failure"
	case 0x0A:
		return "no-identify"
	case 0x0B:
		return "network-rejoin"
	case 0x0C:
		return "formation-success"
	default:
		return fmt.Sprintf("0x%02x", s)
	}
}

// BuildAFDataReq creates an AF_DATA_REQUEST to send data to a ZigBee device.
func BuildAFDataReq(dstAddr uint16, dstEP, srcEP byte, clusterID uint16, transID byte, data []byte) ZNPFrame {
	payload := make([]byte, 0, 10+len(data))
	payload = append(payload, byte(dstAddr), byte(dstAddr>>8)) // DstAddr
	payload = append(payload, dstEP)                           // DstEndpoint
	payload = append(payload, srcEP)                           // SrcEndpoint
	payload = append(payload, byte(clusterID), byte(clusterID>>8))
	payload = append(payload, transID) // TransID
	payload = append(payload, 0x00)    // Options (0=none)
	payload = append(payload, 0x07)    // Radius (default 7 hops)
	payload = append(payload, byte(len(data)))
	payload = append(payload, data...)
	return ZNPFrame{Cmd: CmdAFDataReq, Data: payload}
}

// BuildMgmtPermitJoinReq creates a ZDO_MGMT_PERMIT_JOIN_REQ to open the
// network for device pairing. [MESHSAT-510]
//
// We use unicast self-addressing (addrMode=0x02, dstAddr=0x0000, tcSig=0)
// rather than broadcast (0x0F / 0xFFFC / tcSig=1). On a freshly-restored
// network the NWK layer rejects broadcasts with ZNwkInvalidRequest (0xC2)
// because there are no routers in the neighbour table to relay the
// broadcast to and TC_Significance=1 implies a trust-centre APS round-trip
// the empty NIB cannot service. Unicast to 0x0000 just enables permit-join
// on the local coordinator — no NWK traffic, always works as long as the
// dongle is in DEV_ZB_COORD. This matches zigbee-herdsman's "self permit
// join" path used when no remote network address is supplied.
//
// addrMode: 0x02 = unicast to a 16-bit network address
// dstAddr:  0x0000 = the coordinator itself (us)
// duration: 0-254 seconds (0 = disable, 255 = until turned off)
// tcSignificance: 0 = local change only, no trust-centre propagation
func BuildMgmtPermitJoinReq(duration byte) ZNPFrame {
	payload := []byte{
		0x02,       // AddrMode: unicast
		0x00, 0x00, // DstAddr: 0x0000 (coordinator / self)
		duration, // Duration in seconds
		0x00,     // TC_Significance: local only
	}
	return ZNPFrame{Cmd: CmdZDOMgmtPermitJoinReq, Data: payload}
}

// BuildMgmtLeaveReq creates a ZDO_MGMT_LEAVE_REQ telling a remote device to
// leave the network. Used to properly unpair a device — without this, an
// "unpaired" device on our side stays joined in the Z-Stack NV table and
// will keep talking to us, eventually rebuilding its row on the next
// announce. [MESHSAT-509]
//
// Layout (ZNP MT spec):
//
//	bytes 0-1   DstAddr (LE)        — short address of the device to leave
//	bytes 2-9   DeviceAddress (LE)  — IEEE 64-bit address of the device
//	byte  10    RemoveChildren      — 0 = leave only this device, 1 = also evict its children
//	byte  11    Rejoin              — 0 = device may NOT rejoin, 1 = device may rejoin
//
// We always set RemoveChildren=0 (most field-kit devices have no children
// of their own) and Rejoin=0 (the user clicked "Forget" — they don't want
// it back automatically).
func BuildMgmtLeaveReq(dstAddr uint16, ieeeAddr [8]byte) ZNPFrame {
	payload := make([]byte, 12)
	payload[0] = byte(dstAddr)
	payload[1] = byte(dstAddr >> 8)
	// IEEE: little-endian on the wire (Z-Stack convention).
	for i := 0; i < 8; i++ {
		payload[2+i] = ieeeAddr[i]
	}
	payload[10] = 0x00 // RemoveChildren
	payload[11] = 0x00 // Rejoin
	return ZNPFrame{Cmd: CmdZDOMgmtLeaveReq, Data: payload}
}

// ---- ZNP response parsers ----

// ParseSysVersionRsp parses a SYS_VERSION response.
type SysVersionInfo struct {
	TransportRev byte
	Product      byte
	MajorRel     byte
	MinorRel     byte
	MaintRel     byte
}

func ParseSysVersionRsp(data []byte) (*SysVersionInfo, error) {
	if len(data) < 5 {
		return nil, fmt.Errorf("SYS_VERSION response too short: %d", len(data))
	}
	return &SysVersionInfo{
		TransportRev: data[0],
		Product:      data[1],
		MajorRel:     data[2],
		MinorRel:     data[3],
		MaintRel:     data[4],
	}, nil
}

// ParseAFIncomingMsg parses an AF_INCOMING_MSG notification.
type AFIncomingMsg struct {
	GroupID   uint16
	ClusterID uint16
	SrcAddr   uint16
	SrcEP     byte
	DstEP     byte
	WasBcast  byte
	LQI       byte
	SecUse    byte
	Timestamp uint32
	TransSeq  byte
	Data      []byte
}

func ParseAFIncomingMsg(data []byte) (*AFIncomingMsg, error) {
	if len(data) < 17 {
		return nil, fmt.Errorf("AF_INCOMING_MSG too short: %d", len(data))
	}
	msg := &AFIncomingMsg{
		GroupID:   binary.LittleEndian.Uint16(data[0:2]),
		ClusterID: binary.LittleEndian.Uint16(data[2:4]),
		SrcAddr:   binary.LittleEndian.Uint16(data[4:6]),
		SrcEP:     data[6],
		DstEP:     data[7],
		WasBcast:  data[8],
		LQI:       data[9],
		SecUse:    data[10],
		Timestamp: binary.LittleEndian.Uint32(data[11:15]),
		TransSeq:  data[15],
	}
	dataLen := int(data[16])
	if len(data) < 17+dataLen {
		return nil, fmt.Errorf("AF_INCOMING_MSG data truncated: need %d, have %d", 17+dataLen, len(data))
	}
	msg.Data = make([]byte, dataLen)
	copy(msg.Data, data[17:17+dataLen])
	return msg, nil
}

// ParseDeviceInfo parses a UTIL_GET_DEVICE_INFO response.
type DeviceInfo struct {
	IEEEAddr    [8]byte
	ShortAddr   uint16
	DeviceType  byte
	DeviceState byte
}

func ParseDeviceInfo(data []byte) (*DeviceInfo, error) {
	if len(data) < 13 {
		return nil, fmt.Errorf("UTIL_GET_DEVICE_INFO too short: %d", len(data))
	}
	info := &DeviceInfo{
		ShortAddr:   binary.LittleEndian.Uint16(data[9:11]),
		DeviceType:  data[11],
		DeviceState: data[12],
	}
	copy(info.IEEEAddr[:], data[1:9]) // skip status byte
	return info, nil
}
