package engine

import (
	"os"
	"strconv"

	"github.com/rs/zerolog/log"
)

// meshBearerMTU is the payload size a text may take on the mesh link, used
// by the delivery intents and the Escalator. Defaults to 100 B, the
// empirically verified SF7-LongFast ceiling below which Meshtastic reliably
// delivers PRIVATE_APP frames on the tesseract and parallax field kits.
// Operators on other LoRa presets (SHORT_FAST, MEDIUM_*, LONG_SLOW) or other
// antennas can override it with MESHSAT_MESH_MTU in the range [1, 237];
// values outside the range fall back to 100. It was the HeMB bond's mesh
// symbol size until HeMB v1 was removed. [MESHSAT-672, MESHSAT-1763]
func meshBearerMTU() int {
	if v := os.Getenv("MESHSAT_MESH_MTU"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 237 {
			return n
		}
		log.Warn().Str("MESHSAT_MESH_MTU", v).
			Msg("invalid MESHSAT_MESH_MTU, expected an integer in 1..237, using 100")
	}
	return 100
}
