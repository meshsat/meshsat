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
	"unicode/utf8"

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
	PurgeAfter     time.Duration // how long after the post is closed everything it stored is purged
	Print          bool          // slips go to the kit's receipt printer (Deps.Printer)
	// Zone is the zone the portal shows times in: nil = the process's local
	// zone (time.Local, from TZ; UTC in a container without TZ).
	Zone *time.Location
	// Demo marks the public demo (cmd/meshsat-demo): every page says that
	// nothing is sent and that everything is deleted after an hour, and the
	// about page describes the demo instead of a kit. Only that binary sets
	// it; nothing on a kit reads it from the environment or the API.
	// [MESHSAT-1845]
	Demo bool
	// Ceiling caps the whole post, every phone together: at most Posts
	// posts and Board board lines per Window, and MaxRows rows stored. Zero
	// values are no ceiling (a kit's default; its per-phone ceilings and
	// the approval queue are its limits). A post or line over a ceiling is
	// ErrBusy. [MESHSAT-1845]
	Ceiling Ceiling
}

// Ceiling is Config.Ceiling.
type Ceiling struct {
	Posts   int
	Board   int
	Window  time.Duration
	MaxRows int
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
	if c.PurgeAfter <= 0 {
		c.PurgeAfter = 30 * 24 * time.Hour
	}
	if c.Zone == nil {
		c.Zone = time.Local
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
	settingClosedAt    = "post_closed_at" // RFC 3339; "" = open
	settingPurgedAt    = "post_purged_at" // RFC 3339; the last purge
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
	// Printer takes a slip for an approved post and for a reply; nil = no
	// slips (MESHSAT_POST_PRINT off, or no printer on this kit).
	Printer Printer
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
	printer  printerMemory
	// removeMisses are the wrong board removal codes per phone (its
	// address), for removeTriesPerHour. [MESHSAT-1814]
	removeMisses map[string][]time.Time
	// recentPosts and recentBoard are the times of the posts and board
	// lines inside Config.Ceiling.Window, every phone together.
	recentPosts, recentBoard []time.Time
}

// New builds the service. It does nothing until Start.
func New(cfg Config, deps Deps) *Service {
	cfg.defaults()
	if deps.Now == nil {
		deps.Now = time.Now
	}
	s := &Service{cfg: cfg, deps: deps, signedIn: map[string]time.Time{}, removeMisses: map[string][]time.Time{}}
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
	if s.State().Closed {
		return nil, ErrClosed
	}
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
	if ok, err := s.underCeiling(false); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrBusy
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
// is keyed by the code. A failed post approved again is sent again, keyed
// by the send it failed on, so approving it twice still sends it once.
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
	if m.Status == database.PostStatusQueued && m.DeliveryID > 0 {
		// Already on its way. Its key may be a send-again key, so asking the
		// queue by the code's own key would answer with the first send, the
		// one that failed, and point the post back at it. [MESHSAT-1807]
		return nil
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
	// A post is tried until it leaves or the operator closes the post: a
	// kit under a roof or a heavy sky waits for its satellite. Twelve tries
	// (about 35 minutes) failed both kits' first posts on 6 Nov 2026 while
	// the people who wrote them were still told to wait. [MESHSAT-1807]
	opts := engine.DirectSendOptions{
		Precedence:   string(types.PrecedencePriority),
		Class:        database.DeliveryClassPlain,
		RetryForever: true,
	}
	if emergency {
		opts.Precedence = string(types.PrecedenceFlash)
		opts.Critical = true
	}
	key := "post:" + code
	if m.Status == database.PostStatusFailed && m.DeliveryID > 0 {
		key += ":after:" + strconv.FormatInt(m.DeliveryID, 10)
	}
	id, ref, replayed, err := s.deps.Queue.QueueDirectSendKeyed(SendSource, key, hex.EncodeToString(sum[:]), iface, text, opts)
	if err != nil {
		return err
	}
	if err := s.deps.DB.SetPostStatus(code, database.PostStatusQueued, ref, id); err != nil {
		return err
	}
	log.Info().Str("code", code).Str("iface", iface).Int64("delivery_id", id).Bool("replayed", replayed).Msg("post: approved and queued")
	if !replayed {
		s.printSlip(m, nil)
	}
	return nil
}

// ErrBusy is a post or a board line over Config.Ceiling. [MESHSAT-1845]
var ErrBusy = errors.New("busy")

// underCeiling answers whether one more post (board=false) or board line
// fits Config.Ceiling, and counts it when it does.
func (s *Service) underCeiling(board bool) (bool, error) {
	c := s.cfg.Ceiling
	limit, list := c.Posts, &s.recentPosts
	if board {
		limit, list = c.Board, &s.recentBoard
	}
	if c.MaxRows > 0 {
		n, err := s.deps.DB.CountPostRows()
		if err != nil {
			return false, err
		}
		if n >= c.MaxRows {
			return false, nil
		}
	}
	if limit <= 0 || c.Window <= 0 {
		return true, nil
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := (*list)[:0]
	for _, t := range *list {
		if now.Sub(t) < c.Window {
			kept = append(kept, t)
		}
	}
	*list = kept
	if len(kept) >= limit {
		return false, nil
	}
	*list = append(*list, now)
	return true, nil
}

// ErrUnknownCode is a code the kit never gave out.
var ErrUnknownCode = errors.New("unknown code")

// ErrClosed is the post refusing a new post or board line after the
// operator closed it.
var ErrClosed = errors.New("closed")

// The "Safe and well" board is looked up by name, never browsed, and a
// resident takes their own line off with the code they got. [MESHSAT-1814]
var (
	// ErrQueryShort is a search with fewer than boardMinLetters letters.
	ErrQueryShort = errors.New("query_short")
	// ErrBadRemoveCode is a removal code no line on the board has.
	ErrBadRemoveCode = errors.New("remove_code")
	// ErrRemoveTries is a phone that gave removeTriesPerHour wrong codes
	// within the hour.
	ErrRemoveTries = errors.New("remove_tries")
)

const (
	boardMinLetters    = 3  // letters in a search, all words together
	boardResults       = 20 // lines a search shows at most
	removeTriesPerHour = 5  // wrong removal codes per phone per hour
)

// boardCodeHash is what the board keeps of a removal code.
func boardCodeHash(code string) string {
	h := sha256.Sum256([]byte("board:" + code))
	return hex.EncodeToString(h[:])
}

// State is whether the post is open, and when what it stored goes.
type State struct {
	Closed   bool
	ClosedAt time.Time // zero while open
	PurgeAt  time.Time // ClosedAt + PurgeAfter; zero while open
	PurgedAt time.Time // the last purge; zero when none
}

// State reads the post's state from system_config.
func (s *Service) State() State {
	var st State
	if s.deps.DB == nil {
		return st
	}
	if v, err := s.deps.DB.GetSystemConfig(settingClosedAt); err == nil && v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			st.Closed, st.ClosedAt, st.PurgeAt = true, t, t.Add(s.cfg.PurgeAfter)
		}
	}
	if v, err := s.deps.DB.GetSystemConfig(settingPurgedAt); err == nil && v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			st.PurgedAt = t
		}
	}
	return st
}

