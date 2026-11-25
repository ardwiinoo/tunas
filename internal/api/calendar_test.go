package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

type fakeCal struct {
	deleted []int64
	edited  map[int64]string
}

func (f *fakeCal) Month(_ context.Context, m string) (any, error) {
	return map[string]any{"month": m, "days": []any{}}, nil
}

func (f *fakeCal) Day(_ context.Context, d string) (any, error) {
	return map[string]any{"date": d, "entries": []any{}}, nil
}

func (f *fakeCal) Edit(_ context.Context, id int64, text string) (any, error) {
	switch {
	case id == 404:
		return nil, ErrCalendarGone
	case strings.TrimSpace(text) == "":
		return nil, &WorklogInputError{Reason: "teks catatan kosong"}
	case id == 500:
		return nil, errors.New("pq: connection refused to 10.0.0.5")
	}
	f.edited[id] = text
	return map[string]any{"id": id, "text": text}, nil
}

func (f *fakeCal) Delete(_ context.Context, id int64) error {
	if id == 404 {
		return ErrCalendarGone
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func calHarness(t *testing.T) (*harness, *fakeCal, *[]string) {
	db := storetest.New(t)
	_, tok, _ := db.CreateDevice(context.Background(), "laptop")
	cal := &fakeCal{edited: map[int64]string{}}
	var ran []string
	srv := httptest.NewServer(New(slog.New(slog.DiscardHandler), Options{DB: db, Calendar: cal,
		Command: func(_ context.Context, cmd, args string) (string, bool) {
			ran = append(ran, cmd+" "+args)
			if cmd == "domain" {
				return "", false
			}
			return "hasil " + cmd, true
		}}))
	t.Cleanup(srv.Close)
	return &harness{t: t, srv: srv, token: tok}, cal, &ran
}

func TestCalendarEndpoints(t *testing.T) {
	h, cal, _ := calHarness(t)
	if code, out := h.do("GET", "/v1/agent/worklog/month?m=2026-11", h.token, nil); code != 200 || out["month"] != "2026-11" {
		t.Fatalf("month = %d %v", code, out)
	}
	if code, out := h.do("GET", "/v1/agent/worklog/day?d=2026-11-17", h.token, nil); code != 200 || out["date"] != "2026-11-17" {
		t.Fatalf("day = %d %v", code, out)
	}
	for _, bad := range []string{"/v1/agent/worklog/month", "/v1/agent/worklog/month?m=2026-13", "/v1/agent/worklog/month?m=26-11", "/v1/agent/worklog/day?d=2026-11-32", "/v1/agent/worklog/day?d=x"} {
		if code, _ := h.do("GET", bad, h.token, nil); code != 400 {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}
	if code, _ := h.do("GET", "/v1/agent/worklog/month?m=2026-11", "", nil); code != 401 {
		t.Error("no token")
	}
	if code, out := h.do("PATCH", "/v1/agent/worklog/entries/7", h.token, map[string]string{"text": "baru"}); code != 200 || cal.edited[7] != "baru" {
		t.Fatalf("patch = %d %v", code, out)
	}
	if code, out := h.do("PATCH", "/v1/agent/worklog/entries/7", h.token, map[string]string{"text": " "}); code != 400 || out["error"] != "teks catatan kosong" {
		t.Fatalf("empty text = %d %v", code, out)
	}
	if code, _ := h.do("PATCH", "/v1/agent/worklog/entries/404", h.token, map[string]string{"text": "x"}); code != 404 {
		t.Error("gone entry → 404")
	}
	code, out := h.do("PATCH", "/v1/agent/worklog/entries/500", h.token, map[string]string{"text": "x"})
	if code != 500 || strings.Contains(strings.ToLower(toString(out)), "10.0.0.5") {
		t.Fatalf("internal errors do not leak: %d %v", code, out)
	}
	if code, _ := h.do("PATCH", "/v1/agent/worklog/entries/abc", h.token, map[string]string{"text": "x"}); code != 400 {
		t.Error("bad id")
	}
	if code, _ := h.do("DELETE", "/v1/agent/worklog/entries/7", h.token, nil); code != 200 || len(cal.deleted) != 1 || cal.deleted[0] != 7 {
		t.Fatalf("delete %v", cal.deleted)
	}
	if code, _ := h.do("DELETE", "/v1/agent/worklog/entries/404", h.token, nil); code != 404 {
		t.Error("delete gone")
	}
	if code, _ := h.do("DELETE", "/v1/agent/worklog/entries/-3", h.token, nil); code != 400 {
		t.Error("bad id")
	}
}

func toString(m map[string]any) string {
	var b strings.Builder
	for k, v := range m {
		b.WriteString(k + "=" + strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(fmtAny(v), "\n", " "), "\t", " ")) + ";")
	}
	return b.String()
}

func fmtAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// Only whitelisted commands run; anything else is turned away without
// touching the command function.
func TestCommandWhitelist(t *testing.T) {
	h, _, ran := calHarness(t)
	cmd := func(c, args string) (int, map[string]any) {
		return h.do("POST", "/v1/agent/command", h.token, map[string]string{"cmd": c, "args": args})
	}
	for _, c := range []string{"today", "tasks", "done", "status", "form", "apps", "rekap"} {
		if code, out := cmd(c, "x"); code != 200 || out["kind"] != "text" || out["text"] != "hasil "+c {
			t.Errorf("/%s = %d %v", c, code, out)
		}
	}
	if len(*ran) != 7 {
		t.Fatalf("ran %v", *ran)
	}
	// calendar and /log are drawn and handled on the laptop
	for _, c := range []string{"cal", "log"} {
		if code, out := cmd(c, ""); code != 200 || out["kind"] != "local" {
			t.Errorf("/%s = %d %v", c, code, out)
		}
	}
	// a whitelisted command the server does not have yet (domain, Fase 8c)
	if code, out := cmd("domain", ""); code != 200 || out["kind"] != "unsupported" || out["text"] != UnsupportedText {
		t.Errorf("/domain = %d %v", code, out)
	}
	before := len(*ran)
	for _, c := range []string{"export", "settings", "remind", "start", "tanya", "../today", "TODAY; drop", "", "sql"} {
		code, out := cmd(c, "x")
		if code != 200 || out["kind"] != "unsupported" || out["text"] != UnsupportedText {
			t.Errorf("/%s = %d %v, want unsupported", c, code, out)
		}
	}
	if len(*ran) != before {
		t.Fatalf("a command outside the whitelist reached the handler: %v", (*ran)[before:])
	}
	// the name is case-insensitive; very long arguments are refused
	if code, out := cmd("TODAY", ""); code != 200 || out["kind"] != "text" {
		t.Errorf("TODAY = %d %v", code, out)
	}
	if code, _ := cmd("today", strings.Repeat("a", 501)); code != 400 {
		t.Error("args too long")
	}
	if code, _ := h.do("POST", "/v1/agent/command", "", map[string]string{"cmd": "today"}); code != 401 {
		t.Error("no token")
	}
}
