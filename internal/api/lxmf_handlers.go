package api

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/lxmf"
	"meshsat/internal/rnsstack"
)

// lxmfSendRequest is the body of POST /api/lxmf/send. A voice note (audio:
// the LXMF audio mode, Codec2 0x01-0x09 with 700C = 3, Opus 0x10-0x19, and
// the frames base64) and a photo (image: the format name, "webp", and the
// bytes base64) travel in the message's LXMF fields, as Sideband sends them;
// together at most lxmf.MaxAttachmentBytes (64 KiB). Content may be empty
// when the message carries one. [MESHSAT-1735]
type lxmfSendRequest struct {
	To         string              `json:"to"`         // lxmf.delivery hash, 32 hex
	Content    string              `json:"content"`    // message text
	Title      string              `json:"title"`      // optional
	Precedence string              `json:"precedence"` // optional delivery precedence
	Audio      *rnsstack.LXMFAudio `json:"audio,omitempty"`
	Image      *rnsstack.LXMFImage `json:"image,omitempty"`
}

// lxmfSendMaxBody bounds POST /api/lxmf/send: the text, the attachments
// in base64 and the JSON around them.
const lxmfSendMaxBody = lxmf.MaxTransferBytes + lxmf.MaxAttachmentBytes*2

// lxmfSendResponse is the reply of POST /api/lxmf/send.
type lxmfSendResponse struct {
	DeliveryID int64  `json:"delivery_id"`
	MsgRef     string `json:"msg_ref"`
	Channel    string `json:"channel"`
}

// handleLXMFIdentity returns this bridge's LXMF delivery destination.
// @Summary LXMF delivery identity
// @Tags lxmf
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/lxmf/identity [get]
func (s *Server) handleLXMFIdentity(w http.ResponseWriter, r *http.Request) {
	if s.lxmfRouter == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":   true,
		"dest_hash": s.lxmfRouter.HashHex(),
		"address":   "lxmf@" + s.lxmfRouter.HashHex(),
		"peers":     len(s.lxmfRouter.Peers()),
	})
}

// handleLXMFPeers lists known LXMF destinations.
// @Summary List known LXMF peers
// @Tags lxmf
// @Produce json
// @Success 200 {array} lxmf.Peer
// @Router /api/lxmf/peers [get]
func (s *Server) handleLXMFPeers(w http.ResponseWriter, r *http.Request) {
	if s.lxmfRouter == nil {
		writeError(w, http.StatusServiceUnavailable, "lxmf not running")
		return
	}
	peers := s.lxmfRouter.Peers()
	if peers == nil {
		peers = []lxmf.Peer{}
	}
	writeJSON(w, http.StatusOK, peers)
}

