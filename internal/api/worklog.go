package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// Worklog is the server side of the agent's worklog endpoints (PRD 6.10).
type Worklog interface {
	// Save stores an entry; clientID makes a resend a no-op. A validation
	// problem is a *WorklogInputError.
	Save(ctx context.Context, date, text, clientID string, promptID int64) (WorklogSaved, error)
	Today(ctx context.Context) (WorklogToday, error)
	// Answer applies "skip" (this slot only; "later" is the same) or "mute"
	// (no more reminders today); ErrPromptClosed when already closed.
	Answer(ctx context.Context, promptID int64, action string) error
	// Mute switches today's reminders off (the tray's "Senyapkan hari ini").
	Mute(ctx context.Context) error
}

// WorklogSaved is the reply of POST /v1/agent/worklog.
type WorklogSaved struct {
	ID        int64 `json:"id"`
	Count     int   `json:"count"`   // entries on the paste's first day now
	Entries   int   `json:"entries"` // entries newly stored by this call (a paste can hold many)
	Duplicate bool  `json:"duplicate"`
}

// WorklogToday is the reply of GET /v1/agent/worklog/today.
type WorklogToday struct {
	Count     int        `json:"count"`
	LastAt    *time.Time `json:"last_at"`
	IsWorkday bool       `json:"is_workday"`
}

// WorklogInputError is a rejected entry (400 with the reason, in Indonesian).
type WorklogInputError struct{ Reason string }

func (e *WorklogInputError) Error() string { return e.Reason }

// ErrPromptClosed means the reminder was answered, skipped or expired.
var ErrPromptClosed = errors.New("prompt closed")

func (s *Server) registerWorklogRoutes(mux *http.ServeMux) {
	if s.db == nil || s.worklog == nil {
		return
	}
	mux.Handle("POST /v1/agent/worklog", s.auth(s.postWorklog))
	mux.Handle("GET /v1/agent/worklog/today", s.auth(s.getWorklogToday))
	mux.Handle("POST /v1/agent/worklog/prompts/{id}/answer", s.auth(s.postPromptAnswer))
	mux.Handle("POST /v1/agent/worklog/mute", s.auth(s.postWorklogMute))
}

func (s *Server) postWorklog(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text     string `json:"text"`
		Date     string `json:"date"`
		PromptID int64  `json:"prompt_id"`
		ClientID string `json:"client_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ClientID == "" || len(req.ClientID) > 64 {
		writeError(w, http.StatusBadRequest, "client_id is required (max 64)")
		return
	}
	res, err := s.worklog.Save(r.Context(), req.Date, req.Text, req.ClientID, req.PromptID)
	var bad *WorklogInputError
	switch {
	case errors.As(err, &bad):
		writeError(w, http.StatusBadRequest, bad.Reason)
		return
	case err != nil:
		s.log.Error("worklog save failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) getWorklogToday(w http.ResponseWriter, r *http.Request) {
	t, err := s.worklog.Today(r.Context())
	if err != nil {
		s.log.Error("worklog today failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) postPromptAnswer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid prompt id")
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Action != "later" && req.Action != "skip" && req.Action != "mute" {
		writeError(w, http.StatusBadRequest, "action must be skip or mute")
		return
	}
	switch err := s.worklog.Answer(r.Context(), id, req.Action); {
	case errors.Is(err, ErrPromptClosed):
		writeError(w, http.StatusConflict, "prompt already closed")
	case err != nil:
		s.log.Error("prompt answer failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) postWorklogMute(w http.ResponseWriter, r *http.Request) {
	if err := s.worklog.Mute(r.Context()); err != nil {
		s.log.Error("worklog mute failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
