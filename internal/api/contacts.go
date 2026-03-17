package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/auth"
	"meshsat/internal/database"
)

// ── Unified Contacts CRUD ──

func (s *Server) handleGetContacts(w http.ResponseWriter, r *http.Request) {
	tid := auth.TenantIDFromContext(r.Context())
	contacts, err := s.db.GetContacts(tid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list contacts")
		return
	}
	if contacts == nil {
		contacts = []database.Contact{}
	}
	writeJSON(w, http.StatusOK, contacts)
}

func (s *Server) handleGetContact(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid contact ID")
		return
	}
	tid := auth.TenantIDFromContext(r.Context())
	c, err := s.db.GetContact(id, tid)
	if err != nil {
		writeError(w, http.StatusNotFound, "contact not found")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

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
	tid := auth.TenantIDFromContext(r.Context())
	id, err := s.db.CreateContact(req.DisplayName, req.Notes, tid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create contact")
		return
	}
	c, _ := s.db.GetContact(id, tid)
	writeJSON(w, http.StatusCreated, c)
}

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
	tid := auth.TenantIDFromContext(r.Context())
	if err := s.db.UpdateContact(id, req.DisplayName, req.Notes, tid); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update contact")
		return
	}
	c, _ := s.db.GetContact(id, tid)
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleDeleteContact(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid contact ID")
		return
	}
	tid := auth.TenantIDFromContext(r.Context())
	if err := s.db.DeleteContact(id, tid); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete contact")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Contact Addresses ──

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

	// Verify contact belongs to this tenant
	tid := auth.TenantIDFromContext(r.Context())
	if _, err := s.db.GetContact(contactID, tid); err != nil {
		writeError(w, http.StatusNotFound, "contact not found")
		return
	}

	id, err := s.db.AddContactAddress(contactID, req.Type, req.Address, req.Label, req.EncryptionKey, req.IsPrimary, req.AutoFwd)
	if err != nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("address already exists or invalid: %v", err))
		return
	}

	// Also sync to legacy sms_contacts table for backward compatibility
	if req.Type == "sms" {
		c, _ := s.db.GetContact(contactID, tid)
		if c != nil {
			_, _ = s.db.CreateSMSContact(c.DisplayName, req.Address, c.Notes, req.AutoFwd)
		}
	}

	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

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

func (s *Server) handleLookupContact(w http.ResponseWriter, r *http.Request) {
	addrType := r.URL.Query().Get("type")
	address := r.URL.Query().Get("address")
	if addrType == "" || address == "" {
		writeError(w, http.StatusBadRequest, "type and address query params required")
		return
	}
	tid := auth.TenantIDFromContext(r.Context())
	c, err := s.db.ResolveContact(addrType, address, tid)
	if err != nil {
		writeError(w, http.StatusNotFound, "no contact for this address")
		return
	}
	writeJSON(w, http.StatusOK, c)
}
