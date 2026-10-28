package post

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/types"
)

// Config is what the environment sets (config.Post*). Everything is off
// until Listen is set; the ceilings and the operator's name can be changed
// at run time through the API and are kept in system_config.
type Config struct {
	Listen         string        // the portal listener; "" = the post is off
	PortalURL      string        // the URL the captive detection sends a phone to
	Operator       string        // the operator's name on the "about this post" page
	Bearer         string        // "auto" (the kit's satellite link) or an interface id
	PostsPerHour   int           // per phone
	PostsPerDay    int           // per phone
	MaxTextChars   int           // characters a post may carry
	MaxBytes       int           // bytes a post may carry
	BoardRetention time.Duration // how long a board line stays
	AutoApprove    bool          // the approval queue off: a post leaves at once
}

// Defaults are the plan's: a busy post stays affordable, and a stranger
// cannot empty the kit's satellite credit from one phone.
func (c *Config) defaults() {
	if c.PortalURL == "" {
		c.PortalURL = "http://10.44.0.1/"
	}
	if c.Bearer == "" {
		c.Bearer = "auto"
	}
	if c.PostsPerHour == 0 {
		c.PostsPerHour = 3
	}
	if c.PostsPerDay == 0 {
		c.PostsPerDay = 10
	}
	if c.MaxTextChars <= 0 {
		c.MaxTextChars = DefaultMaxTextChars
	}
	if c.MaxBytes <= 0 {
		c.MaxBytes = DefaultMaxBytes
	}
	if c.BoardRetention <= 0 {
		c.BoardRetention = 30 * 24 * time.Hour
	}
}

// Settings are the operator's run-time choices, kept in system_config.
type Settings struct {
	AutoApprove  bool   `json:"auto_approve"`
	PostsPerHour int    `json:"posts_per_hour"`
	PostsPerDay  int    `json:"posts_per_day"`
	Operator     string `json:"operator"`
}

const (
	settingAutoApprove = "post_auto_approve"
	settingPerHour     = "post_per_hour"
	settingPerDay      = "post_per_day"
	settingOperator    = "post_operator"
)

// Queuer queues one text on an interface under an idempotency key
// (engine.Dispatcher).
type Queuer interface {
	QueueDirectSendKeyed(source, key, fingerprint, interfaceID, text string, opts engine.DirectSendOptions) (int64, string, bool, error)
}

// Deps are the service's connections to the kit.
type Deps struct {
	DB    *database.DB
	Queue Queuer
	// Bearer names the interface a post leaves on, and whether the kit has
	// it at all; nil = the configured one, taken as present.
	Bearer func() (ifaceID string, ok bool)
	Now    func() time.Time
}

// SendSource is the submission source of a post's delivery rows.
const SendSource = "post"

// Service runs the post: the portal's state, the approval queue, the sends
// and the replies.
type Service struct {
	cfg  Config
	deps Deps
	mu   sync.Mutex
	// signedIn is the captive detection's memory per client key, so a phone
	// that opened the portal is not sent to it again by its own probes. The
	// database keeps the same mark; this is the fast path.
	signedIn map[string]time.Time
}

// New builds the service. It does nothing until Start.
func New(cfg Config, deps Deps) *Service {
	cfg.defaults()
	if deps.Now == nil {
		deps.Now = time.Now
	}
	s := &Service{cfg: cfg, deps: deps, signedIn: map[string]time.Time{}}
	if deps.Bearer == nil {
		s.deps.Bearer = func() (string, bool) { return cfg.Bearer, true }
	}
	return s
}

// Config is the configuration as defaulted.
func (s *Service) Config() Config { return s.cfg }

// Enabled reports whether the portal listener is configured.
func (s *Service) Enabled() bool { return s.cfg.Listen != "" }

func (s *Service) now() time.Time { return s.deps.Now().UTC() }

