package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
	cpost "meshsat/internal/post"
)

// The Community Post's operator side (MESHSAT-1733): the approval queue, the
// settings and the counters, on the Bridge's own API for the kiosk page.
// The portal itself is on its own listener (cpost.Service.Handler) and never
// here.

// SetPost wires the Community Post service.
func (s *Server) SetPost(p *cpost.Service) { s.post = p }

type postConfigBody struct {
	Enabled      bool   `json:"enabled"`
	Listen       string `json:"listen,omitempty"`
	PortalURL    string `json:"portal_url,omitempty"`
	Bearer       string `json:"bearer,omitempty"`
	MaxChars     int    `json:"max_chars"`
	MaxBytes     int    `json:"max_bytes"`
	BoardDays    int    `json:"board_days"`
	AutoApprove  bool   `json:"auto_approve"`
	PostsPerHour int    `json:"posts_per_hour"`
	PostsPerDay  int    `json:"posts_per_day"`
	Operator     string `json:"operator"`
}

func (s *Server) postOrError(w http.ResponseWriter) *cpost.Service {
	if s.post == nil {
		writeError(w, http.StatusServiceUnavailable, "the Community Post is not configured on this Bridge (MESHSAT_POST_LISTEN)")
		return nil
	}
	return s.post
}

// handleGetPostConfig returns the Community Post's configuration and
// settings.
// @Summary Get Community Post config
// @Description Whether the portal listener is on, its address and URL, the bearer, the limits from the environment, and the operator's run-time settings (approval queue, per-phone ceilings, operator name)
// @Tags post
// @Produce json
// @Success 200 {object} postConfigBody
// @Failure 503 {object} map[string]string
// @Router /api/post/config [get]
func (s *Server) handleGetPostConfig(w http.ResponseWriter, r *http.Request) {
	p := s.postOrError(w)
	if p == nil {
		return
	}
	cfg, st := p.Config(), p.Settings()
	writeJSON(w, http.StatusOK, postConfigBody{Enabled: p.Enabled(), Listen: cfg.Listen, PortalURL: cfg.PortalURL, Bearer: cfg.Bearer,
		MaxChars: cfg.MaxTextChars, MaxBytes: cfg.MaxBytes, BoardDays: int(cfg.BoardRetention.Hours() / 24),
		AutoApprove: st.AutoApprove, PostsPerHour: st.PostsPerHour, PostsPerDay: st.PostsPerDay, Operator: st.Operator})
}

// handleSetPostConfig saves the operator's run-time settings.
// @Summary Set Community Post settings
// @Description auto_approve turns the approval queue off (a post leaves at once); posts_per_hour and posts_per_day are per phone, 0 = no ceiling; operator is the name shown on the portal's "about" page
// @Tags post
// @Accept json
// @Produce json
// @Param body body cpost.Settings true "Settings"
// @Success 200 {object} postConfigBody
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/post/config [put]
func (s *Server) handleSetPostConfig(w http.ResponseWriter, r *http.Request) {
	p := s.postOrError(w)
	if p == nil {
		return
	}
	st := p.Settings()
	if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := p.SetSettings(st); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.handleGetPostConfig(w, r)
}

// handleGetPostQueue lists the posts waiting for an operator and the recent
// ones.
// @Summary Community Post queue
// @Description The posts waiting for approval (newest first) and the last posts whatever their status, with counts per status
// @Tags post
// @Produce json
// @Success 200 {object} map[string]interface{} "pending, recent, counts"
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/post/queue [get]
func (s *Server) handleGetPostQueue(w http.ResponseWriter, r *http.Request) {
	p := s.postOrError(w)
	if p == nil {
		return
	}
	pending, err := p.Queue(100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recent, err := p.Recent(50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	counts, _ := s.db.CountPostMessages()
	if pending == nil {
		pending = []database.PostMessage{}
	}
	if recent == nil {
		recent = []database.PostMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": pending, "recent": recent, "counts": counts})
}

// handleGetPost returns one post with its replies.
// @Summary Get a Community Post message
// @Tags post
// @Produce json
// @Param code path string true "Pickup code"
// @Success 200 {object} map[string]interface{} "post, replies"
// @Failure 404 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/post/{code} [get]
func (s *Server) handleGetPost(w http.ResponseWriter, r *http.Request) {
	p := s.postOrError(w)
	if p == nil {
		return
	}
	m, replies, err := p.Status(chi.URLParam(r, "code"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if m == nil {
		writeError(w, http.StatusNotFound, "unknown code")
		return
	}
	if replies == nil {
		replies = []database.PostReply{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"post": m, "replies": replies})
}

// handleApprovePost sends a waiting post.
// @Summary Approve a Community Post message
// @Description Queues the post on the kit's satellite link as one "@" lane text; approving twice queues nothing twice
// @Tags post
// @Produce json
// @Param code path string true "Pickup code"
// @Success 200 {object} map[string]interface{} "post"
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string "the post is not waiting"
// @Failure 503 {object} map[string]string
// @Router /api/post/{code}/approve [post]
func (s *Server) handleApprovePost(w http.ResponseWriter, r *http.Request) {
	s.postDecision(w, r, true)
}

// handleRefusePost keeps a waiting post from leaving.
// @Summary Refuse a Community Post message
// @Tags post
// @Produce json
// @Param code path string true "Pickup code"
// @Success 200 {object} map[string]interface{} "post"
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string "the post is not waiting"
// @Failure 503 {object} map[string]string
// @Router /api/post/{code}/refuse [post]
func (s *Server) handleRefusePost(w http.ResponseWriter, r *http.Request) {
	s.postDecision(w, r, false)
}

func (s *Server) postDecision(w http.ResponseWriter, r *http.Request, approve bool) {
	p := s.postOrError(w)
	if p == nil {
		return
	}
	s.touchOperatorActivity()
	code := cpost.NormaliseCode(chi.URLParam(r, "code"))
	if code == "" {
		writeError(w, http.StatusNotFound, "unknown code")
		return
	}
	var err error
	if approve {
		err = p.Approve(code)
	} else {
		err = p.Refuse(code)
	}
	switch {
	case errors.Is(err, cpost.ErrUnknownCode):
		writeError(w, http.StatusNotFound, "unknown code")
		return
	case err != nil && strings.Contains(err.Error(), " is "):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	m, _, _ := p.Status(code)
	writeJSON(w, http.StatusOK, map[string]any{"post": m})
}
