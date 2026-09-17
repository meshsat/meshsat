package transport

import "fmt"

// LoRa region helpers, shared by the radio-setup API and the spectrum
// monitor's startup check. The region decides which frequencies the mesh
// radio transmits on, so the band that watches the mesh has to agree with
// it — see internal/spectrum/mesh_band.go. [MESHSAT-1203]

// LoraRegionNames maps the Meshtastic RegionCode enum to its name.
var LoraRegionNames = map[int]string{
	0:  "UNSET",
	1:  "US",
	2:  "EU_433",
	3:  "EU_868",
	4:  "CN",
	5:  "JP",
	6:  "ANZ",
	7:  "KR",
	8:  "TW",
	9:  "RU",
	10: "IN",
	11: "NZ_865",
	12: "TH",
	13: "LORA_24",
	14: "UA_433",
	15: "UA_868",
	16: "MY_433",
	17: "MY_919",
	18: "SG_923",
}

// LoraRegionCode extracts the region enum from a GetConfig map. The map is
// keyed "config_<protobuf field>" and the LoRa config is field 6; within it,
// field 7 is the region. Returns 0 (UNSET) when the config is missing.
func LoraRegionCode(config map[string]interface{}) int {
	loraRaw, ok := config["config_6"]
	if !ok {
		return 0
	}
	loraMap, ok := loraRaw.(map[string]interface{})
	if !ok {
		return 0
	}
	regionVal, ok := loraMap["7"]
	if !ok {
		return 0
	}
	switch v := regionVal.(type) {
	case uint64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

// LoraRegionName is LoraRegionCode resolved to a name, with the same
// UNKNOWN(n) form the radio-setup endpoint has always reported.
func LoraRegionName(config map[string]interface{}) string {
	code := LoraRegionCode(config)
	if name, ok := LoraRegionNames[code]; ok {
		return name
	}
	return fmt.Sprintf("UNKNOWN(%d)", code)
}
