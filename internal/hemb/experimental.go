package hemb

import (
	"errors"
	"os"
	"strings"
)

// Experimental reports whether HeMB may send or receive at all. HeMB v1 is
// experimental and opt-in: with MESHSAT_HEMB_EXPERIMENTAL unset (or not
// true), no entry point sends a bonded frame and no receive bonder or TUN
// adapter starts, whatever bond groups the database holds. Read on every
// call. [MESHSAT-1581]
func Experimental() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MESHSAT_HEMB_EXPERIMENTAL"))) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// ErrExperimentalOff is what a HeMB send refuses with while Experimental()
// is false: nothing was sent.
var ErrExperimentalOff = errors.New("hemb: experimental and off (set MESHSAT_HEMB_EXPERIMENTAL=true to enable)")
