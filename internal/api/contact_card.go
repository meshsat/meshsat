package api

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// The contact card MeshSat Android and iOS exchange face to face by QR code
// (pair/ContactQR.kt): meshsat:contact:1:<base64url P>.<base64url S>, where P
// is name, signing key, mesh node id, bridge id and issued-at joined by
// U+001F, and S the Ed25519 signature over exactly P by that key. This is not
// the Bridge's own meshsat://contact/ directory card, which the phones refuse.
// [MESHSAT-1416]
const (
	contactCardPrefix  = "meshsat:contact:1:"
	contactCardSep     = "\x1f"
	contactCardMaxName = 48 // UTF-16 units, as Kotlin's String.length
	contactCardDefault = "MeshSat phone"
)

var errCardName = errors.New("a card needs a name")

// encodeContactCard builds the card text, refusing what Android refuses on
// encode: a blank name, a name over 48 UTF-16 units, a separator in any field.
func encodeContactCard(name string, pub ed25519.PublicKey, meshNodeID, bridgeID string, issuedAt int64, sign func([]byte) []byte) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errCardName
	}
	if len(utf16.Encode([]rune(name))) > contactCardMaxName {
		return "", fmt.Errorf("name over %d characters", contactCardMaxName)
	}
	for _, field := range []string{name, meshNodeID, bridgeID} {
		if strings.Contains(field, contactCardSep) {
			return "", errors.New("a field may not contain the separator")
		}
	}
	b64 := base64.RawURLEncoding
	payload := []byte(strings.Join([]string{name, b64.EncodeToString(pub), meshNodeID, bridgeID, strconv.FormatInt(issuedAt, 10)}, contactCardSep))
	return contactCardPrefix + b64.EncodeToString(payload) + "." + b64.EncodeToString(sign(payload)), nil
}

// contactCardFingerprint is the first 8 bytes of SHA-256 over the raw key, as
// four groups of four lower-case hex digits: what two people read aloud.
func contactCardFingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	h := hex.EncodeToString(sum[:8])
	return h[0:4] + " " + h[4:8] + " " + h[8:12] + " " + h[12:16]
}

// cutUTF16 keeps at most n UTF-16 units of s without splitting a character.
func cutUTF16(s string, n int) string {
	units := 0
	for i, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

// handleGetContactCard returns this Bridge's contact card, signed by its
// routing identity (the key its Reticulum announces and key bundles carry).
// @Summary This Bridge's contact card
// @Description MeshSat Android's contact card (meshsat:contact:1:...), signed by the routing identity: the name (the name parameter, else the own node's long name, else "MeshSat phone"), the own node's id, no Hub bridge id (as the phones), issued now. The fingerprint is what the person reads aloud. 503 until the routing identity exists. [MESHSAT-1416]
// @Tags contacts
// @Produce json
// @Param name query string false "Name on the card (at most 48 characters)"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/contacts/card [get]
func (s *Server) handleGetContactCard(w http.ResponseWriter, r *http.Request) {
	if s.routingID == nil {
		writeError(w, http.StatusServiceUnavailable, "routing not initialized")
		return
	}
	name, nodeID := "", ""
	if s.mesh != nil {
		if st, err := s.mesh.GetStatus(r.Context()); err == nil && st != nil {
			name = strings.TrimSpace(st.NodeName)
			if own, ok := s.mesh.(interface{ MyNodeNum() uint32 }); ok && own.MyNodeNum() != 0 {
				nodeID = fmt.Sprintf("!%08x", own.MyNodeNum())
			}
		}
	}
	if name == "" {
		name = contactCardDefault
	}
	name = cutUTF16(name, contactCardMaxName)
	if q, ok := r.URL.Query()["name"]; ok {
		name = q[0]
	}
	pub := s.routingID.SigningPublicKey()
	issued := time.Now().Unix()
	text, err := encodeContactCard(name, pub, nodeID, "", issued, s.routingID.Sign)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"text":         text,
		"fingerprint":  contactCardFingerprint(pub),
		"signing_pub":  hex.EncodeToString(pub),
		"name":         name,
		"mesh_node_id": nodeID,
		"bridge_id":    "",
		"issued_at":    issued,
	})
}
