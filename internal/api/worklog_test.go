package api

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

type fakeWorklog struct{ saved map[string]int }

func (f *fakeWorklog) Save(_ context.Context, date, text, clientID string, promptID int64) (WorklogSaved, error) {
	if text == "" {
		return WorklogSaved{}, &WorklogInputError{Reason: "teks catatan kosong"}
	}
	f.saved[clientID]++
	return WorklogSaved{ID: 7, Count: 1, Duplicate: f.saved[clientID] > 1}, nil
}

func (f *fakeWorklog) Today(context.Context) (WorklogToday, error) {
	last := time.Date(2026, 11, 23, 3, 0, 0, 0, time.UTC)
	return WorklogToday{Count: 2, LastAt: &last, IsWorkday: true}, nil
}

func (f *fakeWorklog) Answer(_ context.Context, id int64, _ string) error {
	if id == 99 {
		return ErrPromptClosed
	}
	return nil
}

func TestWorklogEndpoints(t *testing.T) {
	h := newHarness(t)
	db := storetest.New(t)
	_, token, _ := db.CreateDevice(context.Background(), "laptop")
	h.srv = httptest.NewServer(New(slog.New(slog.DiscardHandler), Options{DB: db, Worklog: &fakeWorklog{saved: map[string]int{}}}))
	t.Cleanup(h.srv.Close)
	h.token = token

	body := map[string]any{"text": "rapat", "client_id": "c1", "prompt_id": 3}
	if code, out := h.do("POST", "/v1/agent/worklog", h.token, body); code != 200 || out["duplicate"] != false {
		t.Fatalf("save %d %v", code, out)
	}
	if code, out := h.do("POST", "/v1/agent/worklog", h.token, body); code != 200 || out["duplicate"] != true {
		t.Fatalf("resend %d %v", code, out)
	}
	if code, out := h.do("POST", "/v1/agent/worklog", h.token, map[string]any{"text": "", "client_id": "c2"}); code != 400 || out["error"] != "teks catatan kosong" {
		t.Fatalf("invalid %d %v", code, out)
	}
	if code, _ := h.do("POST", "/v1/agent/worklog", h.token, map[string]any{"text": "x"}); code != 400 {
		t.Fatal("client_id is required")
	}
	if code, out := h.do("GET", "/v1/agent/worklog/today", h.token, nil); code != 200 || out["count"] != float64(2) || out["is_workday"] != true {
		t.Fatalf("today %d %v", code, out)
	}
	if code, _ := h.do("POST", "/v1/agent/worklog/prompts/5/answer", h.token, map[string]string{"action": "later"}); code != 200 {
		t.Fatal("answer")
	}
	if code, _ := h.do("POST", "/v1/agent/worklog/prompts/99/answer", h.token, map[string]string{"action": "skip"}); code != 409 {
		t.Fatal("closed prompt → 409")
	}
	if code, _ := h.do("POST", "/v1/agent/worklog/prompts/5/answer", h.token, map[string]string{"action": "hapus"}); code != 400 {
		t.Fatal("bad action")
	}
	if code, _ := h.do("POST", "/v1/agent/worklog", "", body); code != 401 {
		t.Fatal("device token required")
	}
}