// Close closes the post: the portal takes no new post and no board line,
// the posts still waiting for the satellite are stopped (they show as not
// sent), replies can still be picked up, and everything the portal stored is
// purged PurgeAfter later by the sweep in Run, or sooner by Purge. Closing
// a closed post changes nothing.
func (s *Service) Close() (State, error) {
	st := s.State()
	if st.Closed {
		return st, nil
	}
	if err := s.deps.DB.SetSystemConfig(settingClosedAt, s.now().Format(time.RFC3339Nano)); err != nil {
		return st, err
	}
	st = s.State()
	log.Info().Time("purge_at", st.PurgeAt).Msg("post: closed by the operator")
	// The posts still waiting for the satellite stop here and show as not
	// sent; the operator can send one again. [MESHSAT-1807]
	if n, err := s.deps.DB.CancelWaitingPostSends(database.PostSendStoppedClosed); err != nil {
		log.Warn().Err(err).Msg("post: closed, but the posts still waiting for the satellite were not stopped")
	} else if n > 0 {
		log.Info().Int64("posts", n).Msg("post: closed; the posts still waiting for the satellite were stopped and show as not sent")
	}
	return st, nil
}

// Open reopens a closed post; the purge it scheduled is off.
func (s *Service) Open() (State, error) {
	if err := s.deps.DB.SetSystemConfigs([2]string{settingClosedAt, ""}, [2]string{settingPurgedAt, ""}); err != nil {
		return s.State(), err
	}
	log.Info().Msg("post: reopened by the operator")
	return s.State(), nil
}

