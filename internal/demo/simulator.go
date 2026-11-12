package demo

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/post"
)

// The demo's satellite link: the interface every approved post is queued
// on. It has no radio behind it; the Simulator alone moves its rows.
const DemoInterface = "iridium_demo"

// Queue is the demo's dispatcher for the Community Post: it writes the
// delivery row the post follows, and nothing else. The row keeps no text
// (its preview says "(demo)"), so a visitor's words live only in the post
// rows the hourly purge removes.
type Queue struct {
	DB *database.DB
}

// QueueDirectSendKeyed is post.Queuer: one row per key, replayed on a
// second approve.
func (q *Queue) QueueDirectSendKeyed(source, key, fingerprint, interfaceID, text string, opts engine.DirectSendOptions) (int64, string, bool, error) {
	ref := "demo-" + key
	var id int64
	err := q.DB.QueryRow(`SELECT id FROM message_deliveries WHERE msg_ref = ? ORDER BY id DESC LIMIT 1`, ref).Scan(&id)
	if err == nil && id > 0 {
		return id, ref, true, nil
	}
	id, err = q.DB.InsertDelivery(database.MessageDelivery{MsgRef: ref, Channel: interfaceID, Status: "queued", Priority: 1,
		TextPreview: "(demo)", Visited: "[]", QoSLevel: 1, Class: opts.Class, Precedence: opts.Precedence})
	if err != nil {
		return 0, "", false, err
	}
	return id, ref, false, nil
}

// Simulator is the demo's volunteer and sky. A post waiting for the
// volunteer is approved after ApproveAfter; its send is marked sent
// SendAfter later; ReplyAfter after that an automatic reply comes back in
// the post's language, through the post's own reply path. Everything is
// purged Retain after it was written.
type Simulator struct {
	Post *post.Service
	DB   *database.DB
	Now  func() time.Time

	ApproveAfter time.Duration
	SendAfter    time.Duration
	ReplyAfter   time.Duration
	Retain       time.Duration

	// When each post was approved and sent, by the simulator's own clock
	// (the post rows' updated_at is the process clock's, not Now's).
	approved, sent map[string]time.Time
}

func (s *Simulator) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Run steps the simulator every interval until ctx ends.
func (s *Simulator) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Step()
		}
	}
}

// Step is one pass: approve, send, reply, purge. Each part is idempotent,
// so a missed tick only delays it.
func (s *Simulator) Step() {
	now := s.now()
	if s.approved == nil {
		s.approved, s.sent = map[string]time.Time{}, map[string]time.Time{}
	}
	pending, err := s.Post.Queue(500)
	if err != nil {
		log.Warn().Err(err).Msg("demo: queue read failed")
	}
	for _, m := range pending {
		if now.Sub(m.CreatedAt) < s.ApproveAfter {
			continue
		}
		if err := s.Post.Approve(m.Code); err != nil {
			log.Warn().Err(err).Str("code", m.Code).Msg("demo: approve failed")
			continue
		}
		s.approved[m.Code] = now
	}
	queued, err := s.DB.ListPostMessages([]string{database.PostStatusQueued}, 500)
	if err != nil {
		log.Warn().Err(err).Msg("demo: queued read failed")
	}
	for _, m := range queued {
		at, ok := s.approved[m.Code]
		if !ok {
			s.approved[m.Code] = now // approved before a restart of the simulator
			continue
		}
		if m.DeliveryID == 0 || now.Sub(at) < s.SendAfter {
			continue
		}
		if err := s.DB.SetDeliveryStatus(m.DeliveryID, "sent", "", "demo"); err != nil {
			log.Warn().Err(err).Str("code", m.Code).Msg("demo: send mark failed")
			continue
		}
		s.sent[m.Code] = now
	}
	// The post's own tick turns a sent delivery into a sent post.
	s.Post.Tick()
	sent, err := s.DB.ListPostMessages([]string{database.PostStatusSent}, 500)
	if err != nil {
		log.Warn().Err(err).Msg("demo: sent read failed")
	}
	for _, m := range sent {
		at, ok := s.sent[m.Code]
		if !ok {
			s.sent[m.Code] = now
			continue
		}
		if now.Sub(at) < s.ReplyAfter {
			continue
		}
		reply := post.T(m.Lang, "demo_reply")
		s.Post.HandleInbound(DemoInterface, "", "[from "+m.Recipient+"] #"+m.Code+" "+reply)
	}
	// Forget the posts the simulator has no more steps for.
	for code := range s.approved {
		if m, _ := s.DB.GetPostMessage(code); m == nil || (m.Status != database.PostStatusQueued && m.Status != database.PostStatusSent) {
			delete(s.approved, code)
			delete(s.sent, code)
		}
	}
	if s.Retain > 0 {
		if c, err := s.DB.PurgePostOlderThan(now.Add(-s.Retain)); err != nil {
			log.Warn().Err(err).Msg("demo: purge failed")
		} else if c.Messages+c.Board > 0 {
			log.Info().Int64("posts", c.Messages).Int64("board", c.Board).Msg("demo: an hour old, purged")
		}
	}
}