// Settings reads the operator's choices, the environment's values where
// nothing was saved.
func (s *Service) Settings() Settings {
	st := Settings{AutoApprove: s.cfg.AutoApprove, PostsPerHour: s.cfg.PostsPerHour, PostsPerDay: s.cfg.PostsPerDay, Operator: s.cfg.Operator}
	if s.deps.DB == nil {
		return st
	}
	if v, err := s.deps.DB.GetSystemConfig(settingAutoApprove); err == nil {
		st.AutoApprove = v == "true"
	}
	if v, err := s.deps.DB.GetSystemConfig(settingPerHour); err == nil {
		if n, err := strconv.Atoi(v); err == nil {
			st.PostsPerHour = n
		}
	}
	if v, err := s.deps.DB.GetSystemConfig(settingPerDay); err == nil {
		if n, err := strconv.Atoi(v); err == nil {
			st.PostsPerDay = n
		}
	}
	if v, err := s.deps.DB.GetSystemConfig(settingOperator); err == nil && strings.TrimSpace(v) != "" {
		st.Operator = strings.TrimSpace(v)
	}
	return st
}

// SetSettings saves the operator's choices.
func (s *Service) SetSettings(st Settings) error {
	if st.PostsPerHour < 0 || st.PostsPerDay < 0 {
		return errors.New("a ceiling is 0 (none) or more")
	}
	op := strings.Join(strings.Fields(st.Operator), " ")
	if len(op) > 80 {
		return errors.New("the operator's name is at most 80 characters")
	}
	auto := "false"
	if st.AutoApprove {
		auto = "true"
	}
	return s.deps.DB.SetSystemConfigs([2]string{settingAutoApprove, auto},
		[2]string{settingPerHour, strconv.Itoa(st.PostsPerHour)},
		[2]string{settingPerDay, strconv.Itoa(st.PostsPerDay)},
		[2]string{settingOperator, op})
}

// Submission is what the write form carries.
type Submission struct {
	Recipient string
	Text      string
	Name      string
	Emergency bool
}

// Create takes a post from a phone: checks it, counts it against the
// phone's ceilings, gives it a code, and sends it at once when the
// approval queue is off. The error is one of the Err* values for the form,
// or another error for the log.
func (s *Service) Create(clientKey, lang string, in Submission) (*database.PostMessage, error) {
	rcpt, err := ParseRecipient(in.Recipient)
	if err != nil {
		return nil, err
	}
	text, err := CleanText(in.Text, s.cfg.MaxTextChars, s.cfg.MaxBytes)
	if err != nil {
		return nil, err
	}
	name, err := CleanName(in.Name)
	if err != nil {
		return nil, err
	}
	st := s.Settings()
	ok, err := s.deps.DB.TakePostSlot(clientKey, s.now(), st.PostsPerHour, st.PostsPerDay)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrQuota
	}
	class := database.PostClassRoutine
	if in.Emergency {
		class = database.PostClassEmergency
	}
	m := &database.PostMessage{Session: clientKey, Recipient: rcpt.Value, Text: text, Name: name, Class: class,
		Status: database.PostStatusPending, Lang: lang, CreatedAt: s.now()}
	// A code is 30^6; a clash is retried, never guessed around.
	for attempt := 0; attempt < 5; attempt++ {
		m.Code = NewCode()
		if existing, err := s.deps.DB.GetPostMessage(m.Code); err != nil {
			return nil, err
		} else if existing != nil {
			continue
		}
		if err := s.deps.DB.InsertPostMessage(m); err != nil {
			return nil, err
		}
		break
	}
	if m.Code == "" {
		return nil, errors.New("no free code")
	}
	log.Info().Str("code", m.Code).Str("class", class).Str("kind", rcpt.Kind).Msg("post: written")
	if st.AutoApprove {
		if err := s.Approve(m.Code); err != nil {
			log.Error().Err(err).Str("code", m.Code).Msg("post: auto-approve failed; left for the operator")
		} else {
			m.Status = database.PostStatusQueued
		}
	}
	return m, nil
}

