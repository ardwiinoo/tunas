package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/agentstats"
	"github.com/ardwiinoo/tunas/internal/apps"
	"github.com/ardwiinoo/tunas/internal/llm"
	"github.com/ardwiinoo/tunas/internal/projects"
	"github.com/ardwiinoo/tunas/internal/store"
)

// Limits for agent input.
const (
	maxBucketsPerBatch = 2000
	maxAppLen          = 128
	maxEventWait       = 25 * time.Second
	maxBucketAge       = 30 * 24 * time.Hour
)

// FormOverrideKey is the settings key for /form override (Phase 3).
const FormOverrideKey = "form_override"

var validCategories = map[string]bool{"work": true, "browse": true, "distract": true, "other": true}

type ctxKey struct{}

func deviceFrom(ctx context.Context) store.Device { return ctx.Value(ctxKey{}).(store.Device) }

// registerAgentRoutes registers the agent endpoints.
func (s *Server) registerAgentRoutes(mux *http.ServeMux) {
	if s.db == nil {
		return
	}
	mux.Handle("POST /v1/agent/activity", s.auth(s.postActivity))
	mux.Handle("GET /v1/agent/events", s.auth(s.getEvents))
	mux.Handle("POST /v1/agent/events/ack", s.auth(s.ackEvents))
	mux.Handle("GET /v1/agent/state", s.auth(s.getState))
	mux.Handle("POST /v1/agent/heartbeat", s.auth(s.postHeartbeat))
	mux.Handle("POST /v1/agent/categories/seed", s.auth(s.postSeedCategories))
	mux.Handle("POST /v1/agent/projects", s.auth(s.postProjects))
	if s.summary != nil {
		mux.Handle("GET /v1/agent/status/summary", s.auth(s.getStatusSummary))
	}
	if s.chat != nil {
		mux.Handle("POST /v1/agent/chat", s.auth(s.postChat))
	}
	if s.confirm != nil {
		mux.Handle("POST /v1/agent/chat/confirm", s.auth(s.postConfirm))
	}
}

// auth requires a valid, unrevoked device token.
func (s *Server) auth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		token = strings.TrimSpace(token)
		if !ok || token == "" || len(token) > 256 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		d, err := s.db.AuthDevice(r.Context(), token, s.now())
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err != nil {
			s.log.Error("device auth failed", "err", err)
			writeError(w, http.StatusInternalServerError, errInternal)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, d)))
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "body too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid json")
		}
		return false
	}
	return true
}

type activityRequest struct {
	Buckets []store.Bucket `json:"buckets"`
}

// ValidateBuckets checks a batch from the agent.
func ValidateBuckets(bs []store.Bucket, now time.Time) error {
	if len(bs) > maxBucketsPerBatch {
		return errors.New("too many buckets")
	}
	for _, b := range bs {
		switch {
		case b.Start.IsZero() || !b.Start.Equal(b.Start.Truncate(time.Minute)):
			return errors.New("bucket_start must be minute-aligned")
		case b.Start.After(now.Add(5*time.Minute)) || b.Start.Before(now.Add(-maxBucketAge)):
			return errors.New("bucket_start out of range")
		case b.App == "" || utf8.RuneCountInString(b.App) > maxAppLen || !utf8.ValidString(b.App):
			return errors.New("invalid app")
		case !validCategories[b.Category]:
			return errors.New("invalid category")
		case b.ActiveSeconds < 0 || b.IdleSeconds < 0 || b.ActiveSeconds+b.IdleSeconds > 60:
			return errors.New("invalid seconds")
		}
	}
	return nil
}

