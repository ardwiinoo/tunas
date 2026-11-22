package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/llm"
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
	if s.chat != nil {
		mux.Handle("POST /v1/agent/chat", s.auth(s.postChat))
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
	writeJSON(w, http.StatusOK, map[string]any{"form_override": override, "now": s.now()})
}

// MaxChatLen bounds overlay chat input (runes).
const MaxChatLen = 500

func (s *Server) postChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
		Form string `json:"form"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" || utf8.RuneCountInString(req.Text) > MaxChatLen || len(req.Form) > 16 {
		writeError(w, http.StatusBadRequest, "invalid text")
		return
	}
	reply, err := s.chat(r.Context(), req.Form, req.Text)
	if err != nil {
		// 503 with a machine-readable reason so the overlay can say why
		// (quota used up, LLM too slow, LLM failing).
		writeError(w, http.StatusServiceUnavailable, llm.Kind(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"reply": reply})
}
