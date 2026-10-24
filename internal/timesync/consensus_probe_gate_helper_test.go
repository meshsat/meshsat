package timesync

// probeGate sets a probe taker and whether media hopping takes probes.
func probeGate(mc *MeshTimeConsensus, on bool) {
	mc.SetProbeFunc(func(string, uint64) {})
	mc.SetProbeSession(func() (uint64, bool) { return 0, on })
}
