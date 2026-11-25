package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Calendar serves the laptop's worklog calendar and slash commands (Fase 8d).
// The day rules (working day, holiday, ...) are decided on the server, once;
// the laptop only draws them. Replies are JSON-able values.
type Calendar interface {
	// Month is the grid data of "YYYY-MM".
	Month(ctx context.Context, month string) (any, error)
	// Day is the entries of "YYYY-MM-DD".
	Day(ctx context.Context, date string) (any, error)
	// Edit replaces an entry's text; a rejected text is a *WorklogInputError.
	Edit(ctx context.Context, id int64, text string) (any, error)
	// Delete removes an entry; ErrCalendarGone when it is already gone.
	Delete(ctx context.Context, id int64) error
}

// ErrCalendarGone means the entry does not exist (any more).
var ErrCalendarGone = errors.New("entry gone")

// CommandFunc runs a whitelisted slash command and returns its text
// (ok=false: the server has no such command).
type CommandFunc func(ctx context.Context, cmd, args string) (text string, ok bool)

// laptopCommands are the slash commands the bubble may run through
// POST /v1/agent/command (PRD 8d). cal and log are drawn/handled on the laptop.
var laptopCommands = map[string]bool{
	"cal": true, "log": true, "today": true, "tasks": true, "done": true, "status": true,
	"form": true, "apps": true, "rekap": true, "domain": true,
}

// localCommands never need the server's text.
var localCommands = map[string]bool{"cal": true, "log": true}

// UnsupportedText is the answer to a command that is not on the laptop.
const UnsupportedText = "Perintah itu belum ada di laptop, pakai Telegram."

var (
	reMonth = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)
	reDate  = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])$`)
)

func (s *Server) registerCalendarRoutes(mux *http.ServeMux) {
	if s.db == nil {
		return
	}
	if s.calendar != nil {
		mux.Handle("GET /v1/agent/worklog/month", s.auth(s.getCalMonth))
		mux.Handle("GET /v1/agent/worklog/day", s.auth(s.getCalDay))
		mux.Handle("PATCH /v1/agent/worklog/entries/{id}", s.auth(s.patchEntry))
		mux.Handle("DELETE /v1/agent/worklog/entries/{id}", s.auth(s.deleteEntry))
	}
	if s.command != nil {
		mux.Handle("POST /v1/agent/command", s.auth(s.postCommand))
	}
}

func (s *Server) calFail(w http.ResponseWriter, what string, err error) {
	var bad *WorklogInputError
	switch {
	case errors.As(err, &bad):
		writeError(w, http.StatusBadRequest, bad.Reason)
	case errors.Is(err, ErrCalendarGone):
		writeError(w, http.StatusNotFound, "entry not found")
	default:
		s.log.Error(what+" failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
	}
}

func (s *Server) getCalMonth(w http.ResponseWriter, r *http.Request) {
	m := r.URL.Query().Get("m")
	if !reMonth.MatchString(m) {
		writeError(w, http.StatusBadRequest, "m must be YYYY-MM")
		return
	}
	v, err := s.calendar.Month(r.Context(), m)
	if err != nil {
		s.calFail(w, "calendar month", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) getCalDay(w http.ResponseWriter, r *http.Request) {
	d := r.URL.Query().Get("d")
	if !reDate.MatchString(d) {
		writeError(w, http.StatusBadRequest, "d must be YYYY-MM-DD")
		return
	}
	v, err := s.calendar.Day(r.Context(), d)
	if err != nil {
		s.calFail(w, "calendar day", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func entryID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) patchEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := entryID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid entry id")
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	v, err := s.calendar.Edit(r.Context(), id, req.Text)
	if err != nil {
		s.calFail(w, "calendar edit", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := entryID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid entry id")
		return
	}
	if err := s.calendar.Delete(r.Context(), id); err != nil {
		s.calFail(w, "calendar delete", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// CommandResult is the reply of POST /v1/agent/command. Kind is "text"
// (draw Text), "local" (the laptop handles it itself) or "unsupported".
type CommandResult struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

func (s *Server) postCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cmd  string `json:"cmd"`
		Args string `json:"args"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	cmd := strings.ToLower(strings.TrimSpace(req.Cmd))
	args := strings.TrimSpace(req.Args)
	switch {
	case len(args) > 500:
		writeError(w, http.StatusBadRequest, "args too long")
	case !laptopCommands[cmd]:
		writeJSON(w, http.StatusOK, CommandResult{Kind: "unsupported", Text: UnsupportedText})
	case localCommands[cmd]:
		writeJSON(w, http.StatusOK, CommandResult{Kind: "local"})
	default:
		text, ok := s.command(r.Context(), cmd, args)
		if !ok || text == "" {
			writeJSON(w, http.StatusOK, CommandResult{Kind: "unsupported", Text: UnsupportedText})
			return
		}
		writeJSON(w, http.StatusOK, CommandResult{Kind: "text", Text: text})
	}
}
