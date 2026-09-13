package transport

import (
	"sync"
	"time"
)

// meshWriteGap is the pause the official Meshtastic Python client leaves after
// every write to the radio (stream_interface.py, _writeBytes sleeps 0.1 s). The
// kit radios run TinyUSB on arduino-esp32 2.0.17, whose CDC write path spins
// with no timeout while the host is not draining it, and a stalled main loop
// ends in the firmware's 90 s task watchdog. The bridge wrote admin frames back
// to back with no gap at all. Package var so tests can shorten it.
// [MESHSAT-850]
var meshWriteGap = 100 * time.Millisecond

// meshDisconnectDwell is how long Close waits after the ToRadio disconnect
// before it closes the port, as the official client does. [MESHSAT-850]
var meshDisconnectDwell = 100 * time.Millisecond

var meshWritePace struct {
	sync.Mutex
	last time.Time
}

// pacedMeshWrite runs write no sooner than meshWriteGap after the previous
// frame written to a Meshtastic radio.
func pacedMeshWrite(write func() error) error {
	meshWritePace.Lock()
	defer meshWritePace.Unlock()
	if wait := meshWriteGap - time.Since(meshWritePace.last); wait > 0 {
		time.Sleep(wait)
	}
	err := write()
	meshWritePace.last = time.Now()
	return err
}
