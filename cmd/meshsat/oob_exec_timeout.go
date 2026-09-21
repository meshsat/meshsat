package main

import "time"

// oobMargin is how much longer than the mesh config handshake an OOB command
// may run: the handshake is the slowest thing a level-1 mesh reset waits for.
const oobMargin = 30 * time.Second

// oobExecTimeout returns the OOB execution deadline for a mesh handshake
// timeout: the handshake plus oobMargin, and never below min. [MESHSAT-810]
func oobExecTimeout(handshake, min time.Duration) time.Duration {
	if d := handshake + oobMargin; d > min {
		return d
	}
	return min
}
