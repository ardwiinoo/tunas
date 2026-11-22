package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

type harness struct {
	t     *testing.T
	db    *store.DB
	srv   *httptest.Server
	token string
	now   time.Time
}

func newHarness(t *testing.T) *harness {
	db := storetest.New(t)
	_, token, err := db.CreateDevice(context.Background(), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, db: db, token: token, now: time.Now().Truncate(time.Minute)}
	h.srv = httptest.NewServer(New(slog.New(slog.DiscardHandler), Options{DB: db, Now: func() time.Time { return h.now }}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) do(method, path, token string, body any) (int, map[string]any) {
	h.t.Helper()
	var rd *bytes.Reader
	if s, ok := body.(string); ok {
		rd = bytes.NewReader([]byte(s))
	} else {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAgentAuth(t *testing.T) {
	h := newHarness(t)
	for _, tok := range []string{"", "wrong"} {
		if code, _ := h.do("GET", "/v1/agent/state", tok, nil); code != 401 {
			t.Fatalf("token %q → %d", tok, code)
		}
	}
	if code, _ := h.do("GET", "/v1/agent/state", h.token, nil); code != 200 {
		t.Fatalf("valid token → %d", code)
	}
	_ = h.db.RevokeDevice(context.Background(), "laptop", h.now)
	if code, _ := h.do("GET", "/v1/agent/state", h.token, nil); code != 401 {
		t.Fatal("revoked token accepted")
	}
}

func TestActivityIdempotent(t *testing.T) {
	h := newHarness(t)
	m := h.now.Add(-3 * time.Minute)
	batch := map[string]any{"buckets": []map[string]any{
		{"bucket_start": m, "app": "Code.exe", "category": "work", "active_seconds": 50, "idle_seconds": 10},
		{"bucket_start": m.Add(time.Minute), "app": "Code.exe", "category": "work", "active_seconds": 20, "idle_seconds": 0},
	}}
	for i := 0; i < 2; i++ { // resend → no duplicates
		if code, out := h.do("POST", "/v1/agent/activity", h.token, batch); code != 200 {
			t.Fatalf("post → %d %v", code, out)
		}
	}
	// a later full total for the same minute replaces it
	update := map[string]any{"buckets": []map[string]any{
		{"bucket_start": m.Add(time.Minute), "app": "Code.exe", "category": "work", "active_seconds": 60, "idle_seconds": 0},
	}}
	h.do("POST", "/v1/agent/activity", h.token, update)

	bs, _ := h.db.BucketsBetween(context.Background(), m.Add(-time.Hour), m.Add(time.Hour))
	if len(bs) != 2 || bs[0].ActiveSeconds != 50 || bs[1].ActiveSeconds != 60 {
		t.Fatalf("buckets = %+v", bs)
	}
}

func TestActivityValidation(t *testing.T) {
	h := newHarness(t)
	m := h.now.Add(-time.Minute)
	bad := []map[string]any{
		{"bucket_start": m.Add(10 * time.Second), "app": "a", "category": "work", "active_seconds": 1},
		{"bucket_start": m, "app": "", "category": "work", "active_seconds": 1},
		{"bucket_start": m, "app": "a", "category": "games", "active_seconds": 1},
		{"bucket_start": m, "app": "a", "category": "work", "active_seconds": 50, "idle_seconds": 20},
		{"bucket_start": m.Add(time.Hour), "app": "a", "category": "work", "active_seconds": 1},
		{"bucket_start": m, "app": strings.Repeat("x", 200), "category": "work", "active_seconds": 1},
		{"bucket_start": m, "app": "a", "category": "work", "active_seconds": 1, "title": "secret doc"},
	}
	for i, b := range bad {
		if code, _ := h.do("POST", "/v1/agent/activity", h.token, map[string]any{"buckets": []any{b}}); code != 400 {
			t.Errorf("case %d → %d, want 400", i, code)
		}
	}
	if code, _ := h.do("POST", "/v1/agent/activity", h.token, "{not json"); code != 400 {
		t.Error("bad json accepted")
	}
	big := `{"buckets":[` + strings.Repeat(`{"app":"`+strings.Repeat("x", 1000)+`"},`, 1100) + `{}]}`
	if code, _ := h.do("POST", "/v1/agent/activity", h.token, big); code != 413 {
		t.Errorf("1MB+ body → %d, want 413", code)
	}
}

func TestEventsLongPollAndAck(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	start := time.Now()
	code, out := h.do("GET", "/v1/agent/events?after=0&wait=1", h.token, nil)
	if code != 200 || len(out["events"].([]any)) != 0 || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("empty poll = %d %v after %v", code, out, time.Since(start))
	}

	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = h.db.CreateEvent(ctx, "reminder.due", map[string]any{"text": "tes"})
	}()
	start = time.Now()
	_, out = h.do("GET", "/v1/agent/events?after=0&wait=10", h.token, nil)
	evs := out["events"].([]any)
	if len(evs) != 1 || time.Since(start) > 3*time.Second {
		t.Fatalf("poll = %v after %v", out, time.Since(start))
	}
	ev := evs[0].(map[string]any)
	if ev["type"] != "reminder.due" {
		t.Fatalf("event = %v", ev)
	}
	id := int64(ev["id"].(float64))

	if code, _ := h.do("POST", "/v1/agent/events/ack", h.token, map[string]any{"up_to": id}); code != 200 {
		t.Fatal("ack failed")
	}
	_, out = h.do("GET", fmt.Sprintf("/v1/agent/events?after=0&wait=0"), h.token, nil)
	if len(out["events"].([]any)) != 0 {
		t.Fatalf("acked event returned again: %v", out)
	}
	if code, _ := h.do("GET", "/v1/agent/events?after=x", h.token, nil); code != 400 {
		t.Fatal("bad after accepted")
	}
}

func TestState(t *testing.T) {
	h := newHarness(t)
	_, out := h.do("GET", "/v1/agent/state", h.token, nil)
	if out["form_override"] != nil || out["now"] == nil {
		t.Fatalf("state = %v", out)
	}
	if err := h.db.SetSetting(context.Background(), FormOverrideKey, "fokus"); err != nil {
		t.Fatal(err)
	}
	_, out = h.do("GET", "/v1/agent/state", h.token, nil)
	if out["form_override"] != "fokus" {
		t.Fatalf("state = %v", out)
	}
}
