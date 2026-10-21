package main

import (
	"context"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/hubreporter"
)

// satModem names the running satellite gateway of a type: its modem's IMEI
// and the interface its messages leave through. ifaceID is "" for any other
// gateway type or when that gateway is not running.
func satModem(m *gateway.Manager, gwType string) (imei, ifaceID string) {
	switch gwType {
	case "iridium_imt":
		if gw := m.GetIMTGateway(); gw != nil {
			return gw.IMEI(), "iridium_imt_0"
		}
	case "iridium":
		if gw := m.GetSBDGateway(); gw != nil {
			return gw.IMEI(), "iridium_0"
		}
	}
	return "", ""
}

// chainReader is where the birth reads a satellite interface's send chain
// (*database.DB).
type chainReader interface {
	GetInterface(id string) (*database.Interface, error)
}

// satFormat is the format the birth declares for a satellite interface:
// "sealed" when its send chain encrypts, "plain" when it does not, and ""
// when the chain could not be read. A failed read declares nothing, so the
// Hub keeps the modem's last declaration: it is never taken for an empty
// chain, which would declare plain.
func satFormat(db chainReader, ifaceID string) string {
	iface, err := db.GetInterface(ifaceID)
	if err != nil || iface == nil {
		log.Warn().Err(err).Str("interface", ifaceID).Msg("birth: the satellite send chain could not be read; no payload format declared")
		return ""
	}
	if engine.ChainEncrypts(iface.EgressTransforms) {
		return "sealed"
	}
	return "plain"
}

// satBirthEntry is a satellite gateway's birth entry: the modem's IMEI, which
// lets the Hub find the bridge that owns it, and the format of the
// interface's payloads (satFormat). The Hub keeps a sealed modem's versioned
// payloads opaque instead of guessing text in them (HeMB-NG S3-2), and reads
// a plain one's as text. egress_sealed stays for a Hub that knows only that
// flag. format "" declares nothing.
func satBirthEntry(info hubreporter.InterfaceInfo, imei, format string) hubreporter.InterfaceInfo {
	info.IMEI = imei
	info.EgressFormat = format
	info.EgressSealed = format == "sealed"
	return info
}

// satModemsKey names the running satellite gateways' modems and what the
// birth declares for them, "type=imei:format" each, in a fixed order.
func satModemsKey(m *gateway.Manager, db chainReader) string {
	var parts []string
	for _, typ := range []string{"iridium_imt", "iridium"} {
		if imei, id := satModem(m, typ); id != "" {
			parts = append(parts, satModemKey(typ, imei, satFormat(db, id)))
		}
	}
	return strings.Join(parts, ",")
}

func satModemKey(typ, imei, format string) string {
	return typ + "=" + imei + ":" + format
}

// watchSatModems republishes the birth when the satellite modems key names
// change. The birth goes out at the Hub connect, which comes before a 9704
// has told its IMEI (2 s later on parallax, 21 Oct 2026), and the Hub ties a
// modem to its bridge, and a sealing declaration to the modem, by that IMEI.
// A chain that could not be read and is read again changes the key too, so
// the declaration it held back goes out without anyone republishing.
func watchSatModems(ctx context.Context, every time.Duration, key func() string, republish func()) {
	last := ""
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if cur := key(); cur != last {
			last = cur
			republish()
		}
	}
}

// birthStamps keeps the last Hub birth's timestamp in system_config, so the
// first birth after a restart is stamped later than the last one before it,
// even when the clock came up behind (hubreporter.BirthStampStore).
type birthStamps struct{ db *database.DB }

const birthStampKey = "hub_last_birth_stamp"

func (s birthStamps) LastBirthStamp() time.Time {
	v, err := s.db.GetSystemConfig(birthStampKey)
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		log.Warn().Err(err).Msg("birth: the stored birth timestamp is unreadable; stamping from the clock")
		return time.Time{}
	}
	return t
}

func (s birthStamps) SaveBirthStamp(t time.Time) {
	if err := s.db.SetSystemConfig(birthStampKey, t.Format(time.RFC3339Nano)); err != nil {
		log.Warn().Err(err).Msg("birth: the birth timestamp was not stored")
	}
}
