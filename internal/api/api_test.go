package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthz(t *testing.T) {
	srv := httptest.NewServer(New(slog.New(slog.DiscardHandler), Options{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "ok" {
		t.Fatalf("got %d %q", resp.StatusCode, b)
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := newRateLimiter(60, 3, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		if !l.allow("a") {
			t.Fatalf("request %d should pass", i)
		}
	}
	if l.allow("a") {
		t.Fatal("4th request should be limited")
	}
	if !l.allow("b") {
		t.Fatal("other client should pass")
	}
	now = now.Add(time.Second)
	if !l.allow("a") {
		t.Fatal("refill after 1s")
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	s := New(slog.New(slog.DiscardHandler), Options{RatePerMinute: 1, RateBurst: 1})
	codes := []int{}
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/agent/state", nil)
		req.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.1")
		s.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	if codes[1] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v", codes)
	}
	// healthz is not limited
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatal("healthz limited")
	}
}
