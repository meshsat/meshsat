package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

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
	// The post's state: closed_at and purge_at while closed, purged_at after
	// a purge; purge_days is MESHSAT_POST_PURGE_DAYS.
	PurgeDays int    `json:"purge_days"`
	Closed    bool   `json:"closed"`
	ClosedAt  string `json:"closed_at,omitempty"`
	PurgeAt   string `json:"purge_at,omitempty"`
	PurgedAt  string `json:"purged_at,omitempty"`
	// print is MESHSAT_POST_PRINT; printer is the receipt printer service as
	// it last reported itself.
	Print   bool               `json:"print"`
	Printer cpost.PrinterState `json:"printer"`
}

// postStateBody is the post's state for the API.
type postStateBody struct {
	Closed   bool   `json:"closed"`
	ClosedAt string `json:"closed_at,omitempty"`
	PurgeAt  string `json:"purge_at,omitempty"`
	PurgedAt string `json:"purged_at,omitempty"`
}

func postState(st cpost.State) postStateBody {
	f := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	return postStateBody{Closed: st.Closed, ClosedAt: f(st.ClosedAt), PurgeAt: f(st.PurgeAt), PurgedAt: f(st.PurgedAt)}
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
	cfg, st, state := p.Config(), p.Settings(), postState(p.State())
	writeJSON(w, http.StatusOK, postConfigBody{Enabled: p.Enabled(), Listen: cfg.Listen, PortalURL: cfg.PortalURL, Bearer: cfg.Bearer,
		MaxChars: cfg.MaxTextChars, MaxBytes: cfg.MaxBytes, BoardDays: int(cfg.BoardRetention.Hours() / 24),
		AutoApprove: st.AutoApprove, PostsPerHour: st.PostsPerHour, PostsPerDay: st.PostsPerDay, Operator: st.Operator,
		PurgeDays: int(cfg.PurgeAfter.Hours() / 24), Closed: state.Closed, ClosedAt: state.ClosedAt, PurgeAt: state.PurgeAt, PurgedAt: state.PurgedAt,
		Print: cfg.Print, Printer: p.PrinterState()})
}

// handleClosePost closes the post.
// @Summary Close the Community Post
// @Description The portal takes no new post and no board line; replies can still be picked up. Everything the portal stored is purged purge_days after closing (MESHSAT_POST_PURGE_DAYS, 30), or sooner with POST /api/post/purge. Closing a closed post changes nothing.
// @Tags post
// @Produce json
// @Success 200 {object} postStateBody
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/post/close [post]
func (s *Server) handleClosePost(w http.ResponseWriter, r *http.Request) {
	p := s.postOrError(w)
	if p == nil {
		return
	}
	s.touchOperatorActivity()
	st, err := p.Close()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, postState(st))
}

// handleOpenPost reopens a closed post.
// @Summary Reopen the Community Post
// @Description The portal takes posts again and the purge that closing scheduled is off
// @Tags post
// @Produce json
// @Success 200 {object} postStateBody
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/post/open [post]
func (s *Server) handleOpenPost(w http.ResponseWriter, r *http.Request) {
	p := s.postOrError(w)
	if p == nil {
		return
	}
	s.touchOperatorActivity()
	st, err := p.Open()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, postState(st))
}

// handlePurgePost purges everything the portal stored, now.
// @Summary Purge the Community Post now
// @Description Removes every post, reply, board line and per-phone counter the portal stored, whether the post is open or closed; the post's state does not change. The body must carry {"confirm": true}. The delivery ledger's own rows stay under the Bridge's retention.
// @Tags post
// @Accept json
// @Produce json
// @Param body body map[string]bool true "confirm"
// @Success 200 {object} map[string]interface{} "purged (counts), state"
// @Failure 400 {object} map[string]string "not confirmed"
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/post/purge [post]
func (s *Server) handlePurgePost(w http.ResponseWriter, r *http.Request) {
	p := s.postOrError(w)
	if p == nil {
		return
	}
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.Confirm {
		writeError(w, http.StatusBadRequest, `a purge needs {"confirm": true}`)
		return
	}
	s.touchOperatorActivity()
	counts, err := p.Purge()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"purged": counts, "state": postState(p.State())})
}

// handleReportPostPrinter takes the receipt printer service's report.
// @Summary Report the receipt printer
// @Description The kit's receipt printer service (deploy/printer) says it follows the event stream, which printer it uses, where the slip sequence is and how its last slip went; the operator screen shows it. A report older than three minutes counts as no printer.
// @Tags post
// @Accept json
// @Produce json
// @Param body body cpost.PrinterReport true "Report"
// @Success 200 {object} cpost.PrinterState
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/post/printer [put]
func (s *Server) handleReportPostPrinter(w http.ResponseWriter, r *http.Request) {
	p := s.postOrError(w)
	if p == nil {
		return
	}
	var rep cpost.PrinterReport
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&rep); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	for _, f := range []*string{&rep.Service, &rep.Printer, &rep.LastSlipAt, &rep.LastError} {
		if len(*f) > 200 {
			*f = (*f)[:200]
		}
	}
	p.ReportPrinter(rep)
	writeJSON(w, http.StatusOK, p.PrinterState())
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
