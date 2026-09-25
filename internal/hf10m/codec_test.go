package hf10m

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/rand"
	"os"
	"testing"
)

type reference struct {
	N0Call    string `json:"n0call"`
	Frame     string `json:"frame"`
	CRC       string `json:"crc"`
	NBlocks   int    `json:"n_blocks"`
	Burst     string `json:"burst"`
	BurstLen  int    `json:"burst_len"`
	Codewords []struct {
		Info     string `json:"info"`
		Codeword string `json:"codeword"`
	} `json:"codewords"`
}

func loadRef(t *testing.T) reference {
	t.Helper()
	raw, err := os.ReadFile("testdata/reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var r reference
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The recipe's own numbers: N0CALL -> 57 7b 2a 46 f8 00, the 71-byte example
// frame with CRC 85 98, 9 blocks, 179 bytes on the air after the Costas tones.
func TestWorkedExample(t *testing.T) {
	ref := loadRef(t)
	call, err := PackCallsign("N0CALL")
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(call[:]) != "577b2a46f800" || ref.N0Call != "577b2a46f800" {
		t.Fatalf("radix-40: %x / ref %s", call, ref.N0Call)
	}
	if UnpackCallsign(call) != "N0CALL" {
		t.Fatalf("unpack: %q", UnpackCallsign(call))
	}
	var dest [16]byte
	copy(dest[:], mustHex(t, "0123456789abcdef0123456789abcdef"))
	f := &Frame{Origin: "N0CALL", Dest: dest, MsgID: 42, FragIndex: 0, FragTotal: 1, Payload: []byte("no internet here. all ok. next check 0900")}
	packed, err := f.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	want := mustHex(t, "1000577b2a46f8000123456789abcdef0123456789abcdef002a01296e6f20696e7465726e657420686572652e20616c6c206f6b2e206e65787420636865636b20303930308598")
	if !bytes.Equal(packed, want) {
		t.Fatalf("frame\n got %x\nwant %x", packed, want)
	}
	if hex.EncodeToString(packed) != ref.Frame || len(packed) != 71 || hex.EncodeToString(packed[69:]) != "8598" {
		t.Fatalf("frame vs reference: %d bytes, crc %x", len(packed), packed[69:])
	}
	back, err := Unmarshal(packed)
	if err != nil {
		t.Fatal(err)
	}
	if back.Origin != "N0CALL" || back.MsgID != 42 || back.FragTotal != 1 || string(back.Payload) != string(f.Payload) || back.Dest != dest {
		t.Fatalf("round trip: %+v", back)
	}
	burst, n, err := BuildBurst(packed)
	if err != nil {
		t.Fatal(err)
	}
	if n != 9 || len(burst) != 179 || ref.NBlocks != 9 || ref.BurstLen != 179 {
		t.Fatalf("burst: %d blocks, %d bytes", n, len(burst))
	}
	if hex.EncodeToString(burst) != ref.Burst {
		t.Fatalf("burst differs from the NumPy reference\n got %s\nwant %s", hex.EncodeToString(burst), ref.Burst)
	}
	// And back: parse from the unique word.
	got, err := ParseBurst(burst[len(Preamble):])
	if err != nil {
		t.Fatal(err)
	}
	if got.Origin != "N0CALL" || string(got.Payload) != string(f.Payload) {
		t.Fatalf("parsed %+v", got)
	}
	// Corrupt the CRC: dropped.
	bad := append([]byte(nil), packed...)
	bad[70] ^= 1
	if _, err := Unmarshal(bad); err != ErrCRC {
		t.Fatalf("bad crc: %v", err)
	}
}

func TestLDPCCodewordsMatchReference(t *testing.T) {
	ref := loadRef(t)
	for i, c := range ref.Codewords {
		info := bytesToBits(mustHex(t, c.Info))
		cw := Encode(info)
		if hex.EncodeToString(bitsToBytes(cw[:])) != c.Codeword {
			t.Fatalf("codeword %d differs from NumPy", i)
		}
		if !Syndrome(cw[:]) {
			t.Fatalf("codeword %d fails its own checks", i)
		}
	}
}

func TestLDPCEncodeSyndromeAndDecode(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	info := make([]uint8, LDPCK)
	for trial := 0; trial < 1000; trial++ {
		for i := range info {
			info[i] = uint8(rng.Intn(2))
		}
		cw := Encode(info)
		if !Syndrome(cw[:]) {
			t.Fatalf("trial %d: H x != 0", trial)
		}
		if got := InfoBits(cw[:]); !bytes.Equal(got, info) {
			t.Fatalf("trial %d: information bits not systematic", trial)
		}
	}
	// Flip up to 8 bits and decode.
	for flips := 1; flips <= 8; flips++ {
		ok := 0
		for trial := 0; trial < 50; trial++ {
			for i := range info {
				info[i] = uint8(rng.Intn(2))
			}
			cw := Encode(info)
			llr := make([]float64, LDPCN)
			for i, b := range cw {
				if b == 1 {
					llr[i] = -3
				} else {
					llr[i] = 3
				}
			}
			for _, pos := range rng.Perm(LDPCN)[:flips] {
				llr[pos] = -llr[pos]
			}
			dec, conv := Decode(llr, 40)
			if conv && bytes.Equal(InfoBits(dec[:]), info) {
				ok++
			}
		}
		t.Logf("%d flipped bits: %d/50 decoded", flips, ok)
		if flips <= 4 && ok < 48 {
			t.Fatalf("%d flipped bits: only %d/50 decoded", flips, ok)
		}
	}
}

func TestFragmentsAndReassembly(t *testing.T) {
	var dest [16]byte
	long := ""
	for len(long) < 450 {
		long += "een bericht van de kit, zonder internet, alles goed. "
	}
	frames, err := Fragment("PA0XYZ", dest, 7, long)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 {
		t.Fatalf("%d fragments", len(frames))
	}
	r := NewReassembler()
	var text string
	var done bool
	for _, i := range []int{2, 0, 1} {
		packed, err := frames[i].Marshal()
		if err != nil {
			t.Fatal(err)
		}
		f, err := Unmarshal(packed)
		if err != nil {
			t.Fatal(err)
		}
		text, done = r.Add(f)
	}
	if !done || text != long {
		t.Fatalf("reassembly failed: done=%v", done)
	}
	if _, err := PackCallsign("n0call!"); err == nil {
		t.Fatal("bad callsign accepted")
	}
	if _, err := (&Frame{Origin: "N0CALL", FragTotal: 1, Payload: make([]byte, 201)}).Marshal(); err == nil {
		t.Fatal("201-byte payload accepted")
	}
}

func TestMajorityBlockCount(t *testing.T) {
	if n, ok := MajorityBlockCount(9, 9, 0x41); !ok || n != 9 {
		t.Fatal("majority")
	}
	if n, ok := MajorityBlockCount(3, 9, 9); !ok || n != 9 {
		t.Fatal("majority 2")
	}
	if _, ok := MajorityBlockCount(1, 2, 3); ok {
		t.Fatal("no majority accepted")
	}
	if _, ok := MajorityBlockCount(0, 0, 0); ok {
		t.Fatal("zero accepted")
	}
}
