package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"meshsat/internal/gateway"
	"meshsat/internal/transport"
)

// PacketRecord is one frame on a bearer as the live packet feed reports it.
// The type lives in the gateway package (gateways fill it without importing
// engine); this alias is the engine-facing name. [MESHSAT-826]
type PacketRecord = gateway.PacketRecord

// PacketRingSize is the number of records the in-memory feed keeps.
const PacketRingSize = 500

// PacketEventType is the MeshEvent type every ring insert broadcasts on
// the SSE stream; Data is the record's JSON.
const PacketEventType = "packet"

// PacketRates counts frames per direction inside one rate window.
type PacketRates struct {
	RX int `json:"rx"`
	TX int `json:"tx"`
}

// packetBearers is the fixed set of bearers the rates endpoint reports,
// zero-filled so the SPA never has to test for a missing key.
var packetBearers = []string{gateway.BearerLoRa, gateway.BearerAPRS, gateway.BearerSMS, gateway.BearerSat}

// PacketRing is a fixed-size, thread-safe, newest-wins ring of packet
// records. It is purely in memory: no table, no migration. Every Add also
// broadcasts a "packet" MeshEvent through the emit callback (the Processor's
// SSE fan-out) so the SPA can animate frames as they happen. A nil ring is
// safe to call: every method is a no-op or returns empty. [MESHSAT-826]
type PacketRing struct {
	mu   sync.Mutex
	buf  []PacketRecord
	next int // index the next record is written to
	n    int // records held (<= len(buf))
	emit func(transport.MeshEvent)
}

// NewPacketRing creates a ring holding size records. emit may be nil.
func NewPacketRing(size int, emit func(transport.MeshEvent)) *PacketRing {
	if size <= 0 {
		size = PacketRingSize
	}
	return &PacketRing{buf: make([]PacketRecord, size), emit: emit}
}

