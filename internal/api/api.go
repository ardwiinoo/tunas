// Package api implements the HTTP API used by the laptop agent.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
)

// maxBody is the request body limit (1 MB).
const maxBody = 1 << 20

const errInternal = "internal error"

// Server is the HTTP handler set.
type Server struct {
	log     *slog.Logger
	db      *store.DB
	now     func() time.Time
	mux     *http.ServeMux
	rl      *rateLimiter
	chat    func(ctx context.Context, form, text string, history []ChatTurn) (ChatResult, error)
	confirm func(ctx context.Context, id string, yes bool) string
	worklog Worklog
}

// Options configures the API.
type Options struct {
	DB  *store.DB // nil: only /healthz is served
	Now func() time.Time
	// RatePerMinute and RateBurst bound requests per client IP on /v1/.
	RatePerMinute, RateBurst int
	// Chat answers overlay chat (form may be "" for the server's active form).
	// An error is an LLM failure, reported to the agent as llm.Kind.
	Chat func(ctx context.Context, form, text string, history []ChatTurn) (ChatResult, error)
	// Confirm resolves a proposal returned by Chat (ChatResult.ConfirmID).
	Confirm func(ctx context.Context, id string, yes bool) string
	// Worklog serves the agent's worklog endpoints (Phase 7). Optional.
	Worklog Worklog
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
		rl: newRateLimiter(o.RatePerMinute, o.RateBurst, o.Now), chat: o.Chat, confirm: o.Confirm, worklog: o.Worklog}
	s.mux.HandleFunc("GET /healthz", s.healthz)
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if v := recover(); v != nil {
			s.log.Error("panic in handler", "path", r.URL.Path, "panic", v)
			writeError(w, http.StatusInternalServerError, errInternal)
		}
	}()
	s.mux.ServeHTTP(w, r)
}

// routes registers /v1/ endpoints (rate-limited).
func (s *Server) routes() {
	v1 := http.NewServeMux()
	s.mux.Handle("/v1/", s.rl.middleware(v1))
	s.registerAgentRoutes(v1)
	s.registerWorklogRoutes(v1)
}

// healthz reports "ok" when the server (and its database, if configured) works.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if s.db != nil {
		if err := s.db.Healthy(r.Context()); err != nil {
			s.log.Warn("healthz: database unavailable", "err", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("db unavailable"))
			return
		}
	}
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
