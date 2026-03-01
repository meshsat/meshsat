package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/transport"
)

// Server holds the API dependencies.
type Server struct {
	db        *database.DB
	mesh      transport.MeshTransport
	processor *engine.Processor
}

// NewServer creates a new API server.
func NewServer(db *database.DB, mesh transport.MeshTransport, proc *engine.Processor) *Server {
	return &Server{
		db:        db,
		mesh:      mesh,
		processor: proc,
	}
}

// Router builds the chi router with all API routes.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(corsMiddleware)

	// Health check
	r.Get("/health", s.handleHealth)

	// API routes
	r.Route("/api", func(r chi.Router) {
		r.Get("/messages", s.handleGetMessages)
		r.Get("/messages/stats", s.handleGetMessageStats)
		r.Post("/messages/send", s.handleSendMessage)

		r.Get("/telemetry", s.handleGetTelemetry)
		r.Get("/positions", s.handleGetPositions)

		r.Get("/nodes", s.handleGetNodes)
		r.Get("/status", s.handleGetStatus)

		r.Get("/events", s.handleSSE)

		r.Get("/gateways", s.handleGetGateways)
	})

	return r
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-HAL-Key")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