// Add stores one record (newest wins once the ring is full) and emits the
// packet event. Missing timestamps are stamped now; text and raw are capped.
func (r *PacketRing) Add(rec PacketRecord) {
	if r == nil {
		return
	}
	if rec.Time.IsZero() {
		rec.Time = time.Now()
	}
	rec.Text = gateway.CapPacketText(rec.Text)
	if len(rec.Raw) > gateway.PacketRawHexCap {
		rec.Raw = rec.Raw[:gateway.PacketRawHexCap]
	}

	r.mu.Lock()
	r.buf[r.next] = rec
	r.next = (r.next + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
	emit := r.emit
	r.mu.Unlock()

	if emit == nil {
		return
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	emit(transport.MeshEvent{
		Type:    PacketEventType,
		Message: PacketSummary(rec),
		Data:    data,
		Time:    rec.Time.UTC().Format(time.RFC3339Nano),
	})
}

// Sink adapts the ring to the gateway callback type.
func (r *PacketRing) Sink() gateway.PacketSink {
	return func(rec PacketRecord) { r.Add(rec) }
}

// Len returns the number of records held.
func (r *PacketRing) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// Newest returns up to limit records, newest first, optionally filtered by
// bearer and direction (empty = any). limit <= 0 means all held records.
func (r *PacketRing) Newest(limit int, bearer, dir string) []PacketRecord {
	if r == nil {
		return []PacketRecord{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 || limit > r.n {
		limit = r.n
	}
	out := make([]PacketRecord, 0, limit)
	size := len(r.buf)
	for i := 1; i <= r.n && len(out) < limit; i++ {
		rec := r.buf[(r.next-i+size)%size]
		if bearer != "" && rec.Bearer != bearer {
			continue
		}
		if dir != "" && rec.Dir != dir {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// Rates counts records per bearer and direction whose timestamp lies inside
// the window ending at now. Every known bearer is present, zero-filled.
func (r *PacketRing) Rates(now time.Time, window time.Duration) map[string]PacketRates {
	out := make(map[string]PacketRates, len(packetBearers))
	for _, b := range packetBearers {
		out[b] = PacketRates{}
	}
	if r == nil {
		return out
	}
	cutoff := now.Add(-window)
	r.mu.Lock()
	defer r.mu.Unlock()
	size := len(r.buf)
	for i := 1; i <= r.n; i++ {
		rec := r.buf[(r.next-i+size)%size]
		if rec.Time.Before(cutoff) || rec.Time.After(now) {
			continue
		}
		c := out[rec.Bearer]
		switch rec.Dir {
		case gateway.DirRX:
			c.RX++
		case gateway.DirTX:
			c.TX++
		}
		out[rec.Bearer] = c
	}
	return out
}

// PacketSummary is the one-line Message of the packet event, e.g.
// "lora rx mesh_0 !a1b2c3d4 -> broadcast TEXT_MESSAGE_APP 13 B snr 6.5".
func PacketSummary(rec PacketRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s", rec.Bearer, rec.Dir, rec.Iface)
	if rec.From != "" || rec.To != "" {
		fmt.Fprintf(&b, " %s -> %s", rec.From, rec.To)
	}
	if rec.PortNumName != "" {
		fmt.Fprintf(&b, " %s", rec.PortNumName)
	}
	fmt.Fprintf(&b, " %d B", rec.Bytes)
	if rec.RSSI != 0 {
		fmt.Fprintf(&b, " rssi %d", rec.RSSI)
	}
	if rec.SNR != 0 {
		fmt.Fprintf(&b, " snr %.1f", rec.SNR)
	}
	if rec.Hops > 0 {
		fmt.Fprintf(&b, " hops %d", rec.Hops)
	}
	if rec.Path != "" {
		fmt.Fprintf(&b, " via %s", rec.Path)
	}
	if rec.MsgRef != "" {
		fmt.Fprintf(&b, " ref %s", rec.MsgRef)
	}
	return b.String()
}

// meshNodeString renders a Meshtastic node number the way the feed shows
// it: "!%08x", with the all-ones broadcast address as "broadcast".
func meshNodeString(num uint32) string {
	if num == 0xffffffff {
		return "broadcast"
	}
	return fmt.Sprintf("!%08x", num)
}

// meshDestinationString maps a send destination (empty or "!nodeid") to the
// feed's To field.
func meshDestinationString(to string) string {
	if to == "" || to == "broadcast" || to == "!ffffffff" || to == "^all" {
		return "broadcast"
	}
	return to
}

// meshLocalNode returns the local radio's node id ("!%08x") when the
// transport can report it without I/O (the HAL-backed transport would need
// an HTTP round trip per send, so it stays ""), else "".
func meshLocalNode(mesh transport.MeshTransport) string {
	if prov, ok := mesh.(transport.LocalNodeProvider); ok {
		return prov.LocalNodeID()
	}
	return ""
}

// MeshTXRecord builds the feed record for a text the bridge sent into the
// mesh (delivery worker, API compose, presets, SOS, demo). msgRef is the
// delivery msg_ref when the send came through the ledger, else "".
func MeshTXRecord(mesh transport.MeshTransport, iface string, req transport.SendRequest, msgRef string) PacketRecord {
	if iface == "" {
		iface = "mesh_0"
	}
	return PacketRecord{
		Time:        time.Now(),
		Bearer:      gateway.BearerLoRa,
		Dir:         gateway.DirTX,
		Iface:       iface,
		From:        meshLocalNode(mesh),
		To:          meshDestinationString(req.To),
		Bytes:       len(req.Text),
		Channel:     req.Channel,
		PortNum:     int(transport.PortNumTextMessage),
		PortNumName: "TEXT_MESSAGE_APP",
		Text:        req.Text,
		MsgRef:      msgRef,
	}
}

// meshRXRecord builds the feed record for one inbound mesh packet, every
// portnum included. Bytes is the decoded text for text packets, else the
// raw (or still encrypted) payload. RSSI comes from the transport's per-node
// last-heard value when the packet arrived over the air; a packet relayed
// via MQTT has none.
func meshRXRecord(mesh transport.MeshTransport, msg *transport.MeshMessage) PacketRecord {
	rec := PacketRecord{
		Time:        time.Now(),
		Bearer:      gateway.BearerLoRa,
		Dir:         gateway.DirRX,
		Iface:       "mesh_0",
		From:        meshNodeString(msg.From),
		To:          meshNodeString(msg.To),
		SNR:         msg.RxSNR,
		Channel:     int(msg.Channel),
		PortNum:     msg.PortNum,
		PortNumName: msg.PortNumName,
	}
	if msg.HopStart > 0 && msg.HopStart >= msg.HopLimit {
		rec.Hops = msg.HopStart - msg.HopLimit
	}
	switch {
	case msg.PortNum == int(transport.PortNumTextMessage):
		rec.Bytes = len(msg.DecodedText)
		rec.Text = msg.DecodedText
	case len(msg.RawPayload) > 0:
		rec.Bytes = len(msg.RawPayload)
	default:
		rec.Bytes = len(msg.EncryptedPayload)
	}
	if !msg.ViaMqtt {
		if prov, ok := mesh.(transport.NodeRSSIProvider); ok {
			if rssi, known := prov.NodeRSSI(msg.From); known {
				rec.RSSI = int(rssi)
			}
		}
	}
	return rec
}
