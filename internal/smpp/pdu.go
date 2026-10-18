// Package smpp is a minimal SMPP 3.4 ESME (client) for the MeshSat cell:
// the PDUs osmo-msc's built-in SMSC speaks (bind_transceiver, unbind,
// enquire_link, submit_sm, deliver_sm, alert_notification, generic_nack),
// the GSM 03.38 alphabet the MSC expects in short_message, and UDH
// concatenation. Pure Go, no CGO. [MESHSAT-1557]
//
// What the server does with these, verified in osmo-msc 1.16.0
// (src/libsmpputil/smpp_msc.c, smpp_smsc.c):
//   - data_coding 0x00/0x01 carries GSM 03.38 septet VALUES one per byte
//     (gsm_7bit_expand "no decode", gsm_septet_pack on submit), never
//     Latin-1; 0x02 is 8-bit data; 0x08 is UCS-2 big-endian.
//   - esm_class mode 2 (forward) is transaction mode: submit_sm_resp comes
//     back only when the phone acknowledged (status 0) or the delivery
//     failed (0xFE); an unknown or unregistered subscriber gets 0x0B at
//     once. Modes 0/1/3 are stored in the MSC's own database instead.
//   - deliver_sm_resp must arrive within 5 s or the phone gets RP cause 38.
//   - the server never sends enquire_link; the client must.
package smpp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Command ids (SMPP 3.4 section 5.1.2.1).
const (
	CmdGenericNack       uint32 = 0x80000000
	CmdBindReceiver      uint32 = 0x00000001
	CmdBindTransmitter   uint32 = 0x00000002
	CmdSubmitSM          uint32 = 0x00000004
	CmdSubmitSMResp      uint32 = 0x80000004
	CmdDeliverSM         uint32 = 0x00000005
	CmdDeliverSMResp     uint32 = 0x80000005
	CmdUnbind            uint32 = 0x00000006
	CmdUnbindResp        uint32 = 0x80000006
	CmdBindTransceiver   uint32 = 0x00000009
	CmdBindTransceiverRe uint32 = 0x80000009
	CmdEnquireLink       uint32 = 0x00000015
	CmdEnquireLinkResp   uint32 = 0x80000015
	CmdAlertNotification uint32 = 0x00000102
	CmdDataSM            uint32 = 0x00000103
)

// Command status values (section 5.1.3) the gateway acts on.
const (
	StatusOK              uint32 = 0x00000000
	StatusInvalidMsgLen   uint32 = 0x00000001
	StatusInvalidCmdLen   uint32 = 0x00000002
	StatusInvalidCmdID    uint32 = 0x00000003
	StatusInvalidBindStat uint32 = 0x00000004
	StatusAlreadyBound    uint32 = 0x00000005
	StatusSysErr          uint32 = 0x00000008
	StatusInvalidSrcAddr  uint32 = 0x0000000A
	StatusInvalidDstAddr  uint32 = 0x0000000B // osmo-msc: unknown or unregistered subscriber (forward mode)
	StatusInvalidMsgID    uint32 = 0x0000000C
	StatusBindFailed      uint32 = 0x0000000D
	StatusInvalidPassword uint32 = 0x0000000E
	StatusInvalidSystemID uint32 = 0x0000000F
	StatusMsgQueueFull    uint32 = 0x00000014
	StatusThrottled       uint32 = 0x00000058
	StatusDeliveryFailure uint32 = 0x000000FE // osmo-msc: transaction mode, paging expired, RP-ERROR or an unknown error
	StatusUnknownErr      uint32 = 0x000000FF
)

// esm_class bits (section 5.2.12).
const (
	ESMModeDefault         byte = 0x00
	ESMModeDatagram        byte = 0x01
	ESMModeForward         byte = 0x02 // transaction mode on osmo-msc
	ESMModeStoreForward    byte = 0x03
	ESMModeMask            byte = 0x03
	ESMTypeMask            byte = 0x3C
	ESMTypeDeliveryReceipt byte = 0x04
	ESMUDHI                byte = 0x40
	ESMReplyPath           byte = 0x80
)

