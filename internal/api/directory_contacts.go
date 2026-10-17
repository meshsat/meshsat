package api

import (
	"net/http"
	"strconv"

	"meshsat/internal/directory"
)

// The directory's contacts as JSON, for the compose screen. Send-to-contact
// resolves a contact in the directory (directory_contacts, which the Hub's
// pushes, vCard and QR imports fill), while GET /api/contacts serves the
// older contacts table; the picker read that one, so a directory contact
// could not be picked and a picked one could not be sent to. [MESHSAT-1584]

// @Summary List directory contacts
// @Description Every contact in the directory with its addresses (kind,
// @Description value, label, rank), the contacts send-to-contact and delivery
// @Description intents resolve, ordered by name. Keys are never included.
// @Description `?team=` filters by team.
// @Tags directory
// @Produce json
// @Param team query string false "Team filter"
// @Param limit query integer false "Max contacts (default 1000, at most 10000)"
// @Success 200 {array} directory.Contact
// @Failure 500 {object} map[string]string
// @Router /api/directory/contacts [get]
func (s *Server) handleListDirectoryContacts(w http.ResponseWriter, r *http.Request) {
	limit := 1000
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = min(v, 10000)
	}
	store := directory.NewSQLStore(s.db.DB)
	contacts, err := store.ListContacts(r.Context(), directory.ContactFilter{Team: r.URL.Query().Get("team"), Limit: limit})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range contacts {
		addrs, err := store.ListAddresses(r.Context(), contacts[i].ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		contacts[i].Addresses = addrs
		contacts[i].Keys = nil // never on this endpoint: a key row can be private key material
	}
	if contacts == nil {
		contacts = []directory.Contact{}
	}
	writeJSON(w, http.StatusOK, contacts)
}
