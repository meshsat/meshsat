package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/directory"
)

// QR contact card export + import. [MESHSAT-561]
//
// Export: GET /api/contacts/{id}/qr (and
// /api/directory/contacts/{id}/qr for the directory-aware
// namespace) renders the contact as a signed JSON card and returns
// both the `meshsat://contact/...` URL and the raw JSON so the
// caller can choose whether to show a QR image, a copyable link,
// or pipe it to their native scanner.
//
// Import: POST /api/directory/contacts/import-qr accepts either a
// URL string or the raw card JSON in the body; verifies the
// signature; and creates / merges the contact into the local
// directory. Trust level starts at 2 (user-confirmed) for imports;
// bumping to 3 (verified in person) happens through the explicit
// QR rescan flow in MESHSAT-560.

type qrResponse struct {
	URL      string      `json:"url"`
	Card     interface{} `json:"card"`
	Signer   string      `json:"signer"`
	Fallback bool        `json:"fallback,omitempty"`
}

// @Summary Export a contact as a signed QR card
// @Description Returns an Ed25519-signed meshsat://contact/... URL
// @Description carrying the contact's display name, SIDC, and all
// @Description known transport addresses. Long-lived (no TTL).
// @Description Scanners verify the signature against the bridge's
// @Description audit-log signer pubkey.
// @Tags contacts
// @Produce json
// @Param id path integer true "Contact ID"
// @Success 200 {object} qrResponse
// @Failure 404 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/contacts/{id}/qr [get]
func (s *Server) handleDirectoryContactQR(w http.ResponseWriter, r *http.Request) {
	if s.signing == nil {
		writeError(w, http.StatusServiceUnavailable, "signing service not available")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid contact ID")
		return
	}
	c, err := s.db.GetContact(id)
	if err != nil || c == nil {
		writeError(w, http.StatusNotFound, "contact not found")
		return
	}

	qc := directory.QRContact{
		ID:          strconv.FormatInt(c.ID, 10),
		DisplayName: c.DisplayName,
		SIDC:        c.SIDC,
		Team:        c.Team,
		Role:        c.Role,
		Org:         c.Org,
	}
	for _, a := range c.Addresses {
		qc.Addresses = append(qc.Addresses, directory.QRAddress{
			Kind:  a.Type,
			Value: a.Address,
			Label: a.Label,
		})
	}

	raw, url, err := directory.BuildQRCard(qc, s.signing.PublicKeyHex(), s.signing.Sign)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "build card: "+err.Error())
		return
	}
	// Echo the parsed card in the response so callers don't need to
	// decode the URL themselves.
	var card directory.QRCard
	_ = json.Unmarshal(raw, &card)
	writeJSON(w, http.StatusOK, qrResponse{
		URL:    url,
		Card:   card,
		Signer: s.signing.PublicKeyHex(),
	})
}

type qrImportRequest struct {
	URL  string `json:"url,omitempty"`
	Card string `json:"card,omitempty"`
}

// @Summary Import a contact from a signed QR card
// @Description Accepts either a meshsat://contact/... URL or the
// @Description raw card JSON. Verifies the signature (refuses tampered
// @Description cards) and creates / merges the contact into the local
// @Description directory. Returns the resulting contact so the UI
// @Description can confirm.
// @Tags contacts
// @Accept json
// @Produce json
// @Param body body qrImportRequest true "URL or raw card JSON"
// @Success 200 {object} database.Contact
// @Failure 400 {object} map[string]string
// @Router /api/directory/contacts/import-qr [post]
func (s *Server) handleDirectoryContactImportQR(w http.ResponseWriter, r *http.Request) {
	var req qrImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	input := req.URL
	if input == "" {
		input = req.Card
	}
	if input == "" {
		writeError(w, http.StatusBadRequest, "either url or card is required")
		return
	}
	card, err := directory.ParseQRCard(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "parse card: "+err.Error())
		return
	}

	// Upsert by display_name (legacy path) or id if we've seen this
	// contact before. For the MVP we create fresh; a richer merge
	// lands with the Hub directory sync (MESHSAT-540).
	id, err := s.db.CreateContact(card.Contact.DisplayName, "imported from QR card")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create contact: "+err.Error())
		return
	}
	for _, a := range card.Contact.Addresses {
		_, _ = s.db.AddContactAddress(id, a.Kind, a.Value, a.Label, "", false, false)
	}
	if card.Contact.SIDC != "" || card.Contact.Team != "" || card.Contact.Role != "" || card.Contact.Org != "" {
		_ = s.db.SetContactDirectoryMeta(id, card.Contact.SIDC, card.Contact.Team, card.Contact.Role, card.Contact.Org)
	}
	c, _ := s.db.GetContact(id)
	writeJSON(w, http.StatusOK, c)
}