// data_coding values (section 5.2.19) as osmo-msc uses them.
const (
	DCSDefault byte = 0x00 // GSM 03.38 septet values
	DCSGSM7    byte = 0x01 // GSM 03.38 septet values (what the MSC sends)
	DCS8Bit    byte = 0x02 // 8-bit data (what the MSC sends for a report)
	DCSOctet   byte = 0x04 // 8-bit data on submit
	DCSUCS2    byte = 0x08 // UCS-2 big-endian
)

// TLV tags (section 5.3.2).
const (
	TagUserMessageReference uint16 = 0x0204
	TagSCInterfaceVersion   uint16 = 0x0210
	TagMSAvailabilityStatus uint16 = 0x0422
	TagMessagePayload       uint16 = 0x0424
)

// TON and NPI values the cell uses.
const (
	TONUnknown         byte = 0
	TONInternational   byte = 1
	TONNetworkSpecific byte = 3 // osmo-msc puts this on deliver_sm source_addr
	NPIUnknown         byte = 0
	NPIISDN            byte = 1
)

// Version is the only interface_version osmo-msc accepts.
const Version byte = 0x34

// MaxShortMessage is the largest short_message a submit_sm carries.
const MaxShortMessage = 254

// Field limits (C-octet strings, including the terminating NUL).
const (
	maxSystemID    = 16
	maxPassword    = 9
	maxSystemType  = 13
	maxAddrRange   = 41
	maxServiceType = 6
	maxAddr        = 21
	maxAlertAddr   = 65
	maxTime        = 17
	maxMessageID   = 65
)

// ErrShort is returned when a PDU ends before its fields do.
var ErrShort = errors.New("smpp: truncated PDU")

// TLV is one optional parameter.
type TLV struct {
	Tag   uint16
	Value []byte
}

// Header is the 16-byte PDU header.
type Header struct {
	Length   uint32
	ID       uint32
	Status   uint32
	Sequence uint32
}

// PDU is one decoded protocol data unit: its header and raw body.
type PDU struct {
	Header
	Body []byte
}

// IsResponse reports whether the PDU answers a request.
func (p PDU) IsResponse() bool { return p.ID&0x80000000 != 0 }

// Bind is the body of bind_transceiver (and the other binds).
type Bind struct {
	SystemID     string
	Password     string
	SystemType   string
	Version      byte
	AddrTON      byte
	AddrNPI      byte
	AddressRange string
}

// BindResp is the body of a bind response.
type BindResp struct {
	SystemID string
	TLVs     []TLV
}

// SM is the body of submit_sm and deliver_sm, which share a layout.
type SM struct {
	ServiceType          string
	SourceTON, SourceNPI byte
	SourceAddr           string
	DestTON, DestNPI     byte
	DestAddr             string
	ESMClass             byte
	ProtocolID           byte
	PriorityFlag         byte
	ScheduleDeliveryTime string
	ValidityPeriod       string
	RegisteredDelivery   byte
	ReplaceIfPresent     byte
	DataCoding           byte
	SMDefaultMsgID       byte
	ShortMessage         []byte
	TLVs                 []TLV
}

// SMResp is the body of submit_sm_resp and deliver_sm_resp.
type SMResp struct {
	MessageID string
}

// Alert is the body of alert_notification.
type Alert struct {
	SourceTON, SourceNPI byte
	SourceAddr           string
	ESMETON, ESMENPI     byte
	ESMEAddr             string
	TLVs                 []TLV
}

// Availability returns the ms_availability_status TLV: 0 available, 1
// denied, 2 unavailable; -1 when absent.
func (a Alert) Availability() int {
	for _, t := range a.TLVs {
		if t.Tag == TagMSAvailabilityStatus && len(t.Value) == 1 {
			return int(t.Value[0])
		}
	}
	return -1
}

// TLV returns the value of the first TLV with the tag.
func (s SM) TLV(tag uint16) ([]byte, bool) {
	for _, t := range s.TLVs {
		if t.Tag == tag {
			return t.Value, true
		}
	}
	return nil, false
}

