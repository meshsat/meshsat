package compress

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Codebook holds the MSVQ-SC codebook vectors and corpus index for
// pure-Go decoding without the sidecar. This enables decode on the
// receiver side without any Python/ONNX dependencies.
type Codebook struct {
	Version  uint8
	Stages   int
	K        int
	Dim      int
	Vectors  [][][]float32 // [stage][entry][dim]
	Corpus   []string      // known sentences
	CorpusEm [][]float32   // their embeddings [N][dim]
}

// Wire format constants
const (
	wireHeaderSize = 1 // 1 byte: 4-bit stages + 4-bit version
	wireIndexSize  = 2 // 2 bytes per stage index (uint16 LE)
	wireVersion    = 1

	codebookMagic    = "MSVQ"
	corpusIndexMagic = "MCIX"
)

// LoadCodebook parses a codebook binary file.
//
// Format:
//
//	4B magic "MSVQ"
//	1B version
//	1B stages
//	2B K (uint16 LE)
//	2B dim (uint16 LE)
//	stages * K * dim float32 values (LE)
func LoadCodebook(data []byte) (*Codebook, error) {
	if len(data) < 10 {
		return nil, fmt.Errorf("msvqsc codebook: data too short (%d bytes)", len(data))
	}
	if string(data[:4]) != codebookMagic {
		return nil, fmt.Errorf("msvqsc codebook: invalid magic %q", data[:4])
	}

	version := data[4]
	stages := int(data[5])
	k := int(binary.LittleEndian.Uint16(data[6:8]))
	dim := int(binary.LittleEndian.Uint16(data[8:10]))

	expectedFloats := stages * k * dim
	expectedBytes := 10 + expectedFloats*4
	if len(data) < expectedBytes {
		return nil, fmt.Errorf("msvqsc codebook: need %d bytes, got %d", expectedBytes, len(data))
	}

	vectors := make([][][]float32, stages)
	offset := 10
	for s := 0; s < stages; s++ {
		vectors[s] = make([][]float32, k)
		for e := 0; e < k; e++ {
			vectors[s][e] = make([]float32, dim)
			for d := 0; d < dim; d++ {
				vectors[s][e][d] = math.Float32frombits(binary.LittleEndian.Uint32(data[offset:]))
				offset += 4
			}
		}
	}

	return &Codebook{
		Version: version,
		Stages:  stages,
		K:       k,
		Dim:     dim,
		Vectors: vectors,
	}, nil
}

// LoadCorpusIndex parses a corpus index binary file and attaches it to the codebook.
//
// Format:
//
//	4B magic "MCIX"
//	1B version
//	4B num_entries (uint32 LE)
//	2B dim (uint16 LE)
//	For each entry:
//	  2B text_len (uint16 LE)
//	  text_len bytes UTF-8 text
//	  dim * 4B float32 embedding
func (cb *Codebook) LoadCorpusIndex(data []byte) error {
	if len(data) < 11 {
		return fmt.Errorf("msvqsc corpus: data too short (%d bytes)", len(data))
	}
	if string(data[:4]) != corpusIndexMagic {
		return fmt.Errorf("msvqsc corpus: invalid magic %q", data[:4])
	}

	// version at data[4]
	numEntries := int(binary.LittleEndian.Uint32(data[5:9]))
	dim := int(binary.LittleEndian.Uint16(data[9:11]))

	if dim != cb.Dim {
		return fmt.Errorf("msvqsc corpus: dim mismatch (codebook=%d, corpus=%d)", cb.Dim, dim)
	}

	cb.Corpus = make([]string, 0, numEntries)
	cb.CorpusEm = make([][]float32, 0, numEntries)

	offset := 11
	for i := 0; i < numEntries; i++ {
		if offset+2 > len(data) {
			return fmt.Errorf("msvqsc corpus: truncated at entry %d", i)
		}
		textLen := int(binary.LittleEndian.Uint16(data[offset:]))
		offset += 2

		if offset+textLen > len(data) {
			return fmt.Errorf("msvqsc corpus: truncated text at entry %d", i)
		}
		text := string(data[offset : offset+textLen])
		offset += textLen

		embBytes := dim * 4
		if offset+embBytes > len(data) {
			return fmt.Errorf("msvqsc corpus: truncated embedding at entry %d", i)
		}
		emb := make([]float32, dim)
		for d := 0; d < dim; d++ {
			emb[d] = math.Float32frombits(binary.LittleEndian.Uint32(data[offset:]))
			offset += 4
		}

		cb.Corpus = append(cb.Corpus, text)
		cb.CorpusEm = append(cb.CorpusEm, emb)
	}

	return nil
}

// DecodeIndices reconstructs approximate text from MSVQ-SC wire format.
// This works without the sidecar — pure Go codebook lookup + nearest-neighbor.
func (cb *Codebook) DecodeIndices(wire []byte) (string, error) {
	if len(wire) < wireHeaderSize {
		return "", fmt.Errorf("msvqsc decode: wire data too short")
	}

	header := wire[0]
	stages := int((header >> 4) & 0x0F)
	version := int(header & 0x0F)

	if version != wireVersion {
		return "", fmt.Errorf("msvqsc decode: unsupported wire version %d", version)
	}

	expectedLen := wireHeaderSize + stages*wireIndexSize
	if len(wire) < expectedLen {
		return "", fmt.Errorf("msvqsc decode: need %d bytes, got %d", expectedLen, len(wire))
	}

	// Extract indices
	indices := make([]int, stages)
	for s := 0; s < stages; s++ {
		offset := wireHeaderSize + s*wireIndexSize
		indices[s] = int(binary.LittleEndian.Uint16(wire[offset:]))
	}

	// Reconstruct embedding: sum codebook vectors at each stage
	reconstructed := make([]float32, cb.Dim)
	for s, idx := range indices {
		if s >= cb.Stages {
			break
		}
		if idx >= cb.K {
			return "", fmt.Errorf("msvqsc decode: index %d out of range (K=%d) at stage %d", idx, cb.K, s)
		}
		for d := 0; d < cb.Dim; d++ {
			reconstructed[d] += cb.Vectors[s][idx][d]
		}
	}

	// Find nearest corpus entry by cosine similarity
	if len(cb.Corpus) == 0 {
		return "", fmt.Errorf("msvqsc decode: no corpus loaded")
	}

	reconNorm := vecNorm(reconstructed)
	bestIdx := 0
	bestSim := float32(-1)

	for i, emb := range cb.CorpusEm {
		sim := vecDot(reconstructed, emb) / (reconNorm * vecNorm(emb))
		if sim > bestSim {
			bestSim = sim
			bestIdx = i
		}
	}

	return cb.Corpus[bestIdx], nil
}

// WireSize returns the wire overhead in bytes for the given number of stages.
func WireSize(stages int) int {
	return wireHeaderSize + stages*wireIndexSize
}

// SuggestStages returns the recommended number of VQ stages for a channel type.
// More stages = higher fidelity but larger wire overhead.
func SuggestStages(channelType string) int {
	switch channelType {
	case "zigbee":
		return 2
	case "cellular", "astrocast":
		return 3
	case "mesh":
		return 4
	case "iridium":
		return 6
	default:
		// webhook, mqtt, or unlimited
		return 8
	}
}

// vecDot computes the dot product of two float32 vectors.
func vecDot(a, b []float32) float32 {
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

// vecNorm computes the L2 norm of a float32 vector.
func vecNorm(v []float32) float32 {
	return float32(math.Sqrt(float64(vecDot(v, v))))
}
