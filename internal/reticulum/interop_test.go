package reticulum_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"meshsat/internal/reticulum"
)

// rnsResult holds the JSON output from the Python RNS validation script.
type rnsResult struct {
	Valid       bool   `json:"valid"`
	Error       string `json:"error,omitempty"`
	DestHash    string `json:"dest_hash,omitempty"`
	NameHash    string `json:"name_hash,omitempty"`
	IdentityHex string `json:"identity_hex,omitempty"`
	PublicKey   string `json:"public_key,omitempty"`
	Signature   string `json:"signature,omitempty"`
	Hops        int    `json:"hops,omitempty"`
	AppData     string `json:"app_data,omitempty"`

	// For generate mode
	AnnounceHex string `json:"announce_hex,omitempty"`
}

const rnsValidateScript = `interop_rns_validate.py`
const rnsGenerateScript = `interop_rns_generate.py`

func findPython() string {
	// Try the venv we created for testing
	if _, err := os.Stat("/tmp/rns-test/bin/python3"); err == nil {
		return "/tmp/rns-test/bin/python3"
	}
	return ""
}

// TestAnnounceWireFormatMatchesRNS generates a bridge announce and validates
// it using the Python RNS library (cross-implementation verification).
func TestAnnounceWireFormatMatchesRNS(t *testing.T) {
	python := findPython()
	if python == "" {
		t.Skip("Python RNS not available (install: python3 -m venv /tmp/rns-test && /tmp/rns-test/bin/pip install rns)")
	}

	scriptPath := findScript(t, rnsValidateScript)

	// Generate a bridge announce
	id, err := reticulum.GenerateIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}

	appName := "test.interop"
	appData := []byte("hello from bridge")

	ann, err := reticulum.NewAnnounce(id, appName, appData)
	if err != nil {
		t.Fatalf("new announce: %v", err)
	}

	// Marshal to full packet (header + payload)
	packet := ann.MarshalPacket()
	packetHex := hex.EncodeToString(packet)

	// Validate with Python RNS
	cmd := exec.Command(python, scriptPath, packetHex, appName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python validation failed: %v\noutput: %s", err, string(out))
	}

	var result rnsResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("parse python output: %v\nraw: %s", err, string(out))
	}

	if !result.Valid {
		t.Fatalf("RNS rejected bridge announce: %s", result.Error)
	}

	// Verify dest hash matches
	expectedDestHash := hex.EncodeToString(ann.DestHash[:])
	if result.DestHash != expectedDestHash {
		t.Errorf("dest hash mismatch: bridge=%s rns=%s", expectedDestHash, result.DestHash)
	}

	t.Logf("Bridge announce validated by RNS: dest_hash=%s hops=%d", result.DestHash, result.Hops)
}

// TestRNSAnnounceReadByBridge generates an announce using Python RNS and
// verifies the bridge can parse and validate it.
func TestRNSAnnounceReadByBridge(t *testing.T) {
	python := findPython()
	if python == "" {
		t.Skip("Python RNS not available")
	}

	scriptPath := findScript(t, rnsGenerateScript)

	appName := "test.interop"
	appData := "hello from rnsd"

	// Generate announce with Python RNS
	cmd := exec.Command(python, scriptPath, appName, appData)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python generate failed: %v\noutput: %s", err, string(out))
	}

	var result rnsResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("parse python output: %v\nraw: %s", err, string(out))
	}

	if result.AnnounceHex == "" {
		t.Fatalf("python did not return announce hex: %+v", result)
	}

	// Parse with bridge
	packetBytes, err := hex.DecodeString(result.AnnounceHex)
	if err != nil {
		t.Fatalf("decode announce hex: %v", err)
	}

	ann, err := reticulum.UnmarshalAnnouncePacket(packetBytes)
	if err != nil {
		t.Fatalf("bridge failed to parse RNS announce: %v", err)
	}

	// Verify the announce
	if err := ann.Verify(); err != nil {
		t.Fatalf("bridge failed to verify RNS announce: %v", err)
	}

	// Check dest hash matches
	bridgeDestHash := hex.EncodeToString(ann.DestHash[:])
	if bridgeDestHash != result.DestHash {
		t.Errorf("dest hash mismatch: rns=%s bridge=%s", result.DestHash, bridgeDestHash)
	}

	// Check app data
	if !bytes.Equal(ann.AppData, []byte(appData)) {
		t.Errorf("app data mismatch: expected=%q got=%q", appData, string(ann.AppData))
	}

	t.Logf("RNS announce parsed and verified by bridge: dest_hash=%s", bridgeDestHash)
}