// UserMessageReference returns the 16-bit user_message_reference TLV.
func (s SM) UserMessageReference() (uint16, bool) {
	v, ok := s.TLV(TagUserMessageReference)
	if !ok || len(v) != 2 {
		return 0, false
	}
	return binary.BigEndian.Uint16(v), true
}

// IsReceipt reports whether the esm_class marks a delivery receipt.
func (s SM) IsReceipt() bool { return s.ESMClass&ESMTypeMask == ESMTypeDeliveryReceipt }

// HasUDH reports whether short_message starts with a user data header.
func (s SM) HasUDH() bool { return s.ESMClass&ESMUDHI != 0 }

// --- encoding ---

type writer struct{ b []byte }

func (w *writer) u8(v byte)     { w.b = append(w.b, v) }
func (w *writer) u16(v uint16)  { w.b = binary.BigEndian.AppendUint16(w.b, v) }
func (w *writer) u32(v uint32)  { w.b = binary.BigEndian.AppendUint32(w.b, v) }
func (w *writer) raw(v []byte)  { w.b = append(w.b, v...) }
func (w *writer) coct(s string) { w.b = append(append(w.b, s...), 0) }
func (w *writer) tlvs(ts []TLV) {
	for _, t := range ts {
		w.u16(t.Tag)
		w.u16(uint16(len(t.Value)))
		w.raw(t.Value)
	}
}

// Encode frames a PDU: header plus body.
func Encode(id, status, seq uint32, body []byte) []byte {
	w := &writer{b: make([]byte, 0, 16+len(body))}
	w.u32(uint32(16 + len(body)))
	w.u32(id)
	w.u32(status)
	w.u32(seq)
	w.raw(body)
	return w.b
}

func checkCOctet(name, s string, max int) error {
	if len(s)+1 > max {
		return fmt.Errorf("smpp: %s is %d characters, at most %d allowed", name, len(s), max-1)
	}
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return fmt.Errorf("smpp: %s contains a NUL byte", name)
		}
	}
	return nil
}

// EncodeBind builds a bind body.
func EncodeBind(b Bind) ([]byte, error) {
	if err := checkCOctet("system_id", b.SystemID, maxSystemID); err != nil {
		return nil, err
	}
	if err := checkCOctet("password", b.Password, maxPassword); err != nil {
		return nil, err
	}
	if err := checkCOctet("system_type", b.SystemType, maxSystemType); err != nil {
		return nil, err
	}
	if err := checkCOctet("address_range", b.AddressRange, maxAddrRange); err != nil {
		return nil, err
	}
	w := &writer{}
	w.coct(b.SystemID)
	w.coct(b.Password)
	w.coct(b.SystemType)
	w.u8(b.Version)
	w.u8(b.AddrTON)
	w.u8(b.AddrNPI)
	w.coct(b.AddressRange)
	return w.b, nil
}

// EncodeBindResp builds a bind response body (the fake server uses it).
func EncodeBindResp(r BindResp) []byte {
	w := &writer{}
	w.coct(r.SystemID)
	w.tlvs(r.TLVs)
	return w.b
}

