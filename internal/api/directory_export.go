package api

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"

	"meshsat/internal/directory"
)

// @Summary Export contacts as vCard 4.0
// @Description Streams every contact in the unified directory as a
// @Description concatenated vCard 4.0 document. X-MESHSAT-* extensions
// @Description carry the bearer-kind metadata not covered by standard
// @Description TEL / EMAIL. `?tenant=` scopes to a specific tenant
// @Description when the bridge is multi-tenant (rare in practice —
// @Description Bridges are single-tenant today and default to an
// @Description empty tenant id).
// @Tags directory
// @Produce text/vcard
// @Param tenant query string false "Tenant ID filter (default: empty tenant)"
// @Param limit query integer false "Max contacts (default 10000)"
// @Success 200 {string} string "vCard 4.0 text"
// @Failure 500 {object} map[string]string
// @Router /api/directory/export/vcard [get]
func (s *Server) handleExportVCard(w http.ResponseWriter, r *http.Request) {
	tenant := r.URL.Query().Get("tenant") // bridges are single-tenant; empty is the default
	limit := 10000
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	store := directory.NewSQLStore(s.db.DB)
	contacts, err := store.ListContacts(r.Context(), directory.ContactFilter{
		TenantID: tenant,
		Limit:    limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// ListContacts does not eager-load addresses or keys (that's the
	// Resolve path). For export we need the full picture, so resolve
	// each. Small N on a bridge (<10k contacts); the per-contact
	// query is cheap.
	full := make([]directory.Contact, 0, len(contacts))
	for i := range contacts {
		c, err := store.Resolve(r.Context(), contacts[i].ID)
		if err == nil && c != nil {
			full = append(full, *c)
			continue
		}
		full = append(full, contacts[i])
	}

	var buf bytes.Buffer
	if err := directory.WriteVCards(&buf, full); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="meshsat-directory-%d.vcf"`, len(contacts)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}
