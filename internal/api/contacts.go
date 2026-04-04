package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
)

// ── Unified Contacts CRUD ──

// @Summary List contacts
// @Description Returns all unified contacts
// @Tags contacts
// @Produce json
// @Success 200 {array} database.Contact
// @Failure 500 {object} map[string]string
// @Router /api/contacts [get]
func (s *Server) handleGetContacts(w http.ResponseWriter, r *http.Request) {
	contacts, err := s.db.GetContacts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list contacts")
		return
	}
	if contacts == nil {
		contacts = []database.Contact{}
	}
	writeJSON(w, http.StatusOK, contacts)
}

// @Summary Get contact
// @Description Returns a single contact by ID with addresses
// @Tags contacts
// @Produce json
// @Param id path integer true "Contact ID"
// @Success 200 {object} database.Contact
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/contacts/{id} [get]
func (s *Server) handleGetContact(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid contact ID")
		return
	}
	c, err := s.db.GetContact(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "contact not found")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// @Summary Create contact
// @Description Creates a new unified contact
// @Tags contacts
// @Accept json
// @Produce json
// @Param body body object true "Contact" example({"display_name":"Alice","notes":""})
// @Success 201 {object} database.Contact
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/contacts [post]
func (s *Server) handleCreateContact(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DisplayName string `json:"display_name"`
		Notes       string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, "display_name is required")
		return
	}
	id, err := s.db.CreateContact(req.DisplayName, req.Notes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create contact")
		return
	}
	c, _ := s.db.GetContact(id)
	writeJSON(w, http.StatusCreated, c)
}

// @Summary Update contact
// @Description Updates an existing contact's display name and notes
// @Tags contacts
// @Accept json
// @Produce json
// @Param id path integer true "Contact ID"
// @Param body body object true "Contact"
// @Success 200 {object} database.Contact
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/contacts/{id} [put]
func (s *Server) handleUpdateContact(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid contact ID")
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
		Notes       string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, "display_name is required")
		return
	}
	if err := s.db.UpdateContact(id, req.DisplayName, req.Notes); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update contact")
		return
	}
	c, _ := s.db.GetContact(id)
	writeJSON(w, http.StatusOK, c)
}

// @Summary Delete contact
// @Description Deletes a contact and all associated addresses
// @Tags contacts
// @Param id path integer true "Contact ID"
// @Success 204
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/contacts/{id} [delete]
func (s *Server) handleDeleteContact(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid contact ID")
		return
	}
	if err := s.db.DeleteContact(id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete contact")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Contact Addresses ──

// @Summary Add contact address
// @Description Adds an address (SMS, mesh, satellite, etc.) to a contact
// @Tags contacts
// @Accept json
// @Produce json
// @Param id path integer true "Contact ID"
// @Param body body object true "Address" example({"type":"sms","address":"+31612345678","label":"Mobile","is_primary":true,"auto_fwd":false})
// @Success 201 {object} map[string]int64
// @Failure 400 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /api/contacts/{id}/addresses [post]
func (s *Server) handleAddContactAddress(w http.ResponseWriter, r *http.Request) {
	contactID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid contact ID")
		return
	}
	var req struct {
		Type          string `json:"type"`
		Address       string `json:"address"`
		Label         string `json:"label"`
		EncryptionKey string `json:"encryption_key"`
		IsPrimary     bool   `json:"is_primary"`
		AutoFwd       bool   `json:"auto_fwd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Type == "" || req.Address == "" {
		writeError(w, http.StatusBadRequest, "type and address are required")
		return
	}
	id, err := s.db.AddContactAddress(contactID, req.Type, req.Address, req.Label, req.EncryptionKey, req.IsPrimary, req.AutoFwd)
	if err != nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("address already exists or invalid: %v", err))
		return
	}

	// Also sync to legacy sms_contacts table for backward compatibility
	if req.Type == "sms" {
		c, _ := s.db.GetContact(contactID)
		if c != nil {
			_, _ = s.db.CreateSMSContact(c.DisplayName, req.Address, c.Notes, req.AutoFwd)
		}
	}

	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

// @Summary Update contact address
// @Description Updates an existing contact address
// @Tags contacts
// @Accept json
// @Produce json
// @Param id path integer true "Contact ID"
// @Param aid path integer true "Address ID"
// @Param body body object true "Address"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/contacts/{id}/addresses/{aid} [put]
func (s *Server) handleUpdateContactAddress(w http.ResponseWriter, r *http.Request) {
	addrID, err := strconv.ParseInt(chi.URLParam(r, "aid"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid address ID")
		return
	}
	var req struct {
		Type          string `json:"type"`
		Address       string `json:"address"`
		Label         string `json:"label"`
		EncryptionKey string `json:"encryption_key"`
		IsPrimary     bool   `json:"is_primary"`
		AutoFwd       bool   `json:"auto_fwd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.db.UpdateContactAddress(addrID, req.Type, req.Address, req.Label, req.EncryptionKey, req.IsPrimary, req.AutoFwd); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update address")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// @Summary Delete contact address
// @Description Removes an address from a contact
// @Tags contacts
// @Param id path integer true "Contact ID"
// @Param aid path integer true "Address ID"
// @Success 204
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/contacts/{id}/addresses/{aid} [delete]
func (s *Server) handleDeleteContactAddress(w http.ResponseWriter, r *http.Request) {
	addrID, err := strconv.ParseInt(chi.URLParam(r, "aid"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid address ID")
		return
	}
	if err := s.db.DeleteContactAddress(addrID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete address")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Unified Conversation ──

// @Summary Get conversation
// @Description Returns unified conversation history for a contact across all transports
// @Tags contacts
// @Produce json
// @Param id path integer true "Contact ID"
// @Param limit query integer false "Max messages (default: 100)"
// @Success 200 {array} database.UnifiedMessage
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/contacts/{id}/conversation [get]
func (s *Server) handleGetConversation(w http.ResponseWriter, r *http.Request) {
	contactID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid contact ID")
		return
	}
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	msgs, err := s.db.GetUnifiedConversation(contactID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get conversation")
		return
	}
	if msgs == nil {
		msgs = []database.UnifiedMessage{}
	}
	writeJSON(w, http.StatusOK, msgs)
}

// ── Contact Lookup (resolve address → contact name) ──

// @Summary Lookup contact by address
// @Description Resolves an address (type + value) to a contact
// @Tags contacts
// @Produce json
// @Param type query string true "Address type (sms, mesh, satellite)"
// @Param address query string true "Address value"
// @Success 200 {object} database.Contact
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/contacts/lookup [get]
func (s *Server) handleLookupContact(w http.ResponseWriter, r *http.Request) {
	addrType := r.URL.Query().Get("type")
	address := r.URL.Query().Get("address")
	if addrType == "" || address == "" {
		writeError(w, http.StatusBadRequest, "type and address query params required")
		return
	}
	c, err := s.db.ResolveContact(addrType, address)
	if err != nil {
		writeError(w, http.StatusNotFound, "no contact for this address")
		return
	}
	writeJSON(w, http.StatusOK, c)
}
