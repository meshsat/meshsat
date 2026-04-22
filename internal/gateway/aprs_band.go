package gateway

// IsLikelyAmateurBand returns true when the given frequency in MHz falls
// inside a well-known amateur-radio allocation in any of the three ITU
// regions. The mapping is deliberately rough — it is used only to emit
// a single one-shot warning when the operator enables end-to-end
// encryption on a frequency that is probably on a ham band, where
// encrypted content is commonly prohibited by licence terms.
//
// It is NOT authoritative: band edges vary by ITU region and national
// regulator, and exemptions exist (e.g. satellite downlinks, emergency
// traffic, specific experimental authorisations). The bridge never
// refuses to transmit on the basis of this function — the operator is
// responsible for compliance with their own licence and jurisdiction.
//
// Ranges included (widest common extent across ITU regions 1/2/3):
//
//	  1.8–  2.0  MHz  160 m
//	  3.5–  4.0  MHz   80 m
//	  5.3–  5.4  MHz   60 m
//	  7.0–  7.3  MHz   40 m
//	 10.1– 10.15 MHz   30 m
//	 14.0– 14.35 MHz   20 m
//	 18.068–18.168 MHz 17 m
//	 21.0– 21.45 MHz   15 m
//	 24.89–24.99 MHz   12 m
//	 28.0– 29.7  MHz   10 m
//	 50.0– 54.0  MHz    6 m
//	144.0–148.0  MHz    2 m  (APRS 144.390 / 144.800 live here)
//	220.0–225.0  MHz  1.25 m
//	420.0–450.0  MHz   70 cm
//	902.0–928.0  MHz   33 cm (region 2 only; shared with ISM in region 1)
//	1240.0–1300.0 MHz  23 cm
func IsLikelyAmateurBand(mhz float64) bool {
	ranges := [...][2]float64{
		{1.8, 2.0},
		{3.5, 4.0},
		{5.3, 5.4},
		{7.0, 7.3},
		{10.1, 10.15},
		{14.0, 14.35},
		{18.068, 18.168},
		{21.0, 21.45},
		{24.89, 24.99},
		{28.0, 29.7},
		{50.0, 54.0},
		{144.0, 148.0},
		{220.0, 225.0},
		{420.0, 450.0},
		{902.0, 928.0},
		{1240.0, 1300.0},
	}
	for _, r := range ranges {
		if mhz >= r[0] && mhz <= r[1] {
			return true
		}
	}
	return false
}
