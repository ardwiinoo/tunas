// Package api implements the HTTP API used by the laptop agent.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
)

// maxBody is the request body limit (1 MB).
const maxBody = 1 << 20

// Server is the HTTP handler set.
type Server struct {
	log *slog.Logger
	db  *store.DB
	now func() time.Time
	mux *http.ServeMux
	rl  *rateLimiter
}

// Options configures the API.
type Options struct {
	DB  *store.DB // nil: only /healthz is served
	Now func() time.Time
	// RatePerMinute and RateBurst bound requests per client IP on /v1/.
	RatePerMinute, RateBurst int
}

// New builds the HTTP handler.
func New(log *slog.Logger, o Options) *Server {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.RatePerMinute == 0 {
		o.RatePerMinute, o.RateBurst = 120, 30
	}
	s := &Server{log: log, db: o.DB, now: o.Now, mux: http.NewServeMux(),
		rl: newRateLimiter(o.RatePerMinute, o.RateBurst, o.Now)}
	s.mux.HandleFunc("GET /healthz", s.healthz)
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if v := recover(); v != nil {
			s.log.Error("panic in handler", "path", r.URL.Path, "panic", v)
			writeError(w, http.StatusInternalServerError, "internal error")
		}
	}()
	s.mux.ServeHTTP(w, r)
}

// routes registers /v1/ endpoints (rate-limited).
func (s *Server) routes() {
	v1 := http.NewServeMux()
	s.mux.Handle("/v1/", s.rl.middleware(v1))
	s.registerAgentRoutes(v1)
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
