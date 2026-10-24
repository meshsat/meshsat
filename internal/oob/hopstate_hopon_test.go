package oob

// setHopOn wires the media hopping switch into a harness, in interval 0.
func setHopOn(h *harness, f func() bool) {
	h.svc.d.HopSession = func() (uint64, bool) { return 0, f() }
}
