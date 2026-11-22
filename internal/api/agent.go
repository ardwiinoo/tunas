package api

import "net/http"

// registerAgentRoutes registers the agent endpoints (Phase 2).
func (s *Server) registerAgentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
}