// Approve sends a pending post. Approving again is harmless: the delivery
// is keyed by the code.
func (s *Service) Approve(code string) error {
	m, err := s.deps.DB.GetPostMessage(code)
	if err != nil {
		return err
	}
	if m == nil {
		return ErrUnknownCode
	}
	switch m.Status {
	case database.PostStatusPending, database.PostStatusQueued, database.PostStatusFailed:
	default:
		return fmt.Errorf("post %s is %s", code, m.Status)
	}
	if s.deps.Queue == nil {
		return errors.New("no dispatcher")
	}
	iface, ok := s.deps.Bearer()
	if !ok {
		return fmt.Errorf("no satellite link for the post (bearer %s)", s.cfg.Bearer)
	}
	rcpt, err := ParseRecipient(m.Recipient)
	if err != nil {
		return err
	}
	emergency := m.Class == database.PostClassEmergency
	text := Compose(rcpt, m.Name, m.Text, m.Code, emergency)
	sum := sha256.Sum256([]byte(text))
	opts := engine.DirectSendOptions{
		Precedence: string(types.PrecedencePriority),
		Class:      database.DeliveryClassPlain,
		MaxRetries: 12,
	}
	if emergency {
		opts.Precedence = string(types.PrecedenceFlash)
		opts.Critical = true
	}
	id, ref, replayed, err := s.deps.Queue.QueueDirectSendKeyed(SendSource, "post:"+code, hex.EncodeToString(sum[:]), iface, text, opts)
	if err != nil {
		return err
	}
	if err := s.deps.DB.SetPostStatus(code, database.PostStatusQueued, ref, id); err != nil {
		return err
	}
	log.Info().Str("code", code).Str("iface", iface).Int64("delivery_id", id).Bool("replayed", replayed).Msg("post: approved and queued")
	return nil
}

// ErrUnknownCode is a code the kit never gave out.
var ErrUnknownCode = errors.New("unknown code")

// Refuse keeps a post from leaving.
func (s *Service) Refuse(code string) error {
	m, err := s.deps.DB.GetPostMessage(code)
	if err != nil {
		return err
	}
	if m == nil {
		return ErrUnknownCode
	}
	if m.Status != database.PostStatusPending {
		return fmt.Errorf("post %s is %s", code, m.Status)
	}
	if err := s.deps.DB.SetPostStatus(code, database.PostStatusRefused, "", 0); err != nil {
		return err
	}
	log.Info().Str("code", code).Msg("post: refused by the operator")
	return nil
}

// Status is a post and its replies, for the pickup page; nil for an
// unknown code.
func (s *Service) Status(code string) (*database.PostMessage, []database.PostReply, error) {
	c := NormaliseCode(code)
	if c == "" {
		return nil, nil, nil
	}
	m, err := s.deps.DB.GetPostMessage(c)
	if err != nil || m == nil {
		return nil, nil, err
	}
	replies, err := s.deps.DB.ListPostReplies(c)
	if err != nil {
		return nil, nil, err
	}
	return m, replies, nil
}

// Queue lists the posts waiting for an operator, newest first.
func (s *Service) Queue(limit int) ([]database.PostMessage, error) {
	return s.deps.DB.ListPostMessages([]string{database.PostStatusPending}, limit)
}

// Recent lists the last posts whatever their status.
func (s *Service) Recent(limit int) ([]database.PostMessage, error) {
	return s.deps.DB.ListPostMessages(nil, limit)
}

// AddBoard puts a line on the safe-and-well board; it expires after the
// retention.
func (s *Service) AddBoard(name, status, place string) (*database.PostBoardEntry, error) {
	name, err := CleanBoardField(name)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, ErrName
	}
	status, err = CleanBoardField(status)
	if err != nil {
		return nil, err
	}
	if status == "" {
		return nil, ErrEmpty
	}
	place, err = CleanBoardField(place)
	if err != nil {
		return nil, err
	}
	now := s.now()
	e := &database.PostBoardEntry{Name: name, Status: status, Place: place, CreatedAt: now, ExpiresAt: now.Add(s.cfg.BoardRetention)}
	if err := s.deps.DB.InsertPostBoardEntry(e, NameKey(name)); err != nil {
		return nil, err
	}
	return e, nil
}

// SearchBoard finds board lines by name; every line when q is empty.
func (s *Service) SearchBoard(q string) ([]database.PostBoardEntry, error) {
	return s.deps.DB.SearchPostBoard(NameKey(q), s.now(), 50)
}

// SignIn marks a phone as having opened the portal: its captive probes get
// "no portal" from now on.
func (s *Service) SignIn(clientKey string) {
	s.mu.Lock()
	s.signedIn[clientKey] = s.now()
	s.mu.Unlock()
	if s.deps.DB != nil {
		if err := s.deps.DB.MarkPostSignedIn(clientKey, s.now()); err != nil {
			log.Warn().Err(err).Msg("post: sign-in mark not saved")
		}
	}
}