// handleLXMFSend queues an LXMF message through the delivery ledger.
// @Summary Send an LXMF message
// @Tags lxmf
// @Accept json
// @Produce json
// @Param body body lxmfSendRequest true "message"
// @Success 202 {object} lxmfSendResponse
// @Failure 400 {object} map[string]string
// @Router /api/lxmf/send [post]
func (s *Server) handleLXMFSend(w http.ResponseWriter, r *http.Request) {
	if s.lxmfRouter == nil || s.dispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "lxmf not running")
		return
	}
	var req lxmfSendRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, lxmfSendMaxBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if b, err := hex.DecodeString(req.To); err != nil || len(b) != lxmf.DestLen {
		writeError(w, http.StatusBadRequest, "to must be a 32-hex LXMF destination hash")
		return
	}
	ledger := rnsstack.LXMFBody{Content: req.Content, Title: req.Title, Audio: req.Audio, Image: req.Image}
	atts, err := ledger.Attachments()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := lxmf.WithAttachments(lxmf.MapOf(), atts...); err != nil {
		if errors.Is(err, lxmf.ErrAttachmentsTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Content == "" && len(atts) == 0 {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	if len(req.Content) > lxmf.MaxTransferBytes {
		writeError(w, http.StatusBadRequest, "content larger than one resource segment")
		return
	}
	// The ledger's preview is the text, or what the message carries when
	// there is none.
	preview := req.Content
	if preview == "" {
		preview = "[" + lxmf.DescribeAttachments(atts) + "]"
	}
	body, _ := json.Marshal(ledger)
	id, ref, err := s.dispatcher.QueueDirectSendTo("lxmf_0", preview, engine.DirectSendOptions{
		Precedence:  req.Precedence,
		Destination: req.To,
		Class:       database.DeliveryClassLXMF,
		MaxRetries:  5,
		Payload:     body,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, lxmfSendResponse{DeliveryID: id, MsgRef: ref, Channel: "lxmf_0"})
}

// handleLXMFAnnounce announces the LXMF destination now.
// @Summary Announce the LXMF destination now
// @Tags lxmf
// @Produce json
// @Success 200 {object} map[string]string
// @Router /api/lxmf/announce [post]
func (s *Server) handleLXMFAnnounce(w http.ResponseWriter, r *http.Request) {
	if s.lxmfRouter == nil {
		writeError(w, http.StatusServiceUnavailable, "lxmf not running")
		return
	}
	if err := s.lxmfRouter.Announce(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "announced", "dest_hash": s.lxmfRouter.HashHex()})
}

// lxmfAttachmentsResponse is the reply of GET /api/lxmf/messages/{id}/attachments.
type lxmfAttachmentsResponse struct {
	MessageID   int64                `json:"message_id"`
	LXMFHash    string               `json:"lxmf_hash"`
	Attachments []lxmfAttachmentItem `json:"attachments"`
}

// lxmfAttachmentItem is one attachment's metadata and where its bytes are.
type lxmfAttachmentItem struct {
	database.LXMFAttachment
	ContentType string `json:"content_type"`
	URL         string `json:"url"`
}

// lxmfMessageID resolves the {id} of the attachment routes: the message's
// row id (messages.id) or its LXMF hash (64 hex). 0 when it names nothing.
func (s *Server) lxmfMessageID(param string) int64 {
	if id, err := strconv.ParseInt(param, 10, 64); err == nil && id > 0 {
		return id
	}
	if b, err := hex.DecodeString(param); err == nil && len(b) == 32 {
		if id, err := s.db.LXMFMessageIDByHash(param); err == nil {
			return id
		}
	}
	return 0
}

// handleLXMFAttachments lists the voice note and photo an LXMF message carried.
// @Summary List an LXMF message's attachments
// @Description The voice note (audio) and photo (image) a received or sent LXMF message carried in its fields, in the message's order, with the media type and the URL of each one's bytes. id is the message's row id (messages.id) or its LXMF hash (64 hex).
// @Tags lxmf
// @Produce json
// @Param id path string true "message row id or LXMF hash"
// @Success 200 {object} lxmfAttachmentsResponse
// @Failure 404 {object} map[string]string
// @Router /api/lxmf/messages/{id}/attachments [get]
func (s *Server) handleLXMFAttachments(w http.ResponseWriter, r *http.Request) {
	id := s.lxmfMessageID(chi.URLParam(r, "id"))
	if ok, err := s.db.MessageExists(id); id == 0 || err != nil || !ok {
		writeError(w, http.StatusNotFound, "no such message")
		return
	}
	list, err := s.db.ListLXMFAttachments(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := lxmfAttachmentsResponse{MessageID: id, Attachments: []lxmfAttachmentItem{}}
	for _, a := range list {
		resp.LXMFHash = a.LXMFHash
		ct := lxmf.Attachment{Kind: a.Kind, Mode: a.Mode, Format: a.Format, Data: []byte{0}}.ContentType()
		resp.Attachments = append(resp.Attachments, lxmfAttachmentItem{LXMFAttachment: a, ContentType: ct,
			URL: "/api/lxmf/messages/" + strconv.FormatInt(id, 10) + "/attachments/" + strconv.Itoa(a.Index)})
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleLXMFAttachment serves one attachment's bytes.
// @Summary Download an LXMF attachment
// @Description The bytes of one attachment: Codec2 frames as audio/codec2 with the mode in X-LXMF-Audio-Mode (700C = 3), an Opus note as audio/ogg, a photo as image/webp. n is the attachment's index in the message, from the list.
// @Tags lxmf
// @Produce octet-stream
// @Param id path string true "message row id or LXMF hash"
// @Param n path int true "attachment index"
// @Success 200 {file} binary
// @Failure 404 {object} map[string]string
// @Router /api/lxmf/messages/{id}/attachments/{n} [get]
func (s *Server) handleLXMFAttachment(w http.ResponseWriter, r *http.Request) {
	id := s.lxmfMessageID(chi.URLParam(r, "id"))
	n, err := strconv.Atoi(chi.URLParam(r, "n"))
	if id == 0 || err != nil || n < 0 {
		writeError(w, http.StatusNotFound, "no such attachment")
		return
	}
	a, data, err := s.db.GetLXMFAttachment(id, n)
	if errors.Is(err, database.ErrNoAttachment) {
		writeError(w, http.StatusNotFound, "no such attachment")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	att := lxmf.Attachment{Kind: a.Kind, Mode: a.Mode, Format: a.Format, Data: data}
	w.Header().Set("Content-Type", att.ContentType())
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if a.Kind == lxmf.AttachmentAudio {
		w.Header().Set("X-LXMF-Audio-Mode", strconv.Itoa(a.Mode))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