func (s *Server) postActivity(w http.ResponseWriter, r *http.Request) {
	var req activityRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := ValidateBuckets(req.Buckets, s.now()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	d := deviceFrom(r.Context())
	if err := s.db.UpsertBuckets(r.Context(), d.ID, req.Buckets); err != nil {
		s.log.Error("upsert buckets failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"accepted": len(req.Buckets)})
}

func (s *Server) getEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	after, err := strconv.ParseInt(q.Get("after"), 10, 64)
	if q.Get("after") == "" {
		after, err = 0, nil
	}
	if err != nil || after < 0 {
		writeError(w, http.StatusBadRequest, "invalid after")
		return
	}
	wait := maxEventWait
	if v := q.Get("wait"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid wait")
			return
		}
		wait = min(time.Duration(n)*time.Second, maxEventWait)
	}
	d := deviceFrom(r.Context())
	evs, err := s.db.PollEvents(r.Context(), d.ID, after, 100, wait)
	if err != nil {
		if r.Context().Err() != nil {
			return // client went away
		}
		s.log.Error("poll events failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	if evs == nil {
		evs = []store.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": evs})
}

func (s *Server) ackEvents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UpTo int64 `json:"up_to"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.UpTo <= 0 {
		writeError(w, http.StatusBadRequest, "invalid up_to")
		return
	}
	n, err := s.db.AckEvents(r.Context(), deviceFrom(r.Context()).ID, req.UpTo, s.now())
	if err != nil {
		s.log.Error("ack failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"acked": n})
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	var override *string
	var v string
	ok, err := s.db.GetSetting(r.Context(), FormOverrideKey, &v)
	if err != nil {
		s.log.Error("get state failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	if ok && v != "" {
		override = &v
	}
	cats, err := s.db.AppCategories(r.Context())
	if err != nil {
		s.log.Error("get categories failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	blocklist, err := s.db.ProjectBlocklist(r.Context())
	if err != nil {
		s.log.Error("get project blocklist failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	etag := store.CategoriesETag(cats)
	out := map[string]any{"form_override": override, "now": s.now(), "categories_etag": etag, "project_blocklist": blocklist}
	if r.URL.Query().Get("categories_etag") != etag { // the agent already has this mapping otherwise
		out["app_categories"] = cats
	}
	writeJSON(w, http.StatusOK, out)
}

var reEditor = regexp.MustCompile(`^[a-z0-9][a-z0-9._ +-]{0,59}$`)

// ValidateProjectBuckets checks a project upload. Only a project name and an
// editor's process name are accepted: names that look like paths or are too
// long are refused, so a window title cannot ride along.
func ValidateProjectBuckets(bs []store.ProjectBucket, now time.Time) error {
	if len(bs) > maxBucketsPerBatch {
		return errors.New("too many buckets")
	}
	for _, b := range bs {
		name, ok := projects.Clean(b.Project)
		switch {
		case b.Start.IsZero() || !b.Start.Equal(b.Start.Truncate(time.Minute)):
			return errors.New("bucket_start must be minute-aligned")
		case b.Start.After(now.Add(5*time.Minute)) || b.Start.Before(now.Add(-maxBucketAge)):
			return errors.New("bucket_start out of range")
		case !ok || name != b.Project:
			return errors.New("invalid project")
		case !reEditor.MatchString(b.Editor):
			return errors.New("invalid editor")
		case b.ActiveSeconds < 0 || b.ActiveSeconds > 60:
			return errors.New("invalid seconds")
		}
	}
	return nil
}

// postProjects stores the agent's per-minute project time.
func (s *Server) postProjects(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Buckets []store.ProjectBucket `json:"buckets"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := ValidateProjectBuckets(req.Buckets, s.now()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	d := deviceFrom(r.Context())
	n, err := s.db.UpsertProjectBuckets(r.Context(), d.ID, req.Buckets)
	if err != nil {
		s.log.Error("upsert project buckets failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"accepted": n})
}

// maxSeedApps bounds one seed request.
const maxSeedApps = 500

// postSeedCategories adds apps from the agent's config.yaml that the server
// has no category for yet. It never changes an existing choice.
func (s *Server) postSeedCategories(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Apps map[string]string `json:"apps"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Apps) > maxSeedApps {
		writeError(w, http.StatusBadRequest, "too many apps")
		return
	}
	clean := make(map[string]string, len(req.Apps))
	for app, cat := range req.Apps {
		name, ok := apps.NormalizeApp(app)
		if !ok || !validCategories[cat] || strings.Contains(app, "/") || strings.Contains(app, "\\") {
			writeError(w, http.StatusBadRequest, "invalid app or category")
			return
		}
		clean[name] = cat
	}
	n, err := s.db.SeedAppCategories(r.Context(), clean, s.now())
	if err != nil {
		s.log.Error("seed categories failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"added": n})
}

// postHeartbeat records the agent's version and tracking state (last_seen
// is already updated by auth) and returns how many events wait for it.
func (s *Server) postHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string          `json:"version"`
		Paused  bool            `json:"paused"`
		Stats   json.RawMessage `json:"stats"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Version) > 64 {
		writeError(w, http.StatusBadRequest, "invalid version")
		return
	}
	d := deviceFrom(r.Context())
	if err := s.db.Heartbeat(r.Context(), d.ID, req.Version, req.Paused, s.now()); err != nil {
		s.log.Error("heartbeat failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	s.storeAgentStats(r.Context(), d.ID, req.Stats)
	n, err := s.db.PendingEvents(r.Context(), d.ID)
	if err != nil {
		s.log.Error("pending events failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"pending_events": n})
}

// getStatusSummary is the server, VPS and LLM part of /status for the
// laptop's bubble and status card (numbers only).
func (s *Server) getStatusSummary(w http.ResponseWriter, r *http.Request) {
	text, err := s.summary(r.Context())
	if err != nil {
		s.log.Error("status summary failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

// SampleEvery is how often an agent's statistics are also kept as a sample
// (for the 24 h peak and trend in /status).
const SampleEvery = 5 * time.Minute

// storeAgentStats keeps the latest statistics of a device. The payload must
// have the fixed shape of package agentstats; anything else is dropped (the
// heartbeat itself still counts as a sign of life) and only the canonical
// re-encoding is stored, never the bytes the client sent.
func (s *Server) storeAgentStats(ctx context.Context, deviceID int64, raw json.RawMessage) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	st, err := agentstats.Parse(raw)
	if err != nil {
		s.log.Warn("agent statistics dropped", "err", err)
		return
	}
	canon, err := st.Marshal()
	if err != nil {
		return
	}
	if err := s.db.SetDeviceStats(ctx, deviceID, canon); err != nil {
		s.log.Warn("agent statistics not stored", "err", err)
		return
	}
	now := s.now()
	s.statsMu.Lock()
	if s.statsLast == nil {
		s.statsLast = map[int64]time.Time{}
	}
	due := now.Sub(s.statsLast[deviceID]) >= SampleEvery
	if due {
		s.statsLast[deviceID] = now
	}
	s.statsMu.Unlock()
	if due {
		if err := s.db.AddStatsSample(ctx, now.Truncate(time.Second), "agent", deviceID, st); err != nil {
			s.log.Warn("agent sample not stored", "err", err)
		}
	}
}

// MaxChatLen bounds overlay chat input (runes).
const MaxChatLen = 500

// ChatTurn is one earlier message of the bubble conversation.
type ChatTurn struct {
	Role string `json:"role"` // "user" | "assistant"
	Text string `json:"text"`
}

// MaxChatHistory bounds the conversation context sent with a message.
const MaxChatHistory = 6

func (s *Server) postChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text    string     `json:"text"`
		Form    string     `json:"form"`
		History []ChatTurn `json:"history"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" || utf8.RuneCountInString(req.Text) > MaxChatLen || len(req.Form) > 16 {
		writeError(w, http.StatusBadRequest, "invalid text")
		return
	}
	if len(req.History) > MaxChatHistory {
		writeError(w, http.StatusBadRequest, "history too long")
		return
	}
	for _, t := range req.History {
		if (t.Role != "user" && t.Role != "assistant") || t.Text == "" || utf8.RuneCountInString(t.Text) > 2000 {
			writeError(w, http.StatusBadRequest, "invalid history")
			return
		}
	}
	res, err := s.chat(r.Context(), req.Form, req.Text, req.History)
	if r.Context().Err() != nil {
		s.log.Info("chat cancelled by the agent")
		return
	}
	if err != nil {
		// 503 with a machine-readable reason so the overlay can say why
		// (quota used up, LLM too slow, LLM failing).
		writeError(w, http.StatusServiceUnavailable, llm.Kind(err))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ChatResult is the chat reply. A non-empty ConfirmID means Reply is a
// proposal (reminder/task) that is only saved after POST /chat/confirm.
type ChatResult struct {
	Reply     string `json:"reply"`
	ConfirmID string `json:"confirm_id,omitempty"`
	// Search marks a web-search answer (the bubble uses the bicara_cari
	// gesture); Sources are shown after the text has been typed out.
	Search  bool         `json:"search,omitempty"`
	Sources []ChatSource `json:"sources,omitempty"`
}

// ChatSource is a web result an answer points to.
type ChatSource struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

func (s *Server) postConfirm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID  string `json:"id"`
		Yes bool   `json:"yes"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ID == "" || len(req.ID) > 64 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"reply": s.confirm(r.Context(), req.ID, req.Yes)})
}