// SignedIn reports whether a phone opened the portal.
func (s *Service) SignedIn(clientKey string) bool {
	s.mu.Lock()
	_, ok := s.signedIn[clientKey]
	s.mu.Unlock()
	if ok {
		return true
	}
	if s.deps.DB == nil {
		return false
	}
	sess, err := s.deps.DB.GetPostSession(clientKey)
	if err != nil || sess == nil || !sess.SignedIn {
		return false
	}
	s.mu.Lock()
	s.signedIn[clientKey] = sess.LastSeenAt
	s.mu.Unlock()
	return true
}

// HandleInbound is the gateway text hook: every text a satellite gateway
// receives, as it came. A reply in the "@" lane's form is stored under its
// code; the Hub's "not sent" notice fails the post it names; anything else
// is not the post's. Unknown codes are dropped without a word.
func (s *Service) HandleInbound(ifaceID, from, text string) {
	if !strings.HasPrefix(ifaceID, "iridium") || s.deps.DB == nil {
		return
	}
	if addr, reason, ok := NoticeFromHub(text); ok {
		s.failLatest(addr, reason)
		return
	}
	known := func(code string) bool {
		m, err := s.deps.DB.GetPostMessage(code)
		return err == nil && m != nil
	}
	r, ok := ParseReply(text, known)
	if !ok {
		return
	}
	var m *database.PostMessage
	var err error
	if r.Code != "" {
		m, err = s.deps.DB.GetPostMessage(r.Code)
	} else {
		m, err = s.deps.DB.LatestOpenPostTo(strings.ToLower(r.From))
		if m == nil && err == nil {
			m, err = s.deps.DB.LatestOpenPostTo(r.From)
		}
	}
	if err != nil {
		log.Warn().Err(err).Msg("post: reply lookup failed")
		return
	}
	if m == nil {
		log.Debug().Str("from", r.From).Str("code", r.Code).Msg("post: a reply for no post, dropped")
		return
	}
	if r.Text == "" {
		return
	}
	if err := s.deps.DB.InsertPostReply(&database.PostReply{Code: m.Code, From: r.From, Text: r.Text, ReceivedAt: s.now()}); err != nil {
		log.Error().Err(err).Str("code", m.Code).Msg("post: reply not stored")
		return
	}
	log.Info().Str("code", m.Code).Str("iface", ifaceID).Int("bytes", len(r.Text)).Msg("post: reply stored")
}

func (s *Service) failLatest(address, reason string) {
	m, err := s.deps.DB.LatestOpenPostTo(strings.ToLower(address))
	if err != nil || m == nil {
		return
	}
	if err := s.deps.DB.SetPostStatus(m.Code, database.PostStatusFailed, "", 0); err != nil {
		return
	}
	_ = s.deps.DB.InsertPostReply(&database.PostReply{Code: m.Code, From: "MeshSat Hub", Text: "not sent: " + reason, ReceivedAt: s.now()})
	log.Warn().Str("code", m.Code).Str("reason", reason).Msg("post: the Hub did not send it")
}

// Run follows the queued posts' delivery rows until they are sent or dead,
// and forgets old phones. It returns when ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 15 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick()
		}
	}
}

// Tick is one pass of Run.
func (s *Service) Tick() {
	queued, err := s.deps.DB.ListPostMessages([]string{database.PostStatusQueued}, 200)
	if err != nil {
		log.Warn().Err(err).Msg("post: queue read failed")
		return
	}
	for _, m := range queued {
		if m.DeliveryID == 0 {
			continue
		}
		d, err := s.deps.DB.GetDelivery(m.DeliveryID)
		if err != nil {
			continue
		}
		switch d.Status {
		case "sent", "delivered":
			_ = s.deps.DB.SetPostStatus(m.Code, database.PostStatusSent, "", 0)
			log.Info().Str("code", m.Code).Msg("post: left the kit")
		case "failed", "dead", "expired", "cancelled":
			_ = s.deps.DB.SetPostStatus(m.Code, database.PostStatusFailed, "", 0)
			log.Warn().Str("code", m.Code).Str("error", d.LastError).Msg("post: the delivery did not go out")
		}
	}
	_ = s.deps.DB.PrunePostSessions(s.now().Add(-7 * 24 * time.Hour))
}
