package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/keystore"
	"meshsat/internal/routing"
)

// PUT/GET/DELETE /api/keys/{type}/{address}: the key of one chat, an SMS
// number with its + (escaped or not) or * for every number.

const (
	chatKeyA = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	chatKeyB = "FFEEDDCCBBAA99887766554433221100FFEEDDCCBBAA99887766554433221100"
)

func newChatKeyServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	s := newTestServerWithDB(t)
	id, err := routing.NewIdentity(s.db)
	if err != nil {
		t.Fatal(err)
	}
	ks, err := keystore.NewKeyStore(s.db, id, "chat-key-api-test")
	if err != nil {
		t.Fatal(err)
	}
	s.SetKeyStore(ks)
	return s, s.Router()
}

func chatKeyCall(t *testing.T, h http.Handler, method, path, body string) (int, map[string]interface{}) {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	out := map[string]interface{}{}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func TestChatKeyAPI_SetGetDelete(t *testing.T) {
	_, h := newChatKeyServer(t)

	// Nothing yet, with the + escaped or not.
	for _, p := range []string{"/api/keys/sms/%2B31612345678", "/api/keys/sms/+31612345678"} {
		if code, _ := chatKeyCall(t, h, "GET", p, ""); code != http.StatusNotFound {
			t.Fatalf("GET %s before any key: %d, want 404", p, code)
		}
	}

	code, out := chatKeyCall(t, h, "PUT", "/api/keys/sms/%2B31612345678", `{"key":"`+chatKeyA+`"}`)
	if code != http.StatusOK || out["status"] != "saved" || out["version"] != float64(1) || out["address"] != "+31612345678" || out["channel_type"] != "sms" {
		t.Fatalf("PUT: %d %v", code, out)
	}
	// The + in the path is the number's, escaped or not.
	for _, p := range []string{"/api/keys/sms/%2B31612345678", "/api/keys/sms/%2b31612345678", "/api/keys/sms/+31612345678"} {
		code, out := chatKeyCall(t, h, "GET", p, "")
		if code != http.StatusOK || out["key"] != chatKeyA || out["version"] != float64(1) || out["label"] != "" {
			t.Fatalf("GET %s: %d %v", p, code, out)
		}
	}
	// "31612345678" without its + is another chat.
	if code, _ := chatKeyCall(t, h, "GET", "/api/keys/sms/31612345678", ""); code != http.StatusNotFound {
		t.Fatalf("GET without the +: %d, want 404", code)
	}

	// Save tapped twice: nothing changes.
	code, out = chatKeyCall(t, h, "PUT", "/api/keys/sms/+31612345678", `{"key":"`+chatKeyA+`"}`)
	if code != http.StatusOK || out["status"] != "unchanged" || out["version"] != float64(1) {
		t.Fatalf("PUT the same key: %d %v", code, out)
	}
	// A new key (upper case is fine) is the next version, read back in lower case.
	code, out = chatKeyCall(t, h, "PUT", "/api/keys/sms/+31612345678", `{"key":"`+chatKeyB+`","label":"from Anna's phone"}`)
	if code != http.StatusOK || out["status"] != "saved" || out["version"] != float64(2) {
		t.Fatalf("PUT a new key: %d %v", code, out)
	}
	code, out = chatKeyCall(t, h, "GET", "/api/keys/sms/%2B31612345678", "")
	if code != http.StatusOK || out["key"] != strings.ToLower(chatKeyB) || out["version"] != float64(2) || out["label"] != "from Anna's phone" {
		t.Fatalf("GET after the new key: %d %v", code, out)
	}

	// DELETE keeps its path shape; the escaped + reaches the number.
	if code, out := chatKeyCall(t, h, "DELETE", "/api/keys/sms/%2B31612345678", ""); code != http.StatusOK || out["status"] != "revoked" {
		t.Fatalf("DELETE: %d %v", code, out)
	}
	if code, _ := chatKeyCall(t, h, "GET", "/api/keys/sms/+31612345678", ""); code != http.StatusNotFound {
		t.Fatalf("GET after DELETE: %d, want 404", code)
	}
	// Set again after a removal: a fresh version, readable.
	if code, out := chatKeyCall(t, h, "PUT", "/api/keys/sms/+31612345678", `{"key":"`+chatKeyA+`"}`); code != http.StatusOK || out["version"] != float64(3) {
		t.Fatalf("PUT after DELETE: %d %v", code, out)
	}
	if code, _ := chatKeyCall(t, h, "DELETE", "/api/keys/sms/+31612345678", ""); code != http.StatusOK {
		t.Fatalf("DELETE with a literal +: %d", code)
	}
	if code, _ := chatKeyCall(t, h, "GET", "/api/keys/sms/%2B31612345678", ""); code != http.StatusNotFound {
		t.Fatalf("GET after DELETE with a literal +: %d, want 404", code)
	}
}

func TestChatKeyAPI_WildcardAndCellular(t *testing.T) {
	_, h := newChatKeyServer(t)
	if code, out := chatKeyCall(t, h, "PUT", "/api/keys/sms/*", `{"key":"`+chatKeyA+`"}`); code != http.StatusOK || out["address"] != "*" {
		t.Fatalf("PUT sms:*: %d %v", code, out)
	}
	if code, out := chatKeyCall(t, h, "GET", "/api/keys/sms/%2A", ""); code != http.StatusOK || out["key"] != chatKeyA {
		t.Fatalf("GET sms:%%2A: %d %v", code, out)
	}
	// cellular is the sms key space.
	if code, out := chatKeyCall(t, h, "PUT", "/api/keys/cellular/%2B31600000001", `{"key":"`+chatKeyA+`"}`); code != http.StatusOK || out["channel_type"] != "sms" {
		t.Fatalf("PUT cellular: %d %v", code, out)
	}
	if code, _ := chatKeyCall(t, h, "GET", "/api/keys/sms/+31600000001", ""); code != http.StatusOK {
		t.Fatalf("GET sms after PUT cellular: %d", code)
	}
	if code, _ := chatKeyCall(t, h, "DELETE", "/api/keys/cellular/+31600000001", ""); code != http.StatusOK {
		t.Fatalf("DELETE cellular: %d", code)
	}
	if code, _ := chatKeyCall(t, h, "GET", "/api/keys/sms/+31600000001", ""); code != http.StatusNotFound {
		t.Fatalf("GET sms after DELETE cellular: %d, want 404", code)
	}
	// A mesh chat keeps a key too.
	if code, _ := chatKeyCall(t, h, "PUT", "/api/keys/mesh/!a1b2c3d4", `{"key":"`+chatKeyB+`","label":"node key"}`); code != http.StatusOK {
		t.Fatalf("PUT mesh: %d", code)
	}
	// The key list shows the label, never the key.
	code, out := chatKeyCall(t, h, "GET", "/api/keys", "")
	list := jsonString(t, out)
	if code != http.StatusOK || !strings.Contains(list, `"label":"node key"`) ||
		strings.Contains(strings.ToLower(list), chatKeyA[8:]) || strings.Contains(strings.ToLower(list), strings.ToLower(chatKeyB[8:])) {
		t.Fatalf("GET /api/keys: %d %s", code, list)
	}
}

func jsonString(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestChatKeyAPI_RejectsWhatIsNotAKey(t *testing.T) {
	_, h := newChatKeyServer(t)
	for _, body := range []string{
		`{"key":"0011"}`,
		`{"key":"` + chatKeyA[:63] + `"}`,
		`{"key":"` + chatKeyA + `0"}`,
		`{"key":"` + strings.Repeat("zz", 32) + `"}`,
		`{"key":" ` + chatKeyA[1:] + `"}`,
		`{"key":""}`,
		`{}`,
		`not json`,
		`{"key":"` + chatKeyA + `","label":"` + strings.Repeat("x", 65) + `"}`,
	} {
		if code, out := chatKeyCall(t, h, "PUT", "/api/keys/sms/%2B31612345678", body); code != http.StatusBadRequest {
			t.Fatalf("PUT %s: %d %v, want 400", body, code, out)
		}
	}
	if code, _ := chatKeyCall(t, h, "GET", "/api/keys/sms/+31612345678", ""); code != http.StatusNotFound {
		t.Fatalf("a refused key was stored: GET %d", code)
	}
	// Unknown types, and management keys (the OOB peers own those).
	for _, p := range []string{"/api/keys/bogus/+31612345678", "/api/keys/mgmt/hub"} {
		if code, _ := chatKeyCall(t, h, "PUT", p, `{"key":"`+chatKeyA+`"}`); code != http.StatusBadRequest {
			t.Fatalf("PUT %s: %d, want 400", p, code)
		}
		if code, _ := chatKeyCall(t, h, "GET", p, ""); code != http.StatusBadRequest {
			t.Fatalf("GET %s: %d, want 400", p, code)
		}
	}
	// A blank address is not a chat.
	if code, _ := chatKeyCall(t, h, "PUT", "/api/keys/sms/%20", `{"key":"`+chatKeyA+`"}`); code != http.StatusBadRequest {
		t.Fatalf("PUT to a blank address: %d, want 400", code)
	}
}

func TestChatKeyAPI_NoKeyStore(t *testing.T) {
	s := newTestServerWithDB(t)
	h := s.Router()
	for _, c := range [][3]string{
		{"GET", "/api/keys/sms/+31612345678", ""},
		{"PUT", "/api/keys/sms/+31612345678", `{"key":"` + chatKeyA + `"}`},
		{"DELETE", "/api/keys/sms/+31612345678", ""},
	} {
		if code, _ := chatKeyCall(t, h, c[0], c[1], c[2]); code != http.StatusServiceUnavailable {
			t.Fatalf("%s without a key store: %d, want 503", c[0], code)
		}
	}
}

// fakeChatKeyResolver answers one key for one address.
type fakeChatKeyResolver map[string]string

func (f fakeChatKeyResolver) ChatKeyHex(channelType, address string) (string, bool, error) {
	k, ok := f[channelType+":"+address]
	return k, ok, nil
}

// The send endpoint's length check counts what goes on air: a text sealed
// with its chat's key is longer than the text, and a plaintext peer's is not
// sealed at all.
func TestSealedSMSLength(t *testing.T) {
	tp := engine.NewTransformPipeline()
	tp.SetChatKeyResolver(fakeChatKeyResolver{"sms:+31612345678": chatKeyA})
	cfg := gateway.CellularConfig{MaxSMSSegments: 1, PlaintextPeers: []string{"+3197010258258"}}
	text := strings.Repeat("x", 120) // SMAZ2 cannot shorten it

	onAir, limit, sealed, err := sealedSMSLength(cfg, tp, "[]", "+31612345678", text)
	// base64 of the version byte, nonce, 120 bytes and the tag: 4*ceil(149/3)
	if err != nil || !sealed || limit != 160 || onAir != 200 {
		t.Fatalf("sealed: %d of %d, sealed=%v, %v; want 200 of 160", onAir, limit, sealed, err)
	}
	if _, _, sealed, _ := sealedSMSLength(cfg, tp, "[]", "+31600000001", text); sealed {
		t.Fatal("a number without a key counted as sealed")
	}
	if _, _, sealed, _ := sealedSMSLength(cfg, tp, "[]", "+3197010258258", text); sealed {
		t.Fatal("a plaintext peer counted as sealed")
	}
	if _, _, sealed, _ := sealedSMSLength(gateway.CellularConfig{}, tp, "[]", "+31612345678", text); sealed {
		t.Fatal("no segment limit, nothing to check")
	}
}
