package device

// ValidateIMEI checks that the IMEI is exactly 15 digits (numeric).
// Note: Luhn check is intentionally not enforced — satellite module IMEIs
// (e.g. RockBLOCK, Iridium 9603N) do not always have a valid Luhn check digit.
func ValidateIMEI(imei string) bool {
	if len(imei) != 15 {
		return false
	}
	for _, c := range imei {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
