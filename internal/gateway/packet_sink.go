package gateway

import (
	"encoding/hex"
	"strings"
	"time"
)

// PacketRecord is one frame seen on a bearer, as the live packet feed reports
// it to the SPA (TTC mode ticker and bearer-crossing animation). The engine's
// ring buffer stores these; gateways hand them to a PacketSink. Field shapes
// are fixed, every field is always present in the JSON. [MESHSAT-826]
type PacketRecord struct {
	Time        time.Time `json:"time"`         // RFC3339Nano
	Bearer      string    `json:"bearer"`       // lora | aprs | sms
	Dir         string    `json:"dir"`          // rx | tx
	Iface       string    `json:"iface"`        // mesh_0, aprs_0, cellular_0
	From        string    `json:"from"`         // !nodeid, CALL-SSID, phone number
	To          string    `json:"to"`           // !nodeid or "broadcast", tocall, phone number
	Bytes       int       `json:"bytes"`        // on-air frame or payload size
	RSSI        int       `json:"rssi"`         // dBm, 0 when unknown
	SNR         float32   `json:"snr"`          // dB, 0 when unknown
	Hops        int       `json:"hops"`         // LoRa: hop_start - hop_limit; APRS: digipeater slots used
	Channel     int       `json:"channel"`      // LoRa channel index
	PortNum     int       `json:"portnum"`      // Meshtastic portnum (LoRa)
	PortNumName string    `json:"portnum_name"` // Meshtastic portnum name (LoRa)
	Text        string    `json:"text"`         // decoded text for text frames only, capped
	Raw         string    `json:"raw"`          // hex of the AX.25 frame (APRS only), capped
	Path        string    `json:"path"`         // APRS digipeater path, comma separated
	MsgRef      string    `json:"msg_ref"`      // delivery msg_ref when known
}

// PacketSink receives packet records from a gateway. The manager sets it on
// the APRS and cellular gateways it creates; the engine's ring buffer is the
// implementation. A callback keeps the gateway package free of an engine
// import, like EventEmitFunc. [MESHSAT-826]
type PacketSink func(PacketRecord)

// Bearer and direction values of PacketRecord.
const (
	BearerLoRa = "lora"
	BearerAPRS = "aprs"
	BearerSMS  = "sms"

	DirRX = "rx"
	DirTX = "tx"

	// PacketTextCap bounds PacketRecord.Text; PacketRawHexCap bounds the
	// hex string in PacketRecord.Raw (200 frame bytes).
	PacketTextCap   = 200
	PacketRawHexCap = 400
)

// CapPacketText truncates s to PacketTextCap bytes on a rune boundary.
func CapPacketText(s string) string {
	if len(s) <= PacketTextCap {
		return s
	}
	cut := PacketTextCap
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// aprsPacketRecord builds the feed record for one AX.25 frame as it passed
// the KISS link. Callsigns, path and text come from the frame itself, so the
// same helper serves receive (Direwolf or serial TNC) and transmit (the
// gateway's own frames and the ax25_0 Reticulum interface). Text is set only
// for frames the APRS parser decodes: encrypted `{E1}` frames and Reticulum
// payloads keep it empty. Hops counts the digipeater slots whose H bit is
// set, the frames a digipeater has already repeated. [MESHSAT-826]
func aprsPacketRecord(dir, iface string, payload []byte, msgRef string) PacketRecord {
	rec := PacketRecord{
		Time:   time.Now(),
		Bearer: BearerAPRS,
		Dir:    dir,
		Iface:  iface,
		Bytes:  len(payload),
		MsgRef: msgRef,
	}
	raw := hex.EncodeToString(payload)
	if len(raw) > PacketRawHexCap {
		raw = raw[:PacketRawHexCap]
	}
	rec.Raw = raw

	frame, err := DecodeAX25Frame(payload)
	if err != nil || frame == nil {
		return rec
	}
	rec.From = FormatCallsign(frame.Src)
	rec.To = FormatCallsign(frame.Dst)
	if len(frame.Path) > 0 {
		parts := make([]string, len(frame.Path))
		for i, p := range frame.Path {
			parts[i] = FormatCallsign(p)
		}
		rec.Path = strings.Join(parts, ",")
	}
	rec.Hops = ax25RepeatedCount(payload)

	if len(frame.Info) >= len(aprsEncryptedPrefix) &&
		string(frame.Info[:len(aprsEncryptedPrefix)]) == aprsEncryptedPrefix {
		return rec // ciphertext: no text
	}
	pkt, err := ParseAPRSPacket(frame)
	if err != nil || pkt == nil {
		return rec // Reticulum or binary: no text
	}
	rec.Text = CapPacketText(aprsFrameText(pkt))
	return rec
}

// aprsFrameText is the human-readable part of a decoded APRS frame: the
// message body for `:` frames, the comment for position frames, the raw
// info field for everything else (status, telemetry, objects).
func aprsFrameText(pkt *APRSPacket) string {
	switch pkt.DataType {
	case ':':
		return pkt.Message
	case '!', '=', '/', '@':
		return pkt.Comment
	default:
		return pkt.Raw
	}
}

// ax25RepeatedCount counts digipeater address slots with the H (has been
// repeated) bit set, bit 7 of the SSID byte, which DecodeAX25Frame drops.
func ax25RepeatedCount(data []byte) int {
	if len(data) < 14 {
		return 0
	}
	if data[13]&0x01 == 1 { // source is the last address: no path
		return 0
	}
	n := 0
	for off := 14; off+7 <= len(data); off += 7 {
		if data[off+6]&0x80 != 0 {
			n++
		}
		if data[off+6]&0x01 == 1 {
			break
		}
	}
	return n
}
