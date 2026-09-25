// Package rnode drives an RNode (Reticulum LoRa radio) over its KISS-derived
// serial protocol, as RNS/Interfaces/RNodeInterface.py does: detect the
// device, push frequency, bandwidth, TX power, spreading factor, coding
// rate and airtime limits, verify the radio reports them back, then
// exchange raw Reticulum packets as KISS data frames. [MESHSAT-1349]
package rnode

import (
	"encoding/binary"
	"fmt"
	"math"

	"meshsat/internal/kiss"
)

// Command bytes (RNodeInterface.py KISS class).
const (
	CmdData       byte = 0x00
	CmdFrequency  byte = 0x01
	CmdBandwidth  byte = 0x02
	CmdTXPower    byte = 0x03
	CmdSF         byte = 0x04
	CmdCR         byte = 0x05
	CmdRadioState byte = 0x06
	CmdRadioLock  byte = 0x07
	CmdDetect     byte = 0x08
	CmdLeave      byte = 0x0A
	CmdSTALock    byte = 0x0B
	CmdLTALock    byte = 0x0C
	CmdReady      byte = 0x0F
	CmdStatRX     byte = 0x21
	CmdStatTX     byte = 0x22
	CmdStatRSSI   byte = 0x23
	CmdStatSNR    byte = 0x24
	CmdStatCHTM   byte = 0x25
	CmdStatPHYPRM byte = 0x26
	CmdStatBat    byte = 0x27
	CmdStatCSMA   byte = 0x28
	CmdStatTemp   byte = 0x29
	CmdRandom     byte = 0x40
	CmdPlatform   byte = 0x48
	CmdMCU        byte = 0x49
	CmdFWVersion  byte = 0x50
	CmdReset      byte = 0x55
	CmdError      byte = 0x90

	DetectReq  byte = 0x73
	DetectResp byte = 0x46

	RadioStateOff byte = 0x00
	RadioStateOn  byte = 0x01

	ErrorInitRadio    byte = 0x01
	ErrorTXFailed     byte = 0x02
	ErrorEEPROMLocked byte = 0x03
	ErrorQueueFull    byte = 0x04
	ErrorMemoryLow    byte = 0x05
	ErrorModemTimeout byte = 0x06

	PlatformAVR   byte = 0x90
	PlatformESP32 byte = 0x80
	PlatformNRF52 byte = 0x70

	ResetMagic byte = 0xF8

	// HWMTU is the largest data frame payload.
	HWMTU = 508
	// RSSIOffset converts a reported byte to dBm.
	RSSIOffset = 157

	RequiredFWMajor = 1
	RequiredFWMinor = 52

	FreqMin = 137_000_000
	FreqMax = 3_000_000_000
)

// Params are the radio parameters an RNode is configured with.
type Params struct {
	Frequency uint32  `json:"frequency"` // Hz
	Bandwidth uint32  `json:"bandwidth"` // Hz
	TXPower   uint8   `json:"txpower"`   // dBm
	SF        uint8   `json:"spreadingfactor"`
	CR        uint8   `json:"codingrate"`
	STALock   float64 `json:"airtime_limit_short,omitempty"` // percent, 0 = not set
	LTALock   float64 `json:"airtime_limit_long,omitempty"`  // percent, 0 = not set
}

// Validate applies the RNodeInterface range checks.
func (p Params) Validate() error {
	if p.Frequency < FreqMin || p.Frequency > FreqMax {
		return fmt.Errorf("rnode: frequency %d Hz outside %d..%d", p.Frequency, FreqMin, FreqMax)
	}
	if p.Bandwidth < 7800 || p.Bandwidth > 1_625_000 {
		return fmt.Errorf("rnode: bandwidth %d Hz outside 7800..1625000", p.Bandwidth)
	}
	if p.TXPower > 37 {
		return fmt.Errorf("rnode: tx power %d dBm above 37", p.TXPower)
	}
	if p.SF < 5 || p.SF > 12 {
		return fmt.Errorf("rnode: spreading factor %d outside 5..12", p.SF)
	}
	if p.CR < 5 || p.CR > 8 {
		return fmt.Errorf("rnode: coding rate %d outside 5..8", p.CR)
	}
	if p.STALock < 0 || p.STALock > 100 || p.LTALock < 0 || p.LTALock > 100 {
		return fmt.Errorf("rnode: airtime limits must be 0..100 percent")
	}
	return nil
}

// Bitrate is the on-air bit rate for the parameters (RNodeInterface.updateBitrate).
func Bitrate(sf, cr uint8, bw uint32) float64 {
	if sf == 0 || cr == 0 || bw == 0 {
		return 0
	}
	return float64(sf) * ((4.0 / float64(cr)) / (math.Pow(2, float64(sf)) / (float64(bw) / 1000))) * 1000
}