// Purge removes everything the portal stored, now: the posts, the replies,
// the board, the phones' counters and sign-in marks. The post's state
// (open or closed) stays as it is.
func (s *Service) Purge() (database.PostPurgeCounts, error) {
	c, err := s.deps.DB.PurgePost()
	if err != nil {
		return c, err
	}
	s.mu.Lock()
	s.signedIn = map[string]time.Time{}
	s.mu.Unlock()
	if err := s.deps.DB.SetSystemConfig(settingPurgedAt, s.now().Format(time.RFC3339Nano)); err != nil {
		log.Warn().Err(err).Msg("post: purge time not saved")
	}
	log.Info().Int64("messages", c.Messages).Int64("replies", c.Replies).Int64("board", c.Board).Int64("sessions", c.Sessions).
		Int64("stopped", c.Stopped).Msg("post: purged")
	return c, nil
}

// sweep purges a closed post once its time has come, once per closing.
func (s *Service) sweep() {
	st := s.State()
	if !st.Closed || s.now().Before(st.PurgeAt) {
		return
	}
	if !st.PurgedAt.IsZero() && !st.PurgedAt.Before(st.ClosedAt) {
		return
	}
	if _, err := s.Purge(); err != nil {
		log.Warn().Err(err).Msg("post: the scheduled purge failed; tried again next tick")
	}
}

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
// retention. The removal code comes back once, here; the board keeps only
// its hash. [MESHSAT-1814]
func (s *Service) AddBoard(name, status, place string) (*database.PostBoardEntry, string, error) {
	if s.State().Closed {
		return nil, "", ErrClosed
	}
	name, err := CleanBoardField(name)
	if err != nil {
		return nil, "", err
	}
	if name == "" {
		return nil, "", ErrName
	}
	status, err = CleanBoardField(status)
	if err != nil {
		return nil, "", err
	}
	if status == "" {
		return nil, "", ErrEmpty
	}
	place, err = CleanBoardField(place)
	if err != nil {
		return nil, "", err
	}
	if ok, err := s.underCeiling(true); err != nil {
		return nil, "", err
	} else if !ok {
		return nil, "", ErrBusy
	}
	now := s.now()
	code := NewCode()
	e := &database.PostBoardEntry{Name: name, Status: status, Place: place, CreatedAt: now, ExpiresAt: now.Add(s.cfg.BoardRetention), RemoveHash: boardCodeHash(code)}
	if err := s.deps.DB.InsertPostBoardEntry(e, NameKey(name)); err != nil {
		return nil, "", err
	}
	return e, code, nil
}

// SearchBoard finds the lines whose name holds every word of q as a whole
// word: "papadaki" finds "Maria Papadaki", "mar" finds nobody. Fewer than
// boardMinLetters letters is ErrQueryShort: the board is looked up by
// name, never browsed. [MESHSAT-1814]
func (s *Service) SearchBoard(q string) ([]database.PostBoardEntry, error) {
	words := strings.Fields(NameKey(q))
	letters := 0
	for _, w := range words {
		letters += utf8.RuneCountInString(w)
	}
	if letters < boardMinLetters {
		return nil, ErrQueryShort
	}
	return s.deps.DB.SearchPostBoard(words, s.now(), boardResults)
}