// EncodeSM builds a submit_sm or deliver_sm body.
func EncodeSM(s SM) ([]byte, error) {
	if err := checkCOctet("service_type", s.ServiceType, maxServiceType); err != nil {
		return nil, err
	}
	if err := checkCOctet("source_addr", s.SourceAddr, maxAddr); err != nil {
		return nil, err
	}
	if err := checkCOctet("destination_addr", s.DestAddr, maxAddr); err != nil {
		return nil, err
	}
	if err := checkCOctet("schedule_delivery_time", s.ScheduleDeliveryTime, maxTime); err != nil {
		return nil, err
	}
	if err := checkCOctet("validity_period", s.ValidityPeriod, maxTime); err != nil {
		return nil, err
	}
	if len(s.ShortMessage) > MaxShortMessage {
		return nil, fmt.Errorf("smpp: short_message is %d bytes, at most %d allowed", len(s.ShortMessage), MaxShortMessage)
	}
	w := &writer{}
	w.coct(s.ServiceType)
	w.u8(s.SourceTON)
	w.u8(s.SourceNPI)
	w.coct(s.SourceAddr)
	w.u8(s.DestTON)
	w.u8(s.DestNPI)
	w.coct(s.DestAddr)
	w.u8(s.ESMClass)
	w.u8(s.ProtocolID)
	w.u8(s.PriorityFlag)
	w.coct(s.ScheduleDeliveryTime)
	w.coct(s.ValidityPeriod)
	w.u8(s.RegisteredDelivery)
	w.u8(s.ReplaceIfPresent)
	w.u8(s.DataCoding)
	w.u8(s.SMDefaultMsgID)
	w.u8(byte(len(s.ShortMessage)))
	w.raw(s.ShortMessage)
	w.tlvs(s.TLVs)
	return w.b, nil
}

// EncodeSMResp builds a submit_sm_resp or deliver_sm_resp body.
func EncodeSMResp(r SMResp) []byte {
	w := &writer{}
	w.coct(r.MessageID)
	return w.b
}

// EncodeAlert builds an alert_notification body (the fake server uses it).
func EncodeAlert(a Alert) []byte {
	w := &writer{}
	w.u8(a.SourceTON)
	w.u8(a.SourceNPI)
	w.coct(a.SourceAddr)
	w.u8(a.ESMETON)
	w.u8(a.ESMENPI)
	w.coct(a.ESMEAddr)
	w.tlvs(a.TLVs)
	return w.b
}

// U16TLV builds a two-byte TLV.
func U16TLV(tag uint16, v uint16) TLV {
	return TLV{Tag: tag, Value: binary.BigEndian.AppendUint16(nil, v)}
}

// --- decoding ---

type reader struct {
	b   []byte
	pos int
	err error
}

func (r *reader) u8() byte {
	if r.err != nil {
		return 0
	}
	if r.pos+1 > len(r.b) {
		r.err = ErrShort
		return 0
	}
	v := r.b[r.pos]
	r.pos++
	return v
}

func (r *reader) u16() uint16 {
	if r.err != nil {
		return 0
	}
	if r.pos+2 > len(r.b) {
		r.err = ErrShort
		return 0
	}
	v := binary.BigEndian.Uint16(r.b[r.pos:])
	r.pos += 2
	return v
}

func (r *reader) coct(max int) string {
	if r.err != nil {
		return ""
	}
	for i := r.pos; i < len(r.b) && i-r.pos < max; i++ {
		if r.b[i] == 0 {
			s := string(r.b[r.pos:i])
			r.pos = i + 1
			return s
		}
	}
	r.err = fmt.Errorf("smpp: C-octet string without a terminator within %d bytes", max)
	return ""
}

func (r *reader) raw(n int) []byte {
	if r.err != nil {
		return nil
	}
	if r.pos+n > len(r.b) {
		r.err = ErrShort
		return nil
	}
	v := append([]byte(nil), r.b[r.pos:r.pos+n]...)
	r.pos += n
	return v
}

func (r *reader) tlvs() []TLV {
	var out []TLV
	for r.err == nil && r.pos < len(r.b) {
		tag := r.u16()
		n := int(r.u16())
		v := r.raw(n)
		if r.err != nil {
			break
		}
		out = append(out, TLV{Tag: tag, Value: v})
	}
	return out
}

// DecodeHeader reads the 16-byte header.
func DecodeHeader(b []byte) (Header, error) {
	if len(b) < 16 {
		return Header{}, ErrShort
	}
	return Header{
		Length:   binary.BigEndian.Uint32(b[0:]),
		ID:       binary.BigEndian.Uint32(b[4:]),
		Status:   binary.BigEndian.Uint32(b[8:]),
		Sequence: binary.BigEndian.Uint32(b[12:]),
	}, nil
}