// Command encoders.

// DetectBurst is the detect + firmware + platform + MCU query sent on open.
func DetectBurst() []byte {
	return []byte{kiss.FEND, CmdDetect, DetectReq, kiss.FEND, CmdFWVersion, 0x00, kiss.FEND, CmdPlatform, 0x00, kiss.FEND, CmdMCU, 0x00, kiss.FEND}
}

// Leave tells the device the host is gone.
func Leave() []byte { return []byte{kiss.FEND, CmdLeave, 0xFF, kiss.FEND} }

// HardReset restarts the device.
func HardReset() []byte { return []byte{kiss.FEND, CmdReset, ResetMagic, kiss.FEND} }

func u32(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}

// SetFrequency / SetBandwidth: 4-byte big-endian, escaped.
func SetFrequency(hz uint32) []byte { return kiss.Encode(CmdFrequency, u32(hz)) }
func SetBandwidth(hz uint32) []byte { return kiss.Encode(CmdBandwidth, u32(hz)) }

// SetTXPower / SetSF / SetCR: one byte.
func SetTXPower(dbm uint8) []byte { return kiss.Encode(CmdTXPower, []byte{dbm}) }
func SetSF(sf uint8) []byte       { return kiss.Encode(CmdSF, []byte{sf}) }
func SetCR(cr uint8) []byte       { return kiss.Encode(CmdCR, []byte{cr}) }

// SetSTALock / SetLTALock: int(pct*100) as 2 bytes big-endian.
func SetSTALock(pct float64) []byte {
	v := uint16(pct * 100)
	return kiss.Encode(CmdSTALock, []byte{byte(v >> 8), byte(v)})
}
func SetLTALock(pct float64) []byte {
	v := uint16(pct * 100)
	return kiss.Encode(CmdLTALock, []byte{byte(v >> 8), byte(v)})
}

// SetRadioState turns the radio on or off.
func SetRadioState(on bool) []byte {
	s := RadioStateOff
	if on {
		s = RadioStateOn
	}
	return kiss.Encode(CmdRadioState, []byte{s})
}

// DataFrame wraps a Reticulum packet for transmission.
func DataFrame(packet []byte) []byte { return kiss.Encode(CmdData, packet) }

// Stats are the values the radio reports.
type Stats struct {
	Frequency        uint32  `json:"frequency"`
	Bandwidth        uint32  `json:"bandwidth"`
	TXPower          uint8   `json:"txpower"`
	SF               uint8   `json:"spreadingfactor"`
	CR               uint8   `json:"codingrate"`
	RadioOn          bool    `json:"radio_on"`
	RSSI             int     `json:"rssi"`
	SNR              float64 `json:"snr"`
	Quality          float64 `json:"quality"`
	AirtimeShort     float64 `json:"airtime_short"`
	AirtimeLong      float64 `json:"airtime_long"`
	ChannelLoadShort float64 `json:"channel_load_short"`
	ChannelLoadLong  float64 `json:"channel_load_long"`
	CurrentRSSI      int     `json:"current_rssi"`
	NoiseFloor       int     `json:"noise_floor"`
	Interference     *int    `json:"interference,omitempty"`
	STALock          float64 `json:"airtime_limit_short"`
	LTALock          float64 `json:"airtime_limit_long"`
	SymbolTimeMs     float64 `json:"symbol_time_ms"`
	SymbolRate       int     `json:"symbol_rate"`
	PreambleSymbols  int     `json:"preamble_symbols"`
	PreambleTimeMs   int     `json:"preamble_time_ms"`
	CSMASlotTimeMs   int     `json:"csma_slot_time_ms"`
	CSMADIFSMs       int     `json:"csma_difs_ms"`
	BatteryState     int     `json:"battery_state"`
	BatteryPercent   int     `json:"battery_percent"`
	Temperature      *int    `json:"temperature_c,omitempty"`
	FirmwareMajor    int     `json:"firmware_major"`
	FirmwareMinor    int     `json:"firmware_minor"`
	Platform         byte    `json:"platform"`
	MCU              byte    `json:"mcu"`
	Detected         bool    `json:"detected"`
	RXPackets        uint64  `json:"rx_packets"`
	TXPackets        uint64  `json:"tx_packets"`
	RXBytes          uint64  `json:"rx_bytes"`
	TXBytes          uint64  `json:"tx_bytes"`
	BitrateBps       float64 `json:"bitrate_bps"`
}

// PlatformName renders the platform byte.
func PlatformName(p byte) string {
	switch p {
	case PlatformAVR:
		return "avr"
	case PlatformESP32:
		return "esp32"
	case PlatformNRF52:
		return "nrf52"
	}
	return fmt.Sprintf("0x%02x", p)
}
