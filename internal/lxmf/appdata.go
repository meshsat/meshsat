package lxmf

// SFCompression is the "supported functionality" code LXMF 1.x announces.
const SFCompression = 0

// EncodeAnnounceAppData builds the lxmf.delivery announce app data:
// msgpack([display_name|nil, stamp_cost|nil, [SF_COMPRESSION]])
// (LXMRouter.get_announce_app_data).
func EncodeAnnounceAppData(displayName string, stampCost int) []byte {
	name := Nil()
	if displayName != "" {
		name = Bin([]byte(displayName))
	}
	cost := Nil()
	if stampCost > 0 && stampCost < 255 {
		cost = Int(int64(stampCost))
	}
	return Pack(Array(name, cost, Array(Int(SFCompression))))
}

// AnnounceInfo is what a peer's announce app data says.
type AnnounceInfo struct {
	DisplayName string
	StampCost   int  // 0 = none
	Compression bool // peer accepts compressed resources
	Legacy      bool // pre-0.5.0 raw UTF-8 name
}

// ParseAnnounceAppData reads the fields (LXMF.display_name_from_app_data,
// stamp_cost_from_app_data, compression_support_from_app_data).
func ParseAnnounceAppData(appData []byte) AnnounceInfo {
	info := AnnounceInfo{Compression: true}
	if len(appData) == 0 {
		return info
	}
	if (appData[0] >= 0x90 && appData[0] <= 0x9F) || appData[0] == 0xDC {
		v, _, err := UnpackValue(appData)
		if err != nil || v.Kind != KindArray {
			return info
		}
		if len(v.Array) > 0 {
			switch v.Array[0].Kind {
			case KindBin:
				info.DisplayName = string(v.Array[0].Bin)
			case KindStr:
				info.DisplayName = v.Array[0].Str
			}
		}
		if len(v.Array) > 1 {
			if c, ok := v.Array[1].AsInt(); ok && c > 0 && c < 255 {
				info.StampCost = int(c)
			}
		}
		if len(v.Array) > 2 && v.Array[2].Kind == KindArray {
			info.Compression = false
			for _, f := range v.Array[2].Array {
				if c, ok := f.AsInt(); ok && c == SFCompression {
					info.Compression = true
				}
			}
		}
		return info
	}
	info.DisplayName = string(appData)
	info.Legacy = true
	return info
}
