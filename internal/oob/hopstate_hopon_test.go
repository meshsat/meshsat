package oob

// setHopOn wires the media hopping switch into a harness.
func setHopOn(h *harness, f func() bool) { h.svc.d.HopOn = f }
