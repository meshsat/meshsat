package reticulum

import "crypto/sha256"

// PathRequestDestName is the PLAIN destination every RNS transport node
// listens on for path requests.
const PathRequestDestName = "rnstransport.path.request"

// ComputePlainDestHash returns the hash of a PLAIN destination:
// SHA-256(name_hash)[:16], with no identity involved.
// Reference: RNS/Destination.py hash() for type PLAIN.
func ComputePlainDestHash(name string) [TruncatedHashLen]byte {
	nameHash := ComputeNameHash(name)
	sum := sha256.Sum256(nameHash[:])
	var out [TruncatedHashLen]byte
	copy(out[:], sum[:TruncatedHashLen])
	return out
}

var pathRequestDest = ComputePlainDestHash(PathRequestDestName)

// PathRequestDestHash is the destination hash path requests are sent to.
func PathRequestDestHash() [TruncatedHashLen]byte { return pathRequestDest }
