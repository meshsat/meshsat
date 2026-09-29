package api

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshsat/internal/routing"
)

// The fixed vector of MeshSat Android's ContactQRTest fields, from the seed
// 00..1f: Ed25519 is deterministic, so the text is the same in Kotlin, Python
// and Go. [MESHSAT-1416]
const (
	cardVectorText = "meshsat:contact:1:S3lyaWFrb3MfQTZFSHZfUE9FTDRkY04wWTUwdkFtV2ZrMWpDYnBRMWZIZHlHWkJKVk1iZx8hYmY2ZWU3YmMfbXNhLWZsYW5ldXIfMTc4OTkwMDAwMA.oKXEDt4bFgUMr1qBHOBVgUiKRqAi50xVXnxeSukzSnMqXztpxqSABg4L9xSqbDVJdlTrh76ycQN_f1A22DgaAA"
	cardVectorMine = "meshsat:contact:1:TWVzaFNhdCBwaG9uZR9BNkVIdl9QT0VMNGRjTjBZNTB2QW1XZmsxakNicFExZkhkeUdaQkpWTWJnHx8fMTc4OTkwMDAwMA.OGA5_8_Q-mGL0x0kavwMIeFHryIyk4UagJH4KlmSgNDR4Xq9Nzzi98e7TS9eN3ZSCTeAV8GmhK5YVVk8H_sxAA"
)

func vectorKey() ed25519.PrivateKey {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func TestContactCard_AndroidVector(t *testing.T) {
	key := vectorKey()
	pub := key.Public().(ed25519.PublicKey)
	sign := func(b []byte) []byte { return ed25519.Sign(key, b) }
	text, err := encodeContactCard("Kyriakos", pub, "!bf6ee7bc", "msa-flaneur", 1789900000, sign)
	if err != nil || text != cardVectorText {
		t.Fatalf("card %q (%v), want Android's %q", text, err, cardVectorText)
	}
	mine, err := encodeContactCard(contactCardDefault, pub, "", "", 1789900000, sign)
	if err != nil || mine != cardVectorMine {
		t.Fatalf("My card %q (%v), want %q", mine, err, cardVectorMine)
	}
	if fp := contactCardFingerprint(pub); fp != "5647 5aa7 5463 474c" {
		t.Fatalf("fingerprint %q", fp)
	}
}

func TestContactCard_RefusesWhatAndroidRefuses(t *testing.T) {
	key := vectorKey()
	pub := key.Public().(ed25519.PublicKey)
	sign := func(b []byte) []byte { return ed25519.Sign(key, b) }
	for _, c := range []struct{ name, node, want string }{
		{"  ", "", "a card needs a name"},
		{strings.Repeat("a", 49), "", "name over 48 characters"},
		{strings.Repeat("😀", 24) + "a", "", "name over 48 characters"}, // 49 UTF-16 units
		{"Kyriakos\x1ffake-key", "", "separator"},
		{"Kyriakos", "!bf6ee7bc\x1f", "separator"},
	} {
		if _, err := encodeContactCard(c.name, pub, c.node, "", 1, sign); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: error %v, want %q", c.name, err, c.want)
		}
	}
	if _, err := encodeContactCard(strings.Repeat("😀", 24), pub, "", "", 1, sign); err != nil {
		t.Errorf("48 UTF-16 units refused: %v", err)
	}
	if got := cutUTF16(strings.Repeat("😀", 30), 48); got != strings.Repeat("😀", 24) {
		t.Errorf("cut to %d runes", len([]rune(got)))
	}
}

func TestContactCard_Endpoint(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/contacts/card", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("without an identity: %d", rec.Code)
	}

	key := vectorKey()
	if err := s.db.SetSystemConfig("routing_signing_key", hex.EncodeToString(key)); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SetSystemConfig("routing_encryption_key", hex.EncodeToString(make([]byte, 32))); err != nil {
		t.Fatal(err)
	}
	id, err := routing.NewIdentity(s.db)
	if err != nil {
		t.Fatal(err)
	}
	s.SetRoutingIdentity(id)

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/contacts/card?name=Kyriakos", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("card: %d %s", rec.Code, rec.Body.String())
	}
	var card struct {
		Text, Fingerprint, Name, SigningPub string
		MeshNodeID                          string `json:"mesh_node_id"`
		BridgeID                            string `json:"bridge_id"`
		IssuedAt                            int64  `json:"issued_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card.Fingerprint != "5647 5aa7 5463 474c" || card.Name != "Kyriakos" || card.BridgeID != "" || card.MeshNodeID != "" {
		t.Fatalf("card %+v", card)
	}
	// The text verifies with the key it carries, over exactly the payload bytes.
	body := strings.TrimPrefix(card.Text, contactCardPrefix)
	dot := strings.IndexByte(body, '.')
	payload, _ := base64.RawURLEncoding.DecodeString(body[:dot])
	sig, _ := base64.RawURLEncoding.DecodeString(body[dot+1:])
	if !ed25519.Verify(key.Public().(ed25519.PublicKey), payload, sig) {
		t.Fatal("the signature does not verify")
	}
	if fields := strings.Split(string(payload), contactCardSep); len(fields) != 5 || fields[0] != "Kyriakos" {
		t.Fatalf("payload %q", payload)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/contacts/card?name=", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank name: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/contacts/card", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"MeshSat phone"`) {
		t.Fatalf("no node, no name: %d %s", rec.Code, rec.Body.String())
	}
}
