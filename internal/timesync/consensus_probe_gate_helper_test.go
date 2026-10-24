package timesync

// probeGate sets a probe taker and whether media hopping takes probes.
func probeGate(mc *MeshTimeConsensus, on bool) {
	mc.SetProbeFunc(func(string) {})
	mc.SetProbeEnabled(func() bool { return on })
}
