// Package api implements the HTTP API used by the laptop agent.
package api

import (
	"log/slog"
	"net/http"
)

// Server is the HTTP handler set.
type Server struct {
	log *slog.Logger
	mux *http.ServeMux
}

// New builds the HTTP handler.
func New(log *slog.Logger) *Server {
	s := &Server{log: log, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", s.healthz)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}
