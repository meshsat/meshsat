package oob

// WindowSize is the width of the anti-replay window in counters.
const WindowSize = 64

// Window is an RFC 6479 style sliding anti-replay window, 64 counters wide.
// High is the highest counter accepted so far; bit i of Bitmap is set when
// counter High-i has been accepted. The zero value accepts counter 1 first.
// Counter 0 is never valid. One window per peer suffices because a peer's
// requests and replies share one transmit counter.
type Window struct {
	High   uint32
	Bitmap uint64
}

// Check reports whether ctr would be accepted. It does not mutate.
func (w *Window) Check(ctr uint32) bool {
	if ctr == 0 {
		return false
	}
	if ctr > w.High {
		return true
	}
	diff := w.High - ctr
	if diff >= WindowSize {
		return false
	}
	return w.Bitmap&(uint64(1)<<diff) == 0
}

// Mark records ctr as accepted. Call only after Open succeeded.
func (w *Window) Mark(ctr uint32) {
	if ctr == 0 {
		return
	}
	if ctr > w.High {
		shift := ctr - w.High
		if shift >= WindowSize {
			w.Bitmap = 0
		} else {
			w.Bitmap <<= shift
		}
		w.Bitmap |= 1
		w.High = ctr
		return
	}
	diff := w.High - ctr
	if diff < WindowSize {
		w.Bitmap |= uint64(1) << diff
	}
}

// MaxCounter is the last usable counter; a sender at this value refuses to
// send (KEY_EXHAUSTED) rather than wrap and reuse a nonce.
const MaxCounter uint32 = 0xFFFFFFFF
