package rnode

// Preset is a regional LoRa starting point. The four below are copied from
// CrossTalk's src/frontend/js/RNodePresets.js (buildwithparallel/crosstalk,
// MIT), so a kit and his radios start on identical parameters. They are
// community starters, not legal requirements; peers must match frequency,
// bandwidth, spreading factor and coding rate exactly.
type Preset struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Params
}

// Presets in the order CrossTalk lists them.
var Presets = []Preset{
	{ID: "us-915", Label: "US 915 MHz (starter)", Description: "Common US ISM starting point around 915 MHz. Match your mesh if it uses a different center frequency.",
		Params: Params{Frequency: 915_000_000, Bandwidth: 125_000, SF: 7, CR: 5, TXPower: 22}},
	{ID: "eu-868", Label: "EU 868 MHz (starter)", Description: "Common EU ISM starting point at 867.2 MHz from Reticulum examples. Confirm local rules and mesh settings.",
		Params: Params{Frequency: 867_200_000, Bandwidth: 125_000, SF: 8, CR: 5, TXPower: 14}},
	{ID: "au-915", Label: "AU/NZ 915 MHz (starter)", Description: "Common starting point in the AU/NZ 915 MHz ISM band. Confirm local rules and mesh settings.",
		Params: Params{Frequency: 915_000_000, Bandwidth: 125_000, SF: 7, CR: 5, TXPower: 22}},
	{ID: "ism-433", Label: "433 MHz (starter)", Description: "For 433 MHz-capable RNodes where that band is allowed. Not all Heltec boards support this.",
		Params: Params{Frequency: 433_000_000, Bandwidth: 125_000, SF: 7, CR: 5, TXPower: 12}},
}

// PresetByID returns a preset or nil.
func PresetByID(id string) *Preset {
	for i := range Presets {
		if Presets[i].ID == id {
			return &Presets[i]
		}
	}
	return nil
}
