package api

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/directory"
	"meshsat/internal/hubreporter"
)

// The directory as the Hub sends it (HeMB-NG stage 2 C7): its groups, what
// the bridge applied, and this bridge's own edits on Hub contacts, which a
// snapshot keeps. [MESHSAT-1584]

// @Summary List directory groups
// @Description Every group in the directory with its member ids and its
// @Description origin: "hub:<tenant>" for what the Hub sent, "local" for
// @Description this bridge's own. Kinds: TEAM, ROLE, LIST, MLS_GROUP.
// @Tags directory
// @Produce json
// @Success 200 {array} directory.Group
// @Failure 500 {object} map[string]string
// @Router /api/directory/groups [get]
func (s *Server) handleListDirectoryGroups(w http.ResponseWriter, r *http.Request) {
	store := directory.NewSQLStore(s.db.DB)
	groups, err := store.ListGroups(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range groups {
		members, err := store.ListGroupMembers(r.Context(), groups[i].ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		ids := make([]string, 0, len(members))
		for _, m := range members {
			ids = append(ids, m.ID)
		}
		groups[i].Members = ids
	}
	if groups == nil {
		groups = []directory.Group{}
	}
	writeJSON(w, http.StatusOK, groups)
}

// directoryState is what GET /api/directory/state answers.
type directoryState struct {
	directory.HubState
	KeyID string `json:"key_id"` // the Hub directory key the bridge trusts; empty: none pinned
	// AddressConflicts: addresses the last snapshot sent for Hub contacts
	// that a contact of this bridge holds, so those Hub contacts go
	// without them (r20 F1).
	AddressConflicts []directory.AddressConflict `json:"address_conflicts"`
}

// @Summary Directory sync state
// @Description What the bridge applied of its Hub's directory: the tenant
// @Description it follows, the version of the last snapshot applied and
// @Description when, apart from it the version of the last key rotation
// @Description applied (key_version), the last refusal and why
// @Description (other_tenant, not_newer), the id of the Hub key it
// @Description trusts (SHA-256 of the PKIX key, hex), and the address
// @Description conflicts of the last snapshot: each address the Hub sent for
// @Description one of its contacts that a contact of this bridge holds
// @Description (holder_contact_id), which the Hub contact goes without until
// @Description the holder lets it go (DELETE
// @Description /api/directory/contacts/{id}/addresses/{address_id}) and the Hub
// @Description sends a newer snapshot.
// @Tags directory
// @Produce json
// @Success 200 {object} directoryState
// @Failure 500 {object} map[string]string
// @Router /api/directory/state [get]
func (s *Server) handleDirectoryState(w http.ResponseWriter, r *http.Request) {
	st, err := directory.NewSQLStore(s.db.DB).GetHubState(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	conflicts, err := directory.NewSQLStore(s.db.DB).AddressConflicts(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := directoryState{HubState: st, AddressConflicts: conflicts}
	if v, err := s.db.GetSystemConfig(directory.RotationKeyConfig); err == nil && v != "" {
		if der, err := hex.DecodeString(v); err == nil {
			out.KeyID = hubreporter.KeyID(der)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type directoryContactEdit struct {
	DisplayName *string `json:"display_name,omitempty"`
	Notes       *string `json:"notes,omitempty"`
}

// @Summary Edit a directory contact
// @Description Sets the display name or notes. On a contact the Hub sent
// @Description the value is kept as this bridge's own (local_override) and
// @Description the next snapshot does not undo it.
// @Tags directory
// @Accept json
// @Produce json
// @Param id path string true "Contact id"
// @Param body body directoryContactEdit true "Fields to set"
// @Success 200 {object} directory.Contact
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/directory/contacts/{id} [patch]
func (s *Server) handleEditDirectoryContact(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body directoryContactEdit
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	store := directory.NewSQLStore(s.db.DB)
	if err := store.SetLocalOverride(r.Context(), id, body.DisplayName, body.Notes); err != nil {
		writeDirectoryStoreError(w, err)
		return
	}
	c, err := store.GetContact(r.Context(), id)
	if err != nil {
		writeDirectoryStoreError(w, err)
		return
	}
	c.Keys = nil
	writeJSON(w, http.StatusOK, c)
}

// @Summary Add an address to a directory contact
// @Description Adds an address of this bridge's own (origin local) to any
// @Description contact, one the Hub sent included; a snapshot keeps it.
// @Tags directory
// @Accept json
// @Produce json
// @Param id path string true "Contact id"
// @Param body body directory.Address true "kind, value, label, primary_rank"
// @Success 201 {object} directory.Address
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string "Another contact has this address"
// @Router /api/directory/contacts/{id}/addresses [post]
func (s *Server) handleAddDirectoryAddress(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var a directory.Address
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&a); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	store := directory.NewSQLStore(s.db.DB)
	if _, err := store.GetContact(r.Context(), id); err != nil {
		writeDirectoryStoreError(w, err)
		return
	}
	a.ID, a.ContactID, a.Origin, a.CreatedAt, a.UpdatedAt = "", id, string(directory.OriginLocal), "", ""
	if err := store.AddAddress(r.Context(), &a); err != nil {
		writeDirectoryStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

// deletedAddress is what DELETE /api/directory/contacts/{id}/addresses/{address_id}
// answers.
type deletedAddress struct {
	Deleted string `json:"deleted"` // the address id
	// WaitingHubContacts are the Hub contacts the last snapshot refused this
	// address; each gets it at the Hub's next snapshot (an edit on the Hub
	// sends one).
	WaitingHubContacts []directory.AddressConflict `json:"waiting_hub_contacts"`
}

// @Summary Remove a local address from a directory contact
// @Description Removes an address this bridge's operator made (origin
// @Description local), for instance one that keeps the Hub's contact for the
// @Description same peer from its address (an address belongs to one contact,
// @Description and a local one wins over the Hub's). The contact, its other
// @Description addresses, notes, groups and history stay. An address the Hub
// @Description sent is refused with 409: its signed snapshot manages it.
// @Description waiting_hub_contacts lists the Hub contacts the last snapshot
// @Description refused this address; they get it at the Hub's next snapshot.
// @Tags directory
// @Produce json
// @Param id path string true "Contact id"
// @Param address_id path string true "Address id"
// @Success 200 {object} deletedAddress
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string "The Hub manages this address"
// @Router /api/directory/contacts/{id}/addresses/{address_id} [delete]
func (s *Server) handleDeleteDirectoryAddress(w http.ResponseWriter, r *http.Request) {
	addressID := chi.URLParam(r, "address_id")
	waiting, err := directory.NewSQLStore(s.db.DB).DeleteLocalAddress(r.Context(), chi.URLParam(r, "id"), addressID)
	if err != nil {
		writeDirectoryStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, deletedAddress{Deleted: addressID, WaitingHubContacts: waiting})
}

func writeDirectoryStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, directory.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, directory.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, directory.ErrConflict), errors.Is(err, directory.ErrHubManaged):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