// RemoveBoard takes a line off the board with the code its writer got.
// A phone (by address, so a fresh cookie does not reset it) that gave
// removeTriesPerHour wrong codes within the hour waits. Allowed while the
// post is closed: taking a name off is never refused. [MESHSAT-1814]
func (s *Service) RemoveBoard(phone, code string) error {
	now := s.now()
	s.mu.Lock()
	misses := s.removeMisses[phone][:0]
	for _, t := range s.removeMisses[phone] {
		if now.Sub(t) < time.Hour {
			misses = append(misses, t)
		}
	}
	s.removeMisses[phone] = misses
	blocked := len(misses) >= removeTriesPerHour
	s.mu.Unlock()
	if blocked {
		return ErrRemoveTries
	}
	if c := NormaliseCode(code); c != "" {
		ok, err := s.deps.DB.DeletePostBoardByCode(boardCodeHash(c))
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	s.mu.Lock()
	s.removeMisses[phone] = append(s.removeMisses[phone], now)
	s.mu.Unlock()
	return ErrBadRemoveCode
}

// BoardLines is every current line, for the operator. [MESHSAT-1814]
func (s *Service) BoardLines() ([]database.PostBoardEntry, error) {
	return s.deps.DB.ListPostBoard(s.now())
}

// RemoveBoardLine takes one line off on the operator's request; false when
// there was no such line. [MESHSAT-1814]
func (s *Service) RemoveBoardLine(id int64) (bool, error) {
	return s.deps.DB.DeletePostBoardEntry(id)
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
	reply := &database.PostReply{Code: m.Code, From: r.From, Text: r.Text, ReceivedAt: s.now()}
	if err := s.deps.DB.InsertPostReply(reply); err != nil {
		log.Error().Err(err).Str("code", m.Code).Msg("post: reply not stored")
		return
	}
	log.Info().Str("code", m.Code).Str("iface", ifaceID).Int("bytes", len(r.Text)).Msg("post: reply stored")
	s.printSlip(m, reply)
}

// The Hub's "not sent" notice is kept as a reply from HubNoticeFrom with
// the text HubNoticePrefix + the Hub's reason (the slip prints it so, in
// English); the portal shows it in the page's language. A person's reply
// can never come from HubNoticeFrom: the reply form's address has no space.
const (
	HubNoticeFrom   = "MeshSat Hub"
	HubNoticePrefix = "not sent: "
)

func (s *Service) failLatest(address, reason string) {
	m, err := s.deps.DB.LatestOpenPostTo(strings.ToLower(address))
	if err != nil || m == nil {
		return
	}
	if err := s.deps.DB.SetPostStatus(m.Code, database.PostStatusFailed, "", 0); err != nil {
		return
	}
	reply := &database.PostReply{Code: m.Code, From: HubNoticeFrom, Text: HubNoticePrefix + reason, ReceivedAt: s.now()}
	if err := s.deps.DB.InsertPostReply(reply); err == nil {
		s.printSlip(m, reply)
	}
	log.Warn().Str("code", m.Code).Str("reason", reason).Msg("post: the Hub did not send it")
}

// Run follows the queued posts' delivery rows until they are sent or dead,
// forgets old phones, and purges a closed post when its time has come. It
// returns when ctx ends.
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
	// Expired board lines go now, not at the next add. [MESHSAT-1814]
	if n, err := s.deps.DB.PrunePostBoard(s.now()); err != nil {
		log.Warn().Err(err).Msg("post: board prune failed")
	} else if n > 0 {
		log.Info().Int64("lines", n).Msg("post: expired board lines removed")
	}
	s.sweep()
}
