// Command meshsat-demo is the public demonstration of the MeshSat Bridge:
// the Bridge's own web app (the release bundle, unchanged) over made-up API
// answers that always read as now, at bridge.meshsat.net, and the Community
// Post's real portal in its demo mode, at app.safertogether.eu.
//
// It holds nothing that reaches a radio, a host, a modem or the network: no
// route of internal/api, no transport, no gateway, no outbound connection.
// Everything a visitor writes on the portal stays in this process's SQLite
// (a memory volume) and is purged an hour later; a visitor's words never
// appear on the public volunteer screen (internal/demo masks them). The
// real Bridge must never face the internet; this binary is what does.
// [MESHSAT-1845]
package main

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/demo"
	"meshsat/internal/post"
)

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(env(key, "")); err == nil {
		return n
	}
	return def
}

func main() {
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = zerolog.New(os.Stdout).With().Timestamp().Logger()

	bridgeAddr := env("MESHSAT_DEMO_LISTEN", ":6050")
	portalAddr := env("MESHSAT_DEMO_POST_LISTEN", ":8081")
	dataDir := env("MESHSAT_DEMO_DATA", "/data")
	portalURL := env("MESHSAT_DEMO_POST_URL", "https://app.safertogether.eu/")
	trusted := demo.ParseNets(strings.Split(env("MESHSAT_DEMO_TRUSTED_PROXIES", strings.Join(demo.TrustedProxies, ",")), ","))
	retain := time.Duration(envInt("MESHSAT_DEMO_RETAIN_MIN", 60)) * time.Minute

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatal().Err(err).Msg("demo: data directory")
	}
	dbPath := filepath.Join(dataDir, "demo.db")
	// A fresh database at every start: nothing a visitor wrote outlives
	// the process.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(dbPath + suffix)
	}
	db, err := database.New(dbPath)
	if err != nil {
		log.Fatal().Err(err).Msg("demo: database")
	}
	defer db.Close()

	fixtures, err := demo.LoadFixtures()
	if err != nil {
		log.Fatal().Err(err).Msg("demo: fixtures")
	}
	dist, err := fs.Sub(embeddedWeb, "web/dist")
	if err != nil {
		log.Fatal().Err(err).Msg("demo: web app")
	}
	spa, err := demo.NewSPA(dist)
	if err != nil {
		log.Fatal().Err(err).Msg("demo: web app index")
	}

	zone, _ := time.LoadLocation("Europe/Amsterdam")
	svc := post.New(post.Config{
		Listen:         portalAddr,
		PortalURL:      portalURL,
		Operator:       "MeshSat (public demo)",
		Bearer:         demo.DemoInterface,
		PostsPerHour:   envInt("MESHSAT_DEMO_POSTS_PER_HOUR", 5),
		PostsPerDay:    envInt("MESHSAT_DEMO_POSTS_PER_DAY", 20),
		BoardRetention: retain,
		PurgeAfter:     retain,
		Zone:           zone,
		Demo:           true,
		Ceiling: post.Ceiling{
			Posts:   envInt("MESHSAT_DEMO_POSTS_PER_WINDOW", 60),
			Board:   envInt("MESHSAT_DEMO_BOARD_PER_WINDOW", 30),
			Window:  10 * time.Minute,
			MaxRows: envInt("MESHSAT_DEMO_MAX_ROWS", 500),
		},
	}, post.Deps{
		DB:     db,
		Queue:  &demo.Queue{DB: db},
		Bearer: func() (string, bool) { return demo.DemoInterface, true },
	})
	if err := svc.SetSettings(post.Settings{AutoApprove: false, PostsPerHour: envInt("MESHSAT_DEMO_POSTS_PER_HOUR", 5),
		PostsPerDay: envInt("MESHSAT_DEMO_POSTS_PER_DAY", 20), Operator: "MeshSat (public demo)"}); err != nil {
		log.Fatal().Err(err).Msg("demo: post settings")
	}

	sim := &demo.Simulator{Post: svc, DB: db, ApproveAfter: 20 * time.Second, SendAfter: 60 * time.Second,
		ReplyAfter: 60 * time.Second, Retain: retain}

	api := &demo.API{Fixtures: fixtures, Post: svc, DB: db}
	bridge := demo.NewServer(bridgeAddr, demo.BridgeHandler(api, spa))
	portal := demo.NewServer(portalAddr, demo.PortalHandler(svc.Handler(), trusted))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go sim.Run(ctx, 5*time.Second)

	errs := make(chan error, 2)
	for _, s := range []*http.Server{bridge, portal} {
		go func(s *http.Server) {
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}(s)
	}
	log.Info().Str("bridge", bridgeAddr).Str("portal", portalAddr).Str("portal_url", portalURL).
		Int("fixtures", len(fixtures.Keys())).Dur("retain", retain).Msg("meshsat demo serving")

	select {
	case <-ctx.Done():
	case err := <-errs:
		log.Error().Err(err).Msg("demo: listener failed")
	}
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = bridge.Shutdown(shut)
	_ = portal.Shutdown(shut)
}