// TestHDLCFramingMatchesRNS verifies HDLC framing is compatible.
func TestHDLCFramingMatchesRNS(t *testing.T) {
	python := findPython()
	if python == "" {
		t.Skip("Python RNS not available")
	}

	// Test vectors that exercise all escape conditions
	testCases := []struct {
		name string
		data []byte
	}{
		{"no_escapes", []byte{0x01, 0x02, 0x03, 0x04}},
		{"contains_flag", []byte{0x01, 0x7E, 0x03}},
		{"contains_esc", []byte{0x01, 0x7D, 0x03}},
		{"both_special", []byte{0x7E, 0x7D, 0x7E}},
		{"all_flags", []byte{0x7E, 0x7E, 0x7E}},
		{"all_esc", []byte{0x7D, 0x7D, 0x7D}},
		{"empty_payload_19b", make([]byte, 19)}, // minimum Reticulum header size
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Bridge escape
			escaped := reticulum.HDLCEscape(tc.data)
			framed := reticulum.HDLCFrame(tc.data)

			// Verify framing: [FLAG][escaped][FLAG]
			if framed[0] != 0x7E || framed[len(framed)-1] != 0x7E {
				t.Errorf("framed data missing FLAG delimiters")
			}

			// Verify unescape roundtrip
			unescaped := reticulum.HDLCUnescape(escaped)
			if !bytes.Equal(unescaped, tc.data) {
				t.Errorf("unescape roundtrip failed: got %x, want %x", unescaped, tc.data)
			}

			// Cross-validate with Python
			dataHex := hex.EncodeToString(tc.data)
			escapedHex := hex.EncodeToString(escaped)

			script := `
import sys, json
sys.path.insert(0, '/tmp/rns-test/lib/python3.11/site-packages')
from RNS.Interfaces.TCPInterface import HDLC
data = bytes.fromhex(sys.argv[1])
escaped = HDLC.escape(data)
print(json.dumps({"escaped": escaped.hex()}))
`
			cmd := exec.Command(python, "-c", script, dataHex)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("python HDLC escape failed: %v\noutput: %s", err, string(out))
			}

			var pyResult struct {
				Escaped string `json:"escaped"`
			}
			if err := json.Unmarshal(out, &pyResult); err != nil {
				t.Fatalf("parse python output: %v\nraw: %s", err, string(out))
			}

			if pyResult.Escaped != escapedHex {
				t.Errorf("HDLC escape mismatch:\n  bridge: %s\n  python: %s", escapedHex, pyResult.Escaped)
			}
		})
	}
}

// TestDestHashComputationMatchesRNS verifies destination hash is computed identically.
func TestDestHashComputationMatchesRNS(t *testing.T) {
	python := findPython()
	if python == "" {
		t.Skip("Python RNS not available")
	}

	id, err := reticulum.GenerateIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}

	appName := "test.desthash"
	pubKeyHex := hex.EncodeToString(id.PublicBytes())
	destHash := id.DestHash(appName)
	bridgeDestHash := hex.EncodeToString(destHash[:])

	script := `
import sys, json, hashlib
sys.path.insert(0, '/tmp/rns-test/lib/python3.11/site-packages')
pub_key = bytes.fromhex(sys.argv[1])
app_name = sys.argv[2]

# RNS computation
name_hash = hashlib.sha256(app_name.encode("utf-8")).digest()[:10]
identity_hash = hashlib.sha256(pub_key).digest()[:16]
dest_hash = hashlib.sha256(name_hash + identity_hash).digest()[:16]
print(json.dumps({"dest_hash": dest_hash.hex(), "name_hash": name_hash.hex(), "identity_hash": identity_hash.hex()}))
`
	cmd := exec.Command(python, "-c", script, pubKeyHex, appName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python dest hash failed: %v\noutput: %s", err, string(out))
	}

	var pyResult struct {
		DestHash     string `json:"dest_hash"`
		NameHash     string `json:"name_hash"`
		IdentityHash string `json:"identity_hash"`
	}
	if err := json.Unmarshal(out, &pyResult); err != nil {
		t.Fatalf("parse python output: %v\nraw: %s", err, string(out))
	}

	if pyResult.DestHash != bridgeDestHash {
		t.Errorf("dest hash mismatch:\n  bridge: %s\n  python: %s", bridgeDestHash, pyResult.DestHash)
	}

	t.Logf("Dest hash match: %s (app=%s)", bridgeDestHash, appName)
}

// TestPacketHeaderFlagsMatchRNS verifies the flags byte layout.
func TestPacketHeaderFlagsMatchRNS(t *testing.T) {
	python := findPython()
	if python == "" {
		t.Skip("Python RNS not available")
	}

	testCases := []struct {
		headerType    byte
		contextFlag   byte
		transportType byte
		destType      byte
		packetType    byte
	}{
		{0, 0, 0, 0, 0x01}, // Simple announce
		{0, 1, 0, 0, 0x01}, // Announce with ratchet
		{1, 0, 1, 0, 0x00}, // Transport data
		{0, 0, 0, 3, 0x00}, // Link data
		{0, 0, 0, 0, 0x02}, // Link request
		{0, 0, 0, 3, 0x03}, // Proof on link
	}

	for _, tc := range testCases {
		h := reticulum.Header{
			HeaderType:    tc.headerType,
			ContextFlag:   tc.contextFlag,
			TransportType: tc.transportType,
			DestType:      tc.destType,
			PacketType:    tc.packetType,
		}
		goFlags := h.PackFlags()

		script := `
import sys, json
ht, cf, tt, dt, pt = int(sys.argv[1]), int(sys.argv[2]), int(sys.argv[3]), int(sys.argv[4]), int(sys.argv[5])
flags = (ht << 6) | (cf << 5) | (tt << 4) | (dt << 2) | pt
print(json.dumps({"flags": flags}))
`
		cmd := exec.Command(python, "-c", script,
			itoa(tc.headerType), itoa(tc.contextFlag), itoa(tc.transportType),
			itoa(tc.destType), itoa(tc.packetType))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("python flags failed: %v", err)
		}

		var pyResult struct {
			Flags int `json:"flags"`
		}
		if err := json.Unmarshal(out, &pyResult); err != nil {
			t.Fatalf("parse: %v", err)
		}

		if byte(pyResult.Flags) != goFlags {
			t.Errorf("flags mismatch for ht=%d cf=%d tt=%d dt=%d pt=%d: bridge=0x%02x python=0x%02x",
				tc.headerType, tc.contextFlag, tc.transportType, tc.destType, tc.packetType,
				goFlags, byte(pyResult.Flags))
		}
	}
}

func itoa(b byte) string {
	return string([]byte{'0' + b})
}

func findScript(t *testing.T, name string) string {
	// Scripts are in the same directory as the test
	paths := []string{
		"testdata/" + name,
		"../reticulum/testdata/" + name,
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skipf("interop script %s not found", name)
	return ""
}
