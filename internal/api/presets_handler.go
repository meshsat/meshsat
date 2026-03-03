package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

func (s *Server) handleGetPresets(w http.ResponseWriter, r *http.Request) {
	presets, err := s.db.GetPresetMessages()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, presets)
}

func (s *Server) handleCreatePreset(w http.ResponseWriter, r *http.Request) {
	var preset database.PresetMessage
	if err := json.NewDecoder(r.Body).Decode(&preset); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if preset.Name == "" || preset.Text == "" {
		writeError(w, http.StatusBadRequest, "name and text are required")
		return
	}
	if preset.Destination == "" {
		preset.Destination = "broadcast"
	}

	id, err := s.db.InsertPresetMessage(&preset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	preset.ID = int(id)
	writeJSON(w, http.StatusCreated, preset)
}

func (s *Server) handleUpdatePreset(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid preset ID")
		return
	}

	var preset database.PresetMessage
	if err := json.NewDecoder(r.Body).Decode(&preset); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	preset.ID = id

	if err := s.db.UpdatePresetMessage(&preset); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, preset)
}

func (s *Server) handleDeletePreset(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid preset ID")
		return
	}

	if err := s.db.DeletePresetMessage(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSendPreset(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid preset ID")
		return
	}

	presets, err := s.db.GetPresetMessages()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var preset *database.PresetMessage
	for _, p := range presets {
		if p.ID == id {
			preset = &p
			break
		}
	}
	if preset == nil {
		writeError(w, http.StatusNotFound, "preset not found")
		return
	}

	req := transport.SendRequest{
		Text: preset.Text,
	}

	// Parse destination
	if preset.Destination != "" && preset.Destination != "broadcast" {
		req.To = preset.Destination
	}

	if err := s.mesh.SendMessage(r.Context(), req); err != nil {
		log.Error().Err(err).Int("preset_id", id).Msg("failed to send preset message")
		writeError(w, http.StatusInternalServerError, "failed to send: "+err.Error())
		return
	}

	// Persist as sent message
	dbMsg := &database.Message{
		FromNode:    "local",
		ToNode:      preset.Destination,
		PortNum:     1, // TEXT_MESSAGE
		PortNumName: "TEXT_MESSAGE_APP",
		DecodedText: preset.Text,
		Direction:   "tx",
		Transport:   "radio",
	}
	s.db.InsertMessage(dbMsg)

	writeJSON(w, http.StatusOK, map[string]string{"status": "sent", "text": preset.Text})
}
