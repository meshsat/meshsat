package transport

import (
	"context"
	"fmt"
	"sync"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
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

// radioPortInterval is the radio's own throttle on what a client hands it
// (Meshtastic firmware 2.8.0, PhoneAPI::handleToRadio): a packet on one of
// these ports that comes within the interval after the previous one on the
// same port is dropped and never sent; for a text the radio answers only with
// a queue status and a RATE_LIMIT_EXCEEDED routing error. On parallax on
// 24 Oct 2026 (16:45:39Z, 16:50:01Z) the second of two OOB reply chunks
// written back to back was dropped this way ("Rate limit portnum 1"). Package
// var so tests can shorten it. [MESHSAT-1682]
var radioPortInterval = map[int32]time.Duration{
	int32(pb.PortNum_TEXT_MESSAGE_APP): 2 * time.Second,
	int32(pb.PortNum_POSITION_APP):     10 * time.Second,
	int32(pb.PortNum_WAYPOINT_APP):     10 * time.Second,
	int32(pb.PortNum_ALERT_APP):        10 * time.Second,
	int32(pb.PortNum_TELEMETRY_APP):    10 * time.Second,
	int32(pb.PortNum_TRACEROUTE_APP):   30 * time.Second,
}

// radioPortMargin is added to every interval: the radio starts its interval
// when it takes a packet, a little after our write. [MESHSAT-1682]
var radioPortMargin = 300 * time.Millisecond

// radioPortMaxWait is the longest a send waits for its port's interval. A text
// (2 s) always waits, as the delivery queues expect; a position, waypoint or
// traceroute (10 s, 30 s) comes from an API call, which is answered at once with
// ErrRadioPortBusy and when the next one can go instead of hanging. [MESHSAT-1682]
var radioPortMaxWait = 3 * time.Second

// ErrRadioPortBusy is a packet refused before it was written because the radio
// would drop it: its port's interval has not passed. It wraps ErrNotSubmitted.
var ErrRadioPortBusy = fmt.Errorf("%w: the radio's interval for this port has not passed", ErrNotSubmitted)

// portPacer spaces the packets a radio throttles, each port on its own
// interval, so a text never waits behind a position. The zero value is ready.
type portPacer struct {
	mu    sync.Mutex
	slots map[int32]*portSlot
}

type portSlot struct {
	sync.Mutex
	last time.Time
}

func (p *portPacer) slot(port int32) *portSlot {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.slots == nil {
		p.slots = map[int32]*portSlot{}
	}
	s, ok := p.slots[port]
	if !ok {
		s = &portSlot{}
		p.slots[port] = s
	}
	return s
}

// send runs send for a packet on port no sooner than the radio's interval for
// that port after the previous packet on it, waiting while ctx allows; a port
// the radio does not throttle goes at once. A wait longer than radioPortMaxWait
// is refused at once with ErrRadioPortBusy, a caller that gives up while
// waiting gets ErrNotSubmitted, and a send that fails does not start the
// interval, since the radio never had the packet.
func (p *portPacer) send(ctx context.Context, port int32, send func() error) error {
	iv, ok := radioPortInterval[port]
	if !ok {
		return send()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s := p.slot(port)
	s.Lock()
	defer s.Unlock()
	if !s.last.IsZero() {
		if wait := iv + radioPortMargin - time.Since(s.last); wait > 0 {
			if wait > radioPortMaxWait {
				return fmt.Errorf("%w: the radio takes one packet on port %d every %v; the next can go in %v",
					ErrRadioPortBusy, port, iv, wait.Round(100*time.Millisecond))
			}
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return fmt.Errorf("%w: waiting out the radio's interval on port %d: %v", ErrNotSubmitted, port, ctx.Err())
			case <-timer.C:
			}
		}
	}
	if err := send(); err != nil {
		return err
	}
	s.last = time.Now()
	return nil
}