// DecodeBind parses a bind body.
func DecodeBind(b []byte) (Bind, error) {
	r := &reader{b: b}
	var out Bind
	out.SystemID = r.coct(maxSystemID)
	out.Password = r.coct(maxPassword)
	out.SystemType = r.coct(maxSystemType)
	out.Version = r.u8()
	out.AddrTON = r.u8()
	out.AddrNPI = r.u8()
	out.AddressRange = r.coct(maxAddrRange)
	return out, r.err
}

// DecodeBindResp parses a bind response body.
func DecodeBindResp(b []byte) (BindResp, error) {
	r := &reader{b: b}
	var out BindResp
	out.SystemID = r.coct(maxSystemID)
	out.TLVs = r.tlvs()
	return out, r.err
}

// DecodeSM parses a submit_sm or deliver_sm body.
func DecodeSM(b []byte) (SM, error) {
	r := &reader{b: b}
	var s SM
	s.ServiceType = r.coct(maxServiceType)
	s.SourceTON = r.u8()
	s.SourceNPI = r.u8()
	s.SourceAddr = r.coct(maxAddr)
	s.DestTON = r.u8()
	s.DestNPI = r.u8()
	s.DestAddr = r.coct(maxAddr)
	s.ESMClass = r.u8()
	s.ProtocolID = r.u8()
	s.PriorityFlag = r.u8()
	s.ScheduleDeliveryTime = r.coct(maxTime)
	s.ValidityPeriod = r.coct(maxTime)
	s.RegisteredDelivery = r.u8()
	s.ReplaceIfPresent = r.u8()
	s.DataCoding = r.u8()
	s.SMDefaultMsgID = r.u8()
	n := int(r.u8())
	s.ShortMessage = r.raw(n)
	s.TLVs = r.tlvs()
	return s, r.err
}

// DecodeSMResp parses a submit_sm_resp or deliver_sm_resp body. A body
// with no bytes at all is tolerated (some servers send none on an error).
func DecodeSMResp(b []byte) (SMResp, error) {
	if len(b) == 0 {
		return SMResp{}, nil
	}
	r := &reader{b: b}
	var out SMResp
	out.MessageID = r.coct(maxMessageID)
	return out, r.err
}

// DecodeAlert parses an alert_notification body.
func DecodeAlert(b []byte) (Alert, error) {
	r := &reader{b: b}
	var a Alert
	a.SourceTON = r.u8()
	a.SourceNPI = r.u8()
	a.SourceAddr = r.coct(maxAlertAddr)
	a.ESMETON = r.u8()
	a.ESMENPI = r.u8()
	a.ESMEAddr = r.coct(maxAlertAddr)
	a.TLVs = r.tlvs()
	return a, r.err
}

// StatusText names a command status for logs and ledger reasons.
func StatusText(status uint32) string {
	switch status {
	case StatusOK:
		return "ok"
	case StatusInvalidMsgLen:
		return "invalid message length"
	case StatusInvalidCmdLen:
		return "invalid command length"
	case StatusInvalidCmdID:
		return "invalid command id"
	case StatusInvalidBindStat:
		return "incorrect bind status for this command"
	case StatusAlreadyBound:
		return "already bound"
	case StatusSysErr:
		return "system error"
	case StatusInvalidSrcAddr:
		return "invalid source address"
	case StatusInvalidDstAddr:
		return "the phone is not registered on the cell (unknown subscriber)"
	case StatusInvalidMsgID:
		return "invalid message id"
	case StatusBindFailed:
		return "bind failed"
	case StatusInvalidPassword:
		return "the cell refused the password"
	case StatusInvalidSystemID:
		return "the cell does not know this system id"
	case StatusMsgQueueFull:
		return "message queue full"
	case StatusThrottled:
		return "throttled"
	case StatusDeliveryFailure:
		return "the cell could not complete the delivery to the phone (paging failed, the phone refused it, or it is unknown whether the phone got it)"
	case StatusUnknownErr:
		return "unknown error"
	}
	return fmt.Sprintf("status 0x%08x", status)
}
